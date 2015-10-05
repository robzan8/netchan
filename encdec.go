package netchan

import (
	"encoding/gob"
	"log"
	"reflect"
)

type msgType int

const (
	_ msgType = iota
	elemMsg
	winupMsg
)

type element struct {
	Name string
	val  reflect.Value // not exported, to be encoded/decoded separately
	Ok   bool          // if not ok, the channel has been closed
}

type winUpdate struct {
	Name string
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
		if e.err != nil {
			log.Fatal("netchan encoder: ", e.err)
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

// TODO: detect when connection gets closed and shutdown everything
func (d *decoder) run() {
	for {
		var typ msgType
		d.decode(&typ)
		if d.err != nil {
			log.Fatal("netchan decoder: ", d.err)
		}
		switch typ {
		case elemMsg:
			var elem element
			d.decode(&elem)
			if d.err != nil {
				log.Fatal("netchan decoder: ", d.err)
			}
			d.types.Lock()
			elemType, ok := d.types.m[elem.Name]
			d.types.Unlock()
			if !ok {
				log.Fatal("netchan decoder: received element of unknown type")
			}
			elem.val = reflect.New(elemType).Elem()
			d.decodeVal(elem.val)
			if d.err != nil {
				log.Fatal("netchan decoder: ", d.err)
			}
			d.toPuller <- elem

		case winupMsg:
			var winup winUpdate
			d.decode(&winup)
			if d.err != nil {
				log.Fatal("netchan decoder: ", d.err)
			}
			d.toPusher <- winup

		default:
			log.Fatal("netchan decoder: invalid message type ", typ)
		}
	}
}
