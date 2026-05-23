package sing

import (
	"reflect"
	"sync"
	"unsafe"

	"github.com/sagernet/sing-box/experimental/cachefile"
)

func clearCacheFileInMemory(cf *cachefile.CacheFile) {
	if cf == nil {
		return
	}
	rv := reflect.ValueOf(cf).Elem()
	if cf.StoreRDRC() {
		withRWMutexLock(fieldRWMutex(rv, "saveRDRCAccess"), func() {
			clearReflectMap(rv, "saveRDRC")
		})
	}
	if cf.StoreFakeIP() {
		withRWMutexLock(fieldRWMutex(rv, "saveFakeIPAccess"), func() {
			clearReflectMap(rv, "saveDomain")
			clearReflectMap(rv, "saveAddress4")
			clearReflectMap(rv, "saveAddress6")
		})
	}
}

func fieldRWMutex(rv reflect.Value, name string) *sync.RWMutex {
	f := rv.FieldByName(name)
	return (*sync.RWMutex)(unsafe.Pointer(f.UnsafeAddr()))
}

func withRWMutexLock(mu *sync.RWMutex, fn func()) {
	mu.Lock()
	defer mu.Unlock()
	fn()
}

func clearReflectMap(rv reflect.Value, name string) {
	f := rv.FieldByName(name)
	if !f.IsValid() || f.Kind() != reflect.Map {
		return
	}
	f.Set(reflect.MakeMap(f.Type()))
}
