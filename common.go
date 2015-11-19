package netchan

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync"
)

// sha1-hashed name of a net-chan
type hashedName [20]byte

func hashName(name string) hashedName {
	return sha1.Sum([]byte(name))
}

// element is used internally to represent messages of type:
// elemMsg, initElemMsg and closeMsg.
type element struct {
	id   int           // ID of the net-chan
	val  reflect.Value // user data
	ok   bool          // if !ok, this is a closeMsg
	name *hashedName   // if not nil, this is an initElemMsg (overrides !ok)
}

// credit represents messages of type:
// creditMsg and initCredMsg.
type credit struct {
	id   int         // ID of the net-chan
	incr int64       // amount of credit
	name *hashedName // if not nil, this is an initCredMsg
}

// The sender component of a manager keeps a table of all the
// net-chans opened for sending and likewise does the receiver.
type chanTable struct {
	sync.RWMutex             // protects remaining fields
	t            []chanEntry // the table
	pending      []chanEntry // only the sender uses this, see sender.go
}

// An entry in a chanTable. Keep its size 64 bytes on x64 if possible.
type chanEntry struct {
	name hashedName // name of the net-chan

	// present is set to false when the net-chan gets closed
	// and the slot in the table can be reused.
	present bool

	// If init is true, the entry has just been added and
	// the initCredMsg or InitElemMsg has yet to be sent.
	init bool

	ch       reflect.Value // the Go channel from Open
	numElems int64         // see credit() and buffered() below
	recvCap  int64         // capacity of the receive channel
}

// For an entry in the sender's table,
// numElem is the credit the sender has for the net-chan.
func (e *chanEntry) credit() *int64 {
	return &e.numElems
}

// For an entry in the receiver's table,
// numElem counts how many elements have been received and put in the buffer.
func (e *chanEntry) buffered() *int64 {
	return &e.numElems
}

func entryByName(table []chanEntry, name hashedName) *chanEntry {
	for i := range table {
		entry := &table[i]
		if entry.present && entry.name == name {
			return entry
		}
	}
	return nil
}

func addEntry(table []chanEntry, entry chanEntry) (newTable []chanEntry) {
	for i := range table {
		if !table[i].present {
			// reuse empty slot
			table[i] = entry
			return table
		}
	}
	return append(table, entry)
}

func newErr(str string) error {
	return errors.New("netchan: " + str)
}

var errInvalidId = newErr("message with invalid ID received")

func fmtErr(format string, a ...interface{}) error {
	return fmt.Errorf("netchan: "+format, a...)
}

func errAlreadyOpen(name string, dir Dir) error {
	return fmtErr("Open: net-chan %s is already open with dir %s",
		strconv.Quote(name), dir)
}
