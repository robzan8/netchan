package netchan

import (
	"encoding/gob"
	"errors"
	"reflect"
)

var (
	errBadId          = errors.New("netchan decoder: out of bounds id received")
	errInvalidMsgType = errors.New("netchan decoder: message with invalid type received")
	errInvalidCred    = errors.New("netchan decoder: credit with non-positive value received")
	errUnwantedElem   = errors.New("netchan decoder: element received for channel not being pulled")
)

type decError struct {
	err error
}

func raiseError(err error) {
	panic(decError{err})
}

type decoder struct {
	toPuller chan<- element
	toPusher chan<- credit
	types    *typeMap
	man      *Manager

	dec *gob.Decoder
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
			raiseError(errBadId)
		}
		switch h.MsgType {
		case elemMsg:
			var elem element
			elem.id = h.ChanId
			d.types.Lock()
			if elem.id >= len(d.types.table) {
				raiseError(errBadId)
			}
			elemType := d.types.table[elem.id]
			d.types.Unlock()
			if elemType == nil {
				raiseError(errBadId)
			}
			elem.val = reflect.New(elemType).Elem()
			d.decodeVal(elem.val)
			d.decode(&elem.ok)
			d.toPuller <- elem

		case creditMsg:
			var cred credit
			cred.id = h.ChanId
			d.decode(&cred.incr)
			d.decode(&cred.open)
			if cred.open {
				cred.name = new(hashedName)
				d.decode(cred.name)
			}
			d.toPusher <- cred

		case errorMsg:
			var errString string
			d.decode(&errString)
			raiseError(errors.New(errString))

		default:
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
	close(d.toPusher)
	close(d.toPuller)
}
