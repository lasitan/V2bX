package sing

import (
	"reflect"
	"unsafe"
)

func reflectUnexportedField(rv reflect.Value, name string) reflect.Value {
	f := rv.FieldByName(name)
	if !f.IsValid() {
		return reflect.Value{}
	}
	return reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
}

func clearReflectMap(rv reflect.Value, name string) {
	f := reflectUnexportedField(rv, name)
	if !f.IsValid() || f.Kind() != reflect.Map {
		return
	}
	f.Set(reflect.MakeMap(f.Type()))
}
