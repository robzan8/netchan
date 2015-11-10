package netchan_test

import (
	"fmt"
	"io"
	"log"

	"github.com/robzan8/netchan"
)

type request struct {
	N      int
	RespCh string
}

// emitIntegers sends the integers from 1 to n on net-chan "integers".
// conn would normally be a TCP-like connection to the other peer.
func client(conn io.ReadWriteCloser) {
	mn := netchan.Manage(conn)

	reqCh := make(chan request)
	err := mn.Open("requests", netchan.Send, reqCh)
	if err != nil {
		log.Fatal(err)
	}
	req := request{100, "response 0"}
	select {
	case reqCh <- req:
	case <-mn.ErrorSignal():
		log.Fatal(mn.Error())
	}
	//close(reqCh)

	respCh := make(chan int, 20)
	err = mn.Open(req.RespCh, netchan.Recv, respCh)
	if err != nil {
		log.Fatal(err)
	}
	for i := 0; i < req.N; i++ {
		select {
		case num := <-respCh:
			if num == 99 {
				fmt.Println(num)
			}
		case <-mn.ErrorSignal():
			log.Fatal(mn.Error())
		}
	}

	mn.ShutDown()
}

// sumIntegers receives the integers from net-chan "integers" and returns their sum.
func server(conn io.ReadWriteCloser) {
	mn := netchan.Manage(conn)

	reqCh := make(chan request, 10)
	err := mn.Open("requests", netchan.Recv, reqCh)
	if err != nil {
		log.Fatal(err)
	}
	var req request
	select {
	case req = <-reqCh:
	case <-mn.ErrorSignal():
		log.Fatal(mn.Error())
	}

	respCh := make(chan int)
	err = mn.Open(req.RespCh, netchan.Send, respCh)
	if err != nil {
		log.Fatal(err)
	}
	for i := 0; i < req.N; i++ {
		select {
		case respCh <- i:
		case <-mn.ErrorSignal():
			log.Fatal(mn.Error())
		}
	}
	//close(respCh)

	// wait that client receives everything and shuts down
	<-mn.ErrorSignal()
	if err := mn.Error(); err != io.EOF {
		log.Fatal(err)
	}
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
	// Output: 99
}
