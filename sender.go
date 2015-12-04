package netchan

import (
	"reflect"
	"sync"
)

type sendEntry struct {
	dataChan reflect.Value
	initCred int
	credits  chan<- int
	quit     <-chan struct{}
}

type sendTable struct {
	sync.Mutex
	m map[hashedName]*sendEntry
}

type sender struct {
	name        hashedName
	dataChan    reflect.Value
	credits     <-chan int
	errorSignal <-chan struct{}
	toEncoder   chan<- message
	quit        chan<- struct{}
	table       *sendTable // table of the credit router
	credit      int
}

func (s *sender) sendToEncoder(payload interface{}) (exit bool) {
	msg := message{s.name, payload}
	// Simply sending to the encoder leads to deadlocks,
	// keep processing credits
	for {
		select {
		case s.toEncoder <- msg:
			return
		case cred := <-s.credits:
			s.credit += cred
		case <-s.errorSignal:
			exit = true
			return
		}
	}
}

func (s *sender) closeChan() {
	s.sendToEncoder(&endOfStream{})
	s.table.Lock()
	delete(s.table.m, s.name)
	s.table.Unlock()
}

func (s *sender) fillAndSend(batch reflect.Value) (exit bool) {
	recvDataDefault := [2]reflect.SelectCase{
		{Dir: reflect.SelectRecv, Chan: s.dataChan},
		{Dir: reflect.SelectDefault},
	}
	const (
		recvData int = iota
		noDataNow
	)
	maxNumItems := s.dataChan.Cap() - 1
	for i := 0; i < maxNumItems && s.credit > 0; i++ {
		caseI, val, ok := reflect.Select(recvDataDefault[:])
		switch caseI {
		case recvData:
			if !ok {
				s.sendToEncoder(batch.Interface())
				s.closeChan()
				exit = true
				return
			}
			batch = reflect.Append(batch, val)
			s.credit--
		case noDataNow:
			return
		}
	}
	return
}

// TODO: send initElemMsg?
func (s sender) run() {
	defer close(s.quit)

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
				s.closeChan()
				return
			}
			maxBatchLen := s.dataChan.Cap()
			batch := reflect.MakeSlice(reflect.SliceOf(val.Type()), 0, maxBatchLen/2)
			batch = reflect.Append(batch, val)
			exit := s.fillAndSend(batch)
			if exit {
				return
			}
			// call Gosched here?
		}
	}
}

type credRouter struct {
	credits   <-chan message // from decoder
	toEncoder chan<- message // elements
	table     sendTable
	mn        *Manager
	halfOpen  int
}

func (r *credRouter) startSender(name hashedName, entry *sendEntry) {
	credits := make(chan int)
	quit := make(chan struct{})

	entry.credits = credits
	entry.quit = quit

	go sender{name, entry.dataChan, credits, r.mn.ErrorSignal(),
		r.toEncoder, quit, &r.table, entry.initCred}.run()
	return
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
	entry, present := r.table.m[name]
	if present {
		// Initial credit already arrived.
		if entry.dataChan != (reflect.Value{}) {
			return errAlreadyOpen("Send", nameStr)
		}
		entry.dataChan = ch
		r.startSender(name, entry)
		r.halfOpen--
		return nil
	}
	// Initial credit did not arrive yet.
	r.table.m[name] = &sendEntry{dataChan: ch}
	return nil
}

// Got a credit from the decoder.
func (r *credRouter) handleCred(name hashedName, cred *credit) error {
	r.table.Lock()
	entry, present := r.table.m[name]
	if !present {
		// It may happen that the entry is not present,
		// because the channel has just been closed; no problem.
		r.table.Unlock()
		return nil
	}
	toSender := entry.credits
	quit := entry.quit
	r.table.Unlock()

	if toSender == nil {
		return newErr("credit arrived for half-open net-chan")
	}
	// If it's not shutting down, the sender is always ready to receive credit.
	select {
	case toSender <- cred.Amount:
	case <-quit: // net-chan closed or error occurred
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
const (
	maxHalfOpen = 256
)

// An initial credit arrived.
func (r *credRouter) handleInitCred(name hashedName, cred *initialCredit) error {
	r.table.Lock()
	defer r.table.Unlock()

	entry, present := r.table.m[name]
	if present {
		// User already called Open(Send).
		if entry.initCred != 0 {
			return newErr("initial credit arrived for already open net-chan")
		}
		entry.initCred = cred.Amount
		r.startSender(name, entry)
		return nil
	}
	// User didn't call Open(Send) yet.
	r.halfOpen++
	if r.halfOpen > maxHalfOpen {
		return newErr("too many half open net-chans")
	}
	r.table.m[name] = &sendEntry{initCred: cred.Amount}
	return nil
}

func (r *credRouter) run() {
	for {
		msg, ok := <-r.credits
		if !ok {
			// An error occurred and decoder shut down.
			return
		}
		err := r.mn.Error()
		if err != nil {
			// keep draining credits so that decoder doesn't block sending
			continue
		}

		switch cred := msg.payload.(type) {
		case *credit:
			err = r.handleCred(msg.name, cred)
		case *initialCredit:
			err = r.handleInitCred(msg.name, cred)
		default:
			panic("unexpected msg type")
		}

		if err != nil {
			go r.mn.ShutDownWith(err)
		}
	}
}
