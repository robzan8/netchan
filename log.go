package netchan

import (
	"fmt"
	"sync/atomic"
)

type Logger interface {
	Log(keyvals ...interface{}) error
}

var logger atomic.Value // *Logger

func init() {
	logger.Store(&defaultLog)
}

func GetLogger() Logger {
	return *logger.Load().(*Logger)
}

func SetLogger(l Logger) {
	if l == nil {
		logger.Store(&discardLog)
	}
	logger.Store(&l)
}

type logFn func(...interface{}) error

func (f logFn) Log(keyvals ...interface{}) error { return f(keyvals...) }

// netchan's default logger. Prints to stderr in logfmt format.
// TODO: implement it and document what it logs and how.
var defaultLog Logger = logFn(func(keyvals ...interface{}) error {
	_, err := fmt.Println(keyvals...)
	return err
})

var discardLog Logger = logFn(func(...interface{}) error { return nil })
