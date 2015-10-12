package netchan

import (
	"errors"
	"reflect"
)

var (
	errAlreadyPushing = errors.New("netchan pusher: Manager.Push called with channel name already present")
	errSynFlood       = errors.New("netchan pusher: too many half open push requests")
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

	chans    map[hashedName]*pushInfo
	cases    []reflect.SelectCase
	names    []hashedName
	halfOpen int
}

const maxHalfOpen = 256

const (
	pushReqCase int = iota
	creditCase
	elemCase
)

func newPusher(toEncoder chan<- element, creditCh <-chan credit, pushReq <-chan addReq, pushResp chan<- error, man *Manager) *pusher {
	p := &pusher{toEncoder: toEncoder, creditCh: creditCh, pushReq: pushReq, pushResp: pushResp, man: man}
	p.chans = make(map[hashedName]*pushInfo)
	p.cases = make([]reflect.SelectCase, elemCase, elemCase*2)
	p.cases[pushReqCase] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(pushReq)}
	p.cases[creditCase] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(creditCh)}
	p.names = make([]hashedName, elemCase, elemCase*2)
	return p
}

func (p *pusher) add(ch reflect.Value, name hashedName) error {
	info, present := p.chans[name]
	if present {
		if info.ch != (reflect.Value{}) {
			return errAlreadyPushing
		}
		info.ch = ch
		p.halfOpen--
		return nil
	}
	p.chans[name] = &pushInfo{ch: ch}
	return nil
}

func (p *pusher) handleCredit(cred credit) {
	info, present := p.chans[cred.chName]
	if !present {
		if p.halfOpen >= maxHalfOpen {
			p.man.signalError(errSynFlood)
			return
		}
		info = new(pushInfo)
		p.chans[cred.chName] = info
		p.halfOpen++
	}
	info.credit += cred.incr
}

func (p *pusher) handleElem(name hashedName, val reflect.Value, ok bool) {
	p.toEncoder <- element{name, val, ok}
	if !ok {
		delete(p.chans, name)
		return
	}
	p.chans[name].credit--
}

func (p *pusher) run() {
	for {
		// generate select cases from active chans
		p.cases = p.cases[:elemCase]
		p.names = p.names[:elemCase]
		for name, info := range p.chans {
			if info.credit > 0 {
				p.cases = append(p.cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: info.ch})
				p.names = append(p.names, name)
			}
		}

		i, val, ok := reflect.Select(p.cases)
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
