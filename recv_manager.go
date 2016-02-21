package netchan

import (
	"reflect"
	"sync"
)

type recvEntry struct {
	buffer chan<- interface{} // chan of batches
	name   string
}

type recvTable struct {
	sync.Mutex
	entry   map[int]recvEntry
	present map[string]struct{}
}

type receiver struct {
	id        int
	name      string
	buffer    <-chan interface{}
	errSig    <-chan struct{}
	dataChan  reflect.Value // chan<- elemType
	toEncoder chan<- credit
	bufCap    int
	received  int64
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
	r.sendToEncoder(credit{r.id, r.bufCap, true, r.name}) // initial credit
	for !r.err {
		select {
		case batchE, ok := <-r.buffer:
			if !ok {
				r.dataChan.Close()
				// recvManager deletes the entry
				return
			}
			batch := reflect.ValueOf(batchE)
			r.received++
			select {
			case batchE2, ok := <-r.buffer:
				if ok {
					batch = reflect.AppendSlice(batch, reflect.ValueOf(batchE2))
				}
			default:
			}
			batchLen := batch.Len()
			r.sendToEncoder(credit{r.id, batchLen, false, ""})
			for i := 0; i < batchLen; i++ {
				r.sendToUser(batch.Index(i))
			}
		case <-r.errSig:
			return
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
	buffer := make(chan interface{}, bufCap)
	m.table.entry[m.newId] = recvEntry{buffer, name}
	m.table.present[name] = struct{}{}
	m.types.elemType[m.newId] = ch.Type().Elem()

	go receiver{m.newId, name, buffer, m.mn.ErrorSignal(),
		ch, m.toEncoder, bufCap, 0, false}.run()
	return nil
}

// Got an element from the decoder.
// if data is nil, we got endOfStream
func (m *recvManager) handleUserData(id int, batch reflect.Value) error {
	if batch.Len() == 0 {
		return nil
	}
	m.table.Lock()
	entry, present := m.table.entry[id]
	m.table.Unlock()
	if !present {
		return newErr("data arrived for closed net-chan")
	}

	select {
	case entry.buffer <- batch.Interface():
		return nil
	default:
		// Sending to the buffer should never be blocking.
		return newErr("peer sent more than its credit allowed")
	}
}

func (m *recvManager) handleEOS(id int) error {
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

	close(entry.buffer)
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
		if data.batch == (reflect.Value{}) {
			err = m.handleEOS(data.id)
		} else {
			err = m.handleUserData(data.id, data.batch)
		}
		if err != nil {
			go m.mn.ShutDownWith(err)
		}
	}
}
