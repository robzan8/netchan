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
	ch  reflect.Value
	win int
}

type pusher struct {
	toEncoder chan<- element
	winupCh   <-chan winUpdate
	addReqCh  <-chan addReq
	man       *Manager

	chans    map[hashedName]*pushInfo
	cases    []reflect.SelectCase
	names    []hashedName
	halfOpen int
}

const maxHalfOpen = 100

const (
	addReqCase int = iota
	winupCase
	elemCase
)

func newPusher(toEncoder chan<- element, winupCh <-chan winUpdate, addReqCh <-chan addReq, man *Manager) *pusher {
	p := &pusher{toEncoder: toEncoder, winupCh: winupCh, addReqCh: addReqCh, man: man}
	p.chans = make(map[hashedName]*pushInfo)
	p.cases = make([]reflect.SelectCase, elemCase, elemCase*2)
	p.cases[addReqCase] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(addReqCh)}
	p.cases[winupCase] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(winupCh)}
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

func (p *pusher) handleWinup(winup winUpdate) {
	info, present := p.chans[winup.ChName]
	if !present {
		if p.halfOpen >= maxHalfOpen {
			p.man.signalError(errSynFlood)
			return
		}
		info = new(pushInfo)
		p.chans[winup.ChName] = info
		p.halfOpen++
	}
	info.win += winup.Incr
}

func (p *pusher) handleElem(name hashedName, val reflect.Value, ok bool) {
	p.toEncoder <- element{name, val, ok}
	if !ok {
		delete(p.chans, name)
		return
	}
	p.chans[name].win--
}

func (p *pusher) run() {
	for {
		// generate select cases from active chans
		p.cases = p.cases[:elemCase]
		p.names = p.names[:elemCase]
		for name, info := range p.chans {
			if info.win > 0 {
				p.cases = append(p.cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: info.ch})
				p.names = append(p.names, name)
			}
		}

		i, val, ok := reflect.Select(p.cases)
		switch i {
		case addReqCase:
			req := val.Interface().(addReq)
			req.resp <- p.add(req.ch, req.name)

		case winupCase:
			if !ok {
				// error occurred and decoder shut down
				close(p.toEncoder)
				return
			}
			p.handleWinup(val.Interface().(winUpdate))

		default:
			p.handleElem(p.names[i], val, ok)
		}
	}
}
