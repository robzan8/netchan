package netchan

import (
	"reflect"
	"sync"
)

type sendEntry struct {
	name     hashedName
	present  bool
	halfOpen bool
	initCred *credit
	toSender chan<- int
	quit     <-chan struct{}
}

type sendTable struct {
	sync.Mutex
	t       []sendEntry
	pending map[hashedName]reflect.Value
}

func entryByName(table []sendEntry, name hashedName) *sendEntry {
	for i := range table {
		entry := &table[i]
		if entry.present && entry.name == name {
			return entry
		}
	}
	return nil
}

type sender struct {
	id          int
	ch          reflect.Value
	credits     <-chan int
	errorSignal <-chan struct{}
	toEncoder   chan<- element
	quit        chan<- struct{}
	table       *sendTable // table of the credit router
	credit      int
	errOccurred bool
}

func (s *sender) sendToEncoder(elem element) {
	// Simply sending to the encoder could lead to deadlocks,
	// keep processing credits
	for {
		select {
		case s.toEncoder <- elem:
			return
		case cred := <-s.credits:
			s.credit += cred
		case <-s.errorSignal:
			s.errOccurred = true
			return
		}
	}
}

// TODO: send initElemMsg?
func (s sender) run() {
	defer close(s.quit)

	recvSomething := [...]reflect.SelectCase{
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(s.credits)},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(s.errorSignal)},
		{Dir: reflect.SelectRecv, Chan: s.ch},
	}
	const (
		recvCredit int = iota
		recvError
		recvData
	)
	for !s.errOccurred {
		// If no credit, do not receive from user channel (third case).
		var numCases int
		if s.credit > 0 {
			numCases = 3
		} else {
			numCases = 2
		}
		i, val, ok := reflect.Select(recvSomething[0:numCases])
		switch i {
		case recvCredit:
			s.credit += val.Interface().(int)
		case recvError:
			return
		case recvData:
			if !ok {
				s.sendToEncoder(element{id: s.id, close: true})
				s.table.Lock()
				s.table.t[s.id] = sendEntry{}
				s.table.Unlock()
				return
			}
			f, fOk := val.Interface().(NetchanFlusher)
			if fOk && f.NetchanFlush() {
				s.sendToEncoder(element{flush: true})
				continue
			}
			s.sendToEncoder(element{id: s.id, val: val})
			s.credit--
			if s.credit == 0 {
				// If no credit, we don't receive from the user channel. Even if the user
				// wants to issue a flush, we ignore it, the data never arrives to peer,
				// peer doesn't send credit and user blocks. Flush preventively:
				s.sendToEncoder(element{flush: true})
			}
		}
	}
}

type credRouter struct {
	credits   <-chan credit // from decoder
	toEncoder chan<- element
	table     sendTable
	mn        *Manager
}

func (r *credRouter) startSender(entry *sendEntry, ch reflect.Value) {
	quit := make(chan struct{})
	credits := make(chan int)

	go sender{entry.initCred.id, ch, credits, r.mn.ErrorSignal(),
		r.toEncoder, quit, &r.table, entry.initCred.amount, false}.run()

	entry.quit = quit
	entry.toSender = credits
	entry.initCred = nil
	return
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
func (r *credRouter) open(name string, ch reflect.Value) error {
	r.table.Lock()
	defer r.table.Unlock()

	hName := hashName(name)
	entry := entryByName(r.table.t, hName)
	if entry != nil {
		// Initial credit already arrived.
		if !entry.halfOpen {
			return errAlreadyOpen("Send", name)
		}
		r.startSender(entry, ch)
		entry.halfOpen = false
		return nil
	}
	// Initial credit did not arrive yet.
	_, present := r.table.pending[hName]
	if present {
		return errAlreadyOpen("Send", name)
	}
	r.table.pending[hName] = ch
	return nil
}

// Got a credit from the decoder.
func (r *credRouter) handleCred(cred credit) error {
	r.table.Lock()
	if cred.id >= len(r.table.t) {
		r.table.Unlock()
		return errInvalidId
	}
	entry := &r.table.t[cred.id]
	if !entry.present {
		// It may happen that the entry is not present,
		// because the channel has just been closed; no problem.
		r.table.Unlock()
		return nil
	}
	if entry.halfOpen {
		r.table.Unlock()
		return newErr("credit arrived for half-open net-chan")
	}
	toSender := entry.toSender
	quit := entry.quit
	r.table.Unlock()

	// If it's not shutting down, the sender is always ready to receive credit.
	select {
	case toSender <- cred.amount:
	case <-quit: // net-chan closed or error occurred
	}
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
	maxHoles    int = 256
	maxHalfOpen     = 256
)

// An initial credit arrived.
func (r *credRouter) handleInitCred(cred credit) error {
	r.table.Lock()
	defer r.table.Unlock()

	entry := entryByName(r.table.t, *cred.name)
	if entry != nil {
		return newErr("initial credit arrived for already open net-chan")
	}
	var holes, halfOpen int
	for i := range r.table.t {
		if !r.table.t[i].present {
			holes++
		} else if r.table.t[i].halfOpen {
			halfOpen++
		}
	}
	if halfOpen > maxHalfOpen {
		return newErr("too many half open net-chans")
	}

	if cred.id < len(r.table.t) {
		// id is a recycled slot.
		if r.table.t[cred.id].present {
			// But it's not free.
			return newErr("initial credit arrived with ID alredy taken")
		}
	} else {
		// id is a fresh slot.
		extend := cred.id - len(r.table.t) + 1
		if holes+extend > maxHoles {
			return newErr("many holes")
		}
		r.table.t = append(r.table.t, make([]sendEntry, extend)...)
	}

	ch, present := r.table.pending[*cred.name]
	r.table.t[cred.id] = sendEntry{
		name:     *cred.name,
		present:  true,
		halfOpen: !present,
		initCred: &cred,
	}
	if present {
		// User already called Open(Send).
		r.startSender(&r.table.t[cred.id], ch)
		delete(r.table.pending, *cred.name)
	}
	return nil
}

func (r *credRouter) run() {
	for {
		cred, ok := <-r.credits
		if !ok {
			// An error occurred and decoder shut down.
			return
		}
		err := r.mn.Error()
		if err != nil {
			// keep draining credits so that decoder doesn't block sending
			continue
		}
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
