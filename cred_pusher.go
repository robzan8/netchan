package netchan

import (
	"sync/atomic"
	"time"
)

type credPusher struct {
	table *pullTable
}

func (c *credPusher) run() {
	for {
		time.Sleep(1 * time.Millisecond)
		c.checkTable()
	}
}

func (c *credPusher) checkTable() {
	c.table.RLock()
	defer c.table.RUnlock()

	for i := range c.table.t {
		entry := &c.table.t[i]
		present := atomic.LoadUint64(&entry.present)
		if present == 0 {
			continue
		}

	}
}
