package netchan

import (
	"reflect"
	"sync"
	"sync/atomic"
)

type senderChans struct {
	credits chan<- int
	quit    <-chan struct{}
}

type openInfo struct {
	id       int
	initCred int
	dataChan reflect.Value
}

type sendTable struct {
	sync.Mutex
	chans map[int]senderChans
	info  map[hashedName]openInfo
}

type sender struct {
	id          int
	dataChan    reflect.Value
	credits     <-chan int
	errorSignal <-chan struct{}
	toEncoder   chan<- userData
	quit        chan<- struct{}
	table       *sendTable // table of the credit router
	credit      int
	recvCap     int
	batchList   chan reflect.Value
	batchLen    int32
}

func (s *sender) sendToEncoder(batch reflect.Value) (err bool) {
	data := userData{s, batch}
	// Simply sending to the encoder leads to deadlocks,
	// keep processing credits
	for {
		select {
		case s.toEncoder <- data:
			return
		case cred := <-s.credits:
			s.credit += cred
		case <-s.errorSignal:
			err = true
			return
		}
	}
}

// non eccedere recvBufCap/2?
func (s *sender) createBatch(firstVal reflect.Value) (batch reflect.Value, eos bool) {
	batch = (<-s.batchList).Slice(0, 1)
	batch.Index(0).Set(firstVal)

	numVals := int(atomic.LoadInt32(&s.batchLen))
	for i := 1; i < numVals && s.credit > 0; i++ {
		val, ok := s.dataChan.TryRecv()
		if !ok {
			eos = val != reflect.Value{}
			return
		}
		s.credit--
		batch = reflect.Append(batch, val)
	}
	return
}

func (s *sender) chanClosed() {
	s.sendToEncoder(reflect.Value{})
	s.table.Lock()
	delete(s.table.m, s.name)
	s.table.Unlock()
}

func (s *sender) run() {
	defer close(s.quit)

	// at most cap(s.toEncoder)+2 batches are around simultaneously, cap(s.toEncoder)
	// in the buffer and other two being processed in the sender and encoder goroutines
	s.batchList = make(chan reflect.Value, cap(s.toEncoder)+2)
	sliceType := reflect.SliceOf(s.dataChan.Type().Elem())
	for i := 0; i < cap(s.batchList); i++ {
		s.batchList <- reflect.MakeSlice(sliceType, 0, 1)
	}

	recvSomething := [3]reflect.SelectCase{
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(s.credits)},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(s.errorSignal)},
		{Dir: reflect.SelectRecv, Chan: s.dataChan},
	}
	const (
		recvCredit int = iota
		recvError
		recvData
	)
	for {
		// If no credit, do not receive from user channel (third case).
		var numCases int
		if s.credit > 0 {
			numCases = 3
		} else {
			numCases = 2
		}
		caseI, val, ok := reflect.Select(recvSomething[0:numCases])
		switch caseI {
		case recvCredit:
			s.credit += val.Interface().(int)
		case recvError:
			return
		case recvData:
			if !ok {
				s.chanClosed()
				return
			}
			s.credit--
			batch, eos := s.createBatch(val)
			err := s.sendToEncoder(batch)
			if eos {
				s.chanClosed()
				return
			}
			if err {
				return
			}
			// call Gosched here?
		}
	}
}

type credRouter struct {
	credits   <-chan credit   // from decoder
	toEncoder chan<- userData // elements
	table     sendTable
	mn        *Manager
}

func (r *credRouter) startSender(info openInfo) {
	credits := make(chan int)
	quit := make(chan struct{})
	// table is locked
	r.table.chans[info.id] = senderChans{credits, quit}

	go (&sender{info.id, info.dataChan, credits, r.mn.ErrorSignal(),
		r.toEncoder, quit, &r.table, info.initCred, info.initCred, nil, 1}).run()
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
func (r *credRouter) open(nameStr string, ch reflect.Value) error {
	r.table.Lock()
	defer r.table.Unlock()

	name := hashName(nameStr)
	info, present := r.table.info[name]
	if present {
		// Initial credit already arrived.
		info.dataChan = ch
		r.startSender(info)
		delete(r.table.info, name)
		return nil
	}
	// Initial credit did not arrive yet.
	r.table.info[name] = openInfo{dataChan: ch}
	return nil
}

// Got a credit from the decoder.
func (r *credRouter) handleCredit(cred credit) error {
	r.table.Lock()
	chans, present := r.table.chans[cred.id]
	r.table.Unlock()
	if !present {
		// It may happen that the entry is not present,
		// because the net-chan has just been closed; no problem.
		return nil
	}

	// If it's not shutting down, the sender is always ready to receive credit.
	select {
	case chans.credits <- cred.Amount:
	case <-chans.quit: // net-chan closed or error occurred
	}
	return nil
}

// A couple of checks to make sure that the other peer is not trying to force us to
// allocate memory.
// "half-open" check:
//     When we receive an initial credit message, we have to store an entry in the table
//     and we say that the net-chan is half-open, until the user calls Open(Send)
//     locally. When we see too many half-open net-chans, we assume it's a "syn-flood"
//     attack and shut down with an error.

// An initial credit arrived.
func (r *credRouter) handleInitCred(cred credit) error {
	r.table.Lock()
	defer r.table.Unlock()

	info, present := r.table.info[*cred.Name]
	if present {
		// User already called Open(Send).
		info.id = cred.id
		info.initCred = cred.Amount
		r.startSender(info)
		delete(r.table.info, *cred.Name)
		return nil
	}
	// User didn't call Open(Send) yet.
	r.table.info[*cred.Name] = openInfo{id: cred.id, initCred: cred.Amount}
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
		if cred.Name == nil {
			err = r.handleCredit(cred)
		} else {
			err = r.handleInitCred(cred)
		}
		if err != nil {
			go r.mn.ShutDownWith(err)
		}
	}
}
