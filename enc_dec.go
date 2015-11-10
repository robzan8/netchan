package netchan

import (
	"encoding/gob"
	"errors"
	"io"
	"net"
	"reflect"
)

type msgType int

const (
	elemMsg = iota
	closeMsg
	creditMsg
	openMsg
	errorMsg
	netErrorMsg
)

type header struct {
	MsgType msgType
	ChanId  int
}

type netError struct {
	Err         string
	IsTimeout   bool
	IsTemporary bool
}

func (e *netError) Error() string {
	return e.Err
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
	if e.err != nil {
		return
	}
	e.err = e.enc.EncodeValue(v)
	if e.err != nil {
		go e.mn.ShutDownWith(e.err)
	}
}

func (e *encoder) encode(v interface{}) {
	e.encodeVal(reflect.ValueOf(v))
}

func (e *encoder) run() {
	for e.elemCh != nil || e.creditCh != nil {
		select {
		case elem, ok := <-e.elemCh:
			switch {
			case !ok:
				e.elemCh = nil
			case !elem.ok:
				e.encode(header{closeMsg, elem.id})
			default:
				e.encode(header{elemMsg, elem.id})
				e.encodeVal(elem.val)
			}

		case cred, ok := <-e.creditCh:
			switch {
			case !ok:
				e.creditCh = nil
			case cred.name != nil:
				e.encode(header{openMsg, cred.id})
				e.encode(cred.incr)
				e.encode(cred.name)
			default:
				e.encode(header{creditMsg, cred.id})
				e.encode(cred.incr)
			}
		}
	}
	err := e.mn.Error()
	netErr, ok := err.(net.Error)
	if ok {
		e.encode(header{MsgType: netErrorMsg})
		e.encode(netError{netErr.Error(), netErr.Timeout(), netErr.Temporary()})
	} else {
		e.encode(header{MsgType: errorMsg})
		e.encode(err.Error())
	}
	// error message in flight can be discarded?
	e.mn.closeConn()
}

var (
	errInvalidId      = errors.New("netchan: message with invalid id received")
	errInvalidCred    = errors.New("netchan: credit with non-positive value received")
	errInvalidMsgType = errors.New("netchan: message with invalid type received")
)

// like io.LimitedReader, but returns a custom error, so that the beginner
// user can understand that he is receiving too big gob messages
type limitedReader struct {
	R io.Reader // underlying reader
	N int       // max bytes remaining
}

func (l *limitedReader) Read(p []byte) (n int, err error) {
	if l.N <= 0 {
		return 0, errors.New("netchan Manager: too big gob message received")
	}
	if len(p) > l.N {
		p = p[0:l.N]
	}
	n, err = l.R.Read(p)
	l.N -= n
	return
}

type decoder struct {
	toReceiver   chan<- element
	toCredRecv   chan<- credit
	table        *recvTable
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
	// run returns only in case of error/shutdown
	defer func() {
		go d.mn.ShutDownWith(err)
		close(d.toReceiver)
		close(d.toCredRecv)
	}()
	for {
		if err = d.mn.Error(); err != nil {
			return
		}
		var h header
		err = d.decode(&h)
		if err != nil {
			return
		}
		if h.ChanId < 0 {
			return errInvalidId
		}
		switch h.MsgType {
		case elemMsg:
			elem := element{id: h.ChanId, ok: true}
			d.table.RLock()
			if elem.id >= len(d.table.t) || !d.table.t[elem.id].present {
				d.table.RUnlock()
				return errInvalidId
			}
			elemType := d.table.t[elem.id].ch.Type().Elem()
			d.table.RUnlock()
			elem.val = reflect.New(elemType).Elem()
			err = d.decodeVal(elem.val)
			if err != nil {
				return
			}
			d.toReceiver <- elem

		case closeMsg:
			d.toReceiver <- element{id: h.ChanId, ok: false}

		case creditMsg, openMsg:
			cred := credit{id: h.ChanId}
			err = d.decode(&cred.incr)
			if err != nil {
				return
			}
			if cred.incr <= 0 {
				return errInvalidCred
			}
			if h.MsgType == openMsg {
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
			return errors.New(errString)

		case netErrorMsg:
			netErr := new(netError)
			err = d.decode(netErr)
			if err != nil {
				return
			}
			return netErr

		default:
			return errInvalidMsgType
		}
	}
}
