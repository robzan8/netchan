package netchan

import (
	"encoding/gob"
	"reflect"
)

type msgType int

const (
	_ msgType = iota
	elemMsg
	winupMsg
)

type encoder struct {
	elemCh   <-chan element   // from pusher
	windowCh <-chan winUpdate // from puller
	enc      *gob.Encoder
	err      error
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
		case elem := <-e.elemCh:
			e.encode(elemMsg)
			e.encode(elem)
			e.encodeVal(elem.val)
		case winup := <-e.windowCh:
			e.encode(winupMsg)
			e.encode(winup)
		}
	}
}

type decoder struct {
	toPuller chan<- element
	toPusher chan<- winUpdate
	dec      *gob.Decoder
	err      error

	types *typeMap
}

func (d *decoder) decodeVal(v reflect.Value) {
	if d.err != nil {
		return
	}
	d.err = d.dec.DecodeValue(v)
}

func (d *decoder) decode(v interface{}) {
	d.decodeVal(reflect.ValueOf(v))
}

func (d *decoder) run() {
	for {
		var typ msgType
		d.dec.Decode(&typ)
		switch typ {
		case elemMsg:
			var elem element
			d.decode(&elem)
			d.types.Lock()
			elemType := d.types.m[elem.Name]
			d.types.Unlock()
			elem.val = reflect.New(elemType).Elem()
			d.decodeVal(elem.val)
			d.toPuller <- elem
		case winupMsg:
			var winup winUpdate
			d.decode(&winup)
			d.toPusher <- winup
		}
	}
}
