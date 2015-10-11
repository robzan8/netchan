package netchan

import (
	"encoding/gob"
	"errors"
	"reflect"
)

var (
	errUnwantedElem   = errors.New("netchan decoder: element arrived for channel not being pulled")
	errInvalidWinup   = errors.New("netchan decoder: window update received with non-positive value")
	errInvalidMsgType = errors.New("netchan decoder: message received with invalid type")
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
	val  reflect.Value
	Ok   bool // if not ok, the channel has been closed
}

type winUpdate struct {
	ChID chanID
	Incr int
}

type encoder struct {
	elemCh   <-chan element   // from pusher
	windowCh <-chan winUpdate // from puller
	man      *Manager
	enc      *gob.Encoder
	err      error
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
	for e.elemCh != nil || e.windowCh != nil {
		select {
		case elem, ok := <-e.elemCh:
			if ok {
				e.encode(elemMsg)
				e.encode(elem)
				e.encodeVal(elem.val)
			} else {
				e.elemCh = nil
			}

		case winup, ok := <-e.windowCh:
			if ok {
				e.encode(winupMsg)
				e.encode(winup)
			} else {
				e.windowCh = nil
			}
		}
	}
	e.encode(errorMsg)
	e.encode(e.man.Error().Error())
}

type decoder struct {
	toPuller chan<- element
	toPusher chan<- winUpdate
	man      *Manager
	dec      *gob.Decoder
	err      error
	types    *typeMap
}

func (d *decoder) run() {
	var err error
loop:
	for d.man.Error() == nil {
		var typ msgType
		err = d.dec.Decode(&typ)
		if err != nil {
			break loop
		}
		switch typ {
		case elemMsg:
			var elem element
			err = d.dec.Decode(&elem)
			if err != nil {
				break loop
			}
			d.types.Lock()
			elemType, ok := d.types.m[elem.ChID]
			d.types.Unlock()
			if !ok {
				err = errUnwantedElem
				break loop
			}
			elem.val = reflect.New(elemType).Elem()
			err = d.dec.DecodeValue(elem.val)
			if err != nil {
				break loop
			}
			d.toPuller <- elem

		case winupMsg:
			var winup winUpdate
			err = d.dec.Decode(&winup)
			if err != nil {
				break loop
			}
			if winup.Incr <= 0 {
				err = errInvalidWinup
				break loop
			}
			d.toPusher <- winup

		default:
			err = errInvalidMsgType
			break loop
		}
	}
	d.man.signalError(err)
	close(d.toPusher)
	close(d.toPuller)
}
