package netchan_test

import (
	"bufio"
	"errors"
	"io"
	"testing"

	"github.com/robzan8/netchan"
)

const tooBig = 1 << 30 // sanity check of 1GB, as in gob/decoder.go
const uint64Size = 8

var (
	errBadUint      = errors.New("limited gob reader: encoded unsigned integer out of range")
	errSizeExceeded = errors.New("limited gob reader: maximum message size exceeded")
)

type bufReader interface {
	io.Reader
	Discard(int) (int, error)
	Peek(int) ([]byte, error)
}

// limGobReader reads from its underlying reader
// and returns an error when a too big gob message is encountered;
// subsequent calls to Read will keep returning the error without advancing.
type limGobReader struct {
	r     bufReader // the underlying reader
	limit uint64    // maximum size of gob message allowed
	next  int       // counts how many bytes are left before reaching the next message
}

// newLimGobReader creates a new limGobReader that reads from r, with n as message size limit.
// If r is not buffered, it is wrapped with a bufio.Reader.
func newLimGobReader(r io.Reader, n int64) *limGobReader {
	br, ok := r.(bufReader)
	if !ok {
		br = bufio.NewReader(r)
	}
	if n < 0 {
		n = 0
	}
	return &limGobReader{br, uint64(n), 0}
}

func (l *limGobReader) Read(buf []byte) (n int, err error) {
	if len(buf) == 0 {
		return
	}
	if l.next == 0 {
		n, err = l.readMsgLen(buf)
		if err != nil {
			return
		}
		buf = buf[n:]
	}

	toRead := l.next
	if toRead > len(buf) {
		toRead = len(buf)
	}
	m, err := l.r.Read(buf[:toRead])
	n += m
	l.next -= m
	return
}

// readMsgLen parses the length of a message like decodeUintReader
// in gob/decode.go and check if it exceeds the limit
func (l *limGobReader) readMsgLen(buf []byte) (n int, err error) {
	p, err := l.r.Peek(1)
	if err != nil {
		return
	}
	count := p[0]
	var width int
	var msgLen uint64
	if count <= 0x7f {
		width = 1
		msgLen = uint64(count)
	} else {
		width = -int(int8(count))
		if width > uint64Size {
			err = errBadUint
			return
		}
		width++ // +1 for the count byte
		p, err = l.r.Peek(width)
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return
		}
		for _, b := range p[1:] {
			msgLen = msgLen<<8 | uint64(b)
		}
	}
	if msgLen > l.limit || msgLen > tooBig {
		err = errSizeExceeded
		return
	}
	n, err = l.r.Discard(copy(buf, p))
	l.next = int(msgLen) + width - n // tooBig check prevents overflow
	return
}

// ReadByte is provided so that gob.NewDecoder understands that limGobReader is buffered
// and does not add a new buffer layer.
// The implementation is not efficient, but gob doesn't actually call this function.
func (l *limGobReader) ReadByte() (c byte, err error) {
	var b [1]byte
	_, err = io.ReadFull(l, b[:])
	c = b[0]
	return
}

func TestLimGobReader(t *testing.T) {
	sideA, sideB := newPipeConn()
	go sliceProducer(t, sideA)
	sliceConsumer(t, sideB)
}

const (
	limit     = 100 // size limit enforced by sliceConsumer
	numSlices = 100 // number of slices to send
)

// sliceProducer sends on "slices". The last slice will be too big.
func sliceProducer(t *testing.T, conn io.ReadWriteCloser) {
	man := netchan.Manage(conn)
	ch := make(chan []byte, 1)
	err := man.Open("slices", netchan.Send, ch)
	if err != nil {
		t.Error(err)
	}
	small := make([]byte, limit-10)
	big := make([]byte, limit+5)
	for i := 1; i <= numSlices; i++ {
		slice := small
		if i == numSlices {
			slice = big
		}
		select {
		case ch <- slice:
		case <-man.ErrorSignal():
			t.Error(man.Error())
		}
	}
	close(ch)
}

// sliceConsumer receives from "slices" using a limGobReader.
// The last slice is too big and must generate errSizeExceeded
func sliceConsumer(t *testing.T, conn io.ReadWriteCloser) {
	type readWriteCloser struct {
		io.Reader
		io.WriteCloser
	}

	rw := readWriteCloser{newLimGobReader(conn, limit), conn}
	man := netchan.Manage(rw)
	// use a receive channel with capacity 1, so that items come
	// one at a time and we get the error for the last one only
	ch := make(chan []byte, 1)
	err := man.Open("slices", netchan.Recv, ch)
	if err != nil {
		t.Error(err)
	}
	for i := 1; i <= numSlices; i++ {
		if i < numSlices {
			select {
			case <-ch:
			case <-man.ErrorSignal():
				t.Error(man.Error())
			}
			continue
		}
		// i == numSlices, expect errSizeExceeded
		select {
		case <-ch:
			t.Error("limited gob reader did not block too big message")
		case <-man.ErrorSignal():
			err := man.Error()
			if err == errSizeExceeded {
				return // success
			}
			t.Error(err)
		}
	}
}
