package netchan

import (
	"fmt"
	"testing"
)

func TestRandomnessMapRange(t *testing.T) {
	n := 30
	m := make(map[int]int)
	for i := 0; i < n; i++ {
		m[i] = i
	}
	var s []int
	for i := 0; i < n; i++ {
		for i := range m {
			s = append(s, i)
			break
		}
	}
	fmt.Println(s)
}
