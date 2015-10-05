package netchan

import (
	"io"
	"testing"
	"time"
)

// simulate full duplex connection
type connection struct {
	readA, readB   *io.PipeReader
	writeA, writeB *io.PipeWriter
}

func newConn() *connection {
	c := new(connection)
	c.readA, c.writeB = io.Pipe()
	c.readB, c.writeA = io.Pipe()
	return c
}

type sideA struct {
	conn *connection
}

func (c sideA) Read(s []byte) (int, error) {
	return c.conn.readA.Read(s)
}
func (c sideA) Write(s []byte) (int, error) {
	return c.conn.writeA.Write(s)
}

type sideB struct {
	conn *connection
}

func (c sideB) Read(s []byte) (int, error) {
	return c.conn.readB.Read(s)
}
func (c sideB) Write(s []byte) (int, error) {
	return c.conn.writeB.Write(s)
}

func TestPushThenPull(t *testing.T) {
	n := 50
	buf := 10

	conn := newConn()
	s := make([]int, n)
	done := make(chan struct{})

	go func() { // producer
		man1 := Manage(sideA{conn})
		ch1 := make(chan int, buf)
		man1.Push(ch1, "netchan")
		for i := 0; i < n; i++ {
			ch1 <- i
		}
		close(ch1)
	}()

	go func() { // consumer
		time.Sleep(50 * time.Millisecond)
		man2 := Manage(sideB{conn})
		ch2 := make(chan int, buf)
		man2.Pull(ch2, "netchan", 20)
		for i := range ch2 {
			s[i] = i
		}
		done <- struct{}{}
	}()

	<-done
	for i, j := range s {
		if i != j {
			t.Error("faileddddddddd", s)
			return
		}
	}
}

func TestPullThenPush(t *testing.T) {
	n := 50
	buf := 10

	conn := newConn()
	s := make([]int, n)
	done := make(chan struct{})

	go func() { // producer
		time.Sleep(50 * time.Millisecond)
		man1 := Manage(sideA{conn})
		ch1 := make(chan int, buf)
		man1.Push(ch1, "netchan")
		for i := 0; i < n; i++ {
			ch1 <- i
		}
		close(ch1)
	}()

	go func() { // consumer
		man2 := Manage(sideB{conn})
		ch2 := make(chan int, buf)
		man2.Pull(ch2, "netchan", 20)
		for i := range ch2 {
			s[i] = i
		}
		done <- struct{}{}
	}()

	<-done
	for i, j := range s {
		if i != j {
			t.Error("faileddddddddd", s)
			return
		}
	}
}
