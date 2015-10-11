package netchan

import (
	"encoding/gob"
	"errors"
	"reflect"
)

var (
	errUnwantedElem   = errors.New("netchan decoder: element received for channel not being pulled")
	errInvalidWinup   = errors.New("netchan decoder: window update received with non-positive value")
	errInvalidMsgType = errors.New("netchan decoder: message received with invalid type")
)

type decoder struct {
	toPuller chan<- element
	toPusher chan<- winUpdate
	man      *Manager
	cache    []hashedName
	dec      *gob.Decoder
	err      error
	types    *typeMap
}

func (d *decoder) decodeVal(v reflect.Value) {
	if d.err != nil {
		return
	}
	d.err = d.dec.DecodeValue(v)
	if d.err != nil {
		d.man.signalError(d.err)
	}
}

func (d *decoder) decode(v interface{}) {
	d.decodeVal(reflect.ValueOf(v))
}

func (d *decoder) setId(id int, name hashedName) {
	if d.err != nil {
		return
	}
	if id == len(d.cache) {
		d.cache = append(d.cache, name)
		return
	}
	if 0 <= id && id < len(cache) {
		d.cache[id] = name
		return
	}
	d.err = errInvalidId
	d.man.signalError(errInvalidId)
}

func (d *decoder) translateId(id int) hashedName {
	if d.err != nil {
		return hashedName{}
	}
	if 0 <= id && id < len(cache) {
		return d.cache[id]
	}
	d.err = errInvalidId
	d.man.signalError(errInvalidId)
}

func (d *decoder) run() {
loop:
	for d.man.Error() == nil {
		var h header
		d.decode(&h)
		if d.err != nil {
			break loop
		}
		switch h.MsgType {
		case elemMsg:
			var elem element
			elem.chName = d.translateId(h.ChId)
			if d.err != nil {
				break loop
			}
			d.types.Lock()
			elemType, ok := d.types.m[elem.chName]
			d.types.Unlock()
			if !ok {
				d.err = errUnwantedElem
				break loop
			}
			elem.val = reflect.New(elemType).Elem()
			d.decodeVal(elem.val)
			d.decode(&elem.ok)
			if d.err != nil {
				break loop
			}
			d.toPuller <- elem

		case winupMsg:
			var winup winUpdate
			winup.chName = d.translateId(h.ChId)
			d.decode(&winup.incr)
			if d.err != nil {
				break loop
			}
			if winup.incr <= 0 {
				d.err = errInvalidWinup
				break loop
			}
			d.toPusher <- winup

		case setIdMsg:
			var name hashedName
			d.decode(&name)
			d.setId(h.ChId, name)
			if d.err != nil {
				break loop
			}

		case errorMsg:
			var errString string
			d.decode(&errString)
			if d.err == nil {
				d.err = errors.New(errString)
			}
			break loop

		default:
			d.err = errInvalidMsgType
			break loop
		}
	}
	d.man.signalError(err)
	close(d.toPusher)
	close(d.toPuller)
}
