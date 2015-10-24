package netchan_test

import (
	"fmt"
	"io"
	"log"
	"netchan"
	"time"
)

// When an error occurs, the manager does not close the channels used for sending, to
// avoid "send on closed chan" panics. Thus, the user should always check the error
// signal while trying to send messages:
func peer1(conn *pipeConn) {
	man := netchan.Manage(sideA(conn))
	ch := make(chan int, 10)
	err := man.Open("integers", ch, netchan.Send)
	if err != nil {
		log.Fatal(err)
	}
	for i := 0; i <= 100; i++ {
		select {
		case ch <- i: // ok
		case <-man.ErrorSignal():
			err := man.Error()
			handleError(err, conn)
			log.Fatal(err)
		}
	}
	close(ch)
}

// The manager closes the channels used for receiving when an error occurs. Thus, the
// user doesn't need to check the error signal while receiving. Consider that the error
// could be EOF; in this case it makes sense to read all the messages in the receive
// buffers, instead of stopping as soon as the error is signaled.
func peer2(conn *pipeConn) {
	man := netchan.Manage(sideB(conn))
	ch := make(chan int, 40)
	err := man.Open("integers", ch, netchan.Recv)
	if err != nil {
		log.Fatal(err)
	}
	sum := 0
	for i := range ch {
		sum += i
	}
	if err := man.Error(); err != nil {
		// may want ot check if err is EOF.
		handleError(err)
		log.Fatal(err, conn)
	}
	fmt.Println("sum is", sum)
	// output should be the sum of the first 100 integers
	// Output: sum is 5050
}

func handleError(err error, conn io.Closer) {
	// wait that netchan propagates the error to the other peer
	time.Sleep(5 * time.Second)
	// close the connection, so that the manager's goroutines that are eventually stuck
	// reading or writing can return
	conn.Close()
	// In general, closing the connection will cause an error to be signaled on the
	// manager (possibly EOF, depending on the connection)
}

func ExampleErrorHandling() {
	conn := newPipeConn() // a connection based on io.PipeReader/Writer
	go peer1(conn)
	go peer2(conn)
}
