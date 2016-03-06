// +build !debug

package netchan

func logDebug(format string, args ...interface{}) {}

type stats struct{}

func (s *stats) update(val float64) {}

func (s *stats) String() string { return "" }
