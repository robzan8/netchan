package netchan

import (
	"errors"
	"reflect"
	"sync/atomic"
)

var (
	errAlreadyPushing = errors.New("netchan pusher: Manager.Push called with channel name already present")
	errSynFlood       = errors.New("netchan pusher: too many half open push requests")
)

const maxHalfOpen = 256

const (
	pushReqCase int = iota
	creditCase
	elemCase = 8 // reserve first 8 slots to other possible select cases
)

type chanEntry struct {
	name    hashedName
	ch      reflect.Value
	credit  int
	present bool
}

type pusher struct {
	toEncoder chan<- element
	creditCh  <-chan credit // from decoder
	pushReq   <-chan addReq
	pushResp  chan<- error
	man       *Manager

	pending  map[hashedName]reflect.Value
	table    []chanEntry
	cases    []reflect.SelectCase
	halfOpen int
}

func (p *pusher) initialize() {
	p.cases = make([]reflect.SelectCase, elemCase)
	p.cases[pushReqCase] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(p.pushReq)}
	p.cases[creditCase] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(p.creditCh)}
	p.names = make([]hashedName, elemCase)
}

func infoByName(table []chanEntry, name hashedName) *chanEntry {
	for i := range table {
		if table[i].name == name {
			return &table[i]
		}
	}
	return nil
}

func (p *pusher) add(ch reflect.Value, name hashedName) error {
	info := infoByName
	if present {
		if p.table[id].ch != (reflect.Value{}) {
			return errAlreadyPushing
		}
		p.table[id].ch = ch
		p.halfOpen--
		return nil
	}
	_, present = p.pending[name]
	if present {
		return errAlreadyPushing
	}
	p.pending[name] = ch
	return nil
}

func (p *pusher) handleCredit(cred credit) error {
	if !cred.open {
		if cred.id < 0 || cred.id >= len(p.chans) {
			return errBadId
		}
		if p.chans[cred.id] == (chanEntry{}) {
			// probably the channel has just been removed,
			// we must ignore the credit, increasing it makes
			// it look like the channel is there
			return nil
		}
		p.chans[cred.id].credit += cred.incr
		return nil
	}
	// credit message wants to open a new channel
	p.index[cred.name] = cred.id
	info := chanEntry{credit: cred.incr}
	ch, present := p.pending[cred.name]
	if present {
		info.ch = ch
		delete(p.pending, cred.name)
	} else {
		p.halfOpen++
		if halfOpen > maxHalfOpen {
			return errSynFlood
		}
	}
	if cred.id == len(p.chans) {
		// cred.id is a fresh slot
		p.chans = append(p.chans, info)
		return nil
	}
	if cred.id >= 0 && cred.id < len(p.chans) {
		// cred.id is an old slot
		if p.chans[cred.id].present {
			// slot is not free
			return errBadId
		}
		p.chans[cred.id] = info
		return nil
	}
	return errBadId
}

func (p *pusher) handleElem(name hashedName, val reflect.Value, ok bool) {
	p.toEncoder <- element{name, val, ok}
	if !ok {
		delete(p.chans, name)
		atomic.AddInt64(&p.count, -1)
		return
	}
	info := p.chans[name]
	info.credit--
	p.chans[name] = info
}

func (p *pusher) bigSelect() (int, reflect.Value, bool) {
	// generate select cases from active chans
	p.cases = p.cases[:elemCase]
	p.names = p.names[:elemCase]
	for name, info := range p.chans {
		if info.credit > 0 {
			p.cases = append(p.cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: info.ch})
			p.names = append(p.names, name)
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
			p.handleElem(p.names[i], val, ok)
		}
	}
}
