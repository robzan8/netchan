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
	bufCap      int
	received    int
	quit        bool
}

func (r *receiver) sendToUser(val reflect.Value) {
	sendAndError := [2]reflect.SelectCase{
		{Dir: reflect.SelectSend, Chan: r.ch, Send: val},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(r.errorSignal)},
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

func (r receiver) run() {
	r.sendToEncoder(credit{r.id, r.bufCap, &r.name}) // initial credit
	for !r.quit {
		select {
		case val, ok := <-r.buffer:
			if !ok {
				r.ch.Close()
				return
			}
			r.received++
			if r.received*2 >= r.bufCap { // i.e. received >= ceil(bufCap/2)
				r.sendToEncoder(credit{r.id, r.received, nil})
				r.received = 0
			}
			r.sendToUser(val)
		case <-r.errorSignal:
			return
		}
	}
}

type elemRouter struct {
	elements  <-chan element // from decoder
	toEncoder chan<- credit
	table     recvTable
	types     *typeTable // decoder's
	mn        *Manager
}

// Open a net-chan for receiving.
func (r *elemRouter) open(name string, ch reflect.Value, bufCap int) error {
	r.table.Lock()
	defer r.table.Unlock()

	hName := hashName(name)
	id := len(r.table.t)
	for i, entry := range r.table.t {
		if entry.present && entry.name == hName {
			return errAlreadyOpen("Recv", name)
		}
		if !entry.present && i < id {
			id = i
		}
	}

	r.types.Lock()
	defer r.types.Unlock()

	if id == len(r.table.t) {
		r.table.t = append(r.table.t, recvEntry{})
		r.types.t = append(r.types.t, nil)
	}
	buffer := make(chan reflect.Value, bufCap)
	r.table.t[id] = recvEntry{hName, true, buffer}
	r.types.t[id] = ch.Type().Elem()

	go receiver{id, hName, buffer, r.mn.ErrorSignal(),
		ch, r.toEncoder, bufCap, 0, false}.run()
	return nil
}

// Got an element from the decoder.
// WARNING: we are not handling initElemMsg
func (r *elemRouter) handleElem(elem element) error {
	r.table.Lock()
	// these checks done already by decoder?
	if elem.id >= len(r.table.t) {
		r.table.Unlock()
		return errInvalidId
	}
	entry := &r.table.t[elem.id]
	if !entry.present {
		r.table.Unlock()
		return newErr("element arrived for closed net-chan")
	}
	buffer := entry.buffer
	if !elem.ok {
		// net-chan closed, delete the entry.
		r.types.Lock()
		*entry = recvEntry{}
		r.types.t[elem.id] = nil
		r.types.Unlock()

		r.table.Unlock()
		close(buffer)
		return nil
	}
	r.table.Unlock()

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
		elem, ok := <-r.elements
		if !ok {
			// An error occurred and decoder shut down.
			return
		}
		err := r.mn.Error()
		if err != nil {
			// keep draining bla bla
			continue
		}
		err = r.handleElem(elem)
		if err != nil {
			go r.mn.ShutDownWith(err)
		}
	}
}
