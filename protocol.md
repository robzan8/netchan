Netchan protocol

The protocol is simple and precise. Every deviation from the protocol causes an error and
every error is fatal. To give some examples: a request for opening a channel that is
already open, data for a net-chan that has been closed, a credit message with a negative
value: all fatal errors.

The protocol involves no retransmissions nor timing issues, the underlying
connection must take care of delivering data in order and with best-effort reliability.

Netchan uses gob for encoding data. Each netchan message is preceded by an header defined
as:
	type header struct {
		MsgType int // type of the message immediately following
		ChanID  int // ID of the net-chan this message is directed to
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
explain two independent subsystems
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

document hashedName
separate receive and send subsystems
