package netchan

import (
	"strconv"
	"testing"
	"time"
)

const smallBuf = 8

func intProducer(t *testing.T, man *Manager, chName string, n int) {
	go func() {
		<-man.GotError()
		t.Error(man.Error())
	}()
	go func() {
		ch := make(chan int, smallBuf)
		err := man.Push(ch, chName)
		if err != nil {
			t.Error(err)
		}
		for i := 0; i < n; i++ {
			ch <- i
		}
		close(ch)
	}()
}

func intConsumer(t *testing.T, man *Manager, chName string) <-chan []int {
	go func() {
		<-man.GotError()
		t.Error(man.Error())
	}()
	sliceCh := make(chan []int, 1)
	go func() {
		var slice []int
		ch := make(chan int, smallBuf)
		err := man.Pull(ch, chName)
		if err != nil {
			t.Error(err)
		}
		for i := range ch {
			slice = append(slice, i)
		}
		sliceCh <- slice
	}()
	return sliceCh
}

func checkIntSlice(t *testing.T, s []int) {
	for i, si := range s {
		if i != si {
			t.Errorf("expected i == s[i], found i == %d, s[i] == %d", i, si)
			return
		}
	}
}

func TestPushThenPull(t *testing.T) {
	conn := newConn()
	intProducer(t, Manage(sideA(conn)), "int chan", 100)
	time.Sleep(50 * time.Millisecond)
	s := <-intConsumer(t, Manage(sideB(conn)), "int chan")
	checkIntSlice(t, s)
}

func TestPullThenPush(t *testing.T) {
	conn := newConn()
	sliceCh := intConsumer(t, Manage(sideB(conn)), "int chan")
	time.Sleep(50 * time.Millisecond)
	intProducer(t, Manage(sideA(conn)), "int chan", 100)
	checkIntSlice(t, <-sliceCh)
}

func TestManyChans(t *testing.T) {
	conn := newConn()
	manA := Manage(sideA(conn))
	manB := Manage(sideB(conn))
	var sliceChs [8]<-chan []int
	for i := range sliceChs {
		chName := "int chan" + strconv.Itoa(i)
		if i%2 == 0 || true { //!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
			// producer is sideA, consumer is sideB
			intProducer(t, manA, chName, 100)
			sliceChs[i] = intConsumer(t, manB, chName)
		} else {
			// producer is sideB, consumer is sideA
			intProducer(t, manB, chName, 100)
			sliceChs[i] = intConsumer(t, manA, chName)
		}
	}
	for _, ch := range sliceChs {
		checkIntSlice(t, <-ch)
	}
}
