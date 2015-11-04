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
sockets. The user is in charge of establishing the connection, which is then handed over
to a netchan.Manager.

Netchan uses gob to serialize messages.

A basic netchan session, where a peer sends some integers to the other, looks like the
following (error handling aside).

On the send side:
	mn := netchan.Manage(conn) // let a netchan.Manager handle the connection
	ch := make(chan int, 5)
	mn.Open("integers", netchan.Send, ch) // open net-chan "integers" for sending with ch
	for i := 0; i < n; i++ {
		ch <- i
	}
	close(ch) // close the net-chan

On the receive side:
	mn := netchan.Manage(conn)
	ch := make(chan int, 20)
	mn.Open("integers", netchan.Recv, ch)
	for i := range ch {
		fmt.Println(i)
	}

All methods that Manager provides are thread safe.

Error handling and timeouts

If a manager shuts down because of a connection error, your goroutines can hang forever
trying to receive or send on a net-chan. For this reason, Manager provides the methods
ErrorSignal and Error. ErrorSignal returns a channel that never gets any message and is
closed when an error occurs; Error returns the error that occurred (or nil, if no error
happened). Their intended use:

	// sending on a net-chan (receiving is analogous):
	select {
	case ch <- val:
		// ok
	case <-mn.ErrorSignal():
		log.Fatal(mn.Error())
	}

Error and ErrorSignal methods are thread safe and do not have scalability issues

Flow control

When multiplexing several streams of data on a single connection, flow control mechanisms
have to be implemented.

Suppose there are 10 net-chans going from peer A to peer B on a TCP connection. Peer A is
sending messages on all channels, but for some reason peer B is not consuming messages
from channel 1. When the netchan manager on peer B receives a message for channel 1, the
operation of sending it to the appropriate Go channel will be blocking, possibly for a
long time, since the user is not consuming that data: one idle stream can block the
entire connection.

The common solution to this problem, adopted by HTTP/2, netchan and other protocols, is
to allocate one receive buffer for each stream and to make sure that the sender never
sends more data than the buffers can hold. This way, the receiving manager will never block. For this reason netchan implements a credit system. The sender has a certain integer credit for each net-chan, that represents the free space in the receive buffer for that net-chan. The sender decreases the credit by 1 each time it sends an element. If the credit becomes 0, the receive buffer is full and the sender must suspend sending messages. The receiver periodically checks the receive buffer and when a good amount of new space is available (the user has consumed data), an increase credit message is sent back to the sender, so that it can go on sending more.
Because of this, the capacity of the receive channels can influence performance: a small buffer means small credit and can cause the sender to frequently stop, waiting for feedback from the receiver; a big buffer can hide the latency involved with the credit sending completely.
You can also use the credits as asynchronous acknowledgments. For example, if the sender uses a buffered channel of 5 elements and the receiver a 30 elements one, when the sender has sent the 100th element, it can be sure that at least 100-30-5=65 messages have been "consumed" by the receiver (taken out of the receive buffer).

A full example that includes error handling can be found in the "examples" section.
See the advanced topics page for information about netchan internals, performance,
security, timeouts and heartbeats (after reading the API documentation).
TODO: insert pointer to advanced topics
*/
package netchan
