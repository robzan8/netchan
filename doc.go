/*
Package netchan enables using Go channels to communicate over a network: one peer sends
messages to a channel and netchan forwards them over a connection to the other peer,
where they are received from another channel of the same type.

Basics

Net-chans are unidirectional: on one side of the connection a net-chan is opened for
sending, on the other side the same net-chan (identified by name) is opened for
receiving; but it is possible to open multiple net-chans, in both directions, on a single
connection.

The connection can be any io.ReadWriteCloser like a TCP connection or unix domain
sockets. The user is in charge of establishing the connection, which is then handed over
to a netchan.Manager.

A basic netchan session, where a peer sends some integers to the other, looks like the
following (error handling aside).

On the send side:
	mn := netchan.Manage(conn) // let a netchan.Manager handle the connection
	ch := make(chan int, 5)
	err := mn.Open("integers", netchan.Send, ch) // open net-chan "integers" for sending through ch
	for i := 0; i < n; i++ {
		ch <- i
	}
	close(ch) // close the net-chan

On the receive side:
	mn := netchan.Manage(conn)
	ch := make(chan int, 20) // channel used for receiving must be buffered
	err := mn.Open("integers", netchan.Recv, ch)
	for i := range ch {
		fmt.Println(i)
	}

All methods that Manager provides are thread-safe.

Netchan uses gob to serialize messages (https://golang.org/pkg/encoding/gob/). Any data
to be transmitted using netchan must obey gob's laws. In particular, channels cannot be
sent, but it is possible to send channel names.

Error handling

When a manager shuts down (because ShutDown is called or because of an error), some
goroutines could hang forever trying to receive or send on a net-chan. For this reason,
Manager provides the methods ErrorSignal and Error. ErrorSignal returns a channel that
never gets any message and is closed when an error occurs; Error returns the error that
occurred. Their intended use:

	// sending on a net-chan (receiving is analogous):
	select {
	case ch <- val:
		// ok
	case <-mn.ErrorSignal():
		log.Fatal(mn.Error())
	}

Flow control

Net-chans are independent of each other: an idle channel does not prevent progress on the
others. This is achieved with a credit-based flow control system analogous to the one of
HTTP/2.

Go channels used for receiving must be buffered. For each net-chan, the receiver
communicates to the sender when there is new free space in the buffer, with credit
messages. The sender must never transmit more than its credit allows.

The receive channel capacity can affect performance: a small buffer could cause the
sender to suspend often, waiting for credit; a big buffer could avoid suspensions
completely.

Security

The netchan protocol is designed to be secure, is extremely simple and is tested also
with go-fuzz (https://github.com/dvyukov/go-fuzz). Given a server that exposes a
netchan-based API, no malicious client should be able to:
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
