package main

import (
	"bufio"
	"encoding/gob"
	"io"
	"log"
	"net"
	"os"
	"sync"
)

type benchTask struct {
	Quit               bool
	ItemSize, NumItems int
}

func executeTask(task benchTask, r io.Reader, bw *bufio.Writer) {
	item := make([]byte, task.ItemSize)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < task.NumItems; i++ {
			_, err := bw.Write(item)
			if err != nil {
				log.Fatal(err)
			}
		}
		bw.Flush()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		dest := make([]byte, task.ItemSize)
		for i := 0; i < task.NumItems; i++ {
			_, err := io.ReadFull(r, dest)
			if err != nil {
				log.Fatal(err)
			}
		}
	}()
	wg.Wait()
}

func main() {
	if len(os.Args) != 3 {
		log.Fatal("len(Args) != 3")
	}
	conn, err := net.Dial(os.Args[1], os.Args[2])
	if err != nil {
		log.Fatal(err)
	}
	r := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	dec := gob.NewDecoder(r)
	for {
		var t benchTask
		err := dec.Decode(&t)
		if t.Quit || err == io.EOF {
			return
		}
		if err != nil {
			log.Fatal(err)
		}
		executeTask(t, r, bw)
	}
}
