package netchan

/* TODO:
- more informative errors
- more tests
- fuzzy testing

performance:
- profile time and memory
- more sophisticated sender's select
- more sophisticated credit sender
- adjust ShutDown timeout
*/

import (
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync/atomic"
	"time"
)

// once is an implementation of sync.Once that uses a channel.
type once struct {
	done  chan struct{}
	state int32
}

// once state
const (
	onceNotDone int32 = iota
	onceDone
	onceDoing
)

func (o *once) Do(f func()) {
	if atomic.LoadInt32(&o.state) == onceDone {
		return
	}
	// Slow path.
	won := atomic.CompareAndSwapInt32(&o.state, onceNotDone, onceDoing)
	if !won {
		<-o.done
		return
	}
	f()
	atomic.StoreInt32(&o.state, onceDone)
	close(o.done)
	// Relative order of the last two statements is important
	// for correctly retrieving Error() after <-ErrorSignal().
}

// A Manager handles the message traffic of its connection, implementing the netchan
// protocol.
type Manager struct {
	conn io.ReadWriteCloser
	send *sender
	recv *receiver

	errOnce, closeOnce once
	err, closeErr      error
}

/*
This graph shows how the goroutines and channels of a manager are organized:

 ----> +----------+       +---------+       +---------+       +----------+ ---->
 ----> |  sender  | ----> | encoder | ====> | decoder | ----> | receiver | ---->
 ----> +----------+       +---------+       +---------+       +----------+ ---->
       [send table]                                           [recv table]
       +----------+       +---------+       +---------+       +----------+
       | credRecv | <---- | decoder | <==== | encoder | <---- | credSend |
       +----------+       +---------+       +---------+       +----------+

The sender has a table that contains one entry for each channel that has been opened for
sending. The user values flow through a pipeline from the sender to the receiver on the
other side of the connection.
Credits flow in the opposite direction. There is no cycle, as, for example, the sender
shares the table with the credit receiver and they do not communicate through channels.
The former graph is a simplification, because each manager has actually both a sender and
a receiver:

 ----> +----------+                                                   +----------+ ---->
 ----> |  sender  | ---\                                         /--> | receiver | ---->
 ----> +----------+     \                                       /     +----------+ ---->
       [send table]      \--> +---------+       +---------+ ---/      [recv table]
       +----------+           | encoder | ====> | decoder |           +----------+
       | credRecv | <-\   /-> +---------+       +---------+ --\   /-- | credSend |
       +----------+    \ /                                     \ /    +----------+
                        X                                       X
       +----------+    / \                                     / \    +----------+
       | credSend | --/   \-- +---------+       +---------+ <-/   \-> | credRecv |
       +----------+           | decoder | <==== | encoder |           +----------+
       [recv table]      /--- +---------+       +---------+ <--\      [send table]
 <---- +----------+     /                                       \     +----------+ <----
 <---- | receiver | <--/                                         \--- |  sender  | <----
 <---- +----------+                                                   +----------+ <----

To avoid deadlocked/leaking goroutines, termination must happen in pipeline order. For
example, the sender and the credit sender both check periodically if an error occurred
with Error(). If so, they close the channels to the encoder. The encoder does not check
Error and keeps draining the channels until they are empty. Then it sends the error to
the peer and closes the connection.
*/

// Manage function starts a new Manager for the specified connection and returns it. The
// connection can be any full-duplex io.ReadWriteCloser that provides in-order delivery
// of data with best-effort reliability. On each end, a connection must have only one
// manager.
//
// There is a default limit imposed on the size of incoming gob messages. To change it,
// use ManageLimit.
func Manage(conn io.ReadWriteCloser) *Manager {
	return ManageLimit(conn, 0)
}

