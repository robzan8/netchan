package netchan_test

import (
	"fmt"
	"io"

	"github.com/robzan8/netchan"
)

// emitIntegers sends the integers from 1 to n on net-chan "integers".
// conn would normally be a TCP-like connection to the other peer.
func emitIntegers(conn io.ReadWriteCloser, n int) {
	// let a netchan.Manager handle the connection
	mn := netchan.Manage(conn)
	ch := make(chan int, 10)
	// open net-chan "integers" for sending with ch
	mn.Open("integers", netchan.Send, ch)
	for i := 1; i <= n; i++ {
		ch <- i
	}
	close(ch) // the other peer's receiving channel will be closed
}

// sumIntegers receives the integers from net-chan "integers" and returns their sum.
func sumIntegers(conn io.ReadWriteCloser) int {
	mn := netchan.Manage(conn)
	ch := make(chan int, 40)
	mn.Open("integers", netchan.Recv, ch)
	sum := 0
	for i := range ch {
		sum += i
	}
	return sum
}

// This example shows a basic netchan session: two peers establish a connection and
// delegate its management to a netchan.Manager (one for peer); peer 1 opens a
// net-chan for sending; peer 2 opens the same net-chan (by name) for receiving;
// the peers communicate using the Go channels associated with the net-chans.
// Warning: this example does not include error handling.
func Example_basic() {
	sideA, sideB := newPipeConn() // a connection based on io.PipeReader/Writer
	go emitIntegers(sideA, 100)
	sum := sumIntegers(sideB)
	fmt.Println(sum)
	// Output: 5050
}
