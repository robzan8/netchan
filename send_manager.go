package netchan

import (
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
)

// Channels that the sendManager uses to talk to a sendProxy.
type sChans struct {
	creditCh chan<- credit
	done     <-chan struct{}
}

// Various info on a channel open for sending.
type sChanInfo struct {
	isOpenLocal    bool
	isOpenRemote   bool
	id, initCredit int
	sChans
}

// Keeps track of all channels open for sending.
type sendTable struct {
	sync.Mutex
	chans  map[int]sChans
	chInfo map[string]sChanInfo
}

// A sendProxy takes data from a channel and forwards it to the encoder.
// It also holds the credits for that channel.
type sendProxy struct {
	ssn       *Session
	chId      int
	chName    string
	dataCh    reflect.Value // <-chan T
	creditCh  <-chan credit
	toEncoder chan<- data
	done      chan<- struct{}
	table     *sendTable // table of the sendManager

	credit        int
	batchLenStats stats
}

func (s *sendProxy) sendToEncoder(dat data) {
	// To keep the credit flow going and avoid deadlocks, the sendProxy must never do
	// blocking operations without a select case that receives credits.
	// Mind the side effect.
	for {
		select {
		case s.toEncoder <- dat:
			return
		case c := <-s.creditCh:
			s.credit += c.amount
		case <-s.ssn.Done():
			return
		}
	}
}

func (s *sendProxy) tryRecvCredit() {
	select {
	case c := <-s.creditCh:
		s.credit += c.amount
		return
	default:
	}
	runtime.Gosched()
	select {
	case c := <-s.creditCh:
		s.credit += c.amount
	default:
	}
}

func (s *sendProxy) run() {
	defer close(s.done)

	// send the wantToSend message and receive the initial credit
	wantToSend := data{header: header{initDataMsg, 0, s.chName}}
	select {
	case s.toEncoder <- wantToSend:
		select {
		case c := <-s.creditCh:
			s.chId = c.ChId
			s.credit = c.amount
		case <-s.ssn.Done():
			return
		}
	case c := <-s.creditCh:
		s.chId = c.ChId
		s.credit = c.amount
		s.sendToEncoder(wantToSend)
	case <-s.ssn.Done():
		return
	}
	defer logDebug("netchan session %d: batchLen stats for channel send%d (%s):\n\t%s",
		s.ssn.id, s.chId, s.chName, &s.batchLenStats)

	batchType := reflect.SliceOf(s.dataCh.Type().Elem())
	// The encoder will calculate the desired batch length for this channel,
	// based on the size of the encoded items, and update *batchLenPt for us.
	batchLenPt := new(int32)
	*batchLenPt = 10 // initial guess
	recvDataCases := [...]reflect.SelectCase{
		{Dir: reflect.SelectRecv, Chan: s.dataCh},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(s.creditCh)},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(s.ssn.Done())},
	}
	const (
		recvData int = iota
		recvCredit
		recvDone
	)
	for {
		if s.credit <= 0 {
			select {
			case c := <-s.creditCh:
				s.credit += c.amount
				continue
			case <-s.ssn.Done():
				return
			}
		}
		i, val, ok := reflect.Select(recvDataCases[:])
		switch i {
		case recvData:
			if !ok {
				s.sendToEncoder(data{header: header{closeMsg, s.chId, ""}})
				s.table.Lock()
				delete(s.table.chans, s.chId)
				delete(s.table.chInfo, s.chName)
				s.table.Unlock()
				logDebug("netchan session %d: channel send%d (%s) closed",
					s.ssn.id, s.chId, s.chName)
				return
			}
			s.credit--
			batch := reflect.MakeSlice(batchType, 1, 8)
			batch.Index(0).Set(val)
			batchLen := int(atomic.LoadInt32(batchLenPt))
			for i := 1; i < batchLen; i++ {
				if s.credit <= 0 {
					s.tryRecvCredit()
					if s.credit <= 0 {
						break
					}
				}
				val, ok := s.dataCh.TryRecv()
				if val == (reflect.Value{}) {
					runtime.Gosched()
					val, ok = s.dataCh.TryRecv()
				}
				if !ok {
					break
				}
				s.credit--
				batch = reflect.Append(batch, val)
			}
			s.batchLenStats.update(float64(batch.Len()))
			s.sendToEncoder(data{header{dataMsg, s.chId, ""}, batch, batchLenPt})
		case recvCredit:
			s.credit += val.Interface().(credit).amount
		case recvDone:
			return
		}
	}
}

