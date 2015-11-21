package netchan

import (
	"reflect"
	"sync"
)

type recvEntry struct {
	name    hashedName
	present bool
	buffer  chan<- reflect.Value
}

type recvTable struct {
	sync.Mutex
	t []recvEntry
}

type receiver struct {
	id          int
	name        hashedName
	buffer      <-chan reflect.Value
	errorSignal <-chan struct{}
	ch          reflect.Value
	toEncoder   chan<- credit
	table       *recvTable // table of the element router
	bufCap      int
	received    int
	quit        bool
}

// can we deadlock somehow?

func (r *receiver) sendToUser(val reflect.Value) {
	sendAndError := [2]reflect.SelectCase{
		{Dir: reflect.SelectSend, Chan: ch, Send: val},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(s.errorSignal)},
	}
	i, _, _ := reflect.Select(sendAndError[:])
	if i == 1 { // errorSignal
		r.quit = true
	}
}

func (r *receiver) sendToEncoder(cred credit) {
	select {
	case r.toEncoder <- cred:
	case <-r.errorSignal:
		r.quit = true
	}
}

// makes a copy
func (r receiver) run() {
	r.sendToEncoder(credit{r.id, r.bufCap, &r.name}) // initial credit
	for !r.quit {
		select {
		case val, ok := <-r.buffer:
			if !ok {
				r.ch.Close()
				return
			}
			r.sendToUser(val)
			r.received++
			if r.received*2 >= r.bufCap { // i.e. received >= ceil(bufCap/2)
				r.sendToEncoder(credit{r.id, r.received, nil})
				r.received = 0
			}
		case <-r.errorSignal:
			return
		}
	}
}

type elemRouter struct {
	elements <-chan element // from decoder
	table    recvTable
	mn       *Manager
}

// Open a net-chan for receiving.
func (r *elemRouter) open(name string, ch reflect.Value, bufCap int) error {
	r.table.Lock()
	defer r.table.Unlock()

	hName := hashName(name)
	id := len(r.table.t)
	for i, entry := range table {
		if entry.present && entry.name == hName {
			return errAlreadyOpen(name, "Recv")
		}
		if !entry.present {
			id = i
		}
	}
	if id == len(r.table.t) {
		r.table.t = append(r.table.t, recvEntry{})
	}

	buffer := make(chan reflect.Value, bufCap)
	r.table.t[id] = recvEntry{hName, true, buffer}
	go receiver{
		id, hName, buffer, r.mn.ErrorSignal(), ch,
		r.mn.toEncoder, &r.table, bufCap, 0, false,
	}.run()
	return nil
}

// Got an element from the decoder.
// WARNING: we are not handling initElemMsg
func (r *elemRouter) handleElem(elem element) error {
	r.table.Lock()
	defer r.table.Unlock()

	if elem.id >= len(r.table.t) {
		return errInvalidId
	}
	entry := &r.table.t[elem.id]
	if !entry.present {
		return newErr("element arrived for closed net-chan")
	}
	if !elem.ok {
		// net-chan closed, delete the entry.
		entry.buffer.Close()
		*entry = chanEntry{}
		return nil
	}
	buffer := entry.buffer
	select {
	case buffer <- elem.val:
		return nil
	default:
		// Sending to the buffer should never be blocking.
		return newErr("peer sent more than its credit allowed")
	}
}

func (r *elemRouter) run() {
	for {
		elem, ok := <-r.elemCh
		if !ok {
			// An error occurred and decoder shut down.
			return
		}
		err := r.handleElem(elem)
		if err != nil {
			r.mn.ShutDownWith(err)
		}
	}
}
