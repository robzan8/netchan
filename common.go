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

// returns ceil(x/y)
func ceilDivide(x, y int) int {
	return (x + y - 1) / y
}

type message struct {
	name    hashedName
	payload interface{}
}

// element payloads:

type hello struct{}

type userData struct {
	val reflect.Value
}

type wantToSend struct{}

type flush struct{}

type wantToFlush1 struct{}

type wantToFlush2 struct{}

type endOfStream struct{}

// credit payloads:

type credit struct {
	Amount int
}

type initialCredit credit

// errors:

func newErr(str string) error {
	return errors.New("netchan: " + str)
}

var errInvalidId = newErr("message with invalid ID received")

func fmtErr(format string, a ...interface{}) error {
	return fmt.Errorf("netchan: "+format, a...)
}

func errAlreadyOpen(dir, name string) error {
	return fmtErr("Open%s: net-chan %s is already open", dir, strconv.Quote(name))
}
