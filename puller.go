package netchan

import (
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

var (
	errAlreadyPulling = errors.New("netchan: Manager.Pull called with channel name already present")
	errCredExceeded   = errors.New("netchan: peer sent more elements than its credit allowed")
)

type pullEntry struct {
	name     hashedName
	present  bool
	init     bool
	ch       reflect.Value
	chCap    int64
	received int64
}

type pullTable struct {
	sync.RWMutex
	t []pullEntry
}

type puller struct {
	elemCh <-chan element
	table  *pullTable
	man    *Manager
}

func (p *puller) add(ch reflect.Value, name hashedName) error {
	p.table.Lock()
	defer p.table.Unlock()

	for _, entry := range p.table.t {
		if entry.name == name {
			return errAlreadyPulling
		}
	}
	// get an id for the new channel
	id := len(p.table.t)
	for i := range p.table.t {
		if !p.table.t[i].present {
			id = i
			break
		}
	}
	entry := pullEntry{name, true, true, ch, int64(ch.Cap()), 0}
	if id == len(p.table.t) {
		p.table.t = append(p.table.t, entry)
	} else {
		p.table.t[id] = entry
	}
	return nil
}

func (p *puller) handleElem(elem element) error {
	p.table.RLock()
	if elem.id >= len(p.table.t) {
		p.table.RUnlock()
		return errInvalidId
	}
	entry := &p.table.t[elem.id]
	if !entry.present {
		p.table.RUnlock()
		return errInvalidId
	}
	if !elem.ok { // netchan closed normally
		p.table.RUnlock()
		// table array can be reallocated here
		p.table.Lock()
		p.table.t[elem.id].ch.Close()
		p.table.t[elem.id] = pullEntry{}
		p.table.Unlock()
		return nil
	}
	ch := entry.ch
	chCap := int(entry.chCap)
	p.table.RUnlock()
	if ch.Len() == chCap {
		return errCredExceeded
	}
	// do not swap Send and AddInt64 operations
	ch.Send(elem.val)
	p.table.RLock()
	atomic.AddInt64(&p.table.t[elem.id].received, 1)
	p.table.RUnlock()
	return nil
}

func (p *puller) run() {
	for {
		elem, ok := <-p.elemCh
		if !ok {
			// error occurred and decoder shut down
			p.table.Lock()
			for _, entry := range p.table.t {
				entry.ch.Close()
			}
			p.table.Unlock()
			return
		}
		err := p.handleElem(elem)
		if err != nil {
			p.man.signalError(err)
		}
	}
}

type credSender struct {
	toEncoder chan<- credit
	table     *pullTable
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
