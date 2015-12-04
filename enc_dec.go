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
	helloT int = 1 + iota

	// element messages
	userDataT
	wantToSendT
	endOfStreamT

	// credit messages
	creditT
	initialCreditT

	// error messages
	errorT
	netErrorT

	lastReservedT = 15
)

// preceedes every message
type header struct {
	MsgType int
	Name    *hashedName // https://github.com/golang/go/issues/13378
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
	messages    <-chan message
	mn          *Manager
	enc         *gob.Encoder
	conn        io.Writer
	encBuf      []byte
	writerBuf   []byte
	encToWriter chan []byte
	writerToEnc chan []byte

	err error
}

func (e *encoder) runConnWriter() {
	for {
		buf := <-e.encToWriter
		_, e.err = e.conn.Write(buf)
		e.writerToEnc <- buf[0:0]
		if e.err != nil {
			return
		}
	}
}

func (e *encoder) Write(data []byte) (int, error) {
	if e.encBuf == nil {
		e.encBuf = <-e.writerToEnc
	}
	if e.err != nil {
		return 0, e.err
	}
	e.encBuf = append(e.encBuf, data...)
	if len(e.encBuf) >= 4096 {
		e.encToWriter <- buf
		e.encBuf = nil
	}
	return len(data), nil
}

func (e *encoder) encodeVal(v reflect.Value) {
	// when an encoding error occurs, e.err is set
	// and subsequent encode operations become NOPs
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
			e.encode(header{MsgType: netErrorT})
			e.encode(netError{netErr.Error(), netErr.Timeout(), netErr.Temporary()})
		} else {
			e.encode(header{MsgType: errorT})
			e.encode(err.Error())
		}
		e.mn.closeConn()
	}()

	e.encode(header{MsgType: helloT})
	e.encode(hello{})
	var lastName hashedName
	for {
		if e.err != nil {
			go e.mn.ShutDownWith(err)
			return e.err
		}
		select {
		case <-e.mn.ErrorSignal():
			return e.mn.Error()

		case msg := <-e.messages:
			var name *hashedName
			if msg.name != lastName {
				lastName = msg.name
				name = &lastName
			}
			switch pay := msg.payload.(type) {
			case *[]interface{}:
				for _, p := range *pay {
					switch pay := p.(type) {
					case *userData:
						e.encode(header{userDataT, name})
						e.encodeVal(pay.val)
					case *wantToSend:
						e.encode(header{wantToSendT, name})
						e.encode(pay)
					case *endOfStream:
						e.encode(header{endOfStreamT, name})
						e.encode(pay)
						// flush
					}
				}
			case *credit:
				e.encode(header{creditT, name})
				e.encode(pay)
			case *initialCredit:
				e.encode(header{initialCreditT, name})
				e.encode(pay)
				// various flush cases for elements and credits
			default:
				panic("unexpected msg type")
			}
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
	m map[hashedName]reflect.Type
}

type decoder struct {
	toElemRtr    chan<- message
	toCredRtr    chan<- message
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
		close(d.toElemRtr)
		close(d.toCredRtr)
	}()

	var h header
	err = d.decode(&h)
	if err != nil {
		return
	}
	if h.MsgType != helloT {
		return fmtErr("expecting hello message, got MsgType %d", h.MsgType)
	}
	var hel hello
	err = d.decode(&hel)
	if err != nil {
		return
	}
	var currName hashedName
	for {
		if err = d.mn.Error(); err != nil {
			return
		}
		var h header
		err = d.decode(&h)
		if err != nil {
			return
		}
		if h.Name != nil {
			currName = *h.Name
		}
		switch h.MsgType {
		case userDataT:
			d.types.Lock()
			typ, present := d.types.m[currName]
			if !present {
				d.types.Unlock()
				return errInvalidId
			}
			d.types.Unlock()
			data := new(userData)
			data.val = reflect.New(typ).Elem()
			err = d.decodeVal(data.val)
			if err != nil {
				return
			}
			d.toElemRtr <- message{currName, data}

		case wantToSendT:
			panic("implement me")

		case endOfStreamT:
			eos := new(endOfStream)
			err = d.decode(eos)
			if err != nil {
				return
			}
			d.toElemRtr <- message{currName, eos}

		case creditT:
			cred := new(credit)
			err = d.decode(cred)
			if err != nil {
				return
			}
			if cred.Amount < 0 {
				return newErr("credit with negative value received")
			}
			d.toCredRtr <- message{currName, cred}

		case initialCreditT:
			initCred := new(initialCredit)
			err = d.decode(initCred)
			if err != nil {
				return
			}
			if initCred.Amount <= 0 {
				return newErr("initial credit with non-positive value received")
			}
			d.toCredRtr <- message{currName, initCred}

		case errorT:
			var errStr string
			err = d.decode(&errStr)
			if err != nil {
				return
			}
			if errStr == io.EOF.Error() {
				return io.EOF
			}
			return errors.New("netchan, error from peer: " + errStr)

		case netErrorT:
			netErr := new(netError)
			err = d.decode(netErr)
			if err != nil {
				return
			}
			netErr.Str = "netchan, error from peer: " + netErr.Str
			return netErr

		default:
			if h.MsgType <= 0 || h.MsgType > lastReservedT {
				return fmtErr("received message with invalid type: %d", h.MsgType)
			}
		}
	}
}
