//go:build darwin

package dfjoin

/*
#cgo amd64 LDFLAGS: -L${SRCDIR}/zlib/darwin-amd64
#cgo arm64 LDFLAGS: -L${SRCDIR}/zlib/darwin-arm64
*/
import "C"
