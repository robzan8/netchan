package netchan_test

import (
	"fmt"
	"io"
	"log"

	"github.com/robzan8/netchan"
)

func checkErrors(mn *netchan.Manager) {
	<-mn.ErrorSignal()
	if err := mn.Error(); err != io.EOF {
		log.Fatal(err)
	}
}

type request struct {
	N          int
	RespChName string
}

// emitIntegers sends the integers from 1 to n on net-chan "integers".
// conn would normally be a TCP-like connection to the other peer.
func client(conn io.ReadWriteCloser) {
	mn := netchan.Manage(conn, 0)
	go checkErrors(mn)

	reqCh := make(chan request, 1)
	err := mn.OpenSend("requests", reqCh)
	if err != nil {
		log.Fatal(err)
	}
	reqCh <- request{10, "response chan 0"}

	respCh := make(chan int, 1)
	err = mn.OpenRecv("response chan 0", respCh, 5)
	if err != nil {
		log.Fatal(err)
	}
	for i := range respCh {
		fmt.Printf("%d ", i)
	}

	mn.ShutDown()
}

// sumIntegers receives the integers from net-chan "integers" and returns their sum.
func server(conn io.ReadWriteCloser) {
	mn := netchan.Manage(conn, 0)
	go checkErrors(mn)

	reqCh := make(chan request, 1)
	err := mn.OpenRecv("requests", reqCh, 5)
	if err != nil {
		log.Fatal(err)
	}
	req := <-reqCh

	respCh := make(chan int, 1)
	err = mn.OpenSend(req.RespChName, respCh)
	if err != nil {
		log.Fatal(err)
	}
	for i := 0; i < req.N; i++ {
		respCh <- i
	}
	close(respCh)

	// wait that client receives everything
	// and shuts down, we will get EOF
	checkErrors(mn)
}

// This example shows a basic netchan session: two peers establish a connection and
// delegate its management to a netchan.Manager (one for peer); peer 1 opens a
// net-chan for sending; peer 2 opens the same net-chan (by name) for receiving;
// the peers communicate using the Go channels associated with the net-chans.
// Warning: this example does not include error handling.
func Example_requestResponse() {
	sideA, sideB := newPipeConn() // a connection based on io.PipeReader/Writer
	go client(sideA)
	server(sideB)
	// Output: 0 1 2 3 4 5 6 7 8 9
}
