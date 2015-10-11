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
	winupMsg
	errorMsg
)

type element struct {
	ChID chanID
	val  reflect.Value // not exported, to be encoded/decoded separately
	Ok   bool          // if not ok, the channel has been closed
}

type winUpdate struct {
	ChID chanID
	Incr int // check not <= 0
}

func (t msgType) String() string {
	switch t {
	case elemMsg:
		return "elemMsg"
	case winupMsg:
		return "winupMsg"
	}
	return "???"
}

type encoder struct {
	elemCh   <-chan element   // from pusher
	windowCh <-chan winUpdate // from puller
	enc      *gob.Encoder
	err      error
	man      *Manager
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
loop:
	for {
		select {
		case elem, ok := <-e.elemCh:
			if ok {
				e.encode(elemMsg)
				e.encode(elem)
				e.encodeVal(elem.val)
			} else {
				// error occurred and pusher shut down
				if e.windowCh == nil {
					break loop
				}
				e.elemCh = nil
			}

		case winup, ok := <-e.windowCh:
			if ok {
				e.encode(winupMsg)
				e.encode(winup)
			} else {
				// error occurred and puller shut down
				if e.elemCh == nil {
					break loop
				}
				e.windowCh = nil
			}
		}
	}
	e.encode(errorMsg)
	e.encode(e.man.Error().Error())
	// hope peer gets the error
}

type decoder struct {
	toPuller chan<- element
	toPusher chan<- winUpdate
	dec      *gob.Decoder
	man      *Manager

	types *typeMap
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

func (d *decoder) lookupType(id chanID) reflect.Type {
	if d.err != nil {
		return nil
	}
	d.types.Lock()
	typ, ok := d.types.m[id]
	d.types.Unlock()
	if ok {
		return typ
	}
	d.err = errUnwantedElem
	d.man.signalError(errUnwantedElem)
	return nil
}

func (d *decoder) reflectNew(typ reflect.Type) reflect.Value {
	if d.err != nil {
		return reflect.Value{}
	}
	return reflect.New(typ).Elem()
}

func (d *decoder) run() {
loop:
	for {
		var typ msgType
		d.decode(&typ)
		if d.man.Error() != nil {
			break loop
		}
		switch typ {
		case elemMsg:
			var elem element
			d.decode(&elem)
			elemType := d.lookupType(elem.ChID)
			elem.val = d.reflectNew(elemType)
			d.decodeVal(elem.val)
			if d.err == nil {
				d.toPuller <- elem
			}
		case winupMsg:
			var winup winUpdate
			d.decode(&winup)
			if d.err == nil {
				d.toPusher <- winup
			}
		case errorMsg:
			var errString string
			d.decode(&errString)
			if d.err == nil {
				d.err = errors.New(errString)
				d.man.signalError(d.err)
			}
		default:
			d.err = errInvalidMsgType
			d.man.signalError(d.err)
		}
	}
	close(d.toPusher)
	close(d.toPuller)
}
