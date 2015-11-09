package netchan_test

import (
	"errors"
	"io"
	"log"
	"time"

	"github.com/robzan8/netchan"
)

/*
If you don't know about heartbeats:

Heartbeating is a common technique used to check whether a connection is alive.
Both peers send small messages to each other at fixed intervals; if no message is
received for a certain amount of time, the connection is considered dead and is closed.

Implementing it correctly on top of a bare TCP-like connection is problematic: if a
peer suspends consuming data, heartbeats will be stuck in TCP's receive buffer and
the connection will appear dead, even if it's perfectly healthy.

With netchan this problem doesn't occur: heartbeats can be implemented with a couple
of dedicated goroutines and channels that will keep going independently from the other
channels used to communicate data.
*/

// interval at which heartbeats are sent. Would be much bigger for real
// network connections
const hbInterval = 50 * time.Millisecond

// if sending or receiving takes more than hbTimeout, the connection is
// considered dead. The timeout would normally be more permissive, like
// hbTimeout = 3 * hbInterval
const hbTimeout = time.Duration(1.2 * float64(hbInterval))

// recvHeartbeat keeps receiving heartbeats
func recvHeartbeat(hb <-chan struct{}, mn *netchan.Manager) {
	for {
		// check the error signal while receiving (and sending) messages,
		// otherwise the operation can block forever in case of error
		select {
		case <-hb:
		case <-mn.ErrorSignal():
			log.Fatal(mn.Error())
		case <-time.After(hbTimeout):
			err := errors.New("heartbeat receive took too long")
			mn.ShutDownWith(err)
			log.Fatal(err)
		}
		time.Sleep(hbInterval)
	}
}

// sendHeartbeat keeps sending heartbeats
func sendHeartbeat(hb chan<- struct{}, mn *netchan.Manager) {
	for {
		select {
		case hb <- struct{}{}:
		case <-mn.ErrorSignal():
			log.Fatal(mn.Error())
		case <-time.After(hbTimeout):
			err := errors.New("heartbeat send took too long")
			mn.ShutDownWith(err)
			log.Fatal(err)
		}
		time.Sleep(hbInterval)
	}
}

// Both peers are opening a "heartbeat" channel for sending and another "heartbeat"
// channel for receiving. The trick is that a net-chan is not identified just by name,
// but by name and direction. So, it is possible to have, on a single connection, two
// net-chans with the same name, one that goes from peer 1 to peer 2, the other that
// goes form peer 2 to peer 1. It is useful in cases where the protocol is symmetrical,
// like heartbeating.
func heartbeatPeer(conn io.ReadWriteCloser) {
	mn := netchan.Manage(conn)
	// the same manager is used to open both channels.
	// On each end, a connection must have only one manager
	recv := make(chan struct{}, 1)
	err := mn.Open("heartbeat", netchan.Recv, recv)
	if err != nil {
		log.Fatal(err)
	}
	go recvHeartbeat(recv, mn)

	send := make(chan struct{}, 1)
	err = mn.Open("heartbeat", netchan.Send, send)
	if err != nil {
		log.Fatal(err)
	}
	go sendHeartbeat(send, mn)
}

// This example shows how to add heartbeats to a netchan session.
// It also shows how to handle errors.
func Example_heartbeats() {
	sideA, sideB := newPipeConn()
	go heartbeatPeer(sideA)
	go heartbeatPeer(sideB)
	time.Sleep(2 * time.Second)
	// Output:
}