// ManageLimit is like Manage, but also allows to specify the maximum size of the gob
// messages that will be accepted from the connection. If msgSizeLimit is 0 or negative,
// the default will be used. When a too big message is received, an error is signaled on
// this manager and the manager shuts down.
func ManageLimit(conn io.ReadWriteCloser, msgSizeLimit int) *Manager {
	if msgSizeLimit <= 0 {
		msgSizeLimit = 32 * 1024
	}
	// Nothing special happens here: we create all the components,
	// connect them with channels and fire up the goroutines.

	send := new(sender)
	recv := new(receiver)
	mn := &Manager{conn: conn, send: send, recv: recv}
	mn.errOnce.done = make(chan struct{})
	mn.closeOnce.done = make(chan struct{})

	const chCap int = 8
	sendElemCh := make(chan element, chCap)
	recvCredCh := make(chan credit, chCap)
	sendTab := new(chanTable)
	*send = sender{toEncoder: sendElemCh, table: sendTab, mn: mn}
	send.init()
	credRecv := &credReceiver{creditCh: recvCredCh, table: sendTab, mn: mn}

	recvElemCh := make(chan element, chCap)
	sendCredCh := make(chan credit, chCap)
	recvTab := new(chanTable)
	*recv = receiver{elemCh: recvElemCh, table: recvTab, mn: mn}
	credSend := &credSender{toEncoder: sendCredCh, table: recvTab, mn: mn}

	enc := &encoder{elemCh: sendElemCh, creditCh: sendCredCh, mn: mn}
	enc.enc = gob.NewEncoder(conn)
	dec := &decoder{
		toReceiver:   recvElemCh,
		toCredRecv:   recvCredCh,
		table:        recvTab,
		mn:           mn,
		msgSizeLimit: msgSizeLimit,
		limReader:    limitedReader{R: conn},
	}
	dec.dec = gob.NewDecoder(&dec.limReader)

	go send.run()
	go recv.run()
	go credRecv.run()
	go credSend.run()
	go enc.run()
	go dec.run()
	return mn
}

// The direction of a net-chan from the perspective of the local side of the connection.
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

// Open method opens a net-chan with the given name and direction on the connection
// handled by the manager. The channel argument must be a channel and will be used for
// receiving or sending data on this net-chan.
//
// If the direction is Recv, the following rules apply: channel must be buffered and its
// buffer must be empty (cap(channel) > 0 && len(channel) == 0). The channel must be used
// only for receiving values from a single net-chan. Sending values on the channel or
// using it as receiver for multiple net-chans will result in
//
// If the direction is Send, the restrictions do not apply. It's possible to use the same
// channel to send on multiple net-chans. The values will be distributed pseudo-randomly
// to the different net-chans. Closing the channel will close all the associated
// net-chans.
//
// Opening a net-chan twice, i.e. with the same name and direction on the same manager,
// will return an error.
//
// To close a net-chan, close the channel used for sending; the receiving channel on the
// other peer will be closed too. Messages that are already in the buffers or in flight
// will not be lost.
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
}

// Error returns the first error that occurred on this manager. If no error
// occurred, it returns nil. When an error occurs, the manager tries to communicate it to
// the peer and then shuts down.
func (m *Manager) Error() error {
	if atomic.LoadInt32(&m.errOnce.state) == onceDone {
		return m.err
	}
	return nil
}

// ErrorSignal returns a channel that never receives any message and is closed when an
// error occurs on this manager.
func (m *Manager) ErrorSignal() <-chan struct{} {
	return m.errOnce.done
}

// ShutDown tries to send a termination message to the peer and then shuts down the
// manager and closes the connection. The ErrorSignal channel is closed and Error will
// return io.EOF. The remote peer will also shut down and get EOF, if the termination
// message is received correctly.
//
// The return value is the result of calling Close on the connection. The connection is
// guaranteed to be closed once and only once, even if ShutDown is called multiple times,
// possibly by multiple goroutines.
func (m *Manager) ShutDown() error {
	return m.ShutDownWith(io.EOF)
}

func (m *Manager) closeConn() {
	m.closeOnce.Do(func() {
		m.closeErr = m.conn.Close()
	})
}

// ShutDownWith is like ShutDown, but err is signaled instead of EOF.
func (m *Manager) ShutDownWith(err error) error {
	if err == nil {
		err = io.EOF
	}
	m.errOnce.Do(func() {
		m.err = err
	})
	select {
	// encoder tries to send error to peer; if/when it succeeds,
	// it closes the connection and we wake up and return
	case <-m.closeOnce.done:
	// if encoder takes too long, we close the connection ourself
	case <-time.After(5 * time.Second):
		m.closeConn()
	}
	return m.closeErr
}
