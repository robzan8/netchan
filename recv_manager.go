package netchan

import (
	"reflect"
	"sync"
	"sync/atomic"
)

const (
	bufMinCap     int = 4
	bufGrowFactor     = 5
	numBufs           = 4
)

type buffer struct {
	maxCap     int
	numElems   int32
	bufs       [bufNumBufs]chan reflect.Value
	send, recv int
	mn         *Manager
	errSig     <-chan struct{}
}

func newBuffer(mn *Manager, maxCap int) *buffer {
	buf := new(buffer)
	buf.maxCap = maxCap
	cap0 := maxCap / 125
	if cap0 <= 0 {
		cap0 = 4
	}
	buf.bufs[0] = make(chan reflect.Value, cap0)
	buf.mn = mn
	buf.errSig = mn.ErrorSignal()
	return buf
}

func (b *buffer) reallocBuf() {
	newCap := b.maxCap
	for i := numBufs; i >= b.send; i-- {
		newCap /= bufGrowFactor
	}
	if newCap < bufMinCap {
		newCap = bufMinCap
	}
	b.send++

}

func (b *buffer) put(batch reflect.Value) error {
	n := int(atomic.AddInt32(&b.numElems, int32(batch.Len())))
	if n > b.maxCap {
		return newErr("peer sent more than its credit allowed")
	}
	select {
	case b.bufs[b.send] <- batch:
		return nil
	default:
		b.reallocBuf()
		b.bufs[b.send] <- batch // won't block, single producer
	}
	return nil
}

func (b *buffer) get() (reflect.Value, error) {
	select {
	case batch, ok := <-b.bufs[b.recv]:
		if ok {
			atomic.AddInt32(&b.numElems, int32(-batch.Len()))
			return batch, nil
		}
		//?
	case <-b.errorSignal:
		return reflect.Value{}, b.mn.Error()
	}
}

func (b *buffer) close() {
	close(b.bufs[b.send])
}

type recvTable struct {
	sync.Mutex
	buffer map[hashedName]chan<- interface{}
}

type receiver struct {
	id          int
	name        hashedName
	buffer      <-chan reflect.Value // <-chan []elemType
	errorSignal <-chan struct{}
	dataChan    reflect.Value // chan<- elemType
	toEncoder   chan<- credit
	bufCap      int
	received    int
	err         bool
}

func (r *receiver) sendToUser(val reflect.Value) (err bool) {
	// Fast path: try sending without involving errorSignal.
	ok := r.dataChan.TrySend(val)
	if ok {
		return
	}
	// Slow path, must use reflect.Select
	sendAndError := [2]reflect.SelectCase{
		{Dir: reflect.SelectSend, Chan: r.dataChan, Send: val},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(r.errorSignal)},
	}
	i, _, _ := reflect.Select(sendAndError[:])
	if i == 1 { // errorSignal
		err = true
	}
	return
}

func (r *receiver) sendToEncoder(cred credit) (err bool) {
	select {
	case r.toEncoder <- cred:
	case <-r.errorSignal:
		err = true
	}
	return
}

func (r receiver) run() {
	r.sendToEncoder(credit{r.id, r.bufCap, &r.name}) // initial credit
	for !r.err {
		select {
		case batch, ok := <-r.buffer:
			if !ok {
				r.dataChan.Close()
				// recvManager deletes the entry
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

type recvManager struct {
	elements  <-chan message // from decoder
	toEncoder chan<- message // credits
	table     recvTable
	types     *typeTable // decoder's
	mn        *Manager
	newId     int
}

// Open a net-chan for receiving.
func (m *recvManager) open(nameStr string, ch reflect.Value, bufCap int) error {
	m.table.Lock()
	defer m.table.Unlock()

	name := hashName(nameStr)
	_, present := m.table.m[name]
	if present {
		return errAlreadyOpen("Recv", nameStr)
	}

	m.types.Lock()
	defer m.types.Unlock()

	buffer := make(chan reflect.Value, bufCap)
	m.table.m[name] = buffer
	m.types.m[name] = ch.Type().Elem()

	m.newId++
	go receiver{m.newId, name, buffer, m.mn.ErrorSignal(),
		ch, m.toEncoder, bufCap, 0, false}.run()
	return nil
}

// Got an element from the decoder.
// if data is nil, we got endOfStream
func (m *recvManager) handleUserData(data *userData) error {
	m.table.Lock()
	buffer, present := m.table.m[data.id]
	m.table.Unlock()
	if !present {
		return newErr("data arrived for closed net-chan")
	}

	select {
	case buffer <- data.val:
		return nil
	default:
		// Sending to the buffer should never be blocking.
		return newErr("peer sent more than its credit allowed")
	}
}

func (m *recvManager) handleEOS(name hashedName) error {
	m.table.Lock()
	buffer, present := m.table.m[name]
	if !present {
		m.table.Unlock()
		return newErr("end of stream message arrived for closed net-chan")
	}
	m.types.Lock()
	delete(m.table.m, name)
	delete(m.types.m, name)
	m.types.Unlock()
	m.table.Unlock()

	close(buffer)
	return nil
}

func (m *recvManager) run() {
	for {
		data, ok := <-m.elements
		if !ok {
			// An error occurred and decoder shut down.
			return
		}
		err := m.mn.Error()
		if err != nil {
			// keep draining bla bla
			continue
		}

		switch pay := msg.payload.(type) {
		case *userData:
			err = m.handleUserData(msg.name, pay)
		case *endOfStream:
			err = m.handleEOS(msg.name)
		default:
			panic("unexpected msg type")
		}

		if err != nil {
			go m.mn.ShutDownWith(err)
		}
	}
}
