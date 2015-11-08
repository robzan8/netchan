package netchan

import (
	"io"
	"strconv"
	"testing"
	"time"
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
func intProducer(t *testing.T, man *Manager, chName string, n int) {
	go func() {
		ch := make(chan int, 8)
		err := man.Open(chName, Send, ch)
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
func intConsumer(t *testing.T, man *Manager, chName string) <-chan []int {
	sliceCh := make(chan []int, 1)
	go func() {
		var slice []int
		ch := make(chan int, 16)
		err := man.Open(chName, Recv, ch)
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
	intProducer(t, Manage(sideA), "integers", 100)
	time.Sleep(50 * time.Millisecond)
	s := <-intConsumer(t, Manage(sideB), "integers")
	checkIntSlice(t, s)
}

// start the consumer before the producer
func TestRecvThenSend(t *testing.T) {
	sideA, sideB := newPipeConn()
	sliceCh := intConsumer(t, Manage(sideB), "integers")
	time.Sleep(50 * time.Millisecond)
	intProducer(t, Manage(sideA), "integers", 100)
	checkIntSlice(t, <-sliceCh)
}

// open many chans in both directions
func TestManyChans(t *testing.T) {
	sideA, sideB := newPipeConn()
	manA := Manage(sideA)
	manB := Manage(sideB)
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
	intProducer(t, Manage(sideA), "integers", 1000)
	s := <-intConsumer(t, Manage(sideB), "integers")
	checkIntSlice(t, s)
}

func TestLimGobReader(t *testing.T) {
	sideA, sideB := newPipeConn()
	go sliceProducer(t, sideA)
	sliceConsumer(t, sideB)
}

const (
	limit     = 20  // size limit enforced by sliceConsumer
	numSlices = 100 // number of slices to send
)

// sliceProducer sends on "slices". The last slice will be too big.
func sliceProducer(t *testing.T, conn io.ReadWriteCloser) {
	mn := Manage(conn)
	ch := make(chan []byte, 1)
	err := mn.Open("slices", Send, ch)
	if err != nil {
		t.Error(err)
	}
	small := make([]byte, limit-10)
	big := make([]byte, limit+5)
	for i := 1; i <= numSlices; i++ {
		slice := small
		if i == numSlices {
			slice = big
		}
		select {
		case ch <- slice:
		case <-mn.ErrorSignal():
			t.Error(mn.Error())
		}
	}
	close(ch)
}

// sliceConsumer receives from "slices" using a LimGobReader.
// The last slice is too big and must generate errSizeExceeded
func sliceConsumer(t *testing.T, conn io.ReadWriteCloser) {
	mn := ManageLimit(conn, limit)
	// use a receive channel with capacity 1, so that items come
	// one at a time and we get the error for the last one only
	ch := make(chan []byte, 1)
	err := mn.Open("slices", Recv, ch)
	if err != nil {
		t.Error(err)
	}
	for i := 1; i <= numSlices; i++ {
		if i < numSlices {
			select {
			case <-ch:
			case <-mn.ErrorSignal():
				t.Error(mn.Error())
			}
			continue
		}
		// i == numSlices, expect errSizeExceeded
		select {
		case <-ch:
			t.Error("LimGobReader did not block too big message")
		case <-mn.ErrorSignal():
			err := mn.Error()
			if err.Error() == "netchan Manager: message size limit exceeded" {
				return // success
			}
			t.Error(err)
		}
	}
}
