/*
WARNING: I wrote this library mainly to learn about Go concurrency patterns.
Tests pass, but don't use it in production.

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
to a netchan.Session.

A basic netchan session, where a peer sends some integers to the other, looks like the
following (error handling aside).

On the send side:
	s := netchan.NewSession(conn) // let a netchan.Session handle the connection
	ch := make(chan int, 20)
	err := s.OpenSend("integers", ch) // open net-chan "integers" for sending through ch
	for i := 0; i < n; i++ {
		ch <- i
	}
	close(ch) // close the net-chan

On the receive side:
	s := netchan.NewSession(conn)
	ch := make(chan int, 20)
	recvBufCap := 100
	err := s.OpenRecv("integers", ch, recvBufCap)
	for i := range ch {
		fmt.Println(i)
	}

All methods that Session provides can be called safely from multiple goroutines.

Netchan uses gob to serialize messages (https://golang.org/pkg/encoding/gob/). Any data
to be transmitted using netchan must obey gob's laws. In particular, channels cannot be
sent, but it is possible to send net-chans' names.

Error handling

When a session shuts down (because Quit is called or because of an error), some
goroutines could hang forever trying to receive or send on a net-chan. For this reason,
Session provides the methods Done and Err. Done returns a channel that
never gets any message and is closed when an error occurs; Err returns the error that
occurred. Their intended use:

	// sending on a net-chan (receiving is analogous):
	select {
	case ch <- val:
		// ok
	case <-s.Done():
		log.Fatal(s.Err())
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
*/
package netchan
