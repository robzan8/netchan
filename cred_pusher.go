package netchan

import (
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

type pullEntry struct {
	name  hashedName
	ch    reflect.Value
	chCap int64
	// quando puller butta un elemento nel buffer, incrementa received
	received int64
}

func isPresent(e *pullEntry) bool {
	return !(e.name == hashedName{})
}

type pullTable struct {
	sync.RWMutex
	t []pullEntry
}

type credPusher struct {
	toEncoder chan<- credit
	table     *pullTable
	credits   []credit
	man       *Manager
}

func (p *credPusher) run() {
	for p.man.Error() == nil {
		time.Sleep(1 * time.Millisecond)

		p.updateCredits()
		for _, cred := range credits {
			toEncoder <- cred
		}
	}
	close(toEncoder)
}

func (p *credPusher) updateCredits() {
	p.table.RLock()
	p.credits = p.credits[:0]
	for id := range p.table.t {
		entry := &p.table.t[id]
		if !isPresent(entry) {
			continue
		}
		// do not swap the next two lines
		received := atomic.LoadInt64(&entry.received)
		chLen := int64(entry.ch.Len())
		consumed := received - chLen
		if consumed*2 >= entry.chCap { // i.e. consumed >= ceil(chCap/2)
			p.credits = append(p.credits, credit{id, consumed, nil})
			atomic.AddInt64(&entry.received, -consumed)
		}
	}
	table.RUnlock()
}
