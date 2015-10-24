package netchan

import (
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

type recvEntry struct {
	name     hashedName
	present  bool
	init     bool
	ch       reflect.Value
	chCap    int64
	received int64
}

type recvTable struct {
	sync.RWMutex
	t []recvEntry
}

type receiver struct {
	elemCh <-chan element
	table  *recvTable
	man    *Manager
}

func (r *receiver) open(name string, ch reflect.Value) error {
	r.table.Lock()
	defer r.table.Unlock()

	for _, entry := range r.table.t {
		if entry.name == name {
			return &errAlreadyOpen{name, Recv}
		}
	}
	// get an id for the new channel
	id := len(r.table.t)
	for i := range r.table.t {
		if !r.table.t[i].present {
			id = i
			break
		}
	}
	entry := recvEntry{name, true, true, ch, int64(ch.Cap()), 0}
	if id == len(r.table.t) {
		r.table.t = append(r.table.t, entry)
	} else {
		r.table.t[id] = entry
	}
	return nil
}

func (r *receiver) handleElem(elem element) error {
	r.table.RLock()
	if elem.id >= len(r.table.t) {
		r.table.RUnlock()
		return errInvalidId
	}
	entry := &r.table.t[elem.id]
	if !entry.present {
		r.table.RUnlock()
		return errInvalidId
	}
	if !elem.ok { // netchan closed normally
		r.table.RUnlock()
		// table array can be reallocated here
		r.table.Lock()
		r.table.t[elem.id].ch.Close()
		r.table.t[elem.id] = recvEntry{}
		r.table.Unlock()
		return nil
	}
	if ch.Len() == int(entry.chCap) {
		r.table.RUnlock()
		return errors.New("netchan Manager: peer sent more than its credit allowed")
	}
	// do not swap Send and AddInt64 operations
	ch.Send(elem.val) // does not block
	atomic.AddInt64(&enrty.received, 1)
	r.table.RUnlock()
	return nil
}

func (r *receiver) run() {
	for {
		elem, ok := <-r.elemCh
		if !ok {
			// error occurred and decoder shut down
			r.table.Lock()
			for _, entry := range r.table.t {
				entry.ch.Close()
			}
			r.table.Unlock()
			return
		}
		err := r.handleElem(elem)
		if err != nil {
			r.man.signalError(err)
		}
	}
}

type credSender struct {
	toEncoder chan<- credit
	table     *recvTable
	man       *Manager

	credits []credit
}

func (s *credSender) run() {
	for s.man.Error() == nil {
		s.updateCredits()
		for _, cred := range s.credits {
			s.toEncoder <- cred
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(s.toEncoder)
}

func (s *credSender) updateCredits() {
	s.table.RLock()
	s.credits = s.credits[:0]
	for id := range s.table.t {
		entry := &s.table.t[id]
		if !entry.present {
			continue
		}
		// do not swap the next two lines
		received := atomic.LoadInt64(&entry.received)
		chLen := int64(entry.ch.Len())
		consumed := received - chLen
		if entry.init {
			s.credits = append(s.credits, credit{id, entry.chCap, &entry.name})
			entry.init = false
		} else if consumed*2 >= entry.chCap { // i.e. consumed >= ceil(chCap/2)
			s.credits = append(s.credits, credit{id, consumed, nil})
			atomic.AddInt64(&entry.received, -consumed)
		}
	}
	s.table.RUnlock()
}
