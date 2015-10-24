/*
Package netchan enables using Go channels to communicate over a network connection.
One peer sends messages to a channel and the other peer receives the messages from
another channel of the same type: netchan takes care of transferring the messages
between the two channels over the connection.

An example of a basic netchan session follows:

Two peers establish a connection and delegate its management to a netchan.Manager.
The manager allows opening net-chans with a name and an associated channel, used for
sending or receiving. Peer 1 opens a net-chan for sending. Peer 2 opens the same
net-chan (with the same name) for receiving. The peers can then communicate using
the channels. In general, many net-chans can be opened on top of a single connection,
in both directions.

	func peer1() {
		conn := net.Dial("tcp", "peer2:1234")
		man := netchan.Manage(conn)
		ch := make(chan int, 10)
		man.Open("integers", ch, netchan.Send)
		for i := 0; i < 300; i++ {
			ch <- i // sending messages to peer 2
		}
		close(ch)
	}

	func peer2() {
		ln := net.Listen("tcp", ":1234")
		conn := ln.Accept()
		man := netchan.Manage(conn)
		ch := make(chan int, 100)
		man.Open("integers", ch, netchan.Recv)
		sum := 0
		for i := range ch { // receiving messages from peer 1
			sum += i
		}
		fmt.Println("sum is", sum)
	}

A full example that includes error handling can be found in the "examples" section.
See the advanced topics page for information about netchan internals, performance,
security, timeouts and heartbeats (after reading the API documentation).
TODO: insert pointer to advanced topics
*/

package netchan

import (
	"crypto/sha1"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
)

// sha1-hashed name of a net-chan
type hashedName [20]byte

func hashName(name string) hashedName {
	return sha1.Sum([]byte(name))
}

type element struct {
	id  int
	val reflect.Value
	ok  bool // if not ok, the channel has been closed
}

type credit struct {
	id   int
	incr int64
	name *hashedName
}

// A Manager handles the message traffic of its connection, implementing the netchan protocol.
type Manager struct {
	send *sender
	recv *receiver

	errMutex  sync.Mutex
	err       atomic.Value // *error
	errSignal chan struct{}
}

// Manage function starts a new Manager for the specified connection and returns it.
// The connection can be any io.ReadWriter that acts like a full duplex connection and
// ensures reliable and in-order delivery of data, like: TCP, TLS, unix domain sockets,
// websockets, in-memory io.PipeReader/Writer.
// On each peer, one connection must have only one Manager.
func Manage(conn io.ReadWriter) *Manager {
	send := new(sender)
	recv := new(receiver)
	m := &Manager{send: send, recv: recv}
	m.err.Store((*error)(nil))
	m.errSignal = make(chan struct{})

	const chCap int = 8
	sendElemCh := make(chan element, chCap)
	recvCredCh := make(chan credit, chCap)
	sendTab := &sendTable{pending: make(map[hashedName]reflect.Value)}
	*send = sender{toEncoder: sendElemCh, table: sendTab, man: m}
	send.initialize()
	credRecv := &credReceiver{creditCh: recvCredCh, table: sendTab, man: m}

	recvElemCh := make(chan element, chCap)
	sendCredCh := make(chan credit, chCap)
	recvTab := new(recvTable)
	*recv = receiver{elemCh: recvElemCh, table: recvTab, man: m}
	credSend := &credSender{toEncoder: sendCredCh, table: recvTab, man: m}

	enc := &encoder{elemCh: sendElemCh, creditCh: sendCredCh, man: m}
	enc.enc = gob.NewEncoder(conn)
	dec := &decoder{toReceiver: recvElemCh, toCredRecv: recvCredCh, table: recvTab, man: m}
	dec.dec = gob.NewDecoder(conn)

	go send.run()
	go recv.run()
	go credRecv.run()
	go credSend.run()
	go enc.run()
	go dec.run()
	return m
}

// The direction of a net-chan from the client's perspective.
type Dir int

const (
	Recv Dir = iota
	Send
)

func (d Dir) String() string {
	switch d {
	case Recv:
		return "Recv"
	case Send:
		return "Send"
	}
	return "???"
}

type errAlreadyOpen struct {
	name string
	dir  Dir
}

func (e *errAlreadyOpen) Error() string {
	return fmt.Sprintf("netchan Open: net-chan \"%s\" is already open with dir %s\n",
		e.name, e.dir)
}

// Open method opens a net-chan with the specified name, channel and direction, on the
// connection handled by the manager.
// The channel parameter must be a channel.
// If the direction is Recv, the channel must have a buffer capacity greater than 0.
// Opening a net-chan twice, i.e. with the same name and direction on the same manager,
// will return an error.
// To close a net-chan, close the channel used for sending and eventually wait that the
// buffers are drained and the close message is propagated to the receiving peer before
// closing the connection.
func (m *Manager) Open(name string, dir Dir, channel interface{}) error {
	ch := reflect.ValueOf(channel)
	if ch.Kind() != reflect.Chan {
		return errors.New("netchan Open: channel arg is not a channel")
	}
	if dir == Recv {
		if ch.Cap() == 0 {
			return errors.New("netchan Open: Recv dir requires a buffered channel")
		}
		if ch.Type().ChanDir()&reflect.SendDir == 0 {
			// manager will not be able to send messages to ch for the user to receive
			return errors.New("netchan Open: Recv dir requires a chan<-")
		}
		return m.recv.open(name, ch)
	}
	if dir == Send {
		if ch.Type().ChanDir()&reflect.RecvDir == 0 {
			// manager will not be able to take messages from ch and forward them
			return errors.New("netchan Open: Send dir requires a <-chan")
		}
		return m.send.open(name, ch)
	}
	return errors.New("netchan Open: dir is not Recv nor Send")
	return nil
}

func (m *Manager) signalError(err error) {
	m.errMutex.Lock()
	defer m.errMutex.Unlock()
	prevErr := m.Error()
	if prevErr != nil {
		// keep oldest error
		return
	}
	m.err.Store(&err)
	close(m.errSignal)
}

// Error returns the first (oldest) error that occurred on this Manager. If no error
// occurred, it returns nil. See the error handling example.
func (m *Manager) Error() error {
	err := m.err.Load().(*error)
	if err == nil {
		return nil
	}
	return *err
}

// ErrorSignal returns a channel that never receives any message and is closed when an
// error occurs on this manager. When an error occurs, the manager shuts down. See the
// error handling example.
func (m *Manager) ErrorSignal() <-chan struct{} {
	return m.errSignal
}
