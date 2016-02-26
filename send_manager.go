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
	info  map[string]openInfo
}

type sender struct {
	id        int
	dataChan  reflect.Value // <-chan T
	credits   <-chan int
	errSig    <-chan struct{}
	toEncoder chan<- userData
	quit      chan<- struct{}
	table     *sendTable // table of the send manager
	credit    int
	batchLen  *int32
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
		case <-s.errSig:
			err = true
			return
		}
	}
}

func (s sender) run() {
	defer close(s.quit)
	s.batchLen = new(int32)
	*s.batchLen = 1
	batchType := reflect.SliceOf(s.dataChan.Type().Elem())
	s.sendToEncoder(userData{id: s.id, Init: true})

	recvSomething := [3]reflect.SelectCase{
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(s.credits)},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(s.errSig)},
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
			s.credit += int(val.Int())
		case recvError:
			return
		case recvData:
			if !ok {
				s.sendToEncoder(userData{id: s.id, Close: true})
				s.table.Lock()
				delete(s.table.chans, s.id)
				s.table.Unlock()
				return
			}
			s.credit--
			batchLen := int(atomic.LoadInt32(s.batchLen))
			batch := reflect.MakeSlice(batchType, 1, 1)
			batch.Index(0).Set(val)
			for i := 1; i < batchLen && s.credit > 0; i++ {
				val, ok := s.dataChan.TryRecv()
				if !ok { // no items available or channel closed
					break
				}
				s.credit--
				batch = reflect.Append(batch, val)
			}
			err := s.sendToEncoder(userData{id: s.id, batch: batch, batchLen: s.batchLen})
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

	go sender{info.id, info.dataChan, credits, m.mn.ErrorSignal(),
		m.toEncoder, quit, &m.table, info.initCred, nil}.run()
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
func (m *sendManager) open(name string, ch reflect.Value) error {
	m.table.Lock()
	defer m.table.Unlock()

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
func (m *sendManager) handleInitCredit(cred credit) error {
	m.table.Lock()
	defer m.table.Unlock()

	info, present := m.table.info[cred.Name]
	if present {
		// User already called Open(Send).
		info.id = cred.id
		info.initCred = cred.Amount
		m.startSender(info)
		delete(m.table.info, cred.Name)
		return nil
	}
	// User didn't call Open(Send) yet.
	m.table.info[cred.Name] = openInfo{id: cred.id, initCred: cred.Amount}
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
		if cred.Init {
			err = m.handleInitCredit(cred)
		} else {
			err = m.handleCredit(cred)
		}
		if err != nil {
			go m.mn.ShutDownWith(err)
		}
	}
}
