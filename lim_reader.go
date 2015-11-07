package netchan

import (
	"bufio"
	"errors"
	"io"
)

type bufReader interface {
	io.Reader
	Discard(int) (int, error)
	Peek(int) ([]byte, error)
}

// LimGobReader reads gob data from the underlying reader and returns an error when a too big gob message is encountered;
// subsequent calls to Read will keep returning the error without advancing.
type LimGobReader struct {
	r     bufReader // the underlying reader
	limit uint64    // maximum size of gob message allowed
	next  int       // counts how many bytes are left before reaching the next message
}

// NewLimGobReader creates a new LimGobReader that reads from r, with n as message size limit.
// If r is not buffered, it is wrapped with a bufio.Reader.
func NewLimGobReader(r io.Reader, n int) *LimGobReader {
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

// readMsgLen parses the length of a message like decodeUintReader
// in gob/decode.go and checks if it exceeds the limit
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
		uint64Size := 8
		if width > uint64Size {
			err = errors.New("netchan LimGobReader: unsigned integer out of range")
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
	var tooBig uint64 = 1 << 30 // sanity check of 1GB, as in gob/decoder.go
	if msgLen > l.limit || msgLen > tooBig {
		err = errors.New("netchan LimGobReader: message size limit exceeded")
		return
	}
	n, err = l.r.Discard(copy(buf, p))
	l.next = int(msgLen) + width - n // tooBig check prevents overflow
	return
}

// ReadByte is provided so that gob.NewDecoder understands that LimGobReader is buffered
// and does not add a new buffer layer.
// The implementation is not efficient, but gob doesn't actually call this function.
func (l *LimGobReader) ReadByte() (c byte, err error) {
	var b [1]byte
	_, err = io.ReadFull(l, b[:])
	c = b[0]
	return
}
