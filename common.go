package netchan

import (
	"errors"
	"fmt"
	"reflect"
)

// EndOfSession is used to signal the graceful end of a netchan session
// (typically with Manager.ShutDown).
var EndOfSession = errors.New("netchan: end of session")

type msgType int

const (
	helloMsg msgType = iota
	dataMsg
	initDataMsg
	closeMsg
	creditMsg
	initCreditMsg
	errorMsg

	lastReservedMsg = 15
)

// preceedes every message
type header struct {
	Type msgType
	Id   int
	Name string
}

type hello struct{}

type userData struct {
	header
	batch      reflect.Value
	batchLenPt *int64
}

type credit struct {
	header
	amount int
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
