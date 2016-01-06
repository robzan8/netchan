package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"

	"github.com/robzan8/netchan"
)

type benchTask struct {
	NumChans, ItemSize, NumItems int
}

// should agree with the one defined in package netchan (enc_dec.go)
const wantBatchSize = 512

const wantBufSize = 2048

func executeTask(task benchTask, mn *netchan.Manager) {
	item := make([]byte, task.ItemSize)
	var wg sync.WaitGroup
	chCap := wantBatchSize / task.ItemSize
	bufCap := wantBufSize / task.ItemSize

	for i := 0; i < task.NumChans; i++ {
		ch := make(chan []byte, chCap)
		err := mn.OpenSend(fmt.Sprintf("items-%d", i), ch)
		fatal(err)
		wg.Add(1)
		go func() {
			for j := 0; j < task.NumItems; j++ {
				ch <- item
			}
			close(ch)
			wg.Done()
		}()
	}

	for i := 0; i < task.NumChans; i++ {
		ch := make(chan []byte, chCap)
		err := mn.OpenRecv(fmt.Sprintf("items-%d", i), ch, bufCap)
		fatal(err)
		wg.Add(1)
		go func() {
			for range ch {
			}
			wg.Done()
		}()
	}

	wg.Wait()
	// runtime.GC()?
}

func fatal(err error) {
	if err != nil {
		log.Output(2, err.Error())
		os.Exit(1)
	}
}

func main() {
	if len(os.Args) != 3 {
		log.Fatal("len(Args) != 3")
	}
	conn, err := net.Dial(os.Args[1], os.Args[2])
	if err != nil {
		log.Fatal(err)
	}
	mn := netchan.Manage(conn)
	go func() {
		<-mn.ErrorSignal()
		if err := mn.Error(); err != io.EOF {
			mn.ShutDown()
			os.Exit(1)
		}
	}()
	tasks := make(chan benchTask)
	err = mn.OpenRecv("tasks", tasks, 1)
	if err != nil {
		log.Fatal(err)
	}
	done := make(chan struct{})
	err = mn.OpenSend("done", done)
	if err != nil {
		log.Fatal(err)
	}

	for t := range tasks {
		executeTask(t, mn)
		done <- struct{}{}
	}
	mn.ShutDown()
}
