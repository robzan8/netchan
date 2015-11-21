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
	incr int         // amount of credit
	name *hashedName // if not nil, this is an initCredMsg
}

// The sender component of a manager keeps a table of all the
// net-chans opened for sending and likewise does the receiver.
type nchTable struct {
	sync.Mutex            // protects remaining fields
	t          []nchEntry // the table
	pending    []nchEntry // only the sender uses this, see sender.go
}

// An entry in a nchTable. Keep it 64 bytes on x64 if possible.
type nchEntry struct {
	name    hashedName
	present bool

	sender   *sender
	halfOpen bool
	toSender chan<- int
	quit     <-chan struct{}

	buffer chan<- reflect.Value
}

func entryByName(table []nchEntry, name hashedName) *nchEntry {
	for i := range table {
		entry = &table[i]
		if entry.present && entry.name == name {
			return entry
		}
	}
	return nil
}

func addEntry(table []nchEntry, entry nchEntry) (newTable []nchEntry, i int) {
	for i = range table {
		if !table[i].present {
			// reuse empty slot
			table[i] = entry
			return table, i
		}
	}
	i = len(table)
	newTable = append(table, entry)
	return
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
