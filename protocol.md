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