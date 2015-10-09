package netchan

import "io"

// simulate full duplex connection
type connection struct {
	readA, readB   io.Reader
	writeA, writeB io.Writer
}

func newConn() *connection {
	c := new(connection)
	c.readA, c.writeB = io.Pipe()
	c.readB, c.writeA = io.Pipe()
	return c
}

type side struct {
	io.Reader
	io.Writer
}

func sideA(conn *connection) io.ReadWriter {
	s := new(side)
	s.Reader = conn.readA
	s.Writer = conn.writeA
	return s
}

func limSideA(conn *connection, n int) io.ReadWriter {
	s := new(side)
	s.Reader = NewLimGobReader(conn.readA, int64(n))
	s.Writer = conn.writeA
	return s
}

func sideB(conn *connection) io.ReadWriter {
	s := new(side)
	s.Reader = conn.readB
	s.Writer = conn.writeB
	return s
}

func limSideB(conn *connection, n int) io.ReadWriter {
	s := new(side)
	s.Reader = NewLimGobReader(conn.readB, int64(n))
	s.Writer = conn.writeB
	return s
}
