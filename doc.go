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
	ch := make(chan int, 10)
	err := mn.OpenSend("integers", ch) // open net-chan "integers" for sending through ch
	for i := 0; i < n; i++ {
		ch <- i
	}
	close(ch) // close the net-chan

On the receive side:
	mn := netchan.Manage(conn)
	ch := make(chan int)
	recvBufCap := 100
	err := mn.OpenRecv("integers", ch, recvBufCap)
	for i := range ch {
		fmt.Println(i)
	}

All methods that Manager provides can be called safely from multiple goroutines.

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

Concurrency patterns

The only restriction that the control flow system imposes is that each net-chan must have
a dedicated buffered channel for receiving and no one else should be sending to that
channel. So, to implement a fan-in of net-chans, a goroutine that reads from the various
buffered channels is necessary.

To implement a fan-out, instead, the same Go channel can be used to open different
net-chans (possibly on different managers/connections) with Send direction. The messages
arriving on the channel will be distributed pseudo-randomly to the various net-chans with
positive credit. This implies that we get automatic load balacing: if one of the
receivers is slower, it will send credit and thus receive messages at a lower rate.
Closing the channel will close all the net-chans in the fan-out.

RPC-like functionality can be implemented by sending a request together with a net-chan
name, that will be opened for the response. However, opening a net-chan for each response
is not ideal in terms of performance. When possible, model your application logic in
terms of streams of data instead of requests-responses, it's where netchan and gob shine
best.

Security

The netchan protocol is designed to be secure, is extremely simple and is tested also
with go-fuzz (https://github.com/dvyukov/go-fuzz). Given a server that exposes a
netchan-based API, no malicious client should be able to:
	- make the server's goroutines panic or deadlock
	- force the server to allocate a lot of memory
	- force the server to consume a big amount of CPU time
*/
package netchan
