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
	Init, Close bool
	id          int
	Name        string
	batch       reflect.Value
	batchLen    *int32
}

type credit struct {
	Init   bool
	id     int
	Name   string
	Amount int
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
