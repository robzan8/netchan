package netchan

import (
	"log"
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

	chans map[chanID]*pullInfo
	types *typeMap
}

func newPuller(elemCh <-chan element, toEncoder chan<- winUpdate, addReqCh <-chan addReq, types *typeMap) *puller {
	p := &puller{elemCh, toEncoder, addReqCh, make(map[chanID]*pullInfo), types}
	return p
}

func (p *puller) add(ch reflect.Value, id chanID, bufCap int) error {
	_, present := p.chans[id]
	if present {
		log.Fatal("netchan puller: adding channel already present")
		return nil
	}
	buf := make(chan reflect.Value, bufCap)
	p.chans[id] = &pullInfo{buf}
	p.types.Lock()
	p.types.m[id] = ch.Type().Elem()
	p.types.Unlock()

	go bufferer(buf, ch, p.toEncoder, id, bufCap)
	return nil
}

func (p *puller) handleElem(elem element) {
	info := p.chans[elem.ChID]
	if elem.Ok {
		if len(info.buf) == cap(info.buf) {
			log.Fatal("netchan puller: window size exceeded")
		}
		info.buf <- elem.val
	} else {
		close(info.buf)
		delete(p.chans, elem.ChID)
		p.types.Lock()
		delete(p.types.m, elem.ChID)
		p.types.Unlock()
	}
}

func bufferer(buf <-chan reflect.Value, ch reflect.Value, toEncoder chan<- winUpdate, id chanID, bufCap int) {
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
			req.resp <- p.add(req.ch, req.id, req.bufCap)
		case elem := <-p.elemCh:
			p.handleElem(elem)
		}
	}
}
