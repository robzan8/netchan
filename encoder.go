package netchan

import (
	"encoding/gob"
	"io"
	"math/rand"
	"reflect"
	"time"
)

const maxCacheLen = 100

type msgType int

const (
	_ msgType = iota
	elemMsg
	winupMsg
	setIdMsg
	errorMsg
)

type header struct {
	MsgType msgType
	ChId    int
}

type element struct {
	chName hashedName
	val    reflect.Value
	ok     bool // if not ok, the channel has been closed
}

type winUpdate struct {
	chName hashedName
	incr   int
}

type encoder struct {
	elemCh   <-chan element   // from pusher
	windowCh <-chan winUpdate // from puller
	man      *Manager
	cache    map[hashedName]int
	rand     *rand.Rand
	enc      *gob.Encoder
	err      error
}

func newEncoder(elemCh <-chan element, windowCh <-chan winUpdate, man *Manager, conn io.Writer) *encoder {
	e = &encoder{elemCh: elemCh, windowCh: windowCh, man: man}
	e.cache = make(map[hashedName]int)
	e.rand = rand.New(time.Now().UnixNano())
	e.enc = gob.NewEncoder(conn)
	return e
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

func (e *encoder) translateName(name hashedName) int {
	if e.err != nil {
		return -1
	}
	id, present := e.cache[name]
	if present {
		return id
	}
	if len(e.cache) < maxCacheLen {
		// get a fresh id
		id = len(e.cache)
	} else if len(e.cache) == maxCacheLen {
		// delete a random element from cache and take its id
		randIndex := e.rand.Intn(maxCacheLen)
		i := 0
		var victim hashedName
		for victim, id = range e.cache {
			if i == randIndex {
				delete(e.cache, victim)
				// id is set
				break
			}
			i++
		}
	} else {
		panic("netchan encoder: maxCacheLen exceeded (bug!)")
	}
	e.cache[name] = id
	e.encode(header{setIdMsg, id})
	e.encode(name)
	return id
}

func (e *encoder) run() {
	for e.elemCh != nil || e.windowCh != nil {
		select {
		case elem, ok := <-e.elemCh:
			if ok {
				id := e.translateName(elem.chName)
				e.encode(header{elemMsg, id})
				e.encodeVal(elem.val)
				e.encode(elem.ok)
			} else {
				e.elemCh = nil
			}

		case winup, ok := <-e.windowCh:
			if ok {
				id := e.translateName(winup.chName)
				e.encode(header{winupMsg, id})
				e.encode(winup.incr)
			} else {
				e.windowCh = nil
			}
		}
	}
	e.encode(header{errorMsg, -1})
	e.encode(e.man.Error().Error())
}
