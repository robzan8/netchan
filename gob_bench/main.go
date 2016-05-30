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

func executeTask(task benchTask, bw *bufio.Writer, enc *gob.Encoder, dec *gob.Decoder) {
	item := make([]byte, task.ItemSize)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < task.NumItems; i++ {
			err := enc.Encode(item)
			if err != nil {
				log.Fatal(err)
			}
		}
		bw.Flush()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		var dest []byte
		for i := 0; i < task.NumItems; i++ {
			err := dec.Decode(&dest)
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
	bw := bufio.NewWriter(conn)
	enc := gob.NewEncoder(bw)
	dec := gob.NewDecoder(conn)
	for {
		var t benchTask
		err := dec.Decode(&t)
		if t.Quit || err == io.EOF {
			return
		}
		if err != nil {
			log.Fatal(err)
		}
		executeTask(t, bw, enc, dec)
	}
}
