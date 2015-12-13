package netchan

import (
	"encoding/gob"
	"errors"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
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

// same as net.Error
type netErrorI interface {
	error
	Timeout() bool   // Is the error a timeout?
	Temporary() bool // Is the error temporary?
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

type countWriter struct {
	w io.Writer
	n int
}

func (c *countWriter) Write(data []byte) (n int, err error) {
	n, err = c.w.Write(data)
	c.n += n
	return
}

type encoder struct {
	dataCh  <-chan userData
	credits <-chan credit
	mn      *Manager
	flushFn func() error
	countWr countWriter
	enc     *gob.Encoder

	err error
}

func (e *encoder) encode(val interface{}) {
	// when an encoding/transmission error occurs,
	// encode and flush operations turn into NOPs
	if e.err != nil {
		return
	}
	e.err = e.enc.EncodeValue(reflect.ValueOf(val))
}

func (e *encoder) flush() {
	if e.err != nil {
		return
	}
	e.err = e.flushFn()
}

const wantBatchSize = 512

func (e *encoder) handleData(data userData) {
	if data.batch == (reflect.Value{}) {
		e.encode(header{closeMsg, data.id})
		return
	}
	e.encode(header{dataMsg, data.id})
	if e.err != nil {
		return
	}
	e.countWr.n = 0
	e.err = e.enc.EncodeValue(data.batch)
	if e.err != nil {
		return
	}
	elemSize := float32(e.countWr.n) / float32(data.batch.Len())
	wantBatchLen := wantBatchSize / elemSize
	if wantBatchLen < 1 {
		wantBatchLen = 1
	}
	batchLen := float32(*data.batchLen)
	if wantBatchLen > batchLen*1.2 || wantBatchLen < batchLen*0.8 {
		atomic.StoreInt32(data.batchLen, int32((batchLen+wantBatchLen+1)*0.5))
		//atomic.StoreInt32(data.batchLen, int32(wantBatchLen+0.5))
	}
}

func (e *encoder) bufAndFlush() {
Loop:
	for i := 0; i < cap(e.dataCh)+cap(e.credits); i++ {
		select {
		case data := <-e.dataCh:
			e.handleData(data)
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
			e.handleData(data)
		case cred := <-e.credits:
			e.encode(header{creditMsg, cred.id})
			e.encode(cred)
		case <-errorSignal:
			break Loop
		}
		e.bufAndFlush()
	}

	err := e.mn.Error()
	netErr, ok := err.(netErrorI)
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
// TODO: preserve ReadByte method of underlying reader
type limitedReader struct {
	bufReader     // underlying reader
	N         int // max bytes remaining
}

var errMsgTooBig = newErr("too big gob message received")

func (l *limitedReader) Read(p []byte) (n int, err error) {
	if l.N <= 0 {
		return 0, errMsgTooBig
	}
	if len(p) > l.N {
		p = p[0:l.N]
	}
	n, err = l.bufReader.Read(p)
	l.N -= n
	return
}

type typeTable struct {
	sync.Mutex
	elemType map[int]reflect.Type
}

type decoder struct {
	toRecvMn     chan<- userData
	toSendMn     chan<- credit
	types        typeTable // updated by recvManager
	mn           *Manager
	msgSizeLimit int
	limitedRd    limitedReader
	dec          *gob.Decoder
}

func (d *decoder) decodeVal(val reflect.Value) error {
	// reset the limit before each Decode invocation
	d.limitedRd.N = d.msgSizeLimit
	return d.dec.DecodeValue(val)
}

func (d *decoder) decode(val interface{}) error {
	return d.decodeVal(reflect.ValueOf(val))
}

func (d *decoder) run() (err error) {
	defer func() {
		close(d.toRecvMn)
		close(d.toSendMn)
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
			d.types.Lock()
			elemType, present := d.types.elemType[h.Id]
			d.types.Unlock()
			if !present {
				return fmtErr("message with invalid ID received (%d)\n", h.Id)
			}
			batch := reflect.New(reflect.SliceOf(elemType)).Elem()
			err = d.decodeVal(batch)
			if err != nil {
				return
			}
			d.toRecvMn <- userData{h.Id, batch, nil}

		case closeMsg:
			d.toRecvMn <- userData{h.Id, reflect.Value{}, nil}

		case creditMsg:
			var cred credit
			err = d.decode(&cred)
			if err != nil {
				return
			}
			if cred.Amount <= 0 {
				return newErr("credit with non-positive value received")
			}
			cred.id = h.Id
			d.toSendMn <- cred

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
