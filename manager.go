/*
Package netchan implements type-safe, buffered channels over io.ReadWriter connections.
After esatblishing a connection and delegating its management to a netchan.Manager,
two peers can communicate using Go channels as proxies to send or receive elements to
and from a networked channel.
The following example demonstrates basic usage of the library, error handling aside:

	// peer 1, producer
	func peer1() {
		conn := net.Dial("tcp", "peer2:1234")
		man := netchan.Manage(conn)
		ch := make(chan int, 10)
		man.SendProxy(ch, "awesome_chan")
		for i := 0; i < 300; i++ {
			ch <- i
		}
		close(ch)
	}
	// peer 2, consumer
	func peer2() {
		ln := net.Listen("tcp", ":1234")
		conn := ln.Accept()
		man := netchan.Manage(conn)
		ch := make(chan int, 100)
		man.RecvProxy(ch, "awesome_chan")
		sum := 0
		for i := range ch {
			sum += i
		}
		fmt.Println("sum is", sum)
	}

Many networked channels can be used on top of a single connection, in both directions.
The connection can be any io.ReadWriter that acts like a full duplex connection and
ensures reliable and in-order delivery of data, like: TCP, TSL, unix domain sockets,
websockets, in-memory io.PipeReader/Writer. Netchan is designed to be used also for
inter-process communication.
TODO: insert here pointers to advanced topics and full example
*/

package netchan

import (
	"crypto/sha1"
	"encoding/gob"
	"errors"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
)

var (
	errBadSendProxy = errors.New("netchan: SendProxy called with bad channel argument")
	errBadRecvProxy = errors.New("netchan: RecvProxy called with bad channel argument")
)

// sha1-hashed name of a networked channel
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

type Manager struct {
	send *sender
	recv *receiver

	errMu  sync.Mutex
	err    atomic.Value // *error
	gotErr chan struct{}
}

// Manage function will start the management of connection conn.

func Manage(conn io.ReadWriter) *Manager {
	send := new(sender)
	recv := new(receiver)
	m := &Manager{send: send, recv: recv}
	m.err.Store((*error)(nil))
	m.gotErr = make(chan struct{})

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

func checkChan(ch reflect.Value, dir reflect.ChanDir) bool {
	return ch.Kind() == reflect.Chan &&
		ch.Cap() > 0 &&
		ch.Type().ChanDir()&dir != 0
}

func (m *Manager) SendProxy(channel interface{}, name string) error {
	ch := reflect.ValueOf(channel)
	if !checkChan(ch, reflect.RecvDir) {
		// sender will not be able to receive from channel
		panic(errBadSendProxy)
	}
	return m.send.add(ch, hashName(name))
}

func (m *Manager) RecvProxy(channel interface{}, name string) error {
	ch := reflect.ValueOf(channel)
	if !checkChan(ch, reflect.SendDir) {
		// receiver will not be able to send to channel
		panic(errBadRecvProxy)
	}
	return m.recv.add(ch, hashName(name))
}

func (m *Manager) signalError(err error) {
	m.errMu.Lock()
	defer m.errMu.Unlock()
	prevErr := m.Error()
	if prevErr != nil {
		// someone signaled an error before us
		return
	}
	m.err.Store(&err)
	close(m.gotErr)
}

func (m *Manager) Error() error {
	err := m.err.Load().(*error)
	if err == nil {
		return nil
	}
	return *err
}

func (m *Manager) GotError() <-chan struct{} {
	return m.gotErr
}
