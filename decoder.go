package netchan

import (
	"encoding/gob"
	"errors"
	"reflect"
)

var (
	errCacheCapExceeded = errors.New("netchan decoder: idCacheCap exceeded")
	errInvalidId        = errors.New("netchan decoder: out of bounds id received")
	errInvalidMsgType   = errors.New("netchan decoder: message with invalid type received")
	errInvalidCred      = errors.New("netchan decoder: credit with non-positive value received")
	errUnwantedElem     = errors.New("netchan decoder: element received for channel not being pulled")
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
	man      *Manager

	types   *typeMap
	idCache []hashedName
	dec     *gob.Decoder
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
	if 0 <= id && id < len(d.idCache) {
		return d.idCache[id]
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
			elem.chName = d.translateId(h.ChanId)
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

		case creditMsg:
			var cred credit
			cred.chName = d.translateId(h.ChanId)
			d.decode(&cred.incr)
			if cred.incr <= 0 {
				raiseError(errInvalidCred)
			}
			d.toPusher <- cred

		case setIdMsg:
			var name hashedName
			d.decode(&name)
			switch {
			case h.ChanId == len(d.idCache) && len(d.idCache) < idCacheCap:
				d.idCache = append(d.idCache, name)
			case 0 <= h.ChanId && h.ChanId < len(d.idCache):
				d.idCache[h.ChanId] = name
			case h.ChanId == len(d.idCache) && len(d.idCache) >= idCacheCap:
				raiseError(errCacheCapExceeded)
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
