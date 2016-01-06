package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"testing"

	"github.com/robzan8/netchan"
)

var (
	mn    *netchan.Manager
	tasks = make(chan benchTask)
	done  = make(chan struct{})
)

func fatal(err error) {
	if err != nil {
		log.Output(2, err.Error())
		os.Exit(1)
	}
}

func TestMain(m *testing.M) {
	flag.Parse()

	cmdName, err := exec.LookPath("netchan_bench")
	if err != nil {
		log.Fatal("Before running the benchmarks, ",
			"you should install netchan_bench and make it reachable from your $PATH.")
	}
	var random [20]byte
	_, err = rand.Read(random[:])
	fatal(err)
	sockDir := "/tmp/netchan/bench_socks"
	err = os.MkdirAll(sockDir, 0700)
	fatal(err)
	network := "unix"
	sockName := sockDir + "/" + hex.EncodeToString(random[:])
	ln, err := net.Listen(network, sockName)
	fatal(err)
	peer := exec.Command(cmdName, network, sockName)
	stderr, err := peer.StderrPipe()
	fatal(err)
	err = peer.Start()
	fatal(err)
	go func() {
		var buf [512]byte
		n, err := stderr.Read(buf[:])
		if err == io.EOF {
			return
		}
		fatal(err)
		log.Fatalf("Error from bench peer: %s", buf[0:n])
	}()
	conn, err := ln.Accept()
	fatal(err)
	ln.Close()
	mn = netchan.Manage(conn)
	go func() {
		<-mn.ErrorSignal()
		if err := mn.Error(); err != io.EOF {
			mn.ShutDown()
			log.Fatal(err)
		}
	}()
	err = mn.OpenSend("tasks", tasks)
	fatal(err)
	err = mn.OpenRecv("done", done, 1)
	fatal(err)

	exitCode := m.Run()

	close(tasks)
	// wait that peer shuts down
	<-mn.ErrorSignal()
	// wait that local shut down completes
	mn.ShutDown()
	os.Exit(exitCode)
}

func Benchmark_Chans1_Size1(b *testing.B) {
	task := benchTask{1, 1, b.N}
	tasks <- task
	executeTask(task, mn)
	<-done
}

func Benchmark_Chans10_Size1(b *testing.B) {
	task := benchTask{10, 1, b.N}
	tasks <- task
	executeTask(task, mn)
	<-done
}

func Benchmark_Chans100_Size1(b *testing.B) {
	task := benchTask{100, 1, b.N}
	tasks <- task
	executeTask(task, mn)
	<-done
}

func Benchmark_Chans1_Size100(b *testing.B) {
	task := benchTask{1, 100, b.N}
	tasks <- task
	executeTask(task, mn)
	<-done
}

func Benchmark_Chans10_Size100(b *testing.B) {
	task := benchTask{10, 100, b.N}
	tasks <- task
	executeTask(task, mn)
	<-done
}

func Benchmark_Chans100_Size100(b *testing.B) {
	task := benchTask{100, 100, b.N}
	tasks <- task
	executeTask(task, mn)
	<-done
}
