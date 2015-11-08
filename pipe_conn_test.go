package netchan_test

import "io"

// identical definitions are in netchan_test.go (package netchan).
// we also want them in package netchan_test, available to examples.

// pipeConn represents one side of a full-duplex
// connection based on io.PipeReader/Writer
type pipeConn struct {
	*io.PipeReader
	*io.PipeWriter
}

func (c pipeConn) Close() error {
	c.PipeReader.Close()
	c.PipeWriter.Close()
	return nil // ignoring errors
}

func newPipeConn() (sideA, sideB pipeConn) {
	sideA.PipeReader, sideB.PipeWriter = io.Pipe()
	sideB.PipeReader, sideA.PipeWriter = io.Pipe()
	return
}
