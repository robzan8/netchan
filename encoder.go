package netchan

import (
	"encoding/gob"
	"io"
	"reflect"
)

type msgType int

const (
	_ msgType = iota
	elemMsg
	creditMsg
	setIdMsg
	deleteIdMsg
	errorMsg
)

// mechanism for getting fresh id is broken

type header struct {
	MsgType msgType
	ChanId  int
}

type encoder struct {
	elemCh   <-chan element // from pusher
	creditCh <-chan credit  // from puller
	man      *Manager

	idTable map[hashedName]int
	freshId int
	enc     *gob.Encoder
	err     error
}

func (e *encoder) initialize(conn io.Writer) {
	e.idTable = make(map[hashedName]int)
	e.enc = gob.NewEncoder(conn)
}

func (e *encoder) encodeVal(v reflect.Value) {
	if e.err != nil {
		return
	}
	e.err = e.enc.EncodeValue(v)
	if e.err != nil {
		e.man.signalError(e.err)
	}
}

func (e *encoder) encode(v interface{}) {
	e.encodeVal(reflect.ValueOf(v))
}

func (e *encoder) deleteId(name hashedName, id int) {
	if e.err != nil {
		return
	}
	delete(e.idTable, name)
	e.encode(header{deleteIdMsg, id})
}

func (e *encoder) translateName(name hashedName) int {
	if e.err != nil {
		return -1
	}
	id, present := e.idTable[name]
	if present {
		return id
	}
	id = e.freshId
	e.freshId++ // don't care if overflows
	e.idTable[name] = id
	e.encode(header{setIdMsg, id})
	e.encode(name)
	return id
}

func (e *encoder) run() {
	for e.elemCh != nil || e.creditCh != nil {
		select {
		case elem, ok := <-e.elemCh:
			if !ok {
				e.elemCh = nil
				continue
			}
			id := e.translateName(elem.chName)
			e.encode(header{elemMsg, id})
			e.encodeVal(elem.val)
			e.encode(elem.ok)
			if !elem.ok {
				// channel we are pushing is being closed
				e.deleteId(elem.chName, id)
			}

		case cred, ok := <-e.creditCh:
			if !ok {
				e.creditCh = nil
				continue
			}
			id := e.translateName(cred.chName)
			if cred.incr == 0 {
				// channel we are pulling is being closed
				e.deleteId(cred.chName, id)
				continue
			}
			e.encode(header{creditMsg, id})
			e.encode(cred.incr)
			e.encode(cred.open)
		}
	}
	e.encode(header{errorMsg, -1})
	e.encode(e.man.Error().Error())
}
