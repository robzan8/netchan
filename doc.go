/*
Package netchan enables using Go channels to communicate over a network: one peer sends
messages to a channel and netchan forwards them over a connection to the other peer,
where they are received from another channel of the same type.

Basics

Net-chans are unidirectional: on one side of the connection a net-chan is opened for
sending, on the other side the same net-chan (identified by name) is opened for
receiving. But it is possible to open multiple net-chans, in both directions, on a single
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
sent, but it is possible to send net-chans' names.

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

Some errors are not caught by netchan. For example, if one peer opens a net-chan with the
wrong direction, both peers might end up waiting to receive messages, but none of them
will send anything. It is advised to use timeouts to identify this kind of errors.

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

The protocol is simple and precise. Every deviation from the protocol causes an error and
every error is fatal. It involves no retransmissions nor timing issues, the underlying
connection must take care of delivering data in order and with best-effort reliability.
Netchan uses gob for encoding and decoding data. Each netchan message is preceded by an
header defined as:
	type header struct {
		MsgType int // type of the message immediately following
		ChanId  int // ID of the net-chan this message is directed to
	}

Possible message types are:
	const (
		helloMsg int = 1 + iota
		// element messages
		elemMsg
		initElemMsg
		closeMsg
		// credit messages
		creditMsg
		initCredMsg
		// error messages
		errorMsg
		netErrorMsg

		lastReserved = 15
	)
Receiving a message with type 0 or type > lastReserved raises an error.

A netchan session starts by exchanging hello messages, with:
	enc.Encode(header{helloMsg, 0}) // enc is a gob encoder

For each net-chan, element messages carry the user values and flow form the sender to the
receiver, while credit messages carry credits and flow from the receiver to the sender.
When a new net-chan is opened, the sender sends an initElemMsg, which signals the
intention of starting to send elements and does not hold user data. The receiver chooses
an ID for the net chan, which must be the smallest non-negative integer not already in
use by channels of the same direction; then, it sends an initCredMsg, that contains the
choosen ID and the receive buffer capacity as credit.

The initElemMsg is encoded as:
	enc.Encode(header{initElemMsg, 0})
	enc.Encode(name) // hashed name of the net-chan

The initCredMsg is encoded as:
	enc.Encode(header{initCredMsg, chanID})
	enc.Encode(bufCap) // receive buffer capacity as int
	enc.Encode(name)   // hashed name of the net-chan

As soon as the sender gets the initial credit, it can start transmitting data:
	enc.Encode(header{elemMsg, chanID})
	enc.EncodeValue(val) // user data

On the receive side, when the user consumes C values from the receive channel, the new
free space in the buffer must be communicated back to the sender with a credit message:
	enc.Encode(header{creditMsg, chanID})
	enc.Encode(C) // credit as int

The credits should be cumulative and should not be sent too often (for example, sending a
credit of 1 for each element consumed generates unnecessary traffic). The protocol does
not impose restrictions here, except that the value of a credit message must be at least
1.

A netchan can be closed only by its sender, with a closeMsg:
	enc.Encode(header{closeMsg, chanID})

When an error occurs or when the user decides to terminate the session, an error message
is sent to the peer:
	enc.Encode(header{errorMsg, 0})
	enc.Encode(err.Error()) // the error string

If the error string is "EOF", the decoder should report io.EOF to the user. Errors that
implement the net.Error interface are encoded follow, to preserve the interface:
	type netError struct { // implements net.Error
		Err         string
		IsTimeout   bool
		IsTemporary bool
	}
	enc.Encode(header{netErrorMsg, 0})
	enc.Encode(netErr) // a netError

*/
package netchan
