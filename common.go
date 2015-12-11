package netchan

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync"
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
	pool  *sync.Pool
}

type credit struct {
	id     int
	Amount int
	Name   *hashedName
}

var (
	poolsMu    sync.Mutex
	slicePools = make(map[reflect.Type]*sync.Pool)
)

func slicePool(elemType reflect.Type) *sync.Pool {
	poolsMu.Lock()
	defer poolsMu.Unlock()

	pool, present := slicePools[elemType]
	if present {
		return pool
	}
	pool = new(sync.Pool)
	pool.New = func() interface{} {
		typ := reflect.SliceOf(elemType)
		slicePt := reflect.New(typ)
		slicePt.Elem().Set(reflect.MakeSlice(typ, 0, 1))
		return slicePt.Interface()
	}
	slicePools[elemType] = pool
	return pool
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
