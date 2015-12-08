package netchan

import (
	"reflect"
	"sync"
)

type recvTable struct {
	sync.Mutex
	buffer map[hashedName]chan<- interface{}
}

type receiver struct {
	id          int
	name        hashedName
	buffer      <-chan interface{}
	errorSignal <-chan struct{}
	dataChan    reflect.Value
	toEncoder   chan<- message // credits
	bufCap      int
	received    int
	err         bool
}

func (r *receiver) sendToUser(val reflect.Value) {
	sendAndError := [2]reflect.SelectCase{
		{Dir: reflect.SelectSend, Chan: r.dataChan, Send: val},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(r.errorSignal)},
	}
	i, _, _ := reflect.Select(sendAndError[:])
	if i == 1 { // errorSignal
		r.err = true
	}
}

func (r *receiver) sendToEncoder(cred credit) {
	select {
	case r.toEncoder <- cred:
	case <-r.errorSignal:
		r.err = true
	}
}

func (r receiver) run() {
	r.sendToEncoder(credit{r.id, r.bufCap, &r.name}) // initial credit
	for !r.err {
		select {
		case batch, ok := <-r.buffer:
			if !ok {
				r.dataChan.Close()
				// elemRouter deletes the entry
				return
			}
			r.received++
			if r.received == ceilDivide(r.bufCap, 2) {
				r.sendToEncoder(&credit{r.id, r.received, nil})
				r.received = 0
			}
			r.sendToUser(val)
		case <-r.errorSignal:
			return
		}
	}
}

type elemRouter struct {
	elements  <-chan message // from decoder
	toEncoder chan<- message // credits
	table     recvTable
	types     *typeTable // decoder's
	mn        *Manager
	newId     int
}

// Open a net-chan for receiving.
func (r *elemRouter) open(nameStr string, ch reflect.Value, bufCap int) error {
	r.table.Lock()
	defer r.table.Unlock()

	name := hashName(nameStr)
	_, present := r.table.m[name]
	if present {
		return errAlreadyOpen("Recv", nameStr)
	}

	r.types.Lock()
	defer r.types.Unlock()

	buffer := make(chan reflect.Value, bufCap)
	r.table.m[name] = buffer
	r.types.m[name] = ch.Type().Elem()

	r.newId++
	go receiver{r.newId, name, buffer, r.mn.ErrorSignal(),
		ch, r.toEncoder, bufCap, 0, false}.run()
	return nil
}

// Got an element from the decoder.
// WARNING: we are not handling initElemMsg
// if data is nil, we got endOfStream
func (r *elemRouter) handleUserData(name hashedName, data *userData) error {
	r.table.Lock()
	buffer, present := r.table.m[name]
	if !present {
		r.table.Unlock()
		return newErr("data arrived for closed net-chan")
	}
	r.table.Unlock()

	select {
	case buffer <- data.val:
		return nil
	default:
		// Sending to the buffer should never be blocking.
		return newErr("peer sent more than its credit allowed")
	}
}

func (r *elemRouter) handleEOS(name hashedName) error {
	r.table.Lock()
	buffer, present := r.table.m[name]
	if !present {
		r.table.Unlock()
		return newErr("end of stream message arrived for closed net-chan")
	}
	r.types.Lock()
	delete(r.table.m, name)
	delete(r.types.m, name)
	r.types.Unlock()
	r.table.Unlock()

	close(buffer)
	return nil
}

func (r *elemRouter) run() {
	for {
		data, ok := <-r.elements
		if !ok {
			// An error occurred and decoder shut down.
			return
		}
		err := r.mn.Error()
		if err != nil {
			// keep draining bla bla
			continue
		}

		switch pay := msg.payload.(type) {
		case *userData:
			err = r.handleUserData(msg.name, pay)
		case *endOfStream:
			err = r.handleEOS(msg.name)
		default:
			panic("unexpected msg type")
		}

		if err != nil {
			go r.mn.ShutDownWith(err)
		}
	}
}
