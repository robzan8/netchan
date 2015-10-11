package netchan

import (
	"crypto/sha1"
	"encoding/gob"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
)

// a chan ID is the sha1 sum of its name
type chanID [20]byte

type Manager struct {
	toPusher chan<- addReq
	toPuller chan<- addReq
	errMu    sync.Mutex
	err      atomic.Value // *error
	gotErr   chan struct{}
}

func Manage(conn io.ReadWriter) *Manager {
	const chCap int = 8

	pushAddCh := make(chan addReq, chCap)
	pullAddCh := make(chan addReq, chCap)
	m := &Manager{toPusher: pushAddCh, toPuller: pullAddCh, gotErr: make(chan struct{})}
	m.err.Store((*error)(nil))

	encElemCh := make(chan element, chCap)
	encWindowCh := make(chan winUpdate, chCap)
	enc := &encoder{
		elemCh:   encElemCh,
		windowCh: encWindowCh,
		man:      m,
		enc:      gob.NewEncoder(conn),
	}

	types := &typeMap{m: make(map[chanID]reflect.Type)}

	decElemCh := make(chan element, chCap)
	decWindowCh := make(chan winUpdate, chCap)
	dec := &decoder{
		toPuller: decElemCh,
		toPusher: decWindowCh,
		man:      m,
		dec:      gob.NewDecoder(conn),
		types:    types,
	}

	push := newPusher(encElemCh, decWindowCh, pushAddCh, m)
	pull := &puller{
		elemCh:    decElemCh,
		toEncoder: encWindowCh,
		addReqCh:  pullAddCh,
		man:       m,
		chans:     make(map[chanID]*pullInfo),
		types:     types,
	}

	go enc.run()
	go dec.run()
	go push.run()
	go pull.run()
	return m
}

type addReq struct { // request for adding (ch, name) to pusher or puller
	ch   reflect.Value
	id   chanID
	resp chan error
}

func (m *Manager) Push(name string, channel interface{}) error {
	ch := reflect.ValueOf(channel)
	if ch.Kind() != reflect.Chan || ch.Type().ChanDir()&reflect.RecvDir == 0 {
		return nil // error: manager will not be able to receive from the channel
	}
	resp := make(chan error, 1) // avoid this by making chans sync
	m.toPusher <- addReq{ch, sha1.Sum([]byte(name)), resp}
	select {
	case err := <-resp:
		return err
	case <-m.GotError():
		return m.Error()
	}
}

func (m *Manager) Pull(name string, channel interface{}) error {
	ch := reflect.ValueOf(channel)
	if ch.Kind() != reflect.Chan || ch.Type().ChanDir()&reflect.SendDir == 0 {
		return nil // error: manager will not be able to send to the channel
	}
	resp := make(chan error, 1)
	m.toPuller <- addReq{ch, sha1.Sum([]byte(name)), resp}
	select {
	case err := <-resp:
		return err
	case <-m.GotError():
		return m.Error()
	}
}

func (m *Manager) signalError(err error) error {
	m.errMu.Lock()
	defer m.errMu.Unlock()
	prevErr := m.Error()
	if prevErr != nil { // someone signaled an error before us
		return prevErr
	}
	m.err.Store(&err)
	close(m.gotErr)
	return err
}

func (m *Manager) Error() error {
	err := m.err.Load().(*error)
	if err == nil {
		return nil
	}
	return *err
}

func (m *Manager) GotError() <-chan struct{} {
	return m.gotErr
}
