package netchan

import (
	"crypto/sha1"
	"errors"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
)

var (
	errBadPushChan = errors.New("netchan Manager: Push called with bad channel argument")
	errBadPullChan = errors.New("netchan Manager: Pull called with bad channel argument")
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
	incr int
	open bool
	name hashedName
}

type Manager struct {
	pushReq  chan<- addReq
	pullReq  chan<- addReq
	pushResp <-chan error
	pullResp <-chan error

	errMu  sync.Mutex
	err    atomic.Value // *error
	gotErr chan struct{}
}

func Manage(conn io.ReadWriter) *Manager {
	const chCap int = 8
	pushReq := make(chan addReq) // these must be unbuffered
	pullReq := make(chan addReq)
	pushResp := make(chan error)
	pullResp := make(chan error)
	encElemCh := make(chan element, chCap)
	encCredCh := make(chan credit, chCap)
	decElemCh := make(chan element, chCap)
	decCredCh := make(chan credit, chCap)
	types := &typeMap{m: make(map[hashedName]reflect.Type)}

	m := &Manager{pushReq: pushReq, pullReq: pullReq, pushResp: pushResp, pullResp: pullResp}
	m.err.Store((*error)(nil))
	m.gotErr = make(chan struct{})
	push := &pusher{
		toEncoder: encElemCh,
		creditCh:  decCredCh,
		pushReq:   pushReq,
		pushResp:  pushResp,
		man:       m,
	}
	push.initialize()
	pull := &puller{
		elemCh:    decElemCh,
		toEncoder: encCredCh,
		pullReq:   pullReq,
		pullResp:  pullResp,
		types:     types,
		man:       m,
	}
	pull.initialize()
	enc := &encoder{elemCh: encElemCh, creditCh: encCredCh, man: m}
	enc.initialize(conn)
	dec := &decoder{
		toPuller:  decElemCh,
		toPusher:  decCredCh,
		pushCount: &push.count,
		pullCount: &pull.count,
		types:     types,
		man:       m,
	}
	dec.initialize(conn)

	go push.run()
	go pull.run()
	go enc.run()
	go dec.run()
	return m
}

type addReq struct { // request for adding (ch, name) to pusher or puller
	ch   reflect.Value
	name hashedName
}

func (m *Manager) Push(channel interface{}, name string) error {
	ch := reflect.ValueOf(channel)
	if ch.Kind() != reflect.Chan || ch.Type().ChanDir()&reflect.RecvDir == 0 {
		// pusher will not be able to receive from channel
		panic(errBadPushChan)
	}
	m.pushReq <- addReq{ch, sha1.Sum([]byte(name))}
	select {
	case err := <-m.pushResp:
		return err
	case <-m.GotError():
		return m.Error()
	}
}

func (m *Manager) Pull(channel interface{}, name string) error {
	ch := reflect.ValueOf(channel)
	if ch.Kind() != reflect.Chan || ch.Type().ChanDir()&reflect.SendDir == 0 {
		// puller will not be able to send to channel
		panic(errBadPullChan)
	}
	m.pullReq <- addReq{ch, sha1.Sum([]byte(name))}
	select {
	case err := <-m.pullResp:
		return err
	case <-m.GotError():
		return m.Error()
	}
}

func (m *Manager) signalError(err error) {
	m.errMu.Lock()
	defer m.errMu.Unlock()
	prevErr := m.Error()
	if prevErr != nil { // someone signaled an error before us
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
