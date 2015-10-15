package netchan

import (
	"errors"
	"reflect"
	"sync"
)

var (
	errAlreadyPulling = errors.New("netchan puller: Manager.Pull called with channel name already present")
	errCredExceeded   = errors.New("netchan puller: peer sent more elements than its credit allowed")
)

type pullEntry struct {
	name hashedName
	buf  chan reflect.Value
}

type typeMap struct {
	sync.Mutex
	table []reflect.Type
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

func (p *puller) entryByName(name hashedName) *pullEntry {
	for i := range p.table {
		if p.table[i].name == name {
			return &p.table[i]
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
	id := len(p.table)
	for i := range p.table {
		if p.table[i].name == (hashedName{}) {
			id = i
			break
		}
	}
	buf := make(chan reflect.Value, bufCap)
	p.types.Lock()
	if id == len(p.table) {
		p.table = append(p.table, pullEntry{name, buf})
		p.types.table = append(p.types.table, ch.Type().Elem())
	} else {
		if p.table[id].name != (hashedName{}) {
			p.types.Unlock()
			return errBadId
		}
		p.table[id] = pullEntry{name, buf}
		p.types.table[id] = ch.Type().Elem()
	}
	p.types.Unlock()

	p.toEncoder <- credit{id, bufCap, true, &name}
	go bufferer(id, name, buf, ch, p.toEncoder)
	return nil
}

func (p *puller) handleElem(elem element) error {
	if elem.id >= len(p.table) {
		return errBadId
	}
	entry := &p.table[elem.id]
	if entry.name == (hashedName{}) {
		return errBadId
	}
	if !elem.ok { // netchan closed normally
		close(entry.buf)
		entry.name = hashedName{}
		p.types.Lock()
		p.types.table[elem.id] = nil
		p.types.Unlock()
		return nil
	}
	if len(entry.buf) == cap(entry.buf) {
		return errCredExceeded
	}
	entry.buf <- elem.val
	return nil
}

func bufferer(id int, name hashedName, buf <-chan reflect.Value, ch reflect.Value, toEncoder chan<- credit) {
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
			toEncoder <- credit{id, sent, false, nil}
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
				// TODO: bug here! wait that bufferers are done before closing toEncoder
				for _, info := range p.table {
					close(info.buf)
				}
				close(p.toEncoder)
				return
			}
			err := p.handleElem(elem)
			if err != nil {
				p.man.signalError(err)
			}
		}
	}
}
