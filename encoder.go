package netchan

import (
	"encoding/gob"
	"reflect"
)

type msgType int

const (
	_ msgType = iota
	elemMsg
	creditMsg
	errorMsg
)

type header struct {
	MsgType msgType
	ChanId  int
}

type encoder struct {
	elemCh   <-chan element // from pusher
	creditCh <-chan credit  // from puller
	man      *Manager

	enc *gob.Encoder
	err error
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

func (e *encoder) run() {
	for e.elemCh != nil || e.creditCh != nil {
		select {
		case elem, ok := <-e.elemCh:
			if ok {
				e.encode(header{elemMsg, elem.id})
				e.encodeVal(elem.val)
				e.encode(elem.ok)
			} else {
				e.elemCh = nil
			}

		case cred, ok := <-e.creditCh:
			if ok {
				e.encode(header{creditMsg, cred.id})
				e.encode(cred.incr)
				e.encode(cred.name != nil)
				if cred.name != nil {
					e.encode(cred.name)
				}
			} else {
				e.creditCh = nil
			}
		}
	}
	e.encode(header{errorMsg, -1})
	e.encode(e.man.Error().Error())
}
