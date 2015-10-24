Advanced topics
---------------

###Security
The netchan protocol is designed to be secure, but it still has to be battle-tested. It should be safe to expose a public API with netchan on top of TLS connections. In particular, given a server that provides a netchan-based service, malicious clients should not be able to:
- Make the server panic;
- Make the server consume an arbitrarily large amount of memory;
- Make the server consume an arbitrarily large amount of CPU time.
Malformed messages will signal an error.
For each connection, the server only opens the number of net-chans that it wants; current implementation protects from syn-flood-like attacks.
A limited gob reader can be used to return an error as soon as a too big gob message is received, before even parsing and allocating memory for it. See example.
A netchan manager can never deadlock: it uses very simple (pipeline based) patterns for communicating and very simple patterns for shared memory accesses.

###Flow control
When multiplexing several streams of data on a single connection, the problem of "head of line blocking" becomes critical.
Suppose there are 10 net-chans going from peer A to peer B on a TCP connection. Peer A is sending data on all channels, but for some reason peer B is not consuming data from channel 1. At some point manager B will receive data for channel 1 on the connection, but, since the user is not consuming that data, the operation of sending it to the appropriate channel will be blocking, possibly for a long time: one idle stream can block the entire connection.
The common solution to this problem, adopted by HTTP/2, netchan and other protocols, is to allocate one receive buffer for each stream and to make sure that the sender never sends more data than the buffers can hold. This way, the receiving manager will never block. For this reason netchan implements a credit system. The sender has a certain integer credit for each net-chan, that represents the free space in the receive buffer for that net-chan. The sender decreases the credit by 1 each time it sends an element. If the credit becomes 0, it must stop sending messages. The receiver periodically checks the receive buffer and when a good amount of new space is available (the user has consumed data), an increase credit message is sent back to the sender, so that it can go on sending more.
Because of this, the capacity of the receive channels can influence performance: a small buffer means small credit and can cause the sender to frequently stop, waiting for feedback from the receiver; a big buffer can hide the latency involved with the credit sending.
The optimal buffer capacity depends on many factors: connection bandwidth, connection latency, size of the gob-encoded elements, rates at which data is produced and consumed. For each particular application, the smallest buffer that gives good performance should be used.

###The netchan protocol
Netchan uses gob for encoding and decoding data. Each netchan message is preceded by an header defined as:

	type header struct {
		MsgType int // type of the message immediately following
		ChanId  int // ID of the channel this message is directed to
	}

There are 3 different types of messages: element messages (type 1), credit messages (type 2) and error messages (type 3). Messages with type in range [4, 10] are reserved for future use and must be ignored by current implementations. Messages with type less than 1 or greater than 10 must raise an error.

Element messages carry values from and to user channels. They can be represented and must be encoded as:

	type element struct {
		id  int           // ID of the channel
		val reflect.Value // the user data been carried
		ok  bool          // !ok signals channel closure
	}
	// encoded with:
	var elem element
	// enc is a gob encoder
	enc.Encode(header{1, elem.id})
	enc.EncodeValue(elem.val)
	enc.Encode(elem.ok)




