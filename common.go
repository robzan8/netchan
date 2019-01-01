package netchan

import (
	"errors"
	"fmt"
	"reflect"
)

// EndOfSession is used to signal the graceful end of a netchan session
// (typically with Session.ShutDown).
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
	Type   msgType
	ChId   int
	ChName string
}

type hello struct{}

type data struct {
	header
	batch      reflect.Value
	batchLenPt *int32
}

type credit struct {
	header
	amount int
}

func fmtErr(format string, a ...interface{}) error {
	return fmt.Errorf("netchan: "+format, a...)
}
