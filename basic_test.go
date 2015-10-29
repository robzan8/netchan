package netchan_test

import (
	"fmt"
	"io"
	"log"
	"time"

	"github.com/robzan8/netchan"
)

// emitIntegers sends the integers from 1 to n on net-chan "integers".
// This happens on peer 1 and conn would normally be a TCP-like connection to peer 2.
func emitIntegers(conn io.ReadWriteCloser, n int) {
	man := netchan.Manage(conn) // let a netchan.Manager handle the connection
	ch := make(chan int, 10)
	// open net-chan "integers" for sending with ch
	err := man.Open("integers", netchan.Send, ch)
	if err != nil {
		log.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		// check the error signal while sending messages,
		// otherwise the send operation can block forever in case of error
		select {
		case ch <- i:
		case <-man.ErrorSignal():
			handleError(man.Error(), man)
		}
	}
	close(ch) // the receiving channel will be closed;
	// messages in flight will not be lost

	// wait that peer receives everything and closes the connection
	<-man.ErrorSignal() // EOF or similar
	conn.Close()
}

// sumIntegers receives the integers from the net-chan and returns their sum.
func sumIntegers(conn io.ReadWriteCloser) int {
	man := netchan.Manage(conn)
	// Open with Recv direction requires a buffered channel. The buffer
	// size can affect performance, see the documentation about flow control
	ch := make(chan int, 40)
	err := man.Open("integers", netchan.Recv, ch)
	if err != nil {
		log.Fatal(err)
	}
	sum := 0
	i := 0
	for open := true; open; {
		select {
		case i, open = <-ch:
			sum += i
		case <-man.ErrorSignal():
			handleError(man.Error(), man)
		}
	}
	conn.Close()
	return sum
}

func handleError(err error, man *netchan.Manager) {
	// wait that the manager communicates the error to the other peer
	time.Sleep(1 * time.Second)
	// close the connection, so that the manager's goroutines
	// that are eventually stuck reading or writing can return
	man.CloseConn()
	log.Fatal(err)
}

// This example shows a basic netchan session: two peers establish a connection and
// delegate its management to a netchan.Manager (one for peer); peer 1 opens a
// net-chan for sending; peer 2 opens the same net-chan (by name) for receiving;
// the peers communicate using the Go channels associated with the net-chans.
func Example_basic() {
	conn := newPipeConn() // a connection based on io.PipeReader/Writer
	// normally emitIntegers and sumIntegers would be executed
	// on different machines, connected by a TCP-like connection
	go emitIntegers(conn.sideA, 100)
	sum := sumIntegers(conn.sideB)
	fmt.Println(sum)
	// Output: 5050
}
