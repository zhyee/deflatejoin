//go:build windows

package dfjoin

/*
#cgo amd64 LDFLAGS: -L${SRCDIR}/zlib/windows-amd64
#cgo 386 LDFLAGS: -L${SRCDIR}/zlib/windows-386
*/
import "C"
