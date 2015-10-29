package netchan_test

import (
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/robzan8/netchan"
)

// pipeConn represents one side of a full-duplex
// connection based on io.PipeReader/Writer
type pipeConn struct {
	*io.PipeReader
	*io.PipeWriter
}

func (c pipeConn) Close() error {
	c.PipeReader.Close()
	c.PipeWriter.Close()
	return nil // ignoring errors
}

func newPipeConn() (sideA, sideB pipeConn) {
	sideA.PipeReader, sideB.PipeWriter = io.Pipe()
	sideB.PipeReader, sideA.PipeWriter = io.Pipe()
	return
}

// intProducer sends integers from 0 to n-1 on net-chan chName
func intProducer(t *testing.T, man *netchan.Manager, chName string, n int) {
	go func() {
		ch := make(chan int, 8)
		err := man.Open(chName, netchan.Send, ch)
		if err != nil {
			t.Error(err)
		}
		for i := 0; i < n; i++ {
			select {
			case ch <- i:
			case <-man.ErrorSignal():
				t.Error(man.Error())
			}
		}
		close(ch)
	}()
}

// intConsumer drains net-chan chName, stores the received integers in a slice
// and delivers the slice on a channel, which is returned
func intConsumer(t *testing.T, man *netchan.Manager, chName string) <-chan []int {
	sliceCh := make(chan []int, 1)
	go func() {
		var slice []int
		ch := make(chan int, 16)
		err := man.Open(chName, netchan.Recv, ch)
		if err != nil {
			t.Error(err)
		}
	Loop:
		for {
			select {
			case i, ok := <-ch:
				if !ok {
					break Loop
				}
				slice = append(slice, i)
			case <-man.ErrorSignal():
				t.Error(man.Error())
			}
		}
		sliceCh <- slice
	}()
	return sliceCh
}

// checks that s[i] == i for each i
func checkIntSlice(t *testing.T, s []int) {
	for i, si := range s {
		if i != si {
			t.Errorf("expected i == s[i], found i == %d, s[i] == %d", i, si)
			return
		}
	}
}

// start the producer before the consumer
func TestSendThenRecv(t *testing.T) {
	sideA, sideB := newPipeConn()
	intProducer(t, netchan.Manage(sideA), "integers", 100)
	time.Sleep(50 * time.Millisecond)
	s := <-intConsumer(t, netchan.Manage(sideB), "integers")
	checkIntSlice(t, s)
}

// start the consumer before the producer
func TestRecvThenSend(t *testing.T) {
	sideA, sideB := newPipeConn()
	sliceCh := intConsumer(t, netchan.Manage(sideB), "integers")
	time.Sleep(50 * time.Millisecond)
	intProducer(t, netchan.Manage(sideA), "integers", 100)
	checkIntSlice(t, <-sliceCh)
}

// open many chans in both directions
func TestManyChans(t *testing.T) {
	sideA, sideB := newPipeConn()
	manA := netchan.Manage(sideA)
	manB := netchan.Manage(sideB)
	var sliceChans [100]<-chan []int
	for i := range sliceChans {
		chName := "integers" + strconv.Itoa(i)
		if i%2 == 0 {
			// producer is sideA, consumer is sideB
			intProducer(t, manA, chName, 90)
			sliceChans[i] = intConsumer(t, manB, chName)
		} else {
			// producer is sideB, consumer is sideA
			intProducer(t, manB, chName, 90)
			sliceChans[i] = intConsumer(t, manA, chName)
		}
	}
	for _, ch := range sliceChans {
		checkIntSlice(t, <-ch)
	}
}

// send many integers on a net-chan with a small buffer. If the credit system is broken,
// at some point the credit will stay 0 (deadlock) or it will excede the limit,
// causing an error
// TODO: find a better way of testing this
func TestCredits(t *testing.T) {
	sideA, sideB := newPipeConn()
	intProducer(t, netchan.Manage(sideA), "integers", 1000)
	s := <-intConsumer(t, netchan.Manage(sideB), "integers")
	checkIntSlice(t, s)
}
