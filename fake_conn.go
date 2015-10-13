package netchan

import "io"

// simulate full duplex connection
type connection struct {
	readA, readB   *io.PipeReader
	writeA, writeB *io.PipeWriter
}

func newConn() *connection {
	c := new(connection)
	c.readA, c.writeB = io.Pipe()
	c.readB, c.writeA = io.Pipe()
	return c
}

func (c *connection) Close() {
	c.writeA.Close()
	c.writeB.Close()
}

type connSide struct {
	io.Reader
	io.Writer
}

func sideA(conn *connection) (s connSide) {
	s.Reader = conn.readA
	s.Writer = conn.writeA
	return
}

func limSideA(conn *connection, n int) (s connSide) {
	s.Reader = NewLimGobReader(conn.readA, int64(n))
	s.Writer = conn.writeA
	return
}

func sideB(conn *connection) (s connSide) {
	s.Reader = conn.readB
	s.Writer = conn.writeB
	return
}

func limSideB(conn *connection, n int) (s connSide) {
	s.Reader = NewLimGobReader(conn.readB, int64(n))
	s.Writer = conn.writeB
	return
}
