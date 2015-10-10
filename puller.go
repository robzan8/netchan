package netchan

import (
	"reflect"
	"sync"
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

func newPuller(elemCh <-chan element, toEncoder chan<- winUpdate, addReqCh <-chan addReq, man *Manager, types *typeMap) *puller {
	p := &puller{elemCh, toEncoder, addReqCh, man, make(map[chanID]*pullInfo), types}
	return p
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

func (p *puller) handleElem(elem element) error {
	info := p.chans[elem.ChID]
	if !elem.Ok {
		close(info.buf)
		delete(p.chans, elem.ChID)
		p.types.Lock()
		delete(p.types.m, elem.ChID)
		p.types.Unlock()
		return nil
	}
	if len(info.buf) == cap(info.buf) {
		return errWinExceeded
	}
	info.buf <- elem.val
	return nil
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
			if !ok { // decoder signaled an error
				return p.handleError(p.man.Error())
			}
			err := p.handleElem(elem)
			if err != nil {
				return p.handleError(err)
			}
		}
	}
}

func (p *puller) handleError(err) {
	p.man.signalError(err)
	// do we need to delete info from chan map?
	for _, info := range p.chans {
		close(info.buf)
	}
}
