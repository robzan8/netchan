// +build debug

package netchan

import (
	"fmt"
	"log"
	"math"
)

func logDebug(format string, args ...interface{}) {
	log.Printf(format, args...)
}

type stats struct {
	count, sum, min, max float64
}

func (s *stats) update(val float64) {
	if s.count == 0 {
		// initialize
		min = math.Inf(1)
		max = math.Inf(-1)
	}
	s.count++
	s.sum += val
	if val < s.min {
		s.min = val
	}
	if val > s.max {
		s.max = val
	}
}

func (s *stats) String() string {
	return fmt.Sprintf("[count: %.0f, avg: %.2f, min: %.2f, max: %.2f]",
		s.count, s.sum/s.count, s.min, s.max)
}
