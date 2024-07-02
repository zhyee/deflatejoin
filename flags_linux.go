//go:build linux

package dfjoin

/*
#cgo 386 LDFLAGS: -L${SRCDIR}/zlib/linux-386
#cgo amd64 LDFLAGS: -L${SRCDIR}/zlib/linux-amd64
#cgo arm64 LDFLAGS: -L${SRCDIR}/zlib/linux-arm64
#cgo mips64 LDFLAGS: -L${SRCDIR}/zlib/linux-mips64
#cgo mips64le LDFLAGS: -L${SRCDIR}/zlib/linux-mips64le
#cgo ppc64 LDFLAGS: -L${SRCDIR}/zlib/linux-ppc64
#cgo ppc64le LDFLAGS: -L${SRCDIR}/zlib/linux-ppc64le
*/
import "C"
