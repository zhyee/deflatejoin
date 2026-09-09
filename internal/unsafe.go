package internal

import "unsafe"

// UnsafeString copies length bytes from ptr into a Go-owned string.
// The caller must keep ptr valid throughout the call.
func UnsafeString(ptr *byte, length int) string {
	return string(unsafe.Slice(ptr, length))
}
