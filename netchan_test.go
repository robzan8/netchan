package netchan

import (
	"crypto/sha1"
	"encoding/binary"
	"math/rand"
	"strconv"
	"testing"
	"time"
)

const smallBuf = 8

func intProducer(man *Manager, chName string, n int) {
	go func() {
		ch := make(chan int, smallBuf)
		man.Push(ch, chName)
		for i := 0; i < n; i++ {
			ch <- i
		}
		close(ch)
	}()
}

func intConsumer(man *Manager, chName string) <-chan []int {
	sliceCh := make(chan []int, 1)
	go func() {
		var slice []int
		ch := make(chan int, smallBuf)
		man.Pull(ch, chName)
		for i := range ch {
			slice = append(slice, i)
		}
		sliceCh <- slice
	}()
	return sliceCh
}

func checkIntSlice(s []int, t *testing.T) {
	for i, si := range s {
		if i != si {
			t.Errorf("expected i == s[i], found i == %d, s[i] == %d", i, si)
			return
		}
	}
}

func TestPushThenPull(t *testing.T) {
	conn := newConn()
	intProducer(Manage(sideA(conn)), "int chan", 100)
	time.Sleep(50 * time.Millisecond)
	s := <-intConsumer(Manage(sideB(conn)), "int chan")
	checkIntSlice(s, t)
}

func TestPullThenPush(t *testing.T) {
	conn := newConn()
	sliceCh := intConsumer(Manage(sideB(conn)), "int chan")
	time.Sleep(50 * time.Millisecond)
	intProducer(Manage(sideA(conn)), "int chan", 100)
	checkIntSlice(<-sliceCh, t)
}

func TestManyChans(t *testing.T) {
	conn := newConn()
	manA := Manage(sideA(conn))
	manB := Manage(sideB(conn))
	var sliceChs [10]<-chan []int
	for i := range sliceChs {
		chName := "int chan" + strconv.Itoa(i)
		if i%2 == 0 {
			// producer is sideA, consumer is sideB
			intProducer(manA, chName, 100)
			sliceChs[i] = intConsumer(manB, chName)
		} else {
			// producer is sideB, consumer is sideA
			intProducer(manB, chName, 100)
			sliceChs[i] = intConsumer(manA, chName)
		}
	}
	for _, ch := range sliceChs {
		checkIntSlice(<-ch, t)
	}
}

var mapN = 1000
var r = rand.New(rand.NewSource(time.Now().UnixNano()))
var intMap = make(map[int]int)
var nameMap = make(map[hashedName]hashedName)
var s = make([]hashedName, 1000)

func init() {
	for i := 0; i < mapN; i++ {
		var name hashedName
		binary.PutVarint(name[:], int64(i))
		s[i] = sha1.Sum(name)
	}
	for i := 0; i < mapN; i++ {
		j := r.Intn(n)
		intMap[i] = j
		nameMap[s[i]] = s[j]
	}
}

func BenchmarkIntMap(b *testing.B) {
	var i int
	for n := 0; n < b.N; n++ {
		i = intMap[i]
	}
}

func BenchmarkNameMap(b *testing.B) {
	var name hashedName
	for n := 0; n < b.N; n++ {
		name = nameMap[name]
	}
}
