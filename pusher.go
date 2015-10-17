package netchan

import (
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

var (
	errAlreadyPushing = errors.New("netchan pusher: Manager.Push called with channel name already present")
	errSynFlood       = errors.New("netchan pusher: too many half open push requests")
)

const maxHalfOpen = 256

type pushEntry struct {
	name   hashedName
	ch     reflect.Value
	credit int64
	_      int64 // padding
}

func (e *pushEntry) isPresent() bool {
	return e.name != hashedName{}
}

func (e *pushEntry) delete() {
	e.name = hashedName{}
}

type pushTable struct {
	sync.RWMutex
	t []pushEntry
}

type pusher struct {
	toEncoder chan<- element
	pending   map[hashedName]reflect.Value
	table     *pushTable
	halfOpen  *int64
	man       *Manager

	cases []reflect.SelectCase
	ids   []int
}

func (p *pusher) initialize() {
	p.cases = []reflect.SelectCase{reflect.SelectCase{Dir: reflect.SelectDefault}}
	p.ids = []int{-1}
}

func (p *pusher) entryByName(name hashedName) *pushEntry {
	for i := range p.table.t {
		entry := &p.table.t[i]
		if entry.name == name {
			return entry
		}
	}
	return nil
}

func (p *pusher) add(ch reflect.Value, name hashedName) error {
	p.table.Lock()
	defer p.table.Unlock()

	entry := p.entryByName(name)
	if entry != nil {
		if entry.ch != (reflect.Value{}) {
			return errAlreadyPushing
		}
		entry.ch = ch
		p.halfOpen--
		return nil
	}
	_, present := p.pending[name]
	if present {
		return errAlreadyPushing
	}
	p.pending[name] = ch
	return nil
}

func (p *pusher) handleElem(elem element) {
	p.toEncoder <- elem
	entry := &p.table[elem.id]
	if !elem.ok {
		// channel has been closed
		entry.delete()
		return
	}
	atomic.AddInt64(&entry.credit, -1)
}

func (p *pusher) bigSelect() (int, reflect.Value, bool) {
	// generate select cases from active chans
	p.cases = p.cases[:1]
	p.ids = p.ids[:1]
	for id := range p.table.t {
		entry := &p.table.t[id]
		if !entry.isPresent() {
			continue
		}
		cred := atomic.LoadInt64(&entry.credit)
		if cred > 0 {
			p.cases = append(p.cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: entry.ch})
			p.ids = append(p.ids, id)
		}
	}
	return reflect.Select(p.cases)
}

func (p *pusher) run() {
	for p.man.Error() == nil {
		i, val, ok := p.bigSelect()
		switch {
		case i > 0:
			p.handleElem(element{p.ids[i], val, ok})
		default:
			time.Sleep(1 * time.Millisecond)
		}
	}
	close(toEncoder)
}

type credPuller struct {
	creditCh <-chan credit
	table    *pushTable
	pending  map[hashedName]reflect.Value
	halfOpen *int64
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
		// check here if too many holes, throw an error
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
