package netchan

import (
	"bytes"
	"testing"
	"time"
)

type connection struct {
	buf1, buf2 bytes.Buffer
}

// simulate full duplex connection
type sideA struct {
	conn *connection
}
type sideB struct {
	conn *connection
}

func (c sideA) Read(s []byte) (int, error) {
	return c.conn.buf1.Read(s)
}
func (c sideA) Write(s []byte) (int, error) {
	return c.conn.buf2.Write(s)
}

func (c sideB) Read(s []byte) (int, error) {
	return c.conn.buf2.Read(s)
}
func (c sideB) Write(s []byte) (int, error) {
	return c.conn.buf1.Write(s)
}

func TestPushThenPull(t *testing.T) {
	n := 50
	buf := 10

	conn := new(connection)
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
		time.Sleep(300 * time.Millisecond)
		man2 := Manage(sideB{conn})
		ch2 := make(chan int, buf)
		man2.Pull(ch2, "netchan")
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

	conn := new(connection)
	s := make([]int, n)
	done := make(chan struct{})

	go func() { // producer
		time.Sleep(300 * time.Millisecond)
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
		man2.Pull(ch2, "netchan")
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
