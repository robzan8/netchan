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
	max  uint64
	next int
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
	for len(buf) > 0 {
		// if we are not there yet, read up to the next message length counter or
		// up to len(buf) limit, whatever comes first (min)
		if l.next > 0 {
			min := len(buf)
			if min > l.next {
				min = l.next
			}
			// min > 0 here
			var n int
			n, err = l.r.Read(buf[:min])
			total += n
			l.next -= n
			buf = buf[min:]
			if n < min || err != nil || len(buf) == 0 {
				return
			}
		}

		// we reached a new counter and buf still has space;
		// parsing of the counter is done with peek, to leave the reader
		// in a consistent state in case of errors
		var p []byte
		p, err = l.r.Peek(1)
		if len(p) < 1 || err != nil {
			log.Fatal("noooooooooo")
			return
		}
		// retrieve message length
		var width int
		var msgLen uint64
		count := p[0]
		if count <= 0x7f {
			width = 0
			msgLen = uint64(count)
		} else {
			width = -int(int8(count))
			if width > uint64Size {
				log.Fatal("errBadUint")
				// set err
				return
			}
			p, err = l.r.Peek(width)
			if len(p) < width || err != nil {
				if err == io.EOF {
					err = io.ErrUnexpectedEOF
				}
				log.Fatal("peek unexpected EOF")
				return
			}
			// Could check that the high byte is zero but it's not worth it.
			for _, b := range p {
				msgLen = msgLen<<8 | uint64(b)
			}
		}

		if msgLen > l.max || msgLen > tooBig {
			log.Fatal("size exceeded")
			// set err
			return
		}
		buf[0] = count
		n := copy(buf[1:], p[:width])
		n++
		total += n
		buf = buf[n:]
		/*m, err := l.r.Discard(n)
		if m < n || err != nil {
			log.Fatal("damn, we were so close")
		}*/
		l.next = int(msgLen) // tooBig protects form overflow
	}
	return
}

func (l *LimGobReader) ReadByte() (c byte, err error) {
	log.Fatal("I don't want gob to actually call this")
	var b [1]byte
	_, err = l.Read(b[:])
	c = b[0]
	return
}
