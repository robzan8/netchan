package netchan

import (
	"reflect"
	"sync/atomic"
	"time"
)

// The receiver receives elements from the decoder and sends them to the user channels
// that are registered in its table (see the graph in manager.go).
type receiver struct {
	elemCh <-chan element
	table  *chanTable
	mn     *Manager
}

// Open a net-chan for receiving.
func (r *receiver) open(name string, ch reflect.Value) error {
	r.table.Lock()
	defer r.table.Unlock()

	hName := hashName(name)
	entry := entryByName(r.table.t, hName)
	if entry != nil {
		return errAlreadyOpen(name, Recv)
	}
	// The position where the entry is added will determine the net-chan's id.
	r.table.t = addEntry(r.table.t, chanEntry{
		name:    hName,
		present: true,
		init:    true,
		ch:      ch,
		recvCap: int64(ch.Cap()),
	})
	return nil
}

// Got an element from the decoder.
func (r *receiver) handleElem(elem element) error {
	r.table.RLock()

	if elem.id >= len(r.table.t) {
		r.table.RUnlock()
		return errInvalidId
	}
	entry := &r.table.t[elem.id]
	if !entry.present {
		r.table.RUnlock()
		return newErr("element arrived for deleted net-chan")
	}
	if !elem.ok {
		// net-chan closed, delete the entry.
		r.table.RUnlock()
		// entry pointer can become invalid here.
		r.table.Lock()
		r.table.t[elem.id].ch.Close()
		r.table.t[elem.id] = chanEntry{}
		r.table.Unlock()
		return nil
	}
	if atomic.LoadInt64(entry.buffered()) >= entry.recvCap {
		r.table.RUnlock()
		return newErr("peer sent more than its credit allowed")
	}
	if int64(entry.ch.Len()) == entry.recvCap {
		r.table.RUnlock()
		return newErr("peer did not exceed its credit and yet the receive buffer" +
			" is full; check the restrictions for Open(Recv) in the docs")
	}
	// Do not swap the next two lines.
	entry.ch.Send(elem.val) // should not block
	atomic.AddInt64(entry.buffered(), 1)
	r.table.RUnlock()
	return nil
}

func (r *receiver) run() {
	for {
		elem, ok := <-r.elemCh
		if !ok {
			// An error occurred and decoder shut down.
			return
		}
		err := r.handleElem(elem)
		if err != nil {
			go r.mn.ShutDownWith(err)
		}
	}
}

// credSender monitors the receive channels and sends credits to the encoder.
type credSender struct {
	toEncoder chan<- credit
	table     *chanTable // same table of the receiver
	mn        *Manager

	credits []credit
}

func (s *credSender) run() {
	for s.mn.Error() == nil {
		s.updateCredits()
		for _, cred := range s.credits {
			s.toEncoder <- cred
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(s.toEncoder)
}

func (s *credSender) updateCredits() {
	s.table.RLock()

	s.credits = s.credits[0:0]
	for id := range s.table.t {
		entry := &s.table.t[id]
		if !entry.present {
			continue
		}
		// Do not swap the next two lines.
		buffered := atomic.LoadInt64(entry.buffered())
		chLen := int64(entry.ch.Len())
		consumed := buffered - chLen
		if entry.init {
			// Initial credit must be sent.
			s.credits = append(s.credits, credit{id, entry.recvCap, &entry.name})
			entry.init = false
		} else if consumed*2 >= entry.recvCap { // i.e. consumed >= ceil(recvCap/2)
			// Regular credit must be sent.
			s.credits = append(s.credits, credit{id, consumed, nil})
			// Forget about the messages the user consumed.
			atomic.AddInt64(entry.buffered(), -consumed)
		}
		// TODO: when much time passes, send credit even if it's small.
	}
	s.table.RUnlock()
}
