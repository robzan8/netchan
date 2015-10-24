package netchan_test

import (
	"netchan"
	"time"
)

// The netchan protocol does not include heartbeats, but it makes them very simple to
// implement. The main problem when implementing heartbeats is head of line blocking: if
// the receiver is not consuming data, heartbeats will be stuck in TCP's receive buffer
// and the connection can appear dead, even if it's perfectly healthy. But netchan's flow
// control algorithm solves this problem for you [see advanced docs]. Heartbeats can be
// implemented easily with a couple of dedicated goroutines and net-chans.

var conn = newPipeConn() // a connection based on io.PipeReader/Writer

func recvHeartbeat(man *netchan.Manager, interval, tolerance time.Duration) {
	ping := make(chan struct{})
	man.Open("ping", netchan.Recv, ping)
	for man.Error() != nil {
		select {
		case <-ping: // ok
		case time.After(tolerance):
			conn.Close()
		}
		time.Sleep(interval)
	}
}

func sendHeartbeat(man *netchan.Manager, interval, tolerance time.Duration) {
	ping := make(chan struct{})
	man.Open("ping", netchan.Send, ping)
	for man.Error() != nil {
		select {
		case ping <- struct{}{}: // ok
		case time.After(tolerance):
			conn.Close()
		}
		time.Sleep(interval)
	}
}

func peer() {
	man := net
}

func ExampleHeartbeat() {
	manA := netchan.Manage(sideA(conn))
	manB := netchan.Manage(sideB(conn))
	interval := 50 * time.Millisecond
	tolerance := 150 * time.Millisecond
	go recvHeartbeat(manA, interval, tolerance)
	go sendHeartbeat(manA, interval, tolerance)
	go recvHeartbeat(manB, interval, tolerance)
	go sendHeartbeat(manB, interval, tolerance)
}
