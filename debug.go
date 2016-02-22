// +build debug

package netchan

import "log"

func logDebug(format string, args ...interface{}) {
	log.Printf(format, args...)
}
