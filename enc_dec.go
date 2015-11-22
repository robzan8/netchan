package netchan

import (
	"encoding/gob"
	"errors"
	"io"
	"net"
	"reflect"
	"sync"
)

// review shutdown procedure

const (
	helloMsg int = 1 + iota

	// element messages
	elemMsg
	initElemMsg
	closeMsg

	// credit messages
	creditMsg
	initCredMsg

	// error messages
	errorMsg
	netErrorMsg

	lastReserved = 15
)

// preceedes every message
type header struct {
	MsgType int
	ChanID  int
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
	elemCh   <-chan element // from sender
	creditCh <-chan credit  // from credit sender
	mn       *Manager
	enc      *gob.Encoder

	err error
}

func (e *encoder) encodeVal(v reflect.Value) {
	// when an encoding error occurs, encoder.err
	// is set and all encode operations become NOPs
	if e.err != nil {
		return
	}
	e.err = e.enc.EncodeValue(v)
}

func (e *encoder) encode(v interface{}) {
	e.encodeVal(reflect.ValueOf(v))
}

func (e *encoder) run() (err error) {
	defer func() {
		netErr, ok := err.(net.Error)
		if ok {
			e.encode(header{netErrorMsg, 0})
			e.encode(netError{netErr.Error(), netErr.Timeout(), netErr.Temporary()})
		} else {
			e.encode(header{errorMsg, 0})
			e.encode(err.Error())
		}
		e.mn.closeConn()
	}()

	e.encode(header{helloMsg, 0})
	for {
		if e.err != nil {
			go e.mn.ShutDownWith(err)
			return e.err
		}
		select {
		case elem := <-e.elemCh:
			switch {
			case elem.name != nil:
				e.encode(header{initElemMsg, 0})
				e.encode(elem.name)
			case !elem.ok:
				e.encode(header{closeMsg, elem.id})
			default:
				e.encode(header{elemMsg, elem.id})
				e.encodeVal(elem.val)
			}

		case cred := <-e.creditCh:
			switch {
			case cred.name != nil:
				e.encode(header{initCredMsg, cred.id})
				e.encode(cred.amount)
				e.encode(cred.name)
			default:
				e.encode(header{creditMsg, cred.id})
				e.encode(cred.amount)
			}

		case <-e.mn.ErrorSignal():
			return e.mn.Error()
		}
	}
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

type typeTable struct {
	sync.Mutex
	t []reflect.Type
}

type decoder struct {
	toReceiver   chan<- element
	toCredRecv   chan<- credit
	types        typeTable // updated by elemRouter
	mn           *Manager
	msgSizeLimit int
	limReader    limitedReader
	dec          *gob.Decoder
}

func (d *decoder) decodeVal(v reflect.Value) error {
	d.limReader.N = d.msgSizeLimit // reset the limit before each Decode invocation
	return d.dec.DecodeValue(v)
}

func (d *decoder) decode(v interface{}) error {
	return d.decodeVal(reflect.ValueOf(v))
}

func (d *decoder) run() (err error) {
	defer func() {
		go d.mn.ShutDownWith(err)
		close(d.toReceiver)
		close(d.toCredRecv)
	}()

	var h header
	err = d.decode(&h)
	if err != nil {
		return
	}
	if h.MsgType != helloMsg {
		return fmtErr("expecting hello message, got MsgType %d", h.MsgType)
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
		if h.ChanID < 0 {
			return errInvalidId
		}
		switch h.MsgType {
		case elemMsg:
			elem := element{id: h.ChanID, ok: true}
			d.types.Lock()
			if elem.id >= len(d.types.t) || d.types.t[elem.id] == nil {
				d.types.Unlock()
				return errInvalidId
			}
			elemType := d.types.t[elem.id]
			d.types.Unlock()
			elem.val = reflect.New(elemType).Elem()
			err = d.decodeVal(elem.val)
			if err != nil {
				return
			}
			d.toReceiver <- elem

		case initElemMsg:
			var name hashedName
			err = d.decode(&name)
			if err != nil {
				return
			}
			// we don't do

		case closeMsg:
			d.toReceiver <- element{id: h.ChanID, ok: false}

		case creditMsg, initCredMsg:
			cred := credit{id: h.ChanID}
			err = d.decode(&cred.amount)
			if err != nil {
				return
			}
			if cred.amount <= 0 {
				return newErr("credit with non-positive value received")
			}
			if h.MsgType == initCredMsg {
				cred.name = new(hashedName)
				err = d.decode(cred.name)
				if err != nil {
					return
				}
			}
			d.toCredRecv <- cred

		case errorMsg:
			var errString string
			err = d.decode(&errString)
			if err != nil {
				return
			}
			if errString == io.EOF.Error() {
				return io.EOF
			}
			return errors.New("netchan, error from peer: " + errString)

		case netErrorMsg:
			netErr := new(netError)
			err = d.decode(netErr)
			if err != nil {
				return
			}
			netErr.Str = "netchan, error from peer: " + netErr.Str
			return netErr

		default:
			if h.MsgType == 0 || h.MsgType > lastReserved {
				return fmtErr("received message with invalid type: %d", h.MsgType)
			}
		}
	}
}
