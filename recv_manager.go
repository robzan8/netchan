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
	ch      chan interface{}
	ssnDone <-chan struct{}
	chName  string
}

func newBuffer(cap int, ssnDone <-chan struct{}, chName string) *buffer {
	// The buffer must be able to hold cap items, we only store batches with non-zero
	// length, so allocating memory for cap batches is sufficient and sometimes more than
	// necessary; a smarter implementation could save some memory by allocating lazily.
	return &buffer{int64(cap), 0, make(chan interface{}, cap), ssnDone, chName}
}

func (b *buffer) put(batch reflect.Value) error {
	if batch.Len() == 0 {
		return nil
	}
	length := atomic.AddInt64(&b.len, int64(batch.Len()))
	if length > b.cap {
		return fmtErr("peer sent more than its credit allowed")
	}
	select {
	case b.ch <- batch.Interface():
		return nil
	default:
		panic("impossible")
	}
}

func (b *buffer) get() (batch reflect.Value, ok, done bool) {
	var batchE interface{}
	select {
	case batchE, ok = <-b.ch:
		if !ok {
			return
		}
		batch = reflect.ValueOf(batchE)
		atomic.AddInt64(&b.len, -int64(batch.Len()))
		return
	case <-b.ssnDone:
		done = true
		return
	}
}

func (b *buffer) close() { close(b.ch) }

type rChanInfo struct {
	isOpenLocal  bool
	isOpenRemote bool
	id           int
}

type recvTable struct {
	sync.Mutex
	buffer map[int]*buffer
	chInfo map[string]rChanInfo
}

type recvProxy struct {
	ssn       *Session
	chId      int
	chName    string
	buf       *buffer
	dataCh    reflect.Value // chan<- T
	toEncoder chan<- credit
}

func (r *recvProxy) sendToUser(val reflect.Value) {
	ok := r.dataCh.TrySend(val)
	if ok {
		return
	}
	// Slow path.
	sendOrDone := [...]reflect.SelectCase{
		{Dir: reflect.SelectSend, Chan: r.dataCh, Send: val},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(r.ssn.Done())},
	}
	reflect.Select(sendOrDone[:])
}

func (r *recvProxy) sendToEncoder(cred credit) {
	select {
	case r.toEncoder <- cred:
	case <-r.ssn.Done():
	}
}

func (r *recvProxy) run() {
	r.sendToEncoder(credit{header{initCreditMsg, r.chId, r.chName}, int(r.buf.cap)})
	for {
		batch, ok, done := r.buf.get()
		if done {
			return
		}
		if !ok {
			r.dataCh.Close()
			return
		}
		batchLen := batch.Len()
		r.sendToEncoder(credit{header{creditMsg, r.chId, ""}, batchLen})
		for i := 0; i < batchLen; i++ {
			r.sendToUser(batch.Index(i))
		}
	}
}

type recvManager struct {
	ssn       *Session
	dataCh    <-chan data
	toEncoder chan<- credit
	table     recvTable
	newChId   int        // protected by table's mutex
	types     *typeTable // decoder's
}

// Open a net-chan for receiving.
func (r *recvManager) open(chName string, ch reflect.Value, bufCap int) error {
	r.table.Lock()
	ci := r.table.chInfo[chName]
	if ci.isOpenLocal {
		r.table.Unlock()
		return fmtErr("channel %s is already open for receiving", chName)
	}
	r.newChId++
	ci.isOpenLocal = true
	ci.id = r.newChId
	r.table.chInfo[chName] = ci
	buf := newBuffer(bufCap, r.ssn.Done(), chName)
	r.table.buffer[ci.id] = buf

	r.types.Lock()
	r.types.batchType[ci.id] = reflect.SliceOf(ch.Type().Elem())
	r.types.Unlock()

	r.table.Unlock()

	go (&recvProxy{r.ssn, ci.id, chName, buf, ch, r.toEncoder}).run()
	if ci.isOpenRemote {
		logDebug("netchan session %d: channel %s opened as recv%d",
			r.ssn.id, chName, ci.id)
		return nil
	}
	logDebug("netchan session %d: opening channel %s for receiving", r.ssn.id, chName)
	return nil
}

// Got an element from the decoder.
func (r *recvManager) handleData(dat data) error {
	r.table.Lock()
	buf, present := r.table.buffer[dat.ChId]
	r.table.Unlock()
	if !present {
		return fmtErr("data arrived for closed net-chan")
	}
	return buf.put(dat.batch)
}

func (r *recvManager) handleInitData(dat data) error {
	r.table.Lock()
	ci := r.table.chInfo[dat.ChName]
	if ci.isOpenRemote {
		r.table.Unlock()
		return fmtErr("initial data received twice for the same channel")
	}
	ci.isOpenRemote = true
	r.table.chInfo[dat.ChName] = ci
	halfOpen := len(r.table.chInfo) - len(r.table.buffer)
	r.table.Unlock()

	if ci.isOpenLocal {
		logDebug("netchan session %d: channel %s opened as recv%d",
			r.ssn.id, dat.ChName, ci.id)
	} else {
		logDebug("netchan session %d: peer wants to send on channel %s",
			r.ssn.id, dat.ChName)
	}
	if halfOpen >= maxHalfOpen {
		return fmtErr("too many half open channels")
	}
	return nil
}

func (r *recvManager) handleClose(dat data) error {
	r.table.Lock()
	buf, present := r.table.buffer[dat.ChId]
	if !present {
		r.table.Unlock()
		return fmtErr("close message arrived for already closed channel")
	}
	delete(r.table.buffer, dat.ChId)
	delete(r.table.chInfo, buf.chName)

	r.types.Lock()
	delete(r.types.batchType, dat.ChId)
	r.types.Unlock()

	r.table.Unlock()

	buf.close()
	logDebug("netchan session %d: channel recv%d (%s) closed",
		r.ssn.id, dat.ChId, buf.chName)
	return nil
}

func (r *recvManager) run() {
	for d := range r.dataCh {
		var err error
		switch d.Type {
		case dataMsg:
			err = r.handleData(d)
		case initDataMsg:
			err = r.handleInitData(d)
		case closeMsg:
			err = r.handleClose(d)
		}
		if err != nil {
			go r.ssn.QuitWith(err)
		}
	}
}
