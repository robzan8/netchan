package netchan

import (
	"bytes"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
)

type Logger interface {
	Log(keyvals ...interface{})
}

type defaultLogger struct{}

var logBufs = sync.Pool{New: func() interface{} { return new(bytes.Buffer) }}

// netchan's default logger. Prints to log.Output in logfmt format.
// TODO: document what it logs and how.
func (*defaultLogger) Log(keyvals ...interface{}) {
	buf := logBufs.Get().(*bytes.Buffer)

	err := false
	for k := 0; k < len(keyvals); k += 2 {
		var key, val interface{}
		key = keyvals[k]
		if k == len(keyvals)-1 {
			val = "[no_val]"
		} else {
			val = keyvals[k+1]
		}
		var sep byte
		if k >= len(keyvals)-2 {
			sep = '\n'
		} else {
			sep = ' '
		}
		if str := key.(string); str == "err" {
			err = true
		}
		format := "%s=%v%c" // key=value, separator
		str, ok := val.(string)
		if ok && strings.ContainsAny(str, " =\"\n\t") {
			format = "%s=%q%c" // quote value
		}
		fmt.Fprintf(buf, format, keyvals[k], val, sep)
	}
	if err {
		trace := make([]byte, 4096)
		n := runtime.Stack(trace, false)
		buf.WriteString("[netchan_begin_stack_trace]\n")
		buf.Write(trace[0:n])
		if n == 4096 {
			buf.WriteString("\n[netchan_stack_trace_truncated]\n")
		}
		buf.WriteString("[netchan_end_stack_trace]\n")
	}
	// calldepth of 3: source of log event -> Manager.log() -> defaultLogger.Log()
	log.Output(3, buf.String())
	// if logger performance sucks, consider going to Stdout through a buffered writer

	// don't hold on to large buffers
	if buf.Cap() > 1024 {
		return
	}
	buf.Reset()
	logBufs.Put(buf)
}

type discardLogger struct{}

func (*discardLogger) Log(...interface{}) {}

// netchan will log here
var logger Logger = (*defaultLogger)(nil)

func SetLogger(l Logger) {
	if l == nil {
		logger = (*discardLogger)(nil)
		return
	}
	logger = l
}
