package dfjoin

/*
#cgo CFLAGS: -I${SRCDIR}/zlib
#cgo LDFLAGS: -lz
#include "dfjoin.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"io"
	"unsafe"
)

const BufSize = 1 << 15

// compressedInput retains read-ahead bytes for trailers and subsequent members.
// Its data belongs to nativeStream and remains allocated until Close.
type compressedInput struct {
	r          io.Reader
	data       []byte
	pos, end   int
	pending    error
	budget     *budget
	windowBits int
}

func (in *compressedInput) ensure() error {
	if err := in.budget.check(); err != nil {
		return err
	}
	if in.pos < in.end {
		return nil
	}
	if in.pending != nil {
		return in.pending
	}
	for attempts := 0; attempts < 100; attempts++ {
		if err := in.budget.check(); err != nil {
			return err
		}
		capacity, probe := allowance(in.budget.MaxCompressedBytes, in.budget.compressed, len(in.data))
		n, err := in.r.Read(in.data[:capacity])
		if n < 0 || n > capacity {
			return errors.New("deflatejoin: invalid reader count")
		}
		if probe && n > 0 {
			in.pos, in.end, in.pending = 0, 0, ErrLimitExceeded
			return ErrLimitExceeded
		}
		in.budget.compressed += int64(n)
		in.pos, in.end, in.pending = 0, n, err
		if n > 0 {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return io.ErrNoProgress
}

func (in *compressedInput) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := in.ensure(); err != nil {
		return 0, err
	}
	n := copy(p, in.data[in.pos:in.end])
	in.pos += n
	return n, nil
}

func (in *compressedInput) ReadByte() (byte, error) {
	if err := in.ensure(); err != nil {
		return 0, err
	}
	b := in.data[in.pos]
	in.pos++
	return b, nil
}

func required(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

type nativeStream struct {
	stream        *C.z_stream
	inMem, outMem unsafe.Pointer
	input         compressedInput
	output        []byte
}

func newNative(r io.Reader, b *budget) (*nativeStream, error) {
	s := &nativeStream{}
	// zlib retains the stream address internally, so the stream itself must
	// also live in C memory, not in the Go heap.
	s.stream = (*C.z_stream)(C.calloc(1, C.size_t(C.sizeof_z_stream)))
	s.inMem = C.calloc(1, BufSize)
	s.outMem = C.calloc(1, BufSize)
	if s.stream == nil || s.inMem == nil || s.outMem == nil {
		s.Close()
		return nil, errors.New("deflatejoin: unable to allocate native buffers")
	}
	s.input = compressedInput{r: r, data: unsafe.Slice((*byte)(s.inMem), BufSize), budget: b}
	s.output = unsafe.Slice((*byte)(s.outMem), BufSize)
	if ret := C.initStream(s.stream); ret != C.Z_OK {
		s.Close()
		return nil, fmt.Errorf("deflatejoin: inflate initialization failed (%d)", int(ret))
	}
	return s, nil
}

func (s *nativeStream) reset() error {
	if ret := C.inflateReset2(s.stream, -C.int(s.input.windowBits)); ret != C.Z_OK {
		return fmt.Errorf("deflatejoin: inflate reset failed (%d)", int(ret))
	}
	return nil
}

// step never refills input: callers must preserve the consumed compressed bytes
// until any required deflate block-bit edits have been written.
func (s *nativeStream) step(block bool) (produced int, boundary, last, full bool, bits int, err error) {
	if err = s.input.budget.check(); err != nil {
		return
	}
	in := &s.input
	before := in.end - in.pos
	s.stream.next_in = (*C.Bytef)(unsafe.Add(s.inMem, in.pos))
	s.stream.avail_in = C.uInt(before)
	s.stream.next_out = (*C.Bytef)(s.outMem)
	capacity, _ := allowance(in.budget.MaxUncompressedBytes, in.budget.total, BufSize)
	s.stream.avail_out = C.uInt(capacity)
	flush := C.int(C.Z_NO_FLUSH)
	if block {
		flush = C.Z_BLOCK
	}
	strict := C.int(0)
	if in.windowBits < 15 {
		strict = 1
	}
	ret := C.inflateStream(s.stream, flush, strict)
	in.pos += before - int(s.stream.avail_in)
	produced = capacity - int(s.stream.avail_out)
	full = s.stream.avail_out == 0
	boundary = s.stream.data_type&128 != 0
	last = ret == C.Z_STREAM_END
	bits = int(s.stream.data_type & 7)
	if ret != C.Z_OK && ret != C.Z_STREAM_END && ret != C.Z_BUF_ERROR {
		err = fmt.Errorf("deflatejoin: inflate failed (%d): %s", int(ret), C.GoString(s.stream.msg))
	} else if produced == 0 && before == int(s.stream.avail_in) && !boundary && !last {
		err = io.ErrNoProgress
	}
	return
}

func (s *nativeStream) Close() error {
	var err error
	if s.stream != nil {
		if s.stream.state != nil {
			if ret := C.inflateEnd(s.stream); ret != C.Z_OK {
				err = fmt.Errorf("deflatejoin: inflate close failed (%d)", int(ret))
			}
		}
		C.free(unsafe.Pointer(s.stream))
		s.stream = nil
	}
	C.free(s.inMem)
	C.free(s.outMem)
	s.inMem, s.outMem = nil, nil
	s.input.data, s.output = nil, nil
	return err
}
