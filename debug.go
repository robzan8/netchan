// +build nchdebug

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
	count, mean, min, max, variance float64
}

// update updates the statistics with the new value x.
// Running variance algorithm is from wikipedia<-Knuth<-Welford.
func (s *stats) update(x float64) {
	if s.count == 0 {
		s.count = 1
		s.mean = x
		s.min = x
		s.max = x
		s.variance = 0
		return
	}
	if x < s.min {
		s.min = x
	}
	if x > s.max {
		s.max = x
	}
	s.count++
	delta := x - s.mean
	s.mean += delta / s.count
	s.variance += delta * (x - s.mean)
}

func (s *stats) String() string {
	return fmt.Sprintf("{count: %.0f, mean: %.2f, min: %.2f, max: %.2f, stdDev: %.2f}",
		s.count, s.mean, s.min, s.max, math.Sqrt(s.variance/(s.count-1)))
}
