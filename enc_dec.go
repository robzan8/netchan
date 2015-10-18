package netchan

import (
	"encoding/gob"
	"errors"
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
	enc      *gob.Encoder

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

var (
	errInvalidId      = errors.New("netchan: message with invalid id received")
	errInvalidCred    = errors.New("netchan: credit with non-positive value received")
	errInvalidMsgType = errors.New("netchan: message with invalid type received")
)

type decError struct {
	err error
}

func raiseError(err error) {
	panic(decError{err})
}

type decoder struct {
	toPuller  chan<- element
	toCPuller chan<- credit
	table     *pullTable
	man       *Manager
	dec       *gob.Decoder
}

func (d *decoder) decodeVal(v reflect.Value) {
	err := d.dec.DecodeValue(v)
	if err != nil {
		raiseError(err)
	}
}

func (d *decoder) decode(v interface{}) {
	d.decodeVal(reflect.ValueOf(v))
}

func (d *decoder) run() {
	defer d.shutDown()
	for {
		var h header
		d.decode(&h)
		if h.ChanId < 0 {
			raiseError(errInvalidId)
		}
		switch h.MsgType {
		case elemMsg:
			var elem element
			elem.id = h.ChanId
			d.table.RLock()
			if elem.id >= len(d.table.t) || !d.table.t[elem.id].present {
				d.table.RUnlock()
				raiseError(errInvalidId)
			}
			elemType := d.table.t[elem.id].ch.Type().Elem()
			d.table.RUnlock()
			elem.val = reflect.New(elemType).Elem()
			d.decodeVal(elem.val)
			d.decode(&elem.ok)
			d.toPuller <- elem

		case creditMsg:
			var cred credit
			cred.id = h.ChanId
			d.decode(&cred.incr)
			if cred.incr <= 0 {
				raiseError(errInvalidCred)
			}
			var isInit bool
			d.decode(&isInit)
			if isInit {
				cred.name = new(hashedName)
				d.decode(cred.name)
			}
			d.toCPuller <- cred

		case errorMsg:
			var errString string
			d.decode(&errString)
			raiseError(errors.New(errString))

		default:
			// more resilient?
			raiseError(errInvalidMsgType)
		}

		if err := d.man.Error(); err != nil {
			raiseError(err)
		}
	}
}

func (d *decoder) shutDown() {
	// we are panicking,
	// it's the only way the decoder can exit the run loop
	e := recover()
	decErr, ok := e.(decError)
	if !ok {
		panic(e)
	}
	d.man.signalError(decErr.err)
	close(d.toPuller)
	close(d.toCPuller)
}
