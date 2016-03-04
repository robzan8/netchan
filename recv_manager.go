package netchan

import (
	"reflect"
	"sync"
	"sync/atomic"
)

type buffer struct {
	cap, len int64 // number of items, not number of batches

	// ch holds batches of items.
	// Keeping batches as interface{} instead of reflect.Values saves some memory.
	ch     chan interface{}
	errSig <-chan struct{}
}

func newBuffer(cap int, errSig <-chan struct{}) *buffer {
	// The buffer must be able to hold cap items, we only store batches with non-zero
	// length, so allocating memory for cap batches is sufficient and sometimes more than
	// necessary; a smarter implementation could save some memory by allocating lazily.
	return &buffer{int64(cap), 0, make(chan interface{}, cap), errSig}
}

func (b *buffer) put(batch reflect.Value) error {
	if batch.Len() == 0 {
		return nil
	}
	length := atomic.AddInt64(&b.len, int64(batch.Len()))
	if length > b.cap {
		return newErr("peer sent more than its credit allowed")
	}
	select {
	case b.ch <- batch.Interface():
		return nil
	default:
		panic("impossible")
	}
}

func (b *buffer) get() (batch reflect.Value, ok, err bool) {
	var batchE interface{}
	select {
	case <-b.errSig:
		err = true
		return
	case batchE, ok = <-b.ch:
		if !ok {
			return
		}
		batch = reflect.ValueOf(batchE)
		atomic.AddInt64(&b.len, -int64(batch.Len()))
		return
	}
}

func (b *buffer) close() { close(b.ch) }

type recvEntry struct {
	buf  *buffer
	name string
}

type recvTable struct {
	sync.Mutex
	entry   map[int]recvEntry
	present map[string]struct{}
}

type receiver struct {
	id        int
	name      string
	buf       *buffer
	errSig    <-chan struct{}
	dataChan  reflect.Value // chan<- elemType
	toEncoder chan<- credit
	bufCap    int
	err       bool
}

func (r *receiver) sendToUser(val reflect.Value) (err bool) {
	// Fast path: try sending without involving errSig.
	ok := r.dataChan.TrySend(val)
	if ok {
		return
	}
	// Slow path, must use reflect.Select
	sendAndError := [2]reflect.SelectCase{
		{Dir: reflect.SelectSend, Chan: r.dataChan, Send: val},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(r.errSig)},
	}
	i, _, _ := reflect.Select(sendAndError[:])
	return i == 1
}

func (r *receiver) sendToEncoder(cred credit) (err bool) {
	select {
	case r.toEncoder <- cred:
	case <-r.errSig:
		err = true
	}
	return
}

func (r receiver) run() {
	r.sendToEncoder(credit{Init: true, id: r.id, Name: r.name, Amount: r.bufCap})
	for !r.err {
		batch, ok, err := r.buf.get()
		if err {
			return
		}
		if !ok {
			r.dataChan.Close()
			// recvManager deletes the entry
			return
		}
		batchLen := batch.Len()
		r.sendToEncoder(credit{id: r.id, Amount: batchLen})
		for i := 0; i < batchLen; i++ {
			r.sendToUser(batch.Index(i))
		}
	}
}

type recvManager struct {
	dataChan  <-chan userData // from decoder
	toEncoder chan<- credit   // credits
	table     recvTable
	types     *typeTable // decoder's
	mn        *Manager
	newId     int
}

// Open a net-chan for receiving.
func (m *recvManager) open(name string, ch reflect.Value, bufCap int) error {
	m.table.Lock()
	defer m.table.Unlock()

	_, present := m.table.present[name]
	if present {
		return errAlreadyOpen("Recv", name)
	}

	m.types.Lock()
	defer m.types.Unlock()

	m.newId++
	buf := newBuffer(bufCap, m.mn.ErrorSignal())
	m.table.entry[m.newId] = recvEntry{buf, name}
	m.table.present[name] = struct{}{}
	m.types.elemType[m.newId] = ch.Type().Elem()

	go receiver{m.newId, name, buf, m.mn.ErrorSignal(),
		ch, m.toEncoder, bufCap, false}.run()
	return nil
}

// Got an element from the decoder.
func (m *recvManager) handleUserData(id int, batch reflect.Value) error {
	m.table.Lock()
	entry, present := m.table.entry[id]
	m.table.Unlock()
	if !present {
		return newErr("data arrived for closed net-chan")
	}
	return entry.buf.put(batch)
}

func (m *recvManager) handleClose(id int) error {
	m.table.Lock()
	entry, present := m.table.entry[id]
	if !present {
		m.table.Unlock()
		return newErr("end of stream message arrived for closed net-chan")
	}
	m.types.Lock()
	delete(m.table.entry, id)
	delete(m.table.present, entry.name)
	delete(m.types.elemType, id)
	m.types.Unlock()
	m.table.Unlock()

	entry.buf.close()
	return nil
}

func (m *recvManager) run() {
	for {
		data, ok := <-m.dataChan
		if !ok {
			// An error occurred and decoder shut down.
			return
		}
		err := m.mn.Error()
		if err != nil {
			// keep draining bla bla
			continue
		}
		if data.Close {
			err = m.handleClose(data.id)
		} else {
			err = m.handleUserData(data.id, data.batch)
		}
		if err != nil {
			go m.mn.ShutDownWith(err)
		}
	}
}
