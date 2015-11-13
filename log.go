package netchan

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

type Logger interface {
	Log(keyvals ...interface{}) error
}

var log Logger = newDefaultLogger()

func SetLogger(logger Logger) {
	if logger == nil {
		log = discardLog
		return
	}
	log = logger
}

type defaultLogger struct {
	sync.Mutex
	*bufio.Writer
}

func newDefaultLogger() *defaultLogger {
	l := new(defaultLogger)
	l.Writer = bufio.NewWriter(os.Stderr)
	return l
}

// netchan's default logger. Prints to stderr in logfmt format.
// TODO: document what it logs and how.
func (l *defaultLogger) Log(keyvals ...interface{}) error {
	l.Lock()
	defer l.Unlock()

	if len(keyvals)%2 == 1 {
		keyvals = append(keyvals, "[no_val]")
	}
	for k := 0; k < len(keyvals); k += 2 {
		val := keyvals[k+1]
		err, ok := val.(error)
		if ok {
			val = err.Error()
		}
		str, ok := val.(string)
		if ok && strings.ContainsAny(str, " \"\n\t\b\f\r\v") {
			val = strconv.Quote(str)
		}
		var sep byte
		if k == len(keyvals)-2 {
			sep = '\n'
		} else {
			sep = ' '
		}
		fmt.Fprintf(l, "%s=%v%c", keyvals[k], val, sep)
	}
	l.Flush()
	return nil
}

type discardLogger struct{}

func (l discardLogger) Log(...interface{}) error { return nil }

var discardLog = discardLogger{}
