package netchan

import (
	"errors"
	"reflect"
	"sync/atomic"
	"time"
)

type sender struct {
	toEncoder chan<- element
	table     *chanTable
	mn        *Manager

	cases []reflect.SelectCase
	ids   []int
}

func (s *sender) init() {
	s.cases = []reflect.SelectCase{reflect.SelectCase{Dir: reflect.SelectDefault}}
	s.ids = []int{-1}
}

func (s *sender) open(name string, ch reflect.Value) error {
	s.table.Lock()
	defer s.table.Unlock()

	hName := hashName(name)
	entry := entryByName(s.table.t, hName)
	if entry != nil {
		if entry.ch != (reflect.Value{}) {
			return &errAlreadyOpen{name, Send}
		}
		entry.init = true
		entry.ch = ch
		return nil
	}
	pend := entryByName(s.table.pending, hName)
	if pend != nil {
		return &errAlreadyOpen{name, Send}
	}
	s.table.pending = addEntry(s.table.pending, chanEntry{
		name:    hName,
		present: true,
		init:    true,
		ch:      ch,
	})
	return nil
}

func (s *sender) handleElem(elem element) {
	if elem.ok {
		atomic.AddInt64(s.table.t[elem.id].credit(), -1)
		s.table.RUnlock()
	} else {
		s.table.RUnlock()
		s.table.Lock()
		s.table.t[elem.id] = chanEntry{}
		s.table.Unlock()
	}
	s.toEncoder <- elem
}

func (s *sender) run() {
	// TODO: check if there are entries (possibly pending) with init = true
	// and send the initElemMsg.
	// also check that init is set correctly everywhere in this file
	for s.mn.Error() == nil {
		s.table.RLock()
		s.cases = s.cases[0:1]
		s.ids = s.ids[0:1]
		for id := range s.table.t {
			entry := &s.table.t[id]
			cred := atomic.LoadInt64(entry.credit())
			if entry.present && cred > 0 {
				s.cases = append(s.cases,
					reflect.SelectCase{Dir: reflect.SelectRecv, Chan: entry.ch})
				s.ids = append(s.ids, id)
			}
		}
		i, val, ok := reflect.Select(s.cases)
		switch {
		case i > 0:
			s.handleElem(element{s.ids[i], val, ok, nil})
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
	table    *chanTable
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
			go r.mn.ShutDownWith(err)
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
	newCred := atomic.AddInt64(entry.credit(), cred.incr)
	if newCred > entry.recvCap {
		r.table.RUnlock()
		return errors.New("too much credit!")
	}
	r.table.RUnlock()
	return nil
}

const (
	maxHoles    = 256
	maxHalfOpen = 256
)

func sanityCheck(table []chanEntry) (bool, bool) {
	var holes, halfOpen int
	for i := range table {
		if !table[i].present {
			holes++
		} else if table[i].ch == (reflect.Value{}) {
			halfOpen++
		}
	}
	return holes > maxHoles, halfOpen > maxHalfOpen
}

func (r *credReceiver) handleInitCred(cred credit) error {
	r.table.Lock()
	defer r.table.Unlock()

	entry := entryByName(r.table.t, *cred.name)
	if entry != nil {
		return errors.New("netchan Manager: initial credit for already open net-chan")
	}
	manyHoles, manyHalfOpen := sanityCheck(r.table.t)
	if manyHalfOpen {
		return errors.New("netchan Manager: too many half open net-chans")
	}

	newEntry := chanEntry{
		name:    *cred.name,
		present: true,
		numElem: cred.incr, // credit
		recvCap: cred.incr,
	}
	pend := entryByName(r.table.pending, *cred.name)
	if pend != nil {
		newEntry.init = pend.init
		newEntry.ch = pend.ch
		pend.present = false
	}
	if cred.id == len(r.table.t) {
		// cred.id is a fresh slot
		if manyHoles {
			return errors.New("netchan Manager: peer does not recycle old net-chan IDs")
		}
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
