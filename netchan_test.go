package netchan

import (
	"sync"
	"testing"
	"time"
)

const smallBuf = 8

func TestPushThenPull(t *testing.T) {
	n := 50

	conn := newConn()
	s := make([]int, n)
	done := make(chan struct{})

	go func() { // producer
		man1 := Manage(sideA(conn))
		ch1 := make(chan int, smallBuf)
		man1.Push(ch1, "netchan test")
		for i := 0; i < n; i++ {
			ch1 <- i
		}
		close(ch1)
	}()

	go func() { // consumer
		time.Sleep(50 * time.Millisecond)
		man2 := Manage(sideB(conn))
		ch2 := make(chan int, smallBuf)
		man2.Pull(ch2, "netchan test")
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

	conn := newConn()
	s := make([]int, n)
	done := make(chan struct{})

	go func() { // producer
		time.Sleep(50 * time.Millisecond)
		man1 := Manage(sideA(conn))
		ch1 := make(chan int, smallBuf)
		man1.Push(ch1, "netchan test")
		for i := 0; i < n; i++ {
			ch1 <- i
		}
		close(ch1)
	}()

	go func() { // consumer
		man2 := Manage(sideB(conn))
		ch2 := make(chan int, smallBuf)
		man2.Pull(ch2, "netchan test")
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

	conn := newConn()
	var s [3][]int
	var wg sync.WaitGroup
	wg.Add(3)

	manA := Manage(sideA(conn))
	manB := Manage(sideB(conn))

	go func() { // producer1
		ch1 := make(chan int, smallBuf)
		manA.Push(ch1, "netchan test1")
		for i := 0; i < n; i++ {
			ch1 <- i
		}
		close(ch1)
	}()
	go func() { // consumer1
		ch2 := make(chan int, smallBuf)
		manB.Pull(ch2, "netchan test1")
		for i := range ch2 {
			s[0] = append(s[0], i)
		}
		wg.Done()
	}()

	go func() { // producer2
		ch1 := make(chan int, smallBuf)
		manB.Push(ch1, "netchan test2")
		for i := 0; i < n; i++ {
			ch1 <- i
		}
		close(ch1)
	}()
	go func() { // consumer2
		ch2 := make(chan int, smallBuf)
		manA.Pull(ch2, "netchan test2")
		for i := range ch2 {
			s[1] = append(s[1], i)
		}
		wg.Done()
	}()

	go func() { // producer3
		ch1 := make(chan int, smallBuf)
		manA.Push(ch1, "netchan test3")
		for i := 0; i < n; i++ {
			ch1 <- i
		}
		close(ch1)
	}()
	go func() { // consumer3
		ch2 := make(chan int, smallBuf)
		manB.Pull(ch2, "netchan test3")
		for i := range ch2 {
			s[2] = append(s[2], i)
		}
		wg.Done()
	}()

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
