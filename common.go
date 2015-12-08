package netchan

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
)

// sha1-hashed name of a net-chan
type hashedName [20]byte

func hashName(name string) hashedName {
	return sha1.Sum([]byte(name))
}

type hello struct{}

type userData struct {
	id    int
	batch reflect.Value // if zero, represents end of stream
}

type credit struct {
	id     int
	Amount int
	Name   *hashedName
}

var poolMu sync.Mutex
var poolMap atomic.Value // map[reflect.Type]*sync.Pool

func init() {
	poolMap.Store(make(map[reflect.Type]*sync.Pool))
}

func getSlice(elemType reflect.Type) reflect.Value {
	m := poolMap.Load().(map[reflect.Type]*sync.Pool)
	pool, present := m[elemType]
	if !present {
		poolMu.Lock()
		pool, present = m[elemType]
		if !present {
			newM := make(map[reflect.Type]*sync.Pool)
			for k, v := range m {
				newM[k] = v
			}
			pool = new(sync.Pool)
			newM[elemType] = pool
			poolMap.Store(newM)
		}
		poolMu.Unlock()
	}
	slicePt := pool.Get()
	if slicePt != nil {
		return reflect.ValueOf(slicePt).Elem()
	}
	typ := reflect.SliceOf(elemType)
	slice := reflect.New(typ).Elem() // slices must be settable for gob decoding
	slice.Set(reflect.MakeSlice(typ, 0, 1))
	return slice
}

func putSlice(slice reflect.Value) {
	elemType := slice.Type().Elem()
	m := poolMap.Load().(map[reflect.Type]*sync.Pool)
	// put pointer to slice to retain settability
	m[elemType].Put(slice.Addr().Interface())
}

func newErr(str string) error {
	return errors.New("netchan: " + str)
}

var errInvalidId = newErr("message with invalid ID received")

func fmtErr(format string, a ...interface{}) error {
	return fmt.Errorf("netchan: "+format, a...)
}

func errAlreadyOpen(dir, name string) error {
	return fmtErr("Open%s: net-chan %s is already open", dir, strconv.Quote(name))
}
