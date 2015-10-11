package netchan

import (
	"crypto/sha1"
	"encoding/gob"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
)

// sha1-hashed name of a net channel
type hashedName [20]byte

type Manager struct {
	pushReq  chan<- addReq
	pullReq  chan<- addReq
	pushResp <-chan error
	pullResp <-chan error
	errMu    sync.Mutex
	err      atomic.Value // *error
	gotErr   chan struct{}
}

func Manage(conn io.ReadWriter) *Manager {
	const chCap int = 8

	pushReq := make(chan addReq)
	pullReq := make(chan addReq)
	pushResp := make(chan error)
	pullResp := make(chan error)
	m := &Manager{pushReq: pushReq, pullReq: pullReq, pushResp: pushResp, pullResp: pullResp}
	m.err.Store((*error)(nil))
	m.gotErr = make(chan struct{})

	encElemCh := make(chan element, chCap)
	encWindowCh := make(chan winUpdate, chCap)
	enc := newEncoder(encElemCh, encWindowCh, m, conn)

	decElemCh := make(chan element, chCap)
	decWindowCh := make(chan winUpdate, chCap)
	types := &typeMap{m: make(map[hashedName]reflect.Type)}
	dec := &decoder{
		toPuller: decElemCh,
		toPusher: decWindowCh,
		man:      m,
		types:    types,
		dec:      gob.NewDecoder(conn),
	}

	push := newPusher(encElemCh, decWindowCh, pushReq, pushResp, m)
	pull := &puller{
		elemCh:    decElemCh,
		toEncoder: encWindowCh,
		pullReq:   pullReq,
		pullResp:  pullResp,
		man:       m,
		chans:     make(map[hashedName]*pullInfo),
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
	name hashedName
}

func (m *Manager) Push(name string, channel interface{}) error {
	ch := reflect.ValueOf(channel)
	if ch.Kind() != reflect.Chan || ch.Type().ChanDir()&reflect.RecvDir == 0 {
		return nil // error: manager will not be able to receive from the channel
	}
	m.pushReq <- addReq{ch, sha1.Sum([]byte(name))}
	select {
	case err := <-m.pushResp:
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
	m.pullReq <- addReq{ch, sha1.Sum([]byte(name))}
	select {
	case err := <-m.pullResp:
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
