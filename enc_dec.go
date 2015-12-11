package netchan

import (
	"encoding/gob"
	"errors"
	"io"
	"net"
	"reflect"
	"sync"
)

const (
	dataMsg int = iota
	helloMsg
	closeMsg
	creditMsg
	errorMsg
	netErrorMsg

	lastReservedMsg = 16
)

// preceedes every message
type header struct {
	MsgType int
	Id      int
}

// used to transmit errors that implement net.Error
type netError struct {
	Str         string
	IsTimeout   bool
	IsTemporary bool
}

func (e *netError) Error() string {
	return e.Str
}

func (e *netError) Timeout() bool {
	return e.IsTimeout
}

func (e *netError) Temporary() bool {
	return e.IsTemporary
}

type encoder struct {
	dataCh  <-chan userData
	credits <-chan credit
	mn      *Manager
	enc     *gob.Encoder
	flushFn func() error

	err error
}

func (e *encoder) encodeVal(val reflect.Value) {
	// when an encoding/transmission error occurs,
	// encode and flush operations turn into NOPs
	if e.err != nil {
		return
	}
	e.err = e.enc.EncodeValue(val)
}

func (e *encoder) encode(val interface{}) {
	e.encodeVal(reflect.ValueOf(val))
}

func (e *encoder) flush() {
	if e.err != nil {
		return
	}
	e.err = e.flushFn()
}

func (e *encoder) encodeData(data userData) {
	if data.batch == (reflect.Value{}) {
		e.encode(header{closeMsg, data.sendr.id})
		return
	}
	e.encode(header{dataMsg, data.sendr.id})
	e.encodeVal(data.batch)

	data.pool.Put(data.batch.Addr().Interface())
}

func (e *encoder) bufAndFlush() {
Loop:
	for i := 0; i < cap(e.dataCh)+cap(e.credits); i++ {
		select {
		case data := <-e.dataCh:
			e.encodeData(data)
		case cred := <-e.credits:
			e.encode(header{creditMsg, cred.id})
			e.encode(cred)
		default:
			break Loop
		}
	}
	e.flush()
}

func (e *encoder) run() {
	errorSignal := e.mn.ErrorSignal()

	e.encode(header{helloMsg, 0})
	e.encode(hello{})
	e.bufAndFlush()
Loop:
	for {
		if e.err != nil {
			e.mn.ShutDownWith(e.err)
			return
		}
		select {
		case data := <-e.dataCh:
			e.encodeData(data)
		case cred := <-e.credits:
			e.encode(header{creditMsg, cred.id})
			e.encode(cred)
		case <-errorSignal:
			break Loop
		}
		e.bufAndFlush()
	}

	err := e.mn.Error()
	netErr, ok := err.(net.Error)
	if ok {
		e.encode(header{netErrorMsg, 0})
		e.encode(netError{netErr.Error(), netErr.Timeout(), netErr.Temporary()})
	} else {
		e.encode(header{errorMsg, 0})
		e.encode(err.Error())
	}
	e.flush()
	e.mn.closeConn()
}

// Like io.LimitedReader, but returns a custom error.
type limitedReader struct {
	R io.Reader // underlying reader
	N int       // max bytes remaining
}

var errMsgTooBig = newErr("too big gob message received")

func (l *limitedReader) Read(p []byte) (n int, err error) {
	if l.N <= 0 {
		return 0, errMsgTooBig
	}
	if len(p) > l.N {
		p = p[0:l.N]
	}
	n, err = l.R.Read(p)
	l.N -= n
	return
}

type poolMap struct {
	sync.Mutex
	m map[int]*sync.Pool
}

type decoder struct {
	toElemRtr    chan<- userData
	toCredRtr    chan<- credit
	pools        poolMap // updated by recvManager
	mn           *Manager
	msgSizeLimit int
	limReader    limitedReader
	dec          *gob.Decoder
}

func (d *decoder) decodeVal(val reflect.Value) error {
	// reset the limit before each Decode invocation
	d.limReader.N = d.msgSizeLimit
	return d.dec.DecodeValue(val)
}

func (d *decoder) decode(val interface{}) error {
	return d.decodeVal(reflect.ValueOf(val))
}

func (d *decoder) run() (err error) {
	defer func() {
		close(d.toElemRtr)
		close(d.toCredRtr)
		d.mn.ShutDownWith(err)
	}()

	var h header
	err = d.decode(&h)
	if err != nil {
		return
	}
	if h.MsgType != helloMsg {
		return fmtErr("expecting hello message, got MsgType %d", h.MsgType)
	}
	var hel hello
	err = d.decode(&hel)
	if err != nil {
		return
	}
	for {
		if err = d.mn.Error(); err != nil {
			return
		}
		var h header
		err = d.decode(&h)
		if err != nil {
			return
		}
		switch h.MsgType {
		case dataMsg:
			d.pools.Lock()
			pool, present := d.pools.m[h.Id]
			d.pools.Unlock()
			if !present {
				return errInvalidId
			}
			batch := reflect.ValueOf(pool.Get()).Elem()
			err = d.decodeVal(batch)
			if err != nil {
				return
			}
			d.toElemRtr <- userData{h.Id, batch, pool}

		case closeMsg:
			d.toElemRtr <- userData{h.Id, reflect.Value{}}

		case creditMsg:
			var cred credit
			err = d.decode(&cred)
			if err != nil {
				return
			}
			if cred.Amount <= 0 {
				return newErr("credit with non-positive value received")
			}
			d.toCredRtr <- cred

		case errorMsg:
			var errStr string
			err = d.decode(&errStr)
			if err != nil {
				return
			}
			if errStr == io.EOF.Error() {
				return io.EOF
			}
			return errors.New("netchan, error from peer: " + errStr)

		case netErrorMsg:
			netErr := new(netError)
			err = d.decode(netErr)
			if err != nil {
				return
			}
			netErr.Str = "netchan, error from peer: " + netErr.Str
			return netErr

		default:
			if h.MsgType < 0 || h.MsgType > lastReservedMsg {
				return fmtErr("received message with invalid type: %d", h.MsgType)
			}
		}
	}
}
