package netchan

import (
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

var (
	errAlreadyPulling = errors.New("netchan puller: Manager.Pull called with channel name already present")
	errCredExceeded   = errors.New("netchan puller: peer sent more elements than its credit allowed")
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
	chCap := int64(ch.Cap())
	entry := pullEntry{name, true, true, ch, chCap, 0}
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
		return errBadId
	}
	if !p.table.t[elem.id].present {
		p.table.RUnlock()
		return errBadId
	}
	if !elem.ok { // netchan closed normally
		p.table.RUnlock()
		// table array can be reallocated here
		p.table.Lock()
		entry := &p.table.t[elem.id]
		entry.ch.Close()
		*entry = pullEntry{}
		p.table.Unlock()
		return nil
	}
	entry := &p.table.t[elem.id]
	if entry.ch.Len() == entry.ch.Cap() {
		p.table.RUnlock()
		return errCredExceeded
	}
	// do not swap the next two lines
	entry.ch.Send(elem.val)
	atomic.AddInt64(&entry.received, 1)
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

type credPusher struct {
	toEncoder chan<- credit
	table     *pullTable
	man       *Manager

	credits []credit
}

func (p *credPusher) run() {
	for p.man.Error() == nil {
		p.updateCredits()
		for _, cred := range p.credits {
			p.toEncoder <- cred
		}
		time.Sleep(1 * time.Millisecond)
	}
	close(p.toEncoder)
}

func (p *credPusher) updateCredits() {
	p.table.RLock()
	p.credits = p.credits[:0]
	for id := range p.table.t {
		entry := &p.table.t[id]
		if !entry.present {
			continue
		}
		// do not swap the next two lines
		received := atomic.LoadInt64(&entry.received)
		chLen := int64(entry.ch.Len())
		consumed := received - chLen
		if entry.init {
			p.credits = append(p.credits, credit{id, entry.chCap, &entry.name})
			entry.init = false
		} else if consumed*2 >= entry.chCap { // i.e. consumed >= ceil(chCap/2)
			p.credits = append(p.credits, credit{id, consumed, nil})
			atomic.AddInt64(&entry.received, -consumed)
		}
	}
	p.table.RUnlock()
}
