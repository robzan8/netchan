package netchan

import (
	"crypto/sha1"
	"encoding/gob"
	"io"
	"reflect"
)

// a chan ID is the sha1 sum of its name
type chanID [20]byte

type errString string

func (e *errString) Error() string {
	return string(*e)
}

type addReq struct { // request for adding (ch, name) to pusher or puller
	ch   reflect.Value
	id   chanID
	resp chan error
}

type Manager struct {
	toPusher chan<- addReq
	toPuller chan<- addReq
}

func checkChan(ch reflect.Value, dir reflect.ChanDir) bool {
	return ch.Kind() == reflect.Chan && ch.Type().ChanDir()&dir != 0
}

func (m *Manager) Push(name string, channel interface{}) error {
	ch := reflect.ValueOf(channel)
	if !checkChan(ch, reflect.RecvDir) {
		return nil // error: manager will not be able to receive from the channel
	}
	resp := make(chan error, 1)
	m.toPusher <- addReq{ch, sha1.Sum([]byte(name)), resp}
	return <-resp
}

func (m *Manager) Pull(name string, channel interface{}) error {
	ch := reflect.ValueOf(channel)
	if !checkChan(ch, reflect.SendDir) {
		return nil // error: manager will not be able to send to the channel
	}
	resp := make(chan error, 1)
	m.toPuller <- addReq{ch, sha1.Sum([]byte(name)), resp}
	return <-resp
}

func Manage(conn io.ReadWriter) *Manager {
	const chCap int = 8

	pushAddCh := make(chan addReq, chCap)
	pullAddCh := make(chan addReq, chCap)
	m := &Manager{pushAddCh, pullAddCh}

	encElemCh := make(chan element, chCap)
	encWindowCh := make(chan winUpdate, chCap)
	enc := &encoder{encElemCh, encWindowCh, gob.NewEncoder(conn), nil}

	types := &typeMap{m: make(map[chanID]reflect.Type)}

	decElemCh := make(chan element, chCap)
	decWindowCh := make(chan winUpdate, chCap)
	dec := &decoder{decElemCh, decWindowCh, gob.NewDecoder(conn), nil, types}

	push := newPusher(encElemCh, decWindowCh, pushAddCh)
	pull := newPuller(decElemCh, encWindowCh, pullAddCh, types)

	go enc.run()
	go dec.run()
	go push.run()
	go pull.run()
	return m
}
