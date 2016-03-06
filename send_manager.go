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
	name      string
	mn        *Manager
	dataChan  reflect.Value // <-chan T
	credits   <-chan int
	toEncoder chan<- userData
	quit      chan<- struct{}
	table     *sendTable // table of the send manager

	credit        int
	batchLenStats stats
	creditStats   stats
}

func (s *sender) sendToEncoder(data userData) {
	// Simply sending to the encoder leads to deadlocks,
	// keep processing credits
	for {
		select {
		case s.toEncoder <- data:
			return
		case cred := <-s.credits:
			s.creditStats.update(float64(cred))
			s.credit += cred
		case <-s.mn.ErrorSignal():
			return
		}
	}
}

func (s *sender) run() {
	batchType := reflect.SliceOf(s.dataChan.Type().Elem())
	batchLenPt := new(int64)
	*batchLenPt = 1
	defer func() {
		logDebug("manager %d: channel send%d closed.\n"+
			"\tlength of batches stats: %s\n\tcredit messages stats: %s",
			s.mn.id, s.id, &s.batchLenStats, &s.creditStats)
		close(s.quit)
	}()
	s.sendToEncoder(userData{header: header{initDataMsg, s.id, s.name}})

	recvSomething := [3]reflect.SelectCase{
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(s.credits)},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(s.mn.ErrorSignal())},
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
			cred := val.Int()
			s.creditStats.update(float64(cred))
			s.credit += int(cred)
		case recvError:
			return
		case recvData:
			if !ok {
				s.sendToEncoder(userData{header: header{closeMsg, s.id, ""}})
				s.table.Lock()
				delete(s.table.chans, s.id)
				s.table.Unlock()
				return
			}
			s.credit--
			batch := reflect.MakeSlice(batchType, 1, 1)
			batch.Index(0).Set(val)
			batchLen := int(atomic.LoadInt64(batchLenPt))
			for i := 1; i < batchLen && s.credit > 0; i++ {
				val, ok := s.dataChan.TryRecv()
				if !ok { // no items available or channel closed
					break
				}
				s.credit--
				batch = reflect.Append(batch, val)
			}
			s.batchLenStats.update(float64(batch.Len()))
			s.sendToEncoder(userData{header{dataMsg, s.id, ""}, batch, batchLenPt})
		}
	}
}

type sendManager struct {
	credits   <-chan credit   // from decoder
	toEncoder chan<- userData // elements
	table     sendTable
	mn        *Manager
}

func (m *sendManager) startSender(name string, info openInfo) {
	credits := make(chan int)
	quit := make(chan struct{})
	// table is locked
	m.table.chans[info.id] = senderChans{credits, quit}

	go (&sender{info.id, name, m.mn, info.dataChan, credits,
		m.toEncoder, quit, &m.table, info.initCred, stats{}, stats{}}).run()
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
		m.startSender(name, info)
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
	chans, present := m.table.chans[cred.Id]
	m.table.Unlock()
	if !present {
		// It may happen that the entry is not present,
		// because the net-chan has just been closed; no problem.
		return nil
	}

	// If it's not shutting down, the sender is always ready to receive credit.
	select {
	case chans.credits <- cred.amount:
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
		info.id = cred.Id
		info.initCred = cred.amount
		m.startSender(cred.Name, info)
		delete(m.table.info, cred.Name)
		return nil
	}
	// User didn't call Open(Send) yet.
	m.table.info[cred.Name] = openInfo{id: cred.Id, initCred: cred.amount}
	return nil
}

func (m *sendManager) run() {
	for cred := range m.credits {
		err := m.mn.Error()
		if err != nil {
			// keep draining credits so that decoder doesn't block sending
			continue
		}
		switch cred.Type {
		case creditMsg:
			err = m.handleCredit(cred)
		case initCreditMsg:
			err = m.handleInitCredit(cred)
		}
		if err != nil {
			go m.mn.ShutDownWith(err)
		}
	}
	// An error occurred and decoder shut down.
}
