package netchan

import (
	"reflect"
	"sync/atomic"
	"time"
)

// The sender receives from the user channels that are registered in its table and sends
// the messages to the encoder (see the graph in manager.go).
type sender struct {
	toEncoder chan<- element
	table     *chanTable
	mn        *Manager

	cases []reflect.SelectCase
	ids   []int
}

func (s *sender) init() {
	s.cases = []reflect.SelectCase{reflect.SelectCase{Dir: reflect.SelectDefault}}
	s.ids = []int{-1}
}

// Open a net-chan for sending.
// When a new net-chan is opened, the receiver chooses its id. Then it sends an initial
// credit message to the sender, communicating the id and the receive buffer capacity.
// Two scenarios are possible:
// 1) The initial credit arrives, then the user calls Open(Send):
//     In this case, the entry is added to the table when the credit arrives, with a zero
//     ch. When open is called, the entry is patched with the channel value provided by
//     the user.
// 2) The user calls Open(Send), then the initial credit arrives:
//     In this case, open adds the entry to the pending table (we don't know the channel
//     id yet), with 0 credit. When the message arrives, we patch the entry with the
//     credit and move it from the pending table to the final table.
func (s *sender) open(name string, ch reflect.Value) error {
	s.table.Lock()
	defer s.table.Unlock()

	hName := hashName(name)
	entry := entryByName(s.table.t, hName)
	if entry != nil {
		// Initial credit already arrived.
		if entry.ch != (reflect.Value{}) {
			return errAlreadyOpen(name, Send)
		}
		entry.init = true
		entry.ch = ch
		return nil
	}
	// Initial credit did not arrive yet.
	pend := entryByName(s.table.pending, hName)
	if pend != nil {
		return errAlreadyOpen(name, Send)
	}
	s.table.pending = addEntry(s.table.pending, chanEntry{
		name:    hName,
		present: true,
		init:    true,
		ch:      ch,
	})
	return nil
}

// An element was taken out of a user's channel. s.table is Rlocked (see sender.run).
func (s *sender) handleElem(elem element) {
	if elem.ok {
		atomic.AddInt64(s.table.t[elem.id].credit(), -1)
		s.table.RUnlock()
	} else {
		// net-chan closed, delete the entry.
		s.table.RUnlock()
		s.table.Lock()
		s.table.t[elem.id] = chanEntry{}
		s.table.Unlock()
	}
	s.toEncoder <- elem
}

func (s *sender) run() {
	// TODO: check if there are entries (possibly pending) with init = true
	// and send the initial element message.
	for s.mn.Error() == nil {
		s.table.RLock()

		// Build a select that receives from all the user channels with positive credit.
		// For each select case cases[i], the net-chan id is stored in ids[i].
		// cases[0] is the default case.
		s.cases = s.cases[0:1]
		s.ids = s.ids[0:1]
		for id := range s.table.t {
			entry := &s.table.t[id]
			if entry.present && atomic.LoadInt64(entry.credit()) > 0 {
				s.cases = append(s.cases,
					reflect.SelectCase{Dir: reflect.SelectRecv, Chan: entry.ch})
				s.ids = append(s.ids, id)
			}
		}
		i, val, ok := reflect.Select(s.cases)
		switch {
		case i > 0:
			s.handleElem(element{s.ids[i], val, ok, nil})
			// handleElem does table.RUnlock()
		default:
			// No elements from the user, retry later.
			s.table.RUnlock()
			time.Sleep(10 * time.Millisecond)
		}
	}
	close(s.toEncoder)
}

// credReceiver receives credits from the decoder and updates the channel table.
type credReceiver struct {
	creditCh <-chan credit
	table    *chanTable // same table of the sender
	mn       *Manager
}

func (r *credReceiver) run() {
	for {
		cred, ok := <-r.creditCh
		if !ok {
			// An error occurred and decoder shut down.
			return
		}
		var err error
		if cred.name == nil {
			err = r.handleCred(cred)
		} else {
			err = r.handleInitCred(cred)
		}
		if err != nil {
			go r.mn.ShutDownWith(err)
		}
	}
}

// Got a credit from the decoder.
func (r *credReceiver) handleCred(cred credit) error {
	r.table.RLock()

	if cred.id >= len(r.table.t) {
		r.table.RUnlock()
		return errInvalidId
	}
	entry := &r.table.t[cred.id]
	if !entry.present {
		// It may happen that the entry is not present,
		// because the channel has just been closed; no problem.
		r.table.RUnlock()
		return nil
	}
	newCred := atomic.AddInt64(entry.credit(), cred.incr)
	if newCred > entry.recvCap {
		r.table.RUnlock()
		return newErr("too much credit received")
	}
	r.table.RUnlock()
	return nil
}

// A couple of checks to make sure that the other peer is not trying to force us to
// allocate memory.
// "holes" check:
//     When a net-chan gets closed, we set to zero its entry in the table, but we can't
//     recompact the table because ids are indices in the table. If there are a lot of
//     holes and yet the peer wants to open a new net-chan with a fresh id, we shut down
//     with an error.
// "half-open" check:
//     When we receive an initial credit message, we have to store an entry in the table
//     and we say that the net-chan is half-open, until the user calls Open(Send)
//     locally. When we see too many half-open net-chans, we assume it's a "syn-flood"
//     attack and shut down with an error.
const (
	maxHoles    = 256
	maxHalfOpen = 256
)

func sanityCheck(table []chanEntry) (manyHoles, manyHalfOpen bool) {
	var holes, halfOpen int
	for i := range table {
		if !table[i].present {
			holes++
		} else if table[i].ch == (reflect.Value{}) {
			halfOpen++
		}
	}
	return holes > maxHoles, halfOpen > maxHalfOpen
}

// An initial credit arrived.
func (r *credReceiver) handleInitCred(cred credit) error {
	r.table.Lock()
	defer r.table.Unlock()

	entry := entryByName(r.table.t, *cred.name)
	if entry != nil {
		return newErr("initial credit arrived for already open net-chan")
	}
	manyHoles, manyHalfOpen := sanityCheck(r.table.t)
	if manyHalfOpen {
		return newErr("too many half open net-chans")
	}

	newEntry := chanEntry{
		name:     *cred.name,
		present:  true,
		numElems: cred.incr, // credit
		recvCap:  cred.incr,
	}
	pend := entryByName(r.table.pending, *cred.name)
	if pend != nil {
		// User already called Open(Send).
		newEntry.init = pend.init
		newEntry.ch = pend.ch
		*pend = chanEntry{}
	}
	if cred.id == len(r.table.t) {
		// id is a fresh slot.
		if manyHoles {
			return newErr("peer does not reuse IDs of closed net-chans")
		}
		r.table.t = append(r.table.t, newEntry)
		return nil
	}
	if cred.id < len(r.table.t) {
		// id is a recycled slot.
		if r.table.t[cred.id].present {
			// But it's not free.
			return newErr("initial credit arrived with ID alredy taken")
		}
		r.table.t[cred.id] = newEntry
		return nil
	}
	return errInvalidId
}
