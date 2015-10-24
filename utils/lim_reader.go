package netchan

import (
	"bufio"
	"errors"
	"io"
)

// sanity check of 1GB, as in gob/decoder.go
const tooBig = 1 << 30

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

type LimGobReader struct {
	r    bufReader
	max  uint64 // maximum size of gob message allowed
	next int    // counts how many bytes are left before reaching the next length of message
}

func NewLimGobReader(r io.Reader, n int64) *LimGobReader {
	br, ok := r.(bufReader)
	if !ok {
		br = bufio.NewReader(r)
	}
	if n < 0 {
		n = 0
	}
	return &LimGobReader{br, uint64(n), 0}
}

func (l *LimGobReader) Read(buf []byte) (n int, err error) {
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

// reached the length of a new message,
// parse it like decodeUintReader in gob/decode.go
func (l *LimGobReader) readMsgLen(buf []byte) (n int, err error) {
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
	if msgLen > l.max || msgLen > tooBig {
		err = errSizeExceeded
		return
	}
	n, err = l.r.Discard(copy(buf, p))
	l.next = int(msgLen) + width - n // tooBig check prevents overflow
	return
}

// ReadByte is provided so that gob understands that this reader is buffered;
// see https://golang.org/pkg/encoding/gob/#NewDecoder
// The implementation is not efficient, but gob doesn't actually call this function.
func (l *LimGobReader) ReadByte() (c byte, err error) {
	var b [1]byte
	_, err = io.ReadFull(l, b[:])
	c = b[0]
	return
}
