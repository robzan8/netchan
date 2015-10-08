package netchan

import (
	"bufio"
	"io"
	"log"
)

// sanity check of 1GB, as in gob/decoder.go
const tooBig = 1 << 30

const uint64Size = 8

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

func (l *LimGobReader) Read(buf []byte) (total int, err error) {
	if len(buf) == 0 {
		return
	}
	if l.next == 0 {
		// reached the length of a new message,
		// parse it like decodeUintReader in gob/decode.go
		var p []byte
		p, err = l.r.Peek(1)
		if err != nil {
			return
		}
		var width int
		var msgLen uint64
		count := p[0]
		if count <= 0x7f {
			width = 1
			msgLen = uint64(count)
		} else {
			width = -int(int8(count))
			if width > uint64Size {
				log.Fatal("errBadUint")
				// set err
				return
			}
			width++ // +1 for the count byte
			p, err = l.r.Peek(width)
			if err != nil {
				if err == io.EOF {
					err = io.ErrUnexpectedEOF
				}
				log.Fatal("peek failed")
				return
			}
			// Could check that the high byte is zero but it's not worth it.
			for _, b := range p[1:] {
				msgLen = msgLen<<8 | uint64(b)
			}
		}
		if msgLen > l.max || msgLen > tooBig {
			log.Fatal("size exceeded")
			// set err
			return
		}
		var n int
		n, err = l.r.Discard(copy(buf, p))
		total += n
		buf = buf[n:]
		l.next = int(msgLen) + width - n // tooBig protects form overflow
		if err != nil {
			return
		}
	}

	// if we are not there yet, read up to the next message length counter or
	// up to len(buf) limit, whatever comes first (min)
	min := len(buf)
	if min > l.next {
		min = l.next
	}
	// min > 0 here
	var n int
	n, err = l.r.Read(buf[:min])
	total += n
	l.next -= n
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
