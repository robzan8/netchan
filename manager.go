package netchan

import (
	"crypto/sha1"
	"encoding/gob"
	"errors"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
)

var (
	errBadPushChan = errors.New("netchan: Push called with bad channel argument")
	errBadPullChan = errors.New("netchan: Pull called with bad channel argument")
)

// sha1-hashed name of a net channel
type hashedName [20]byte

func hashName(name string) hashedName {
	return sha1.Sum([]byte(name))
}

type element struct {
	id  int
	val reflect.Value
	ok  bool // if not ok, the channel has been closed
}

type credit struct {
	id   int
	incr int64
	name *hashedName
}

type Manager struct {
	push *pusher
	pull *puller

	errMu  sync.Mutex
	err    atomic.Value // *error
	gotErr chan struct{}
}

func Manage(conn io.ReadWriter) *Manager {
	push := new(pusher)
	pull := new(puller)
	m := &Manager{push: push, pull: pull}
	m.err.Store((*error)(nil))
	m.gotErr = make(chan struct{})

	const chCap int = 8
	pushElemCh := make(chan element, chCap)
	pullCredCh := make(chan credit, chCap)
	pushTab := &pushTable{pending: make(map[hashedName]reflect.Value)}
	*push = pusher{toEncoder: pushElemCh, table: pushTab, man: m}
	push.initialize()
	cRecv := &credReceiver{creditCh: pullCredCh, table: pushTab, man: m}

	pullElemCh := make(chan element, chCap)
	pushCredCh := make(chan credit, chCap)
	pullTab := new(pullTable)
	*pull = puller{elemCh: pullElemCh, table: pullTab, man: m}
	cSend := &credSender{toEncoder: pushCredCh, table: pullTab, man: m}

	enc := &encoder{elemCh: pushElemCh, creditCh: pushCredCh, man: m}
	enc.enc = gob.NewEncoder(conn)
	dec := &decoder{toPuller: pullElemCh, toCPuller: pullCredCh, table: pullTab, man: m}
	dec.dec = gob.NewDecoder(conn)

	go push.run()
	go pull.run()
	go cRecv.run()
	go cSend.run()
	go enc.run()
	go dec.run()
	return m
}

func checkChan(ch reflect.Value, dir reflect.ChanDir) bool {
	return ch.Kind() == reflect.Chan &&
		ch.Cap() > 0 &&
		ch.Type().ChanDir()&dir != 0
}

func (m *Manager) Push(channel interface{}, name string) error {
	ch := reflect.ValueOf(channel)
	if !checkChan(ch, reflect.RecvDir) {
		// pusher will not be able to receive from channel
		panic(errBadPushChan)
	}
	return m.push.add(ch, hashName(name))
}

func (m *Manager) Pull(channel interface{}, name string) error {
	ch := reflect.ValueOf(channel)
	if !checkChan(ch, reflect.SendDir) {
		// puller will not be able to send to channel
		panic(errBadPullChan)
	}
	return m.pull.add(ch, hashName(name))
}

func (m *Manager) signalError(err error) {
	m.errMu.Lock()
	defer m.errMu.Unlock()
	prevErr := m.Error()
	if prevErr != nil {
		// someone signaled an error before us
		return
	}
	m.err.Store(&err)
	close(m.gotErr)
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
