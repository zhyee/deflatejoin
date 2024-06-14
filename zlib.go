package dfjoin

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/adler32"
	"io"
	"unsafe"
)

/*
#include "dfjoin.h"
*/
import "C"

var simpleZlibHeader = []byte{0x78, 0x9c}

// #define Z_OK            0
// #define Z_STREAM_END    1
// #define Z_NEED_DICT     2
// #define Z_ERRNO        (-1)
// #define Z_STREAM_ERROR (-2)
// #define Z_DATA_ERROR   (-3)
// #define Z_MEM_ERROR    (-4)
// #define Z_BUF_ERROR    (-5)
// #define Z_VERSION_ERROR (-6)
var inflateErrors = map[int]string{
	int(C.Z_NEED_DICT):     "Z_NEED_DICT",
	int(C.Z_ERRNO):         "Z_ERRNO",
	int(C.Z_STREAM_ERROR):  "Z_STREAM_ERROR",
	int(C.Z_DATA_ERROR):    "Z_DATA_ERROR",
	int(C.Z_MEM_ERROR):     "Z_MEM_ERROR",
	int(C.Z_BUF_ERROR):     "Z_BUF_ERROR",
	int(C.Z_VERSION_ERROR): "Z_VERSION_ERROR",
}

type inflater struct {
	stream         *C.z_stream
	inputBuf       *C.uchar
	outputBuf      *C.uchar
	inputAvailSize int
	offset         int
	br             *bufio.Reader
	lastBlock      bool
	inflateEnd     bool
}

func (z *inflater) feedIn() error {
	var err error
	z.inputAvailSize, err = io.ReadFull(z.br, unsafe.Slice((*byte)(z.inputBuf), BufSize))
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("unable to read: %w", err)
	}
	if z.inputAvailSize == 0 {
		return io.ErrUnexpectedEOF
	}
	z.stream.avail_in = C.uint(z.inputAvailSize)
	z.stream.next_in = z.inputBuf
	return nil
}

func (z *inflater) inflate() (err error) {
	if z.inflateEnd {
		return nil
	}

	if z.stream.avail_in == 0 && z.stream.avail_out > 0 {
		if err = z.feedIn(); err != nil {
			return fmt.Errorf("unable to read data: %w", err)
		}
	}

	// dfjoin._Ctype_uint, *dfjoin._Ctype_uchar
	//fmt.Printf("%T, %T\n", stream.avail_in, stream.next_in)
	ret := C.inflate(z.stream, C.Z_BLOCK)

	if errCode, ok := inflateErrors[int(ret)]; ok {
		return fmt.Errorf("unable to inflate, error code: %d(%s)", int(ret), errCode)
	}

	if z.stream.data_type&C.int(128) != 0 {
		if z.lastBlock {
			z.inflateEnd = true
			return nil
		}
		pos := z.stream.data_type & 7 // 00000111
		if pos != 0 {
			pos = 0x100 >> pos
			preByte := unsafe.Slice((*byte)(z.inputBuf), BufSize)[z.inputAvailSize-int(z.stream.avail_in)-1]
			z.lastBlock = byte(pos)&preByte != 0
		} else {
			if z.stream.avail_in == 0 {
				if err = z.feedIn(); err != nil {
					return err
				}
			}
			z.lastBlock = (*(*byte)(z.stream.next_in))&1 != 0
		}
	}
	return nil
}

func (z *inflater) Close() error {
	if ret := C.inflateEnd(z.stream); ret != C.Z_OK {
		return fmt.Errorf("unable to free z_stream: %v\n", ret)
	}
	C.free(unsafe.Pointer(z.inputBuf))
	C.free(unsafe.Pointer(z.outputBuf))
	return nil
}

