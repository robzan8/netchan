package netchan

import (
	"encoding/gob"
	"errors"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
)

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
	e.encode(data.header)
	if e.err != nil || data.Type == initDataMsg || data.Type == closeMsg {
		return
	}
	// data.Type is dataMsg
	e.countWr.n = 0
	e.err = e.enc.EncodeValue(data.batch)
	if e.err != nil {
		return
	}
	itemSize := float64(e.countWr.n) / float64(data.batch.Len())
	if itemSize < 1 {
		itemSize = 1
	}
	wantBatchLen := wantBatchSize / itemSize
	if wantBatchLen < 1 {
		wantBatchLen = 1
	}
	batchLen := float64(*data.batchLenPt)
	if batchLen < wantBatchLen*0.75 || batchLen > wantBatchLen*1.25 {
		intLen := int64(wantBatchLen + 0.5)
		atomic.StoreInt64(data.batchLenPt, intLen)
		logDebug("manager %d, channel send-%d: batch length changed to %d",
			e.mn.id, data.Id, intLen)
	}
}

func (e *encoder) bufAndFlush() {
CreditsLoop:
	for i := 0; i < cap(e.credits); i++ {
		select {
		case cred := <-e.credits:
			e.encode(cred.header)
			e.encode(cred.amount)
		default:
			break CreditsLoop
		}
	}
DataLoop:
	for i := 0; i < cap(e.dataCh); i++ {
		select {
		case data := <-e.dataCh:
			e.handleData(data)
		default:
			break DataLoop
		}
	}
	e.flush()
}

func (e *encoder) run() {
	errorSignal := e.mn.ErrorSignal()

	e.encode(header{Type: helloMsg})
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
			e.encode(cred.header)
			e.encode(cred.amount)
		case <-errorSignal:
			break Loop
		}
		e.bufAndFlush()
	}

	e.encode(header{Type: errorMsg})
	e.encode(e.mn.Error().Error())
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
	if h.Type != helloMsg {
		return fmtErr("expecting hello message, got Type %d", h.Type)
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
		switch h.Type {
		case helloMsg:
			return newErr("hello message received again")

		case dataMsg:
			d.types.Lock()
			elemType, present := d.types.elemType[h.Id] // make sliceType directly
			d.types.Unlock()
			if !present {
				return fmtErr("message with invalid ID received (%d)\n", h.Id)
			}
			batch := reflect.New(reflect.SliceOf(elemType)).Elem()
			err = d.decodeVal(batch)
			if err != nil {
				return
			}
			d.toRecvMn <- userData{header: h, batch: batch}

		case initDataMsg:
			// log

		case closeMsg:
			d.toRecvMn <- userData{header: h}

		case creditMsg, initCreditMsg:
			cred := credit{header: h}
			err = d.decode(&cred.amount)
			if err != nil {
				return
			}
			if cred.amount < 0 {
				return newErr("received credit with negative amount")
			}
			if len(cred.Name) > maxNameLen {
				return newErr("received credit with too long net-chan name")
			}
			d.toSendMn <- cred

		case errorMsg:
			var errStr string
			err = d.decode(&errStr)
			if err != nil {
				return
			}
			if errStr == EndOfSession.Error() {
				return EndOfSession
			}
			return errors.New("error from netchan peer: " + errStr)

		default:
			if h.Type < 0 || h.Type > lastReservedMsg {
				return fmtErr("received message with invalid type: %d", h.Type)
			}
		}
	}
}
