package netchan

import (
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
)

var (
	errAlreadyPulling = errors.New("netchan puller: Manager.Pull called with channel name already present")
	errCredExceeded   = errors.New("netchan puller: peer sent more elements than its credit allowed")
)

type pullEntry struct {
	name    hashedName
	buf     chan reflect.Value
	present bool
}

type typeMap struct {
	sync.Mutex
	m map[hashedName]reflect.Type
}

type puller struct {
	elemCh    <-chan element // from decoder
	toEncoder chan<- credit
	pullReq   <-chan addReq // from Manager.Pull (user)
	pullResp  chan<- error
	types     *typeMap
	man       *Manager

	table []pullEntry
}

func (p *puller) initialize() {
	p.table = make([]pullEntry, firstEntry)
	// first #firstEntry entries are reserved
	for i := range p.table {
		p.table[i].present = true
	}
}

func (p *puller) entryByName(name hashedName) *pullEntry {
	for i := firstEntry; i < len(p.table); i++ {
		if table[i].name == name {
			return &table[i]
		}
	}
	return nil
}

const bufCap = 512

func (p *puller) add(ch reflect.Value, name hashedName) error {
	entry := p.entryByName(name)
	if entry != nil {
		return errAlreadyPulling
	}
	// get an id for the new channel
	id := -1
	for i := firstEntry; i < len(p.table); i++ {
		if !table[i].present {
			id = i
			break
		}
	}
	if id == -1 {
		id := len(p.table)
		p.table = append(p.table, pullEntry{})
	}

	buf := make(chan reflect.Value, bufCap)
	p.table[id] = pullEntry{name, buf, true}
	p.types.Lock()
	p.types.m[name] = ch.Type().Elem()
	p.types.Unlock()

	go bufferer(buf, ch, id, name, p.toEncoder)
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
		atomic.AddInt64(&p.count, -1)
		return
	}
	if len(info.buf) == cap(info.buf) {
		p.man.signalError(errCredExceeded)
		return
	}
	info.buf <- elem.val
}

func bufferer(buf <-chan reflect.Value, ch reflect.Value, id int, name hashedName, toEncoder chan<- credit) {
	toEncoder <- credit{id, bufCap, true, name}
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
			toEncoder <- credit{id, sent, false, hashedName{}}
			sent = 0
		}
	}
}

func (p *puller) run() {
	for {
		select {
		case req := <-p.pullReq:
			p.pullResp <- p.add(req.ch, req.name)
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
