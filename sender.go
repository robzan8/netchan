package netchan

import (
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

// When a net-chan is opened for receiving, a credit message is sent to the peer with the
// id, name and the receiver's buffer capacity as credit increment value
const (
	maxHalfOpen = 256
	maxHoles    = 256
)

type sendEntry struct {
	name    hashedName
	present bool
	ch      reflect.Value
	credit  int64
	recvCap int64
}

type sendTable struct {
	sync.RWMutex
	t       []sendEntry
	pending map[hashedName]reflect.Value
}

type sender struct {
	toEncoder chan<- element
	table     *sendTable
	mn        *Manager

	cases []reflect.SelectCase
	ids   []int
}

func (s *sender) initialize() {
	s.cases = []reflect.SelectCase{reflect.SelectCase{Dir: reflect.SelectDefault}}
	s.ids = []int{-1}
}

// The ID of a newly opened net-chan is defin
func (s *sender) open(name string, ch reflect.Value) error {
	s.table.Lock()
	defer s.table.Unlock()

	hName := hashName(name)
	var entry *sendEntry
	for i := range s.table.t {
		e := &s.table.t[i]
		if e.present && e.name == hName {
			entry = e
			break
		}
	}
	if entry != nil {
		if entry.ch != (reflect.Value{}) {
			return &errAlreadyOpen{name, Send}
		}
		entry.ch = ch
		return nil
	}
	_, present := s.table.pending[hName]
	if present {
		return &errAlreadyOpen{name, Send}
	}
	s.table.pending[hName] = ch
	return nil
}

func (s *sender) handleElem(elem element) {
	atomic.AddInt64(&s.table.t[elem.id].credit, -1)
	s.table.RUnlock()
	s.toEncoder <- elem
	if !elem.ok {
		// channel has been closed;
		// an initial credit may arrive in this moment and add a new
		// entry in the table, possibly triggering a whole reallocation of
		// the underlying array, but our id will remain the same and
		// our entry will be untouched.
		s.table.Lock()
		s.table.t[elem.id] = sendEntry{}
		s.table.Unlock()
	}
}

func (s *sender) run() {
	for s.mn.Error() == nil {
		s.table.RLock()
		s.cases = s.cases[:1]
		s.ids = s.ids[:1]
		for id := range s.table.t {
			entry := &s.table.t[id]
			cred := atomic.LoadInt64(&entry.credit)
			if entry.present && cred > 0 {
				s.cases = append(s.cases,
					reflect.SelectCase{Dir: reflect.SelectRecv, Chan: entry.ch})
				s.ids = append(s.ids, id)
			}
		}
		i, val, ok := reflect.Select(s.cases)
		switch {
		case i > 0:
			s.handleElem(element{s.ids[i], val, ok})
			// handleElem does table.RUnlock()
		default:
			s.table.RUnlock()
			time.Sleep(10 * time.Millisecond)
		}
	}
	close(s.toEncoder)
}

type credReceiver struct {
	creditCh <-chan credit
	table    *sendTable
	mn       *Manager
}

func (r *credReceiver) run() {
	for {
		cred, ok := <-r.creditCh
		if !ok {
			// error occurred and decoder shut down
			return
		}
		var err error
		if cred.name == nil {
			err = r.handleCred(cred)
		} else {
			err = r.handleInitCred(cred)
		}
		if err != nil {
			r.mn.signalError(err)
		}
	}
}

func (r *credReceiver) handleCred(cred credit) error {
	r.table.RLock()
	if cred.id >= len(r.table.t) {
		r.table.RUnlock()
		return errInvalidId
	}
	entry := &r.table.t[cred.id]
	if !entry.present {
		// it may happen that the entry is not present,
		// because the channel has just been closed; no problem.
		r.table.RUnlock()
		return nil
	}
	newCred := atomic.AddInt64(&entry.credit, cred.incr)
	if newCred > entry.recvCap {
		r.table.RUnlock()
		return errors.New("wrong credit!")
	}
	r.table.RUnlock()
	return nil
}

func (r *credReceiver) handleInitCred(cred credit) error {
	r.table.Lock()
	defer r.table.Unlock()

	// explain checks
	halfOpen := 0
	holes := 0
	for i := range r.table.t {
		entry := &r.table.t[i]
		if entry.name == *cred.name {
			return errors.New("netchan Manager: initial credit for already open net-chan")
		}
		if !entry.present {
			holes++
		} else if entry.ch == (reflect.Value{}) {
			halfOpen++
		}
	}
	if halfOpen > maxHalfOpen {
		return errors.New("netchan Manager: too many half open net-chans")
	}
	if holes > maxHoles {
		return errors.New("netchan Manager: peer does not recycle old net-chan IDs")
	}

	newEntry := sendEntry{
		name:    *cred.name,
		present: true,
		ch:      r.table.pending[*cred.name], // may not be present
		credit:  cred.incr,
		recvCap: cred.incr,
	}
	delete(r.table.pending, *cred.name)
	if cred.id == len(r.table.t) {
		// cred.id is a fresh slot
		r.table.t = append(r.table.t, newEntry)
		return nil
	}
	if cred.id < len(r.table.t) {
		// cred.id is a recycled slot
		if r.table.t[cred.id].present {
			// slot is not free
			return errInvalidId
		}
		r.table.t[cred.id] = newEntry
		return nil
	}
	return errInvalidId
}