type sendManager struct {
	ssn       *Session
	creditCh  <-chan credit
	toEncoder chan<- data
	table     sendTable
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
func (s *sendManager) open(chName string, ch reflect.Value) error {
	s.table.Lock()
	ci := s.table.chInfo[chName]
	if ci.isOpenLocal {
		s.table.Unlock()
		return fmtErr("channel %s is already open for sending", chName)
	}
	ci.isOpenLocal = true
	creditCh := make(chan credit, internalChCap)
	done := make(chan struct{}, internalChCap)
	ci.creditCh = creditCh
	ci.done = done
	s.table.chInfo[chName] = ci
	if ci.isOpenRemote {
		s.table.chans[ci.id] = sChans{creditCh, done}
		creditCh <- credit{header{initCreditMsg, ci.id, chName}, ci.initCredit}
	}
	s.table.Unlock()

	go (&sendProxy{s.ssn, 0, chName, ch, creditCh,
		s.toEncoder, done, &s.table, 0, stats{}}).run()
	if ci.isOpenRemote {
		logDebug("netchan session %d: channel %s opened as send%d",
			s.ssn.id, chName, ci.id)
		return nil
	}
	logDebug("netchan session %d: opening channel %s for sending", s.ssn.id, chName)
	return nil
}

// Got a credit from the decoder.
func (s *sendManager) handleCredit(cred credit) {
	s.table.Lock()
	chs, present := s.table.chans[cred.ChId]
	s.table.Unlock()
	if !present {
		// It may happen that the entry is not present,
		// because the net-chan has just been closed; no problems.
		return
	}
	select {
	case chs.creditCh <- cred:
	case <-chs.done:
	}
}

// A couple of checks to make sure that the other peer is not trying to force us to
// allocate memory.
// "half-open" check:
//     When we receive an initial credit message, we have to store an entry in the table
//     and we say that the net-chan is half-open, until the user calls Open(Send)
//     locally. When we see too many half-open net-chans, we assume it's a "syn-flood"
//     attack and shut down with an error.
const maxHalfOpen = 256

// An initial credit arrived.
func (s *sendManager) handleInitCredit(cred credit) error {
	s.table.Lock()
	ci := s.table.chInfo[cred.ChName]
	if ci.isOpenRemote {
		s.table.Unlock()
		return fmtErr("received initial credit for already open channel")
	}
	ci.isOpenRemote = true
	ci.id = cred.ChId
	ci.initCredit = cred.amount
	s.table.chInfo[cred.ChName] = ci
	if ci.isOpenLocal {
		s.table.chans[cred.ChId] = ci.sChans
		ci.creditCh <- cred
	}
	halfOpen := len(s.table.chInfo) - len(s.table.chans)
	s.table.Unlock()

	if ci.isOpenLocal {
		logDebug("netchan session %d: channel %s opened as send%d",
			s.ssn.id, cred.ChName, ci.id)
		return nil
	}
	logDebug("netchan session %d: peer wants to receive on channel %s",
		s.ssn.id, cred.ChName)
	if halfOpen >= maxHalfOpen {
		return fmtErr("too many half open channels")
	}
	return nil
}

func (s *sendManager) run() {
	for c := range s.creditCh {
		switch c.Type {
		case creditMsg:
			s.handleCredit(c)
		case initCreditMsg:
			err := s.handleInitCredit(c)
			if err != nil {
				go s.ssn.QuitWith(err)
			}
		}
	}
}