func ConcatZlib(w io.Writer, inputs ...io.Reader) error {
	if len(inputs) == 0 {
		return fmt.Errorf("empty sources")
	}

	switch len(inputs) {
	case 0:
		return fmt.Errorf("empty sources")
	case 1:
		_, err := io.Copy(w, inputs[0])
		return err
	default:
		gm, err := newZlibMerger(w)
		if err != nil {
			return fmt.Errorf("unable to write gzip header: %w", err)
		}
		for i, r := range inputs {
			if err = gm.concat(r, i == len(inputs)-1); err != nil {
				return fmt.Errorf("unable to concat gzip: %w", err)
			}
		}
	}
	return nil
}

type zlibReader struct {
	inflater
	adler32 hash.Hash32
}

func readZlibHeader(br *bufio.Reader) (n int, err error) {
	cmf, err := br.ReadByte()
	if err != nil {
		return n, fmt.Errorf("unable to read CMF byte: %w", err)
	}
	n++
	if cmf&0x0f != 8 {
		return n, fmt.Errorf("only support deflate compression method(8), got %d", cmf&0x0f)
	}
	if cmf>>4 > 7 {
		return n, fmt.Errorf("value of CINFO above 7 is not allowed")
	}

	flags, err := br.ReadByte()
	if err != nil {
		return n, fmt.Errorf("unable to read flags byte: %w", err)
	}
	n++

	if (uint16(cmf)<<8|uint16(flags))%31 != 0 {
		return n, fmt.Errorf("malformed FCHECK")
	}

	if flags&0x20 != 0 {
		discarded, err := br.Discard(4)
		if err != nil {
			return n, fmt.Errorf("unable to read DICT checksum: %w", err)
		}
		n += discarded
	}

	return n, nil
}

func (z *zlibReader) readHeader() (n int, err error) {
	return readZlibHeader(z.br)
}

func (z *zlibReader) Read(p []byte) (n int, err error) {
	defer func() {
		if n > 0 {
			_, _ = z.adler32.Write(p[:n]) // adler32.Write always return nil error
		}
		if errors.Is(err, io.EOF) {
			unRead := io.MultiReader(
				bytes.NewReader(unsafe.Slice((*byte)(z.stream.next_in), int(z.stream.avail_in))),
				z.br,
			)
			checksumBytes := make([]byte, 4)
			if _, ex := io.ReadFull(unRead, checksumBytes); ex != nil {
				err = fmt.Errorf("unable to read gzip trailer: %w", ex)
				return
			}

			adler32Sum := binary.BigEndian.Uint32(checksumBytes)
			if z.adler32.Sum32() != adler32Sum {
				err = fmt.Errorf("%w: expect 0x%x, got 0x%x", ErrZlibSum, z.adler32, adler32Sum)
				return
			}
		}
	}()

	for n < len(p) {
		if z.offset > 0 && z.offset >= int(BufSize-z.stream.avail_out) {
			z.offset = 0
			z.stream.next_out = z.outputBuf
			z.stream.avail_out = BufSize
		}

		for !z.inflateEnd && z.offset >= int(BufSize-z.stream.avail_out) {
			if err = z.inflate(); err != nil {
				return 0, fmt.Errorf("inflater: %w", err)
			}
		}

		if z.offset >= int(BufSize-z.stream.avail_out) {
			if n == 0 {
				return 0, io.EOF
			}
			return n, nil
		}

		uncompressed := unsafe.Slice((*byte)(z.outputBuf), int(BufSize-z.stream.avail_out))
		copied := copy(p[n:], uncompressed[z.offset:])
		n += copied
		z.offset += copied
	}
	return
}

