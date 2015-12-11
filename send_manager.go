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

func (s *sender) sendToEncoder(data userData) (err bool) {
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

func (s *sender) chanClosed() {
	s.sendToEncoder(reflect.Value{})
	s.table.Lock()
	delete(s.table.m, s.name)
	s.table.Unlock()
}

func (s *sender) run() {
	defer close(s.quit)

	pool := slicePool(s.dataChan.Type().Elem())

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
			batch = reflect.ValueOf(pool.Get()).Elem().Slice(0, 1)
			batch.Index(0).Set(firstVal)

			numVals := int(atomic.LoadInt32(&s.batchLen))
			for i := 1; i < numVals && s.credit > 0; i++ {
				val, ok := s.dataChan.TryRecv()
				if !ok {
					eos = val != reflect.Value{}
					break
				}
				s.credit--
				batch.Set(reflect.Append(batch, val))
			}
			err := s.sendToEncoder(batch)
			if eos {
				s.chanClosed()
				return
			}
			if err {
				return
			}
		}
	}
}

type sendManager struct {
	credits   <-chan credit   // from decoder
	toEncoder chan<- userData // elements
	table     sendTable
	mn        *Manager
}

func (m *sendManager) startSender(info openInfo) {
	credits := make(chan int)
	quit := make(chan struct{})
	// table is locked
	m.table.chans[info.id] = senderChans{credits, quit}

	go (&sender{info.id, info.dataChan, credits, m.mn.ErrorSignal(),
		m.toEncoder, quit, &m.table, info.initCred, info.initCred, nil, 1}).run()
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
func (m *sendManager) open(nameStr string, ch reflect.Value) error {
	m.table.Lock()
	defer m.table.Unlock()

	name := hashName(nameStr)
	info, present := m.table.info[name]
	if present {
		// Initial credit already arrived.
		info.dataChan = ch
		m.startSender(info)
		delete(m.table.info, name)
		return nil
	}
	// Initial credit did not arrive yet.
	m.table.info[name] = openInfo{dataChan: ch}
	return nil
}

// Got a credit from the decoder.
func (m *sendManager) handleCredit(cred credit) error {
	m.table.Lock()
	chans, present := m.table.chans[cred.id]
	m.table.Unlock()
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
func (m *sendManager) handleInitCred(cred credit) error {
	m.table.Lock()
	defer m.table.Unlock()

	info, present := m.table.info[*cred.Name]
	if present {
		// User already called Open(Send).
		info.id = cred.id
		info.initCred = cred.Amount
		m.startSender(info)
		delete(m.table.info, *cred.Name)
		return nil
	}
	// User didn't call Open(Send) yet.
	m.table.info[*cred.Name] = openInfo{id: cred.id, initCred: cred.Amount}
	return nil
}

func (m *sendManager) run() {
	for {
		cred, ok := <-m.credits
		if !ok {
			// An error occurred and decoder shut down.
			return
		}
		err := m.mn.Error()
		if err != nil {
			// keep draining credits so that decoder doesn't block sending
			continue
		}
		if cred.Name == nil {
			err = m.handleCredit(cred)
		} else {
			err = m.handleInitCred(cred)
		}
		if err != nil {
			go m.mn.ShutDownWith(err)
		}
	}
}
