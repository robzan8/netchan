package netchan

import "reflect"

type pushInfo struct {
	ch  reflect.Value
	win int
}

type pusher struct {
	toEncoder chan<- element
	winupCh   <-chan winUpdate
	addReqCh  <-chan addReq

	chans map[string]*pushInfo
	cases []reflect.SelectCase
	names []string
}

const (
	addReqCase int = iota
	winupCase
	elemCase
)

func newPusher(toEncoder chan<- element, winupCh <-chan winUpdate, addReqCh <-chan addReq) *pusher {
	p := &pusher{toEncoder: toEncoder, winupCh: winupCh, addReqCh: addReqCh}
	p.chans = make(map[string]*pushInfo)
	p.cases = make([]reflect.SelectCase, elemCase)
	p.cases[addReqCase] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(addReqCh)}
	p.cases[winupCase] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(winupCh)}
	p.names = make([]string, elemCase)
	return p
}

func (p *pusher) add(ch reflect.Value, name string) error {
	info, present := p.chans[name]
	if present {
		if info.ch == (reflect.Value{}) {
			info.ch = ch
			return nil
		}
		return nil // err: adding chan twice
	}
	p.chans[name] = &pushInfo{ch: ch}
	return nil
}

func (p *pusher) handleWinup(winup winUpdate) {
	info, present := p.chans[winup.Name]
	if !present {
		info = new(pushInfo)
		p.chans[winup.Name] = info
	}
	info.win += winup.Incr
}

func (p *pusher) handleElem(name string, val reflect.Value, ok bool) {
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
			p.handleWinup(val.Interface().(winUpdate))
		default:
			p.handleElem(p.names[i], val, ok)
		}
	}
}
