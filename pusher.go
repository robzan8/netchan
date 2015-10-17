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
	errOldIdsUnused   = errors.New("netchan pusher: peer keeps using fresh ids instead of old ones")
	errSynFlood       = errors.New("netchan pusher: too many half open push requests")
)

const (
	maxHalfOpen = 256
	maxHoles    = 256
)

type pushEntry struct {
	name    hashedName
	present bool
	ch      reflect.Value
	credit  int64
	_       int64 // padding
}

type pushTable struct {
	sync.RWMutex
	t       []pushEntry
	pending map[hashedName]reflect.Value
}

type pusher struct {
	toEncoder chan<- element
	table     *pushTable
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
		if entry.present && entry.name == name {
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
		return nil
	}
	_, present := p.table.pending[name]
	if present {
		return errAlreadyPushing
	}
	p.table.pending[name] = ch
	return nil
}

func (p *pusher) run() {
	for p.man.Error() == nil {
		p.table.RLock()
		p.cases = p.cases[:1]
		p.ids = p.ids[:1]
		for id := range p.table.t {
			entry := &p.table.t[id]
			cred := atomic.LoadInt64(&entry.credit)
			if entry.present && cred > 0 {
				p.cases = append(p.cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: entry.ch})
				p.ids = append(p.ids, id)
			}
		}
		i, val, ok := reflect.Select(p.cases)
		switch {
		case i > 0:
			id := p.ids[i]
			atomic.AddInt64(&p.table.t[id].credit, -1)
			p.table.RUnlock()
			p.toEncoder <- element{id, val, ok}
			if !ok {
				// channel has been closed;
				// an initial credit may arrive in this moment and add a new
				// entry in the table, possibly triggering a whole reallocation of
				// the underlying array, but our id will remain the same and
				// our entry will be untouched.
				p.table.Lock()
				p.table.t[id] = pushEntry{}
				p.table.Unlock()
			}
			// handleElem does table.RUnlock()
		default:
			p.table.RUnlock()
			time.Sleep(1 * time.Millisecond)
		}
	}
	close(p.toEncoder)
}

type credPuller struct {
	creditCh <-chan credit
	table    *pushTable
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
	p.table.RLock()
	if cred.id >= len(p.table.t) {
		p.table.RUnlock()
		return errBadId
	}
	// it may happen that the entry is not present, because the channel
	// has just been closed; increasing the credit doesn't do any damage
	atomic.AddInt64(&p.table.t[cred.id].credit, cred.incr)
	p.table.RUnlock()
	return nil
}

func (p *credPuller) handleInitCred(cred credit) error {
	p.table.Lock()
	defer p.table.Unlock()

	entry := pushEntry{name: *cred.name, present: true, credit: cred.incr}
	ch, present := p.table.pending[*cred.name]
	if present {
		entry.ch = ch
		delete(p.table.pending, *cred.name)
	} else {
		halfOpen := 0
		for _, e := range p.table.t {
			if e.present && e.ch == (reflect.Value{}) {
				halfOpen++
			}
		}
		if halfOpen > maxHalfOpen {
			return errSynFlood
		}
	}
	if cred.id == len(p.table.t) {
		// cred.id is a fresh slot
		holes := 0
		for _, e := range p.table.t {
			if !e.present {
				holes++
			}
		}
		if holes > maxHoles {
			return errOldIdsUnused
		}
		p.table.t = append(p.table.t, entry)
		return nil
	}
	if cred.id < len(p.table.t) {
		// cred.id is an old slot
		if p.table.t[cred.id].present {
			// slot is not free
			return errBadId
		}
		p.table.t[cred.id] = entry
		return nil
	}
	return errBadId
}
