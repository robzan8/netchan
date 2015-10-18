package netchan

import (
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

var (
	errAlreadyPushing = errors.New("netchan: Manager.Push called with channel name already present")
	errOldIdsUnused   = errors.New("netchan: peer keeps using fresh ids instead of recycling old ones")
	errSynFlood       = errors.New("netchan: too many half open net channels")
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

func (p *pusher) add(ch reflect.Value, name hashedName) error {
	p.table.Lock()
	defer p.table.Unlock()

	var entry *pushEntry
	for i := range p.table.t {
		e := &p.table.t[i]
		if e.present && e.name == name {
			entry = e
			break
		}
	}
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

func (p *pusher) handleElem(elem element) {
	atomic.AddInt64(&p.table.t[elem.id].credit, -1)
	p.table.RUnlock()
	p.toEncoder <- elem
	if !elem.ok {
		// channel has been closed;
		// an initial credit may arrive in this moment and add a new
		// entry in the table, possibly triggering a whole reallocation of
		// the underlying array, but our id will remain the same and
		// our entry will be untouched.
		p.table.Lock()
		p.table.t[elem.id] = pushEntry{}
		p.table.Unlock()
	}
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
			p.handleElem(element{p.ids[i], val, ok})
			// handleElem does table.RUnlock()
		default:
			p.table.RUnlock()
			time.Sleep(10 * time.Millisecond)
		}
	}
	close(p.toEncoder)
}

type credReceiver struct {
	creditCh <-chan credit
	table    *pushTable
	man      *Manager
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
			r.man.signalError(err)
		}
	}
}

func (r *credReceiver) handleCred(cred credit) error {
	r.table.RLock()
	if cred.id >= len(r.table.t) {
		r.table.RUnlock()
		return errInvalidId
	}
	// it may happen that the entry is not present, because the channel
	// has just been closed; increasing the credit doesn't do any damage
	atomic.AddInt64(&r.table.t[cred.id].credit, cred.incr)
	r.table.RUnlock()
	return nil
}

func (r *credReceiver) handleInitCred(cred credit) error {
	r.table.Lock()
	defer r.table.Unlock()

	entry := pushEntry{name: *cred.name, present: true, credit: cred.incr}
	ch, present := r.table.pending[*cred.name]
	if present {
		entry.ch = ch
		delete(r.table.pending, *cred.name)
	} else {
		halfOpen := 0
		for _, e := range r.table.t {
			if e.present && e.ch == (reflect.Value{}) {
				halfOpen++
			}
		}
		if halfOpen > maxHalfOpen {
			return errSynFlood
		}
	}
	if cred.id == len(r.table.t) {
		// cred.id is a fresh slot
		holes := 0
		for _, e := range r.table.t {
			if !e.present {
				holes++
			}
		}
		if holes > maxHoles {
			return errOldIdsUnused
		}
		r.table.t = append(r.table.t, entry)
		return nil
	}
	if cred.id < len(r.table.t) {
		// cred.id is an old slot
		if r.table.t[cred.id].present {
			// slot is not free
			return errInvalidId
		}
		r.table.t[cred.id] = entry
		return nil
	}
	return errInvalidId
}
