package netchan

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"reflect"
)

// sha1-hashed name of a net-chan
type hashedName [20]byte

func hashName(name string) hashedName {
	return sha1.Sum([]byte(name))
}

type hello struct{}

type userData struct {
	id       int
	batch    reflect.Value // if zero, represents end of stream
	batchLen *int32
}

type credit struct {
	id     int
	Amount int
	Name   *hashedName
}

func newErr(str string) error {
	return errors.New("netchan: " + str)
}

func fmtErr(format string, a ...interface{}) error {
	return fmt.Errorf("netchan: "+format, a...)
}

func errAlreadyOpen(dir, name string) error {
	return fmtErr("Open%s: net-chan %s is already open", dir, name)
}
