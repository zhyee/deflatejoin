package internal

import (
	"reflect"
	"unsafe"
)

func UnsafeString(ptr *byte, length int) string {
	sh := reflect.StringHeader{
		Data: uintptr(unsafe.Pointer(ptr)),
		Len:  length,
	}
	return *(*string)(unsafe.Pointer(&sh))
}
