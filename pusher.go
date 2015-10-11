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

	chans    map[chanID]*pushInfo
	cases    []reflect.SelectCase
	chIDs    []chanID
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
	p.chans = make(map[chanID]*pushInfo)
	p.cases = make([]reflect.SelectCase, elemCase, elemCase*2)
	p.cases[addReqCase] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(addReqCh)}
	p.cases[winupCase] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(winupCh)}
	p.chIDs = make([]chanID, elemCase, elemCase*2)
	return p
}

func (p *pusher) add(ch reflect.Value, id chanID) error {
	info, present := p.chans[id]
	if present {
		if info.ch != (reflect.Value{}) {
			return errAlreadyPushing
		}
		info.ch = ch
		p.halfOpen--
		return nil
	}
	p.chans[id] = &pushInfo{ch: ch}
	return nil
}

func (p *pusher) handleWinup(winup winUpdate) {
	info, present := p.chans[winup.ChID]
	if !present {
		if p.halfOpen >= maxHalfOpen {
			p.man.signalError(errSynFlood)
			return
		}
		info = new(pushInfo)
		p.chans[winup.ChID] = info
		p.halfOpen++
	}
	info.win += winup.Incr
}

func (p *pusher) handleElem(id chanID, val reflect.Value, ok bool) {
	p.toEncoder <- element{id, val, ok}
	if !ok {
		delete(p.chans, id)
		return
	}
	p.chans[id].win--
}

func (p *pusher) run() {
	for {
		// generate select cases from active chans
		p.cases = p.cases[:elemCase]
		p.chIDs = p.chIDs[:elemCase]
		for id, info := range p.chans {
			if info.win > 0 {
				p.cases = append(p.cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: info.ch})
				p.chIDs = append(p.chIDs, id)
			}
		}

		i, val, ok := reflect.Select(p.cases)
		switch i {
		case addReqCase:
			req := val.Interface().(addReq)
			req.resp <- p.add(req.ch, req.id)

		case winupCase:
			if !ok {
				// error occurred and decoder shut down
				close(p.toEncoder)
				return
			}
			p.handleWinup(val.Interface().(winUpdate))

		default:
			p.handleElem(p.chIDs[i], val, ok)
		}
	}
}
