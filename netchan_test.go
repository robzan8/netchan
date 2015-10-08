package netchan

import (
	"fmt"
	"io"
	"runtime"
	"sync"
	"testing"
	"time"
)

// simulate full duplex connection
type connection struct {
	readA, readB   io.Reader
	writeA, writeB io.Writer
}

func newConn() *connection {
	c := new(connection)
	c.readA, c.writeB = io.Pipe()
	//c.readB, c.writeA = io.Pipe()
	r, w := io.Pipe()
	c.readB = NewLimGobReader(r, 60)
	c.writeA = w
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
	smallBuf := 10
	bigBuf := 700

	conn := newConn()
	s := make([]int, n)
	done := make(chan struct{})

	go func() { // producer
		man1 := Manage(sideA{conn})
		ch1 := make(chan int, smallBuf)
		man1.Push("netchan test", ch1)
		for i := 0; i < n; i++ {
			ch1 <- i
		}
		close(ch1)
	}()

	go func() { // consumer
		time.Sleep(50 * time.Millisecond)
		man2 := Manage(sideB{conn})
		ch2 := make(chan int, smallBuf)
		man2.Pull("netchan test", ch2, bigBuf)
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
	smallBuf := 10
	bigBuf := 700

	conn := newConn()
	s := make([]int, n)
	done := make(chan struct{})

	go func() { // producer
		time.Sleep(50 * time.Millisecond)
		man1 := Manage(sideA{conn})
		ch1 := make(chan int, smallBuf)
		man1.Push("netchan test", ch1)
		for i := 0; i < n; i++ {
			ch1 <- i
		}
		close(ch1)
	}()

	go func() { // consumer
		man2 := Manage(sideB{conn})
		ch2 := make(chan int, smallBuf)
		man2.Pull("netchan test", ch2, bigBuf)
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

func TestManyChans(t *testing.T) {
	n := 500
	smallBuf := 10
	bigBuf := 100

	conn := newConn()
	var s [3][]int
	var wg sync.WaitGroup
	wg.Add(3)

	manA := Manage(sideA{conn})
	manB := Manage(sideB{conn})

	go func() { // producer1
		ch1 := make(chan int, smallBuf)
		manA.Push("netchan test1", ch1)
		for i := 0; i < n; i++ {
			ch1 <- i
		}
		close(ch1)
	}()
	go func() { // consumer1
		ch2 := make(chan int, smallBuf)
		manB.Pull("netchan test1", ch2, bigBuf)
		for i := range ch2 {
			s[0] = append(s[0], i)
		}
		wg.Done()
	}()

	go func() { // producer2
		ch1 := make(chan int, smallBuf)
		manB.Push("netchan test2", ch1)
		for i := 0; i < n; i++ {
			ch1 <- i
		}
		close(ch1)
	}()
	go func() { // consumer2
		ch2 := make(chan int, smallBuf)
		manA.Pull("netchan test2", ch2, bigBuf)
		for i := range ch2 {
			s[1] = append(s[1], i)
		}
		wg.Done()
	}()

	go func() { // producer3
		ch1 := make(chan int, smallBuf)
		manA.Push("netchan test3", ch1)
		for i := 0; i < n; i++ {
			ch1 <- i
		}
		close(ch1)
	}()
	go func() { // consumer3
		ch2 := make(chan int, smallBuf)
		manB.Pull("netchan test3", ch2, bigBuf)
		for i := range ch2 {
			s[2] = append(s[2], i)
		}
		wg.Done()
	}()

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	fmt.Println("memory!!!!!!!!!", mem.Alloc)

	wg.Wait()
	for k := 0; k < 3; k++ {
		for i, j := range s[k] {
			if i != j {
				t.Error("faileddddddddd")
				return
			}
		}
	}
}
