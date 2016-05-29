package netchan

import (
	"bufio"
	"encoding/gob"
	"errors"
	"io"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
)

type bufWriter interface {
	io.Writer
	Flush() error
}

type countWriter struct {
	w          io.Writer
	batchBytes int
	flushBytes int
}

func (c *countWriter) Write(data []byte) (n int, err error) {
	n, err = c.w.Write(data)
	c.batchBytes += n
	c.flushBytes += n
	return
}

type encoder struct {
	ssn      *Session
	dataCh   <-chan data
	creditCh <-chan credit
	countWr  countWriter
	enc      *gob.Encoder
	flush    func() error

	err        error
	flushStats stats
}

func newEncoder(ssn *Session, dataCh <-chan data, creditCh <-chan credit,
	conn io.Writer) *encoder {
	e := &encoder{ssn: ssn, dataCh: dataCh, creditCh: creditCh}
	bw, ok := conn.(bufWriter)
	if !ok {
		bw = bufio.NewWriter(conn)
	}
	e.countWr = countWriter{w: bw}
	e.enc = gob.NewEncoder(&e.countWr)
	e.flush = bw.Flush
	return e
}

func (e *encoder) encode(val interface{}) {
	// when an encoding error occurs, encode operations turn into NOPs
	if e.err != nil {
		return
	}
	e.err = e.enc.EncodeValue(reflect.ValueOf(val))
}

const wantBatchSize = 4096

func (e *encoder) handleData(dat data) {
	e.encode(dat.header)
	if e.err != nil || dat.Type == initDataMsg || dat.Type == closeMsg {
		return
	}
	// dat.Type is dataMsg
	e.countWr.batchBytes = 0
	e.err = e.enc.EncodeValue(dat.batch)
	if e.err != nil {
		return
	}
	itemSize := float64(e.countWr.batchBytes) / float64(dat.batch.Len())
	if itemSize < 1 {
		itemSize = 1
	}
	wantBatchLen := wantBatchSize / itemSize
	if wantBatchLen < 1 {
		wantBatchLen = 1
	}
	batchLen := float64(*dat.batchLenPt)
	if batchLen < wantBatchLen*0.75 || batchLen > wantBatchLen*1.25 {
		atomic.StoreInt32(dat.batchLenPt, int32(wantBatchLen))
	}
}

func (e *encoder) bufAndFlush() {
	for i := 0; i < cap(e.creditCh); i++ {
		select {
		case c := <-e.creditCh:
			e.encode(c.header)
			e.encode(c.amount)
			continue
		default:
		}
		break
	}
	for i := 0; i < cap(e.dataCh); i++ {
		select {
		case d := <-e.dataCh:
			e.handleData(d)
			continue
		default:
		}
		runtime.Gosched()
		select {
		case d := <-e.dataCh:
			e.handleData(d)
			continue
		default:
		}
		break
	}
	if e.err != nil {
		return
	}
	e.flushStats.update(float64(e.countWr.flushBytes))
	e.countWr.flushBytes = 0
	e.err = e.flush()
}

func (e *encoder) run() {
	e.encode(header{Type: helloMsg})
	e.encode(hello{})
	e.bufAndFlush()
Loop:
	for {
		if e.err != nil {
			e.ssn.QuitWith(e.err)
			return
		}
		select {
		case d := <-e.dataCh:
			e.handleData(d)
		case c := <-e.creditCh:
			e.encode(c.header)
			e.encode(c.amount)
		case <-e.ssn.Done():
			break Loop
		}
		e.bufAndFlush()
	}

	e.encode(header{Type: errorMsg})
	e.encode(e.ssn.Err().Error())
	e.flush()
	logDebug("netchan session %d is done, flushBytes stats:\n\t%s",
		e.ssn.id, &e.flushStats)
	e.ssn.closeConn()
}

type bufReader interface {
	io.Reader
	io.ByteReader
}

// Like io.LimitedReader, but returns a custom error.
type limitedReader struct {
	bufReader     // underlying reader
	n         int // max bytes remaining
}

var errMsgTooBig = fmtErr("too big gob message received")

func (l *limitedReader) Read(p []byte) (m int, err error) {
	if l.n <= 0 {
		return 0, errMsgTooBig
	}
	if len(p) > l.n {
		p = p[0:l.n]
	}
	m, err = l.bufReader.Read(p)
	l.n -= m
	return
}

type typeTable struct {
	sync.Mutex
	batchType map[int]reflect.Type
}

type decoder struct {
	ssn          *Session
	toRecvMn     chan<- data
	toSendMn     chan<- credit
	msgSizeLimit int
	types        typeTable // updated by recvManager
	limitedRd    limitedReader
	dec          *gob.Decoder
}

func newDecoder(ssn *Session, dataCh chan<- data, creditCh chan<- credit,
	conn io.Reader, lim int) *decoder {
	d := &decoder{ssn: ssn, toRecvMn: dataCh, toSendMn: creditCh, msgSizeLimit: lim}
	d.types.batchType = make(map[int]reflect.Type)
	br, ok := conn.(bufReader)
	if !ok {
		br = bufio.NewReader(conn)
	}
	d.limitedRd = limitedReader{bufReader: br}
	d.dec = gob.NewDecoder(&d.limitedRd)
	return d
}

func (d *decoder) decode(val interface{}) error {
	d.limitedRd.n = d.msgSizeLimit
	return d.dec.DecodeValue(reflect.ValueOf(val))
}

func (d *decoder) run() (err error) {
	defer func() {
		close(d.toRecvMn)
		close(d.toSendMn)
		d.ssn.QuitWith(err)
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
		if err = d.ssn.Err(); err != nil {
			return
		}
		var h header
		err = d.decode(&h)
		if err != nil {
			return
		}
		// check h.ChName length?
		switch h.Type {
		case helloMsg:
			return fmtErr("hello message received again")

		case dataMsg:
			d.types.Lock()
			batchType, present := d.types.batchType[h.ChId]
			d.types.Unlock()
			if !present {
				return fmtErr("message with invalid ID received (%d)\n", h.ChId)
			}
			batch := reflect.New(batchType).Elem()
			d.limitedRd.n = d.msgSizeLimit
			err = d.dec.DecodeValue(batch)
			if err != nil {
				return
			}
			d.toRecvMn <- data{header: h, batch: batch}

		case initDataMsg, closeMsg:
			d.toRecvMn <- data{header: h}

		case creditMsg:
			c := credit{header: h}
			err = d.decode(&c.amount)
			if err != nil {
				return
			}
			// sendManager expects only positive credits.
			if c.amount == 0 {
				continue
			}
			if c.amount < 0 {
				return fmtErr("received credit with negative amount")
			}
			d.toSendMn <- c

		case initCreditMsg:
			c := credit{header: h}
			err = d.decode(&c.amount)
			if err != nil {
				return
			}
			if c.amount < 1 {
				return fmtErr("received initial credit with non-positive amount")
			}
			d.toSendMn <- c

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
