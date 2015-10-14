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

type pushInfo struct {
	ch     reflect.Value
	name   hashedName
	credit int
}

type pusher struct {
	toEncoder chan<- element
	creditCh  <-chan credit // from decoder
	pushReq   <-chan addReq
	pushResp  chan<- error
	man       *Manager

	halfOpen int
	pending  map[hashedName]reflect.Value
	chans    []pushInfo
	cases    []reflect.SelectCase
}

func (p *pusher) initialize() {
	p.cases = make([]reflect.SelectCase, elemCase)
	p.cases[pushReqCase] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(p.pushReq)}
	p.cases[creditCase] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(p.creditCh)}
	p.names = make([]hashedName, elemCase)
}

func infoByName(chans []pushInfo, name hashedName) *pushInfo {
	for i := range chans {
		if chans[i].name == name {
			return &chans[i]
		}
	}
	return nil
}

func (p *pusher) add(ch reflect.Value, name hashedName) error {
	info := infoByName(p.chans, name)
	if info != nil {
		if info.ch != (reflect.Value{}) {
			return errAlreadyPushing
		}
		info.ch = ch
		p.halfOpen--
		return nil
	}
	p.pending[name] = ch
	// check if present? how dumb can the user be?
	return nil
}

func (p *pusher) handleCredit(cred credit) error {
	if cred.name == "" {
		if cred.id < 0 || cred.id >= len(p.chans) {
			return errBadId
		}
		p.chans[cred.id].credit += cred.incr
		return nil
	}
	// credit message wants to open a new channel
	info := pushInfo{name: cred.name, credit: cred.incr}
	ch, present := p.pending[cred.name]
	if present {
		info.ch = ch
		delete(p.pending, cred.name)
	}
	if cred.id == len(p.chans) {
		// cred.id is a fresh slot
		p.chans = append(p.chans, info)
		return nil
	}
	if cred.id >= 0 && cred.id < len(p.chans) {
		// cred.id is an old slot
		if p.chans != (pushInfo{}) {
			return errBadId
		}
		p.chans[cred.id] = info
		return nil
	}
	/* there can be "holes" in p.chans
	if halfOpen >= maxHalfOpen {
		p.man.signalError(errSynFlood)
		return
	}
	info, present := p.chans[cred.chName]
	if !present && !cred.open {
		// we got a credit message for a channel that we are not pushing
		// and it's not a new channel that peer wants to open;
		// might happen when a channel is being closed, ignore it
		return
	}
	info.credit += cred.incr
	p.chans[cred.chName] = info*/
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
			p.handleCredit(val.Interface().(credit))
		default: // i >= elemCase
			p.handleElem(p.names[i], val, ok)
		}
	}
}
