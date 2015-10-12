package netchan

import (
	"encoding/gob"
	"io"
	"math/rand"
	"reflect"
	"time"
)

const idCacheCap = 257

type msgType int

const (
	_ msgType = iota
	elemMsg
	creditMsg
	setIdMsg
	errorMsg
)

type header struct {
	MsgType msgType
	ChanId  int
}

type encoder struct {
	elemCh   <-chan element // from pusher
	creditCh <-chan credit  // from puller
	man      *Manager

	idCache map[hashedName]int
	rand    *rand.Rand
	enc     *gob.Encoder
	err     error
}

func newEncoder(elemCh <-chan element, creditCh <-chan credit, man *Manager, conn io.Writer) *encoder {
	e := &encoder{elemCh: elemCh, creditCh: creditCh, man: man}
	e.idCache = make(map[hashedName]int)
	e.rand = rand.New(rand.NewSource(time.Now().UnixNano()))
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
	id, present := e.idCache[name]
	if present {
		return id
	}
	if len(e.idCache) < idCacheCap {
		// get a fresh id
		id = len(e.idCache)
	} else {
		// delete a random element from idCache and take its id
		randIndex := e.rand.Intn(len(e.idCache))
		i := 0
		var victim hashedName
		for victim, id = range e.idCache {
			if i == randIndex {
				delete(e.idCache, victim)
				// id is set
				break
			}
			i++
		}
	}
	e.idCache[name] = id
	e.encode(header{setIdMsg, id})
	e.encode(name)
	return id
}

func (e *encoder) run() {
	for e.elemCh != nil || e.creditCh != nil {
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

		case cred, ok := <-e.creditCh:
			if ok {
				id := e.translateName(cred.chName)
				e.encode(header{creditMsg, id})
				e.encode(cred.incr)
			} else {
				e.creditCh = nil
			}
		}
	}
	e.encode(header{errorMsg, -1})
	e.encode(e.man.Error().Error())
}
