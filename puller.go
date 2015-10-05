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
	m map[string]reflect.Type
}

type puller struct {
	elemCh    <-chan element // from decoder
	toEncoder chan<- winUpdate
	addReqCh  <-chan addReq // from Manager.Pull (user)

	chans map[string]*pullInfo
	types *typeMap
}

func newPuller(elemCh <-chan element, toEncoder chan<- winUpdate, addReqCh <-chan addReq, types *typeMap) *puller {
	p := &puller{elemCh, toEncoder, addReqCh, make(map[string]*pullInfo), types}
	return p
}

func (p *puller) add(ch reflect.Value, name string, bufSize int) error {
	_, present := p.chans[name]
	if present {
		return nil // error, adding chan already present
	}
	buf := make(chan reflect.Value, bufSize)
	p.chans[name] = &pullInfo{buf}
	p.types.Lock()
	p.types.m[name] = ch.Type().Elem()
	p.types.Unlock()

	go bufferer(buf, ch, p.toEncoder, name, bufSize)
	return nil
}

func (p *puller) handleElem(elem element) {
	info := p.chans[elem.Name]
	if elem.Ok {
		// when to discard elem because of full buffers because of misbehaving peer
		info.buf <- elem.val
	} else {
		close(info.buf)
		delete(p.chans, elem.Name)
		p.types.Lock()
		delete(p.types.m, elem.Name)
		p.types.Unlock()
	}
}

func bufferer(buf <-chan reflect.Value, ch reflect.Value, toEncoder chan<- winUpdate, name string, bufSize int) {
	toEncoder <- winUpdate{name, bufSize}
	sent := 0
	for {
		val, ok := <-buf
		if !ok {
			ch.Close()
			return
		}
		ch.Send(val)
		sent++
		if sent*2 >= bufSize { // i.e. sent >= ceil(bufSize/2)
			toEncoder <- winUpdate{name, sent}
			sent = 0
		}
	}
}

func (p *puller) run() {
	for {
		select {
		case req := <-p.addReqCh:
			req.resp <- p.add(req.ch, req.name, req.bufSize)
		case elem := <-p.elemCh:
			p.handleElem(elem)
		}
	}
}
