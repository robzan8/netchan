package netchan

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"reflect"
	"strconv"
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
