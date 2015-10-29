package netchan_test

import (
	"fmt"
	"io"
	"log"

	"github.com/robzan8/netchan"
)

// emitIntegers sends the integers from 1 to n on net-chan "integers".
// conn would normally be a TCP-like connection to the other peer.
func emitIntegers(conn io.ReadWriter, n int) {
	man := netchan.Manage(conn) // let a netchan.Manager handle the connection
	ch := make(chan int, 10)
	// open net-chan "integers" for sending with ch
	err := man.Open("integers", netchan.Send, ch)
	if err != nil {
		log.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		// check the error signal while sending (and receiving) messages,
		// otherwise the operation can block forever in case of error
		select {
		case ch <- i:
		case <-man.ErrorSignal():
			// when an error occurs, close the connection, so that the manager's
			// goroutines that are eventually stuck reading or writing can return
			man.CloseConn()
			log.Fatal(man.Error())
		}
	}
	close(ch) // the other peer's receiving channel will be closed;
	// messages in flight will not be lost

	// wait that the other peer receives everything
	// and closes the connection, we will get EOF or similar
	<-man.ErrorSignal()
}

// sumIntegers receives the integers from net-chan "integers" and returns their sum.
func sumIntegers(conn io.ReadWriter) int {
	man := netchan.Manage(conn)
	// Open with Recv direction requires a buffered channel. The buffer
	// size can affect performance, see the documentation about flow control
	ch := make(chan int, 40)
	err := man.Open("integers", netchan.Recv, ch)
	if err != nil {
		log.Fatal(err)
	}
	sum := 0
Loop:
	for {
		select {
		case i, ok := <-ch:
			if !ok {
				break Loop
			}
			sum += i
		case <-man.ErrorSignal():
			man.CloseConn()
			log.Fatal(man.Error())
		}
	}
	man.CloseConn()
	return sum
}

// This example shows a basic netchan session: two peers establish a connection and
// delegate its management to a netchan.Manager (one for peer); peer 1 opens a
// net-chan for sending; peer 2 opens the same net-chan (by name) for receiving;
// the peers communicate using the Go channels associated with the net-chans.
func Example_basic() {
	sideA, sideB := newPipeConn() // a connection based on io.PipeReader/Writer
	go emitIntegers(sideA, 100)
	sum := sumIntegers(sideB)
	fmt.Println(sum)
	// Output: 5050
}
