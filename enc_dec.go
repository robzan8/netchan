package netchan

import (
	"encoding/gob"
	"errors"
	"reflect"
)

type msgType int

const (
	elemMsg = iota
	closeMsg
	creditMsg
	openMsg
	errorMsg
)

type header struct {
	MsgType msgType
	ChanId  int
}

type encoder struct {
	elemCh   <-chan element // from sender
	creditCh <-chan credit  // from credit sender
	mn       *Manager
	enc      *gob.Encoder

	err error
}

func (e *encoder) encodeVal(v reflect.Value) {
	if e.err != nil {
		return
	}
	e.err = e.enc.EncodeValue(v)
	if e.err != nil {
		e.mn.signalError(e.err)
	}
}

func (e *encoder) encode(v interface{}) {
	e.encodeVal(reflect.ValueOf(v))
}

func (e *encoder) run() {
	for e.elemCh != nil || e.creditCh != nil {
		select {
		case elem, ok := <-e.elemCh:
			switch {
			case !ok:
				e.elemCh = nil
			case !elem.ok:
				e.encode(header{closeMsg, elem.id})
			default:
				e.encode(header{elemMsg, elem.id})
				e.encodeVal(elem.val)
			}

		case cred, ok := <-e.creditCh:
			switch {
			case !ok:
				e.creditCh = nil
			case cred.name != nil:
				e.encode(header{openMsg, cred.id})
				e.encode(cred.incr)
				e.encode(cred.name)
			default:
				e.encode(header{creditMsg, cred.id})
				e.encode(cred.incr)
			}
		}
	}
	e.encode(header{errorMsg, -1})
	e.encode(e.mn.Error().Error())
}

var (
	errInvalidId      = errors.New("netchan: message with invalid id received")
	errInvalidCred    = errors.New("netchan: credit with non-positive value received")
	errInvalidMsgType = errors.New("netchan: message with invalid type received")
)

type decError struct {
	err error
}

func decPanic(err error) {
	panic(decError{err})
}

type decoder struct {
	toReceiver chan<- element
	toCredRecv chan<- credit
	table      *recvTable
	mn         *Manager
	dec        *gob.Decoder
}

func (d *decoder) decodeVal(v reflect.Value) {
	err := d.dec.DecodeValue(v)
	if err != nil {
		decPanic(err)
	}
}

func (d *decoder) decode(v interface{}) {
	d.decodeVal(reflect.ValueOf(v))
}

func (d *decoder) run() {
	defer d.shutDown()
	for {
		if err := d.mn.Error(); err != nil {
			decPanic(err)
		}

		var h header
		d.decode(&h)
		if h.ChanId < 0 {
			decPanic(errInvalidId)
		}
		switch h.MsgType {
		case elemMsg:
			elem := element{id: h.ChanId, ok: true}
			d.table.RLock()
			if elem.id >= len(d.table.t) || !d.table.t[elem.id].present {
				d.table.RUnlock()
				decPanic(errInvalidId)
			}
			elemType := d.table.t[elem.id].ch.Type().Elem()
			d.table.RUnlock()
			elem.val = reflect.New(elemType).Elem()
			d.decodeVal(elem.val)
			d.toReceiver <- elem

		case closeMsg:
			d.toReceiver <- element{id: h.ChanId, ok: false}

		case creditMsg, openMsg:
			cred := credit{id: h.ChanId}
			d.decode(&cred.incr)
			if cred.incr <= 0 {
				decPanic(errInvalidCred)
			}
			if h.MsgType == openMsg {
				cred.name = new(hashedName)
				d.decode(cred.name)
			}
			d.toCredRecv <- cred

		case errorMsg:
			var errString string
			d.decode(&errString)
			decPanic(errors.New(errString))

		default:
			decPanic(errInvalidMsgType)
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
	d.mn.signalError(decErr.err)
	close(d.toReceiver)
	close(d.toCredRecv)
}