func NewZlibReader(r io.Reader) (io.ReadCloser, error) {
	var stream C.z_stream

	if ret := C.initStream(&stream); ret != C.Z_OK {
		return nil, fmt.Errorf("unable to init z_stream: %d", int(ret))
	}

	zlibInBuf := C.malloc(BufSize)
	zlibOutBuf := C.malloc(BufSize)

	if zlibInBuf == nil || zlibOutBuf == nil {
		errMessage := C.errMessage()
		return nil, fmt.Errorf("unable to malloc buffer memory: %s",
			unsafe.String((*byte)(unsafe.Pointer(errMessage)), int(C.strlen(errMessage))))
	}

	br := bufio.NewReader(r)

	stream.avail_out = BufSize
	stream.next_out = (*C.uchar)(zlibOutBuf)

	zl := &zlibReader{
		inflater: inflater{
			stream:    &stream,
			inputBuf:  (*C.uchar)(zlibInBuf),
			outputBuf: (*C.uchar)(zlibOutBuf),
			br:        br,
		},
		adler32: adler32.New(),
	}
	if _, err := zl.readHeader(); err != nil {
		_ = zl.Close()
		return nil, fmt.Errorf("unable to read gzip header data: %w", err)
	}

	return zl, nil
}

type zlibMerger struct {
	adler32Sum uint32
	w          *bufio.Writer
	zlibInBuf  unsafe.Pointer
	zlibOutBuf unsafe.Pointer
}

func newZlibMerger(w io.Writer) (*zlibMerger, error) {
	memInBuf := C.malloc(BufSize)
	memOutBuf := C.malloc(BufSize)

	if memInBuf == nil || memOutBuf == nil {
		errMessage := C.errMessage()
		return nil, fmt.Errorf("unable to malloc memory for inflating: %s",
			unsafe.String((*byte)(unsafe.Pointer(errMessage)), int(C.strlen(errMessage))))
	}

	zm := &zlibMerger{
		zlibInBuf:  memInBuf,
		zlibOutBuf: memOutBuf,
		adler32Sum: 1, // adler32 checksum should be initialized to 1.
		w:          bufio.NewWriter(w),
	}

	if _, err := zm.w.Write(simpleZlibHeader); err != nil {
		return nil, fmt.Errorf("unable to write zlib header: %w", err)
	}
	return zm, nil
}

