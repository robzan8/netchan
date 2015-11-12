package netchan

import (
	"crypto/sha1"
	"reflect"
	"sync"
)

// sha1-hashed name of a net-chan
type hashedName [20]byte

func hashName(name string) hashedName {
	return sha1.Sum([]byte(name))
}

type element struct {
	id   int
	val  reflect.Value
	ok   bool // if not ok, the channel has been closed
	name *hashedName
}

type credit struct {
	id   int
	incr int64
	name *hashedName
}

type chanEntry struct {
	name    hashedName
	present bool
	init    bool
	ch      reflect.Value
	numElem int64
	recvCap int64
}

func (e *chanEntry) credit() *int64 {
	return &e.numElem
}

func (e *chanEntry) received() *int64 {
	return &e.numElem
}

type chanTable struct {
	sync.RWMutex
	t       []chanEntry
	pending []chanEntry
}

func entryByName(table []chanEntry, name hashedName) *chanEntry {
	for i := range table {
		entry := &table[i]
		if entry.present && entry.name == name {
			return entry
		}
	}
	return nil
}

func addEntry(table []chanEntry, entry chanEntry) (newTable []chanEntry) {
	for i := range table {
		if !table[i].present {
			table[i] = entry
			return table
		}
	}
	return append(table, entry)
}
