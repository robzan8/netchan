package netchan

import (
	"reflect"
	"sync"
	"sync/atomic"
)

type pushEntry struct {
	name   hashedName
	ch     reflect.Value
	credit int64
	_      int64 // padding
}

func isPresent(e *pushEntry) bool {
	return !(e.name == hashedName{})
}

type pushTable struct {
	sync.RWMutex
	t []pushEntry
}

type credPuller struct {
	creditCh <-chan credit
	table    *pushTable
	pending  map[hashedName]reflect.Value
	man      *Manager
}

func (p *credPuller) run() {
	for {
		cred, ok := <-p.creditCh
		if !ok {
			// error occurred and decoder shut down
			return
		}
		var err error
		if cred.name == nil {
			err = p.handleCred(cred)
		} else {
			err = p.handleInitCred(cred)
		}
		if err != nil {
			p.man.signalError(err)
		}
	}
}

func (p *credPuller) handleCred(cred credit) error {
	if cred.id >= len(p.table.t) {
		return errBadId
	}
	entry := &p.table.t[cred.id]
	if !isPresent(entry) {
		// credit to deleted channel, ignore
		return nil
	}
	atomic.AddInt64(&entry.credit, cred.incr)
	return nil
}

func handleInitCred(cred credit) error {
	p.table.Lock()
	defer p.table.Unlock()

	entry := pushEntry{name: *cred.name, credit: cred.incr}
	ch, present := p.pending[*cred.name]
	if present {
		entry.ch = ch
		delete(p.pending, *cred.name)
	} else {
		p.halfOpen++
		if p.halfOpen > maxHalfOpen {
			return errSynFlood
		}
	}
	if cred.id == len(p.table) {
		// cred.id is a fresh slot
		p.table = append(p.table, entry)
		return nil
	}
	if cred.id < len(p.table) {
		// cred.id is an old slot
		if isPresent(p.table.t[cred.id]) {
			// slot is not free
			return errBadId
		}
		p.table[cred.id] = entry
		return nil
	}
	return errBadId
}