func (z *zlibMerger) concat(r io.Reader, isLastReader bool) (err error) {
	br := bufio.NewReader(r)
	if _, err = readZlibHeader(br); err != nil {
		return fmt.Errorf("unable to skip the gzip header: %w", err)
	}

	var stream C.z_stream

	if ret := C.initStream(&stream); ret != C.Z_OK {
		return fmt.Errorf("unable to init z_stream: %d", int(ret))
	}
	defer C.inflateEnd(&stream)

	inputBuf := (*C.uchar)(z.zlibInBuf)
	outputBuf := (*C.uchar)(z.zlibOutBuf)

	adler32Checker := adler32.New()
	uncompressedSize64 := int64(0)

	readSize, err := readToBuf(&stream, br, inputBuf)
	if err != nil {
		return err
	}

	stream.next_out = outputBuf
	stream.avail_out = BufSize

	lastBlock := (*(*byte)(inputBuf))&1 != 0
	if lastBlock && !isLastReader {
		*(*byte)(inputBuf) = *(*byte)(inputBuf) & (^byte(1))
	}

	for {
		if stream.avail_in == 0 && stream.avail_out > 0 {
			if _, err = z.w.Write(unsafe.Slice((*byte)(inputBuf), readSize)); err != nil {
				return fmt.Errorf("unable to write: %w", err)
			}
			if readSize, err = readToBuf(&stream, br, inputBuf); err != nil {
				return err
			}
		}

		if stream.avail_out < BufSize {
			_, _ = adler32Checker.Write(unsafe.Slice((*byte)(outputBuf), int(BufSize-stream.avail_out)))
			stream.next_out = outputBuf
			stream.avail_out = BufSize
		}

		// dfjoin._Ctype_uint, *dfjoin._Ctype_uchar
		//fmt.Printf("%T, %T\n", stream.avail_in, stream.next_in)
		ret := C.inflate(&stream, C.Z_BLOCK)

		switch ret {
		case C.Z_MEM_ERROR:
			return fmt.Errorf("inflater return error code: %d(Z_MEM_ERROR)", int(C.Z_MEM_ERROR))
		case C.Z_DATA_ERROR:
			return fmt.Errorf("inflater return error code: %d(Z_DATA_ERROR)", int(C.Z_DATA_ERROR))
		}

		uncompressedSize64 += int64(BufSize - stream.avail_out)

		if stream.data_type&C.int(128) != 0 {
			if lastBlock {
				break
			}
			pos := stream.data_type & 7 // 00000111
			if pos != 0 {
				pos = 0x100 >> pos
				preByte := unsafe.Slice((*byte)(inputBuf), readSize)[readSize-int(stream.avail_in)-1]
				lastBlock = byte(pos)&preByte != 0
				if lastBlock && !isLastReader {
					unsafe.Slice((*byte)(inputBuf), readSize)[readSize-int(stream.avail_in)-1] &= ^byte(pos)
				}
			} else {
				if stream.avail_in == 0 {
					if _, err = z.w.Write(unsafe.Slice((*byte)(inputBuf), readSize)); err != nil {
						return fmt.Errorf("unable to output: %w", err)
					}

					if readSize, err = readToBuf(&stream, br, inputBuf); err != nil {
						return
					}
				}
				lastBlock = (*(*byte)(stream.next_in))&1 != 0
				if lastBlock && !isLastReader {
					*(*byte)(stream.next_in) &= ^byte(1)
				}
			}
		}
	}

	if stream.avail_out < BufSize {
		_, _ = adler32Checker.Write(unsafe.Slice((*byte)(outputBuf), int(BufSize-stream.avail_out)))
	}

	pos := stream.data_type & 7
	if _, err = z.w.Write(unsafe.Slice((*byte)(inputBuf), readSize-int(stream.avail_in)-1)); err != nil {
		err = fmt.Errorf("unable to output: %w", err)
		return
	}

	lastByte := unsafe.Slice((*byte)(inputBuf), readSize)[readSize-int(stream.avail_in)-1]

	if pos == 0 || isLastReader {
		if err = z.w.WriteByte(lastByte); err != nil {
			err = fmt.Errorf("unable to output last byte: %w", err)
			return
		}
	} else {
		lastByte &= byte((int(0x100) >> pos) - 1)
		if pos&1 != 0 {
			// odd
			if err = z.w.WriteByte(lastByte); err != nil {
				err = fmt.Errorf("unable to output last byte: %w", err)
				return
			}
			if pos == 1 {
				if err = z.w.WriteByte(0); err != nil {
					err = fmt.Errorf("unable to output last byte: %w", err)
					return
				}
			}
			if _, err = z.w.Write([]byte{0, 0, 255, 255}); err != nil {
				err = fmt.Errorf("unable to output empty block: %w", err)
				return
			}
		} else {
			// even
			switch pos {
			case 6:
				if err = z.w.WriteByte(lastByte | 8); err != nil {
					err = fmt.Errorf("unable to output last byte: %w", err)
					return
				}
				lastByte = 0
				fallthrough
			case 4:
				if err = z.w.WriteByte(lastByte | 0x20); err != nil {
					err = fmt.Errorf("unable to output last byte: %w", err)
					return
				}
				lastByte = 0
				fallthrough
			case 2:
				if err = z.w.WriteByte(lastByte | 0x80); err != nil {
					err = fmt.Errorf("unable to output last byte: %w", err)
					return
				}
				if err = z.w.WriteByte(0); err != nil {
					err = fmt.Errorf("unable to output last byte: %w", err)
					return
				}
			}
		}
	}

	z.adler32Sum = Adler32Combine(z.adler32Sum, adler32Checker.Sum32(), uncompressedSize64)

	if isLastReader {
		trailer := binary.BigEndian.AppendUint32(nil, z.adler32Sum)
		if _, err := z.w.Write(trailer); err != nil {
			return fmt.Errorf("unable to output gzip trailer: %w", err)
		}
		if err = z.w.Flush(); err != nil {
			return fmt.Errorf("unable to flush write buffer: %w", err)
		}

		C.free(z.zlibInBuf)
		C.free(z.zlibOutBuf)
	}

	return nil
}
