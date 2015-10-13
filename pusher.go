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
	elemCase
)

type pushInfo struct {
	ch     reflect.Value
	credit int
}

type pusher struct {
	toEncoder chan<- element
	creditCh  <-chan credit // from decoder
	pushReq   <-chan addReq
	pushResp  chan<- error
	man       *Manager

	chans map[hashedName]pushInfo
	count int64
	cases []reflect.SelectCase
	names []hashedName
}

func (p *pusher) initialize() {
	p.chans = make(map[hashedName]pushInfo)
	p.cases = make([]reflect.SelectCase, elemCase, elemCase*2)
	p.cases[pushReqCase] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(p.pushReq)}
	p.cases[creditCase] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(p.creditCh)}
	p.names = make([]hashedName, elemCase, elemCase*2)
}

func (p *pusher) add(ch reflect.Value, name hashedName) error {
	info, present := p.chans[name]
	if present && info.ch != (reflect.Value{}) {
		return errAlreadyPushing
	}
	info.ch = ch
	p.chans[name] = info
	atomic.AddInt64(&p.count, 1)
	return nil
}

func (p *pusher) handleCredit(cred credit) {
	halfOpen := int64(len(p.chans)) - atomic.LoadInt64(&p.count)
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
	p.chans[cred.chName] = info
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
