package netchan

import (
	"errors"
	"reflect"
	"sync/atomic"
	"time"
)

type receiver struct {
	elemCh <-chan element
	table  *chanTable
	mn     *Manager
}

func (r *receiver) open(name string, ch reflect.Value) error {
	r.table.Lock()
	defer r.table.Unlock()

	hName := hashName(name)
	entry := entryByName(r.table.t, hName)
	if entry != nil {
		return &errAlreadyOpen{name, Recv}
	}
	// the position where the entry is added will
	// actually determine the net-chan's id
	r.table.t = addEntry(r.table.t, chanEntry{
		name:    hName,
		present: true,
		init:    true,
		ch:      ch,
		recvCap: int64(ch.Cap()),
	})
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
		r.table.t[elem.id] = chanEntry{}
		r.table.Unlock()
		return nil
	}
	if atomic.LoadInt64(entry.received()) >= entry.recvCap {
		r.table.RUnlock()
		return errors.New("netchan Manager: peer sent more than its credit allowed")
	}
	if int64(entry.ch.Len()) == entry.recvCap {
		r.table.RUnlock()
		return errors.New("using one channel to receive from more than one net-chan?")
	}
	// do not swap Send and AddInt64 operations
	entry.ch.Send(elem.val) // does not block
	atomic.AddInt64(entry.received(), 1)
	r.table.RUnlock()
	return nil
}

func (r *receiver) run() {
	for {
		elem, ok := <-r.elemCh
		if !ok {
			// error occurred and decoder shut down
			return
		}
		err := r.handleElem(elem)
		if err != nil {
			go r.mn.ShutDownWith(err)
		}
	}
}

type credSender struct {
	toEncoder chan<- credit
	table     *chanTable
	mn        *Manager

	credits []credit
}

func (s *credSender) run() {
	for s.mn.Error() == nil {
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

	s.credits = s.credits[0:0]
	for id := range s.table.t {
		entry := &s.table.t[id]
		if !entry.present {
			continue
		}
		// do not swap the next two lines
		received := atomic.LoadInt64(entry.received())
		chLen := int64(entry.ch.Len())
		consumed := received - chLen
		if entry.init {
			s.credits = append(s.credits, credit{id, entry.recvCap, &entry.name})
			entry.init = false
		} else if consumed*2 >= entry.recvCap { // i.e. consumed >= ceil(recvCap/2)
			s.credits = append(s.credits, credit{id, consumed, nil})
			// forget about the messages the user consumed
			atomic.AddInt64(entry.received(), -consumed)
		}
		// TODO: when much time passes, send credit even if it's small
	}
	s.table.RUnlock()
}
