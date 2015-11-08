/*
Package netchan enables using Go channels to communicate over a network.
One peer sends messages to a channel and the other peer receives the messages from
another channel of the same type: netchan takes care of transferring the messages between
the two channels over a connection.

Basics

Net-chans are unidirectional: items of a net-chan are sent from one side of the
connection and received on the other side; but it is possible to open multiple net-chans,
in both directions, on a single connection.

The connection can be any io.ReadWriteCloser like a TCP connection or unix domain
socket. The user is in charge of establishing the connection, which is then handed over
to a netchan.Manager.

A basic netchan session, where a peer sends some integers to the other, looks like the
following (error handling aside).

On the send side:
	mn := netchan.Manage(conn, 0) // let a netchan.Manager handle the connection
	ch := make(chan int, 5)
	mn.Open("integers", netchan.Send, ch) // open net-chan "integers" for sending with ch
	for i := 0; i < n; i++ {
		ch <- i
	}
	close(ch) // close the net-chan

On the receive side:
	mn := netchan.Manage(conn, 0)
	ch := make(chan int, 20)
	mn.Open("integers", netchan.Recv, ch)
	for i := range ch {
		fmt.Println(i)
	}

Netchan uses gob to serialize messages (https://golang.org/pkg/encoding/gob/). Any data
to be transmitted using netchan must obey gob's laws.

Error handling

When a manager shuts down (because the connection is closed or because of an error), your
goroutines can hang forever trying to receive or send on a net-chan. For this reason,
Manager provides the methods ErrorSignal and Error. ErrorSignal returns a channel that
never gets any message and is closed when an error occurs; Error returns the error that
occurred (or nil, if no error happened). Their intended use:

	// sending on a net-chan (receiving is analogous):
	select {
	case ch <- val:
		// ok
	case <-mn.ErrorSignal():
		log.Fatal(mn.Error())
	}

ErrorSignal and Error are thread safe (like all methods that Manager provides).

Flow control

Netchan implements a credit-based flow control algorithm analogous to the one of HTTP/2.

Go channels used for receiving from a net-chan must be buffered. For each net-chan, the
sender has an integer credit that indicates how many free slots there are in the
receiver's buffer. Each time the sender transmits an item, it decrements the credit by
one. If the credit becomes zero, the sender must suspend sending items, as the receive
buffer could be full. On the receive side, when a good number of items have been taken
out of the channel (and the buffer has new free space), the number is communicated back
to the sender with an increase credit message.

The receive channel capacity can affect performance: a small buffer could cause the
sender to suspend often; a big buffer could avoid suspensions completely.

Security

The netchan protocol is designed to be secure, is very simple and is tested also with
go-fuzz (https://github.com/dvyukov/go-fuzz). Given a server that exposes a netchan-based
API, no malicious client should be able to:
	- make the server's goroutines panic or deadlock
	- force the server to allocate a lot of memory
	- force the server to consume a big amount of CPU time

Netchan protocol

The netchan protocol is designed to be simple and precise. Every deviation from the protocol causes an
error and every error is fatal. It involves no retransmissions nor timing issues, the
underlying connection must take of delivering data in order and with best-effort
reliability.
Netchan uses gob for encoding and decoding data. Each netchan message is preceded by an header defined as:

	type header struct {
		MsgType int // type of the message immediately following
		ChanId  int // ID of the net-chan this message is directed to
	}

There are 3 different types of messages: element messages (type 1), credit messages (type 2) and error messages (type 3).
Receiving a message with an unknown type must raise an error. The netchan protocol is not extensible.

Element messages carry values from and to user channels. They must be encoded as:

	// enc is a gob encoder
	enc.Encode(header{1, chanId})
	enc.EncodeValue(value) // user data
	enc.Encode(elem.ok)
*/
package netchan
