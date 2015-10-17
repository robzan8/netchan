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
	name  hashedName
	ch    reflect.Value
	chCap int64
	// quando puller butta un elemento nel buffer, incrementa received
	received int64
}

func (e *pullEntry) isPresent() bool {
	return e.name != hashedName{}
}

func (e *pullEntry) delete() {
	e.name = hashedName{}
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
		if !(&p.table.t[i]).isPresent() {
			id = i
			break
		}
	}
	chCap := int64(ch.Cap())
	entry := pullEntry{name, ch, chCap, 0}
	if id == len(p.table.t) {
		p.table = append(p.table, entry)
	} else {
		p.table.t[id] = entry
	}

	p.toEncoder <- credit{id, chCap, &name} // nooooooooooooooo
	return nil
}

func (p *puller) handleElem(elem element) error {
	if elem.id >= len(p.table.t) {
		return errBadId
	}
	entry := &p.table.t[elem.id]
	if !entry.isPresent() {
		return errBadId
	}
	if !elem.ok { // netchan closed normally
		p.table.Lock()
		entry.ch.Close()
		entry.delete()
		p.table.Unlock()
		return nil
	}
	if entry.ch.Len() == entry.ch.Cap() {
		return errCredExceeded
	}
	entry.ch.Send(elem.val)
	return nil
}

func (p *puller) run() {
	for {
		elem, ok := <-p.elemCh
		if !ok {
			// error occurred and decoder shut down
			for _, entry := range p.table.t {
				entry.ch.Close()
			}
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
	credits   []credit
	man       *Manager
}

func (p *credPusher) run() {
	for p.man.Error() == nil {
		time.Sleep(1 * time.Millisecond)

		p.updateCredits()
		for _, cred := range credits {
			toEncoder <- cred
		}
	}
	close(toEncoder)
}

func (p *credPusher) updateCredits() {
	p.table.RLock()
	p.credits = p.credits[:0]
	for id := range p.table.t {
		entry := &p.table.t[id]
		if !isPresent(entry) {
			continue
		}
		// do not swap the next two lines
		received := atomic.LoadInt64(&entry.received)
		chLen := int64(entry.ch.Len())
		consumed := received - chLen
		if consumed*2 >= entry.chCap { // i.e. consumed >= ceil(chCap/2)
			p.credits = append(p.credits, credit{id, consumed, nil})
			atomic.AddInt64(&entry.received, -consumed)
		}
	}
	table.RUnlock()
}
