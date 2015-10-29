/*
Package netchan enables using Go channels to communicate over a network connection.
One peer sends messages to a channel and the other peer receives the messages from
another channel of the same type: netchan takes care of transferring the messages
between the two channels over the connection.
It is possible to open multiple net-chans, in both directions, on a single connection.
The connection can be any io.ReadWriter like a TCP one and unix domain sockets.

A full example that includes error handling can be found in the "examples" section.
See the advanced topics page for information about netchan internals, performance,
security, timeouts and heartbeats (after reading the API documentation).
TODO: insert pointer to advanced topics
*/
package netchan
