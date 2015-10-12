package netchan

import (
	"encoding/gob"
	"errors"
	"reflect"
)

var (
	errUnwantedElem   = errors.New("netchan decoder: element received for channel not being pulled")
	errInvalidId      = errors.New("netchan decoder: out of bounds channel id received")
	errInvalidWinup   = errors.New("netchan decoder: window update received with non-positive value")
	errInvalidMsgType = errors.New("netchan decoder: message received with invalid type")
)

type decError struct {
	err error
}

func raiseError(err error) {
	panic(decError{err})
}

type decoder struct {
	toPuller chan<- element
	toPusher chan<- winUpdate
	man      *Manager
	types    *typeMap
	cache    []hashedName
	dec      *gob.Decoder
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

func (d *decoder) translateId(id int) hashedName {
	if 0 <= id && id < len(d.cache) {
		return d.cache[id]
	}
	raiseError(errInvalidId)
	return hashedName{}
}

func (d *decoder) run() {
	defer d.shutDown()
	for {
		var h header
		d.decode(&h)

		switch h.MsgType {
		case elemMsg:
			var elem element
			elem.chName = d.translateId(h.ChId)
			d.types.Lock()
			elemType, ok := d.types.m[elem.chName]
			d.types.Unlock()
			if !ok {
				raiseError(errUnwantedElem)
			}
			elem.val = reflect.New(elemType).Elem()
			d.decodeVal(elem.val)
			d.decode(&elem.ok)
			d.toPuller <- elem

		case winupMsg:
			var winup winUpdate
			winup.chName = d.translateId(h.ChId)
			d.decode(&winup.incr)
			if winup.incr <= 0 {
				raiseError(errInvalidWinup)
			}
			d.toPusher <- winup

		case setIdMsg:
			var name hashedName
			d.decode(&name)
			switch {
			case h.ChId == len(d.cache) && len(d.cache) < maxCacheLen:
				d.cache = append(d.cache, name)
			case 0 <= h.ChId && h.ChId < len(d.cache):
				d.cache[h.ChId] = name
			case h.ChId == len(d.cache) && len(d.cache) >= maxCacheLen:
				raiseError(errCacheLimitExceeded)
			default:
				raiseError(errInvalidId)
			}

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
