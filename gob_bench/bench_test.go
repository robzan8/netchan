package main

import (
	"bufio"
	"crypto/rand"
	"encoding/gob"
	"encoding/hex"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"testing"
)

var (
	bw  *bufio.Writer
	enc *gob.Encoder
	dec *gob.Decoder
)

const network = "unix"

func check(err error) {
	if err != nil {
		log.Output(2, err.Error())
		os.Exit(1)
	}
}

func TestMain(m *testing.M) {
	flag.Parse()

	peerPath, err := exec.LookPath("gob_bench")
	if err != nil {
		log.Fatal("Before running the benchmarks, ",
			"you should install gob_bench and make it reachable from your $PATH.")
	}
	var random [20]byte
	_, err = rand.Read(random[:])
	check(err)
	sockDir := "/tmp/netchan/bench_socks"
	check(os.MkdirAll(sockDir, 0700))
	sockName := sockDir + "/" + hex.EncodeToString(random[:])
	ln, err := net.Listen(network, sockName)
	check(err)
	peer := exec.Command(peerPath, network, sockName)
	stderr, err := peer.StderrPipe()
	check(err)
	check(peer.Start())
	go func() {
		var buf [512]byte
		n, err := stderr.Read(buf[:])
		if err == io.EOF {
			return
		}
		check(err)
		log.Fatalf("Error from bench peer: %s", buf[0:n])
	}()
	conn, err := ln.Accept()
	check(err)
	ln.Close()
	bw = bufio.NewWriter(conn)
	enc = gob.NewEncoder(bw)
	dec = gob.NewDecoder(conn)

	exitCode := m.Run()

	enc.Encode(benchTask{Quit: true})
	bw.Flush()
	conn.Close()
	os.Exit(exitCode)
}

func Benchmark_Size10(b *testing.B) {
	task := benchTask{false, 10, b.N}
	enc.Encode(task)
	executeTask(task, bw, enc, dec)
}
