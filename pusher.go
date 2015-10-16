package netchan

import (
	"errors"
	"reflect"
)

var (
	errAlreadyPushing = errors.New("netchan pusher: Manager.Push called with channel name already present")
	errSynFlood       = errors.New("netchan pusher: too many half open push requests")
)

const maxHalfOpen = 256

const (
	pushReqCase int = iota
	creditCase
	elemCase
)

type pushEntry struct {
	name    hashedName
	ch      reflect.Value
	credit  int64
	padding int64
}

type pusher struct {
	toEncoder chan<- element
	creditCh  <-chan credit // from decoder
	pushReq   <-chan addReq
	pushResp  chan<- error
	man       *Manager

	pending  map[hashedName]reflect.Value
	table    []pushEntry
	cases    []reflect.SelectCase
	ids      []int
	halfOpen int
}

func (p *pusher) initialize() {
	p.pending = make(map[hashedName]reflect.Value)
	p.cases = make([]reflect.SelectCase, elemCase)
	p.cases[pushReqCase] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(p.pushReq)}
	p.cases[creditCase] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(p.creditCh)}
	p.ids = make([]int, elemCase)
}

func (p *pusher) entryByName(name hashedName) *pushEntry {
	for i := range p.table {
		if p.table[i].name == name {
			return &p.table[i]
		}
	}
	return nil
}

func (p *pusher) add(ch reflect.Value, name hashedName) error {
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

func (p *pusher) handleCredit(cred credit) error {
	if !cred.open {
		if cred.id >= len(p.table) {
			return errBadId
		}
		if p.table[cred.id].name == (hashedName{}) {
			// credit to deleted channel, ignore
			return nil
		}
		p.table[cred.id].credit += cred.incr
		return nil
	}
	// credit message wants to open a new channel
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
		if p.table[cred.id].name != (hashedName{}) {
			// slot is not free
			return errBadId
		}
		p.table[cred.id] = entry
		return nil
	}
	return errBadId
}

func (p *pusher) handleElem(elem element) {
	p.toEncoder <- elem
	if !elem.ok {
		// channel has been closed
		p.table[elem.id].name = hashedName{}
		return
	}
	p.table[elem.id].credit--
}

func (p *pusher) bigSelect() (int, reflect.Value, bool) {
	// generate select cases from active chans
	p.cases = p.cases[:elemCase]
	p.ids = p.ids[:elemCase]
	for id, entry := range p.table {
		if entry.credit > 0 && entry.name != (hashedName{}) {
			p.cases = append(p.cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: entry.ch})
			p.ids = append(p.ids, id)
		}
	}
	return reflect.Select(p.cases)
}

func (p *pusher) run() {
	for {
		i, val, ok := p.bigSelect()
		switch i {
		case pushReqCase:
			req := val.Interface().(addReq)
			p.pushResp <- p.add(req.ch, req.name)
		case creditCase:
			if !ok {
				// error occurred and decoder shut down
				close(p.toEncoder)
				return
			}
			err := p.handleCredit(val.Interface().(credit))
			if err != nil {
				p.man.signalError(err)
			}
		default: // i >= elemCase
			p.handleElem(element{p.ids[i], val, ok})
		}
	}
}
