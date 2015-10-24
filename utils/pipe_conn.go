package netchan_test

import "io"

// simulate full duplex connection
type pipeConn struct {
	readA, readB   *io.PipeReader
	writeA, writeB *io.PipeWriter
}

func newPipeConn() *pipeConn {
	c := new(pipeConn)
	c.readA, c.writeB = io.Pipe()
	c.readB, c.writeA = io.Pipe()
	return c
}

func (c *pipeConn) Close() {
	c.writeA.Close()
	c.writeB.Close()
	c.readA.Close()
	c.readB.Close()
}

// one of the two sides of the connection
type connSide struct {
	io.PipeReader
	io.PipeWriter
}

func sideA(conn *pipeConn) (s connSide) {
	s.Reader = conn.readA
	s.Writer = conn.writeA
	return
}

func sideB(conn *pipeConn) (s connSide) {
	s.Reader = conn.readB
	s.Writer = conn.writeB
	return
}
