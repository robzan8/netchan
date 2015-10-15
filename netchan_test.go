package netchan

import (
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

func checkError(man *Manager, t *testing.T) {
	<-man.GotError()
	t.Error(man.Error())
}

func TestPushThenPull(t *testing.T) {
	conn := newConn()
	manA := Manage(sideA(conn))
	manB := Manage(sideB(conn))
	go checkError(manA, t)
	go checkError(manB, t)
	intProducer(manA, "int chan", 100)
	time.Sleep(50 * time.Millisecond)
	s := <-intConsumer(manB, "int chan")
	checkIntSlice(s, t)
}

func TestPullThenPush(t *testing.T) {
	conn := newConn()
	manA := Manage(sideA(conn))
	manB := Manage(sideB(conn))
	go checkError(manA, t)
	go checkError(manB, t)
	sliceCh := intConsumer(manB, "int chan")
	time.Sleep(50 * time.Millisecond)
	intProducer(manA, "int chan", 100)
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
