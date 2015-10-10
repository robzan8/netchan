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
}

func (e *encoder) encode(v interface{}) {
	e.encodeVal(reflect.ValueOf(v))
}

func (e *encoder) run() {
	for {
		select {
		case elem, ok := <-e.elemCh:
			if !ok { // pusher saw an error
				e.encode(errorMsg)
				e.encode(e.man.Error().Error())
				return // hope peer gets it
			}
			e.encode(elemMsg)
			e.encode(elem)
			e.encodeVal(elem.val)

		case winup := <-e.windowCh:
			e.encode(winupMsg)
			e.encode(winup)
		}
		if e.err != nil {
			e.man.signalError(e.err)
			// do not return, wait that pusher is done?
		}
	}
}

type decoder struct {
	toPuller chan<- element
	toPusher chan<- winUpdate
	dec      *gob.Decoder
	man      *Manager

	types *typeMap
}

// TODO: detect when connection gets closed and shutdown everything
func (d *decoder) run() {
	for {
		err := d.man.Error()
		if err != nil {
			return d.handleError(err)
		}

		var typ msgType
		err = d.dec.decode(&typ)
		if err != nil {
			return d.handleError(err)
		}
		switch typ {
		case elemMsg:
			var elem element
			err := d.dec.Decode(&elem)
			if err != nil {
				return d.handleError(err)
			}
			d.types.Lock()
			elemType, ok := d.types.m[elem.ChID]
			d.types.Unlock()
			if !ok {
				return d.handleError(errUnwantedElem)
			}
			elem.val = reflect.New(elemType).Elem()
			err = d.dec.DecodeValue(elem.val)
			if err != nil {
				return d.handleError(err)
			}
			d.toPuller <- elem

		case winupMsg:
			var winup winUpdate
			err := d.dec.Decode(&winup)
			if err != nil {
				return d.handleError(err)
			}
			d.toPusher <- winup

		case errorMsg:
			var errString string
			d.dec.Decode(&errString)
			return d.handleError(errors.New(errString))
			// infinite loop of error signaling between peers?

		default:
			return d.handleError(errInvalidMsgType)
		}
	}
}

func (d *decoder) handleError(err error) {
	d.man.signalError(err)
	close(d.toPuller)
}
