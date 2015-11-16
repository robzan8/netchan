Using netchan
-------------

###Troubleshooting netchan programs
Rule 0 is: check for errors with the ErrorSignal and Error methods.
The errors that can occur are basically:
- connection errors:<br />
	Like when the connection is closed.
- netchan protocol errors:
	If this happens, you are most likely misusing the library. For example, you are calling Manage more than once on the same connection, or you are receiving from two net-chans with the same Go channel.
- gob errors:
	You are violating some gob rule. Like sending with a chan uint and receiving with a chan int, or sending a struct that has no exported fields.

Rule 1 is: use timeouts.
Some errors are not detected by netchan and can be caught only with timeouts. For example, if you open a net-chan with direction Recv on both peers, each peer will be expecting to receive messages from the other, but no messages will be sent and no error will occur.

The Open method adds an entry in the table of the channels that the manager is tracking and returns immediately, before an exchange of messages happens to so it returns an error only 