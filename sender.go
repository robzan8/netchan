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
		entry = &table[i]
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
	quitChan    chan<- struct{}
	table       *sendTable // table of the credit router
	credit      int
	quit        bool
}

func (s *sender) sendToEncoder(val reflect.Value, ok bool) {
	elem := element{s.id, val, ok, nil}
	// Simply sending to the encoder could lead to deadlocks,
	// keep processing credits
	for {
		select {
		case s.toEncoder <- elem:
			if !ok {
				// net-chan has been closed
				s.table.Lock()
				s.table[s.id] = sendEntry{}
				s.table.Unlock()
				s.quit = true
				return
			}
			s.credit--
			return
		case cred := <-s.credits:
			s.credit += cred
		case <-s.errorSignal:
			s.quit = true
			return
		}
	}
}

// TODO: send initElemMsg?
func (s sender) run() {
	recvSomething := [3]reflect.SelectCase{
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(s.credits)},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(s.errorSignal)},
		{Dir: reflect.SelectRecv, Chan: s.ch},
	}
	const (
		recvCredit int = iota
		recvError
		recvData
	)
	for !s.quit {
		var numCases int
		if s.credit > 0 {
			numCases = 3
		} else {
			// Do not receive from user channel (3rd case).
			numCases = 2
		}
		i, val, ok := reflect.Select(recvSomething[0:numCases])
		switch i {
		case recvCredit:
			s.credit += val.Interface().(int)
		case recvError:
			s.quit = true
		case recvData:
			s.sendToEncoder(val, ok)
		}
	}
	close(s.quitChan)
}

type credRouter struct {
	credits <-chan credit // from decoder
	table   sendTable
	mn      *Manager
}

func (r *credRouter) startSender(entry *sendEntry, ch reflect.Value) {
	quit := make(chan struct{})
	credits := make(chan int)

	go sender{entry.initCred.id, ch, credits, r.mn.ErrorSignal(),
		r.mn.toEncoder, quit, &r.table, entry.initCred.amount, false}.run()

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
			return errAlreadyOpen(name, "Send")
		}
		r.startSender(entry, ch)
		entry.halfOpen = false
		return nil
	}
	// Initial credit did not arrive yet.
	_, present := r.table.pending[hName]
	if present {
		return errAlreadyOpen(name, "Send")
	}
	r.table.pending[hName] = ch
	return nil
}

// Got a credit from the decoder.
func (r *credRouter) handleCred(cred credit) error {
	r.table.Lock()
	if cred.id >= len(r.table) {
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
func sanityCheck(table []sendEntry) (manyHoles, manyHalfOpen bool) {
	const (
		maxHoles    int = 256
		maxHalfOpen     = 256
	)
	var holes, halfOpen int
	for i := range table {
		if !table[i].present {
			holes++
		} else if table[i].halfOpen {
			halfOpen++
		}
	}
	return holes > maxHoles, halfOpen > maxHalfOpen
}

// An initial credit arrived.
func (r *credRouter) handleInitCred(cred credit) error {
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
	switch {
	case cred.id == len(r.table.t):
		// id is a fresh slot.
		if manyHoles {
			return newErr("peer does not reuse IDs of closed net-chans")
		}
		r.table.t = append(r.table.t, sendEntry{})
	case cred.id < len(r.table.t):
		// id is a recycled slot.
		if r.table.t[cred.id].present {
			// But it's not free.
			return newErr("initial credit arrived with ID alredy taken")
		}
	default:
		return errInvalidId
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
		return
	}
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
