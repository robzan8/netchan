package netchan

import (
	"errors"
	"reflect"
	"sync"
)

var (
	errAlreadyPulling = errors.New("netchan puller: Manager.Pull called with channel name already present")
	errWinExceeded    = errors.New("netchan puller: receive buffer limit not respected by peer")
)

type pullInfo struct {
	buf chan reflect.Value
}

type typeMap struct {
	sync.Mutex
	m map[chanID]reflect.Type
}

type puller struct {
	elemCh    <-chan element // from decoder
	toEncoder chan<- winUpdate
	addReqCh  <-chan addReq // from Manager.Pull (user)
	man       *Manager

	chans map[chanID]*pullInfo
	types *typeMap
}

const bufCap = 512

func (p *puller) add(ch reflect.Value, id chanID) error {
	_, present := p.chans[id]
	if present {
		return errAlreadyPulling
	}
	buf := make(chan reflect.Value, bufCap)
	p.chans[id] = &pullInfo{buf}
	p.types.Lock()
	p.types.m[id] = ch.Type().Elem()
	p.types.Unlock()

	go bufferer(buf, ch, p.toEncoder, id)
	return nil
}

func (p *puller) handleElem(elem element) {
	info := p.chans[elem.ChID]
	if !elem.Ok { // netchan closed normally
		close(info.buf)
		delete(p.chans, elem.ChID)
		p.types.Lock()
		delete(p.types.m, elem.ChID)
		p.types.Unlock()
		return
	}
	if len(info.buf) == cap(info.buf) {
		p.man.signalError(errWinExceeded)
		return
	}
	info.buf <- elem.val
}

func bufferer(buf <-chan reflect.Value, ch reflect.Value, toEncoder chan<- winUpdate, id chanID) {
	toEncoder <- winUpdate{id, bufCap}
	sent := 0
	for {
		val, ok := <-buf
		if !ok {
			ch.Close()
			return
		}
		ch.Send(val)
		sent++
		if sent*2 >= bufCap { // i.e. sent >= ceil(bufCap/2)
			toEncoder <- winUpdate{id, sent}
			sent = 0
		}
	}
}

func (p *puller) run() {
	for {
		select {
		case req := <-p.addReqCh:
			req.resp <- p.add(req.ch, req.id)

		case elem, ok := <-p.elemCh:
			if !ok {
				// error occurred and decoder shut down
				for _, info := range p.chans {
					close(info.buf)
				}
				close(p.toEncoder)
				return
			}
			p.handleElem(elem)
		}
	}
}
