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
	m map[hashedName]reflect.Type
}

type puller struct {
	elemCh    <-chan element // from decoder
	toEncoder chan<- winUpdate
	addReqCh  <-chan addReq // from Manager.Pull (user)
	man       *Manager

	chans map[hashedName]*pullInfo
	types *typeMap
}

const bufCap = 512

func (p *puller) add(ch reflect.Value, name hashedName) error {
	_, present := p.chans[name]
	if present {
		return errAlreadyPulling
	}
	buf := make(chan reflect.Value, bufCap)
	p.chans[name] = &pullInfo{buf}
	p.types.Lock()
	p.types.m[name] = ch.Type().Elem()
	p.types.Unlock()

	go bufferer(buf, ch, p.toEncoder, name)
	return nil
}

func (p *puller) handleElem(elem element) {
	info := p.chans[elem.chName]
	if !elem.ok { // netchan closed normally
		close(info.buf)
		delete(p.chans, elem.chName)
		p.types.Lock()
		delete(p.types.m, elem.chName)
		p.types.Unlock()
		return
	}
	if len(info.buf) == cap(info.buf) {
		p.man.signalError(errWinExceeded)
		return
	}
	info.buf <- elem.val
}

func bufferer(buf <-chan reflect.Value, ch reflect.Value, toEncoder chan<- winUpdate, name hashedName) {
	toEncoder <- winUpdate{name, bufCap}
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
			toEncoder <- winUpdate{name, sent}
			sent = 0
		}
	}
}

func (p *puller) run() {
	for {
		select {
		case req := <-p.addReqCh:
			req.resp <- p.add(req.ch, req.name)

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
