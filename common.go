package netchan

import (
	"errors"
	"fmt"
	"reflect"
)

// EndOfSession is used to signal the graceful end of a netchan session
// (typically with Manager.ShutDown).
var EndOfSession = errors.New("netchan: end of session")

type hello struct{}

type userData struct {
	id          int
	Init, Close bool
	batch       reflect.Value
	batchLen    *int32
}

type credit struct {
	id     int
	Init   bool
	Amount int
	Name   string
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
