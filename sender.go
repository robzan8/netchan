package netchan

import "reflect"

// what happens if a credit arrives for a deleted channel?

func sender(id int, mn *Manager, userCh reflect.Value,
	creditCh <-chan int, toEncoder chan<- element) {

	const (
		userIndex int = iota
		creditIndex
		encIndex
		errorIndex
		numCases
	)
	userCase := reflect.SelectCase{Dir: reflect.SelectRecv, Chan: userCh}
	encCase := reflect.SelectCase{Dir: reflect.SelectSend, Chan: reflect.ValueOf(toEncoder)}
	cases := [numCases]reflect.SelectCase{
		{}, {Dir: reflect.SelectRecv, Chan: reflect.ValueOf(creditCh)},
		{}, {Dir: reflect.SelectRecv, Chan: reflect.ValueOf(mn.ErrorSignal())},
	}
	credit := 0
	sending := false
	closed := false
	for {
		i, val, ok := reflect.Select(cases[:])
		switch i {
		case userIndex:
			// suspend receiving from user and start trying to send the element to encoder
			cases[userIndex] = reflect.SelectCase{}
			cases[encIndex] = encCase
			cases[encIndex].Send = reflect.ValueOf(element{id, val, ok, nil})
			sending = true
			closed = !ok
		case creditIndex:
			if credit == 0 && !sending {
				cases[userIndex] = userCase // start receiving from user again
			}
			credit += val.Interface().(int)
		case encIndex:
			if closed { // net-chan has been closed
				return
			}
			credit--
			if credit > 0 {
				cases[userIndex] = userCase // start receiving from user again
			}
			sending = false
		case errorIndex:
			return
		}
	}
}

type sendEntry struct {
	name     hashedName
	present  bool
	ch       reflect.Value
	toSender chan<- int // credits from the credRouter
	padding  int
}

type credRouter struct {
	creditCh   <-chan credit // from decoder
	openReqCh  <-chan openReq
	openRespCh chan<- error
	mn         *Manager

	table, pending []sendEntry
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
func (r *credRouter) open(name string, userCh reflect.Value) error {
	hName := hashName(name)
	entry, id := entryByName(r.table, hName)
	if entry != nil {
		// Initial credit already arrived.
		if entry.ch != nil {
			return errAlreadyOpen(name, "Send")
		}
		entry.userCh = userCh
		creditCh = make(chan int)
		entry.toSender = creditCh
		go sender(id, r.mn, userCh, creditCh, r.toEncoder)
		return nil
	}
	// Initial credit did not arrive yet.
	pend, _ := entryByName(r.pending, hName)
	if pend != nil {
		return errAlreadyOpen(name, "Send")
	}
	r.pending = addEntry(r.pending, sendEntry{
		name:    hName,
		present: true,
		userCh:  userCh,
	})
	return nil
}

func (r *credRouter) run() {
	for {
		select {
		case req := <-r.openReqCh:
			r.openRespCh <- r.open(req.name, req.ch)
		case cred, ok := <-r.creditCh:
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
				r.mn.ShutDownWith(err)
				return
			}
		}
	}
}

// Got a credit from the decoder.
func (r *credRouter) handleCred(cred credit) error {
	if cred.id >= len(r.table) {
		return errInvalidId
	}
	entry := &r.table[cred.id]
	if !entry.present {
		// It may happen that the entry is not present,
		// because the channel has just been closed; no problem.
		return nil
	}
	// if no error, credits will always be processed in a timely fashion by the sender
	select {
	case entry.toSender <- cred.incr:
		return nil
	case r.mn.ErrorSignal():
		return r.mn.Error()
	}
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
func (r *credRouter) handleInitCred(cred credit) error {
	entry := entryByName(r.table, *cred.name)
	if entry != nil {
		return newErr("initial credit arrived for already open net-chan")
	}
	manyHoles, manyHalfOpen := sanityCheck(r.table)
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
