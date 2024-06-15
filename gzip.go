package dfjoin

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"unsafe"

	"github.com/zhyee/deflatejoin/internal"
)

/*
#cgo LDFLAGS: -lz
#include "dfjoin.h"
*/
import "C"

const BufSize = 1 << 15

var (
	ErrHeader     = gzip.ErrHeader
	ErrChecksum   = gzip.ErrChecksum
	ErrCheckSize  = errors.New("gzip: invalid trailer size")
	ErrZlibHeader = errors.New("zlib: invalid header")
	ErrZlibSum    = errors.New("zlib: invalid checksum")
)

var simpleGzipHeader = []byte("\x1f\x8b\x08\x00\x00\x00\x00\x00\x00\xff")

type gzReader struct {
	inflater
	crc32Sum    uint32
	checkSize32 uint32
}

func ConcatGzip(w io.Writer, inputs ...io.Reader) error {
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
		gm, err := newGzMerger(w)
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

func (g *gzReader) readHeader() (int, error) {
	return readGzipHeader(g.br)
}

func readGzipHeader(r *bufio.Reader) (int, error) {
	skipBytes := 0

	var magic [3]byte
	n, err := io.ReadFull(r, magic[:])
	skipBytes += n
	if err != nil {
		return skipBytes, fmt.Errorf("unable to read gzip magic: %w", err)
	}
	if magic[0] != 0x1f || magic[1] != 0x8b || magic[2] != 8 {
		return skipBytes, ErrHeader
	}
	flags, err := r.ReadByte()
	if err != nil {
		return skipBytes, fmt.Errorf("unable to read gzip flags: %w", err)
	}
	skipBytes++
	if flags&0xe0 != 0 {
		return skipBytes, fmt.Errorf("unknown reserved bits set")
	}
	discarded, err := r.Discard(6)
	if err != nil {
		return skipBytes, fmt.Errorf("unable to skip bytes: %w", err)
	}
	skipBytes += discarded

	// skip extra field
	if flags&4 != 0 {
		var extraLen uint16
		if err = binary.Read(r, binary.LittleEndian, &extraLen); err != nil {
			return skipBytes, fmt.Errorf("unable to read extra field length: %w", err)
		}
		skipBytes += 2
		if extraLen > 0 {
			if discarded, err = r.Discard(int(extraLen)); err != nil {
				return skipBytes, fmt.Errorf("unable to skip bytes: %w", err)
			}
			skipBytes += discarded
		}
	}

	// skip file name
	if flags&8 != 0 {
		for {
			b, err := r.ReadByte()
			if err != nil {
				return skipBytes, fmt.Errorf("unable to read byte: %w", err)
			}
			skipBytes++
			if b == 0 {
				break
			}
		}
	}

	// skip comments
	if flags&16 != 0 {
		for {
			b, err := r.ReadByte()
			if err != nil {
				return skipBytes, fmt.Errorf("unable to read next byte: %w", err)
			}
			skipBytes++
			if b == 0 {
				break // Read to NULL
			}
		}
	}

	// skip header crc
	if flags&2 != 0 {
		discarded, err = r.Discard(2)
		if err != nil {
			return skipBytes, fmt.Errorf("unable to skip bytes: %w", err)
		}
		skipBytes += discarded
	}
	return skipBytes, nil
}

type gzMerger struct {
	w           *bufio.Writer
	zlibInBuf   unsafe.Pointer
	zlibOutBuf  unsafe.Pointer
	crc32Sum    uint32
	checkSize32 uint32
}

func newGzMerger(w io.Writer) (*gzMerger, error) {
	inMemBuf := C.malloc(BufSize)
	outMemBuf := C.malloc(BufSize)

	if inMemBuf == nil || outMemBuf == nil {
		errMessage := C.errMessage()
		return nil, fmt.Errorf("unable to malloc memory for buffer: %s",
			internal.UnsafeString((*byte)(unsafe.Pointer(errMessage)), int(C.strlen(errMessage))))
	}

	gm := &gzMerger{
		zlibInBuf:  inMemBuf,
		zlibOutBuf: outMemBuf,
		w:          bufio.NewWriter(w),
	}
	if _, err := gm.w.Write(simpleGzipHeader); err != nil {
		return nil, fmt.Errorf("unable to output gzip header: %w", err)
	}
	return gm, nil
}

func (g *gzMerger) concat(r io.Reader, isLastReader bool) (err error) {
	br := bufio.NewReader(r)
	if _, err = readGzipHeader(br); err != nil {
		return fmt.Errorf("unable to skip the gzip header: %w", err)
	}

	var stream C.z_stream

	if ret := C.initStream(&stream); ret != C.Z_OK {
		return fmt.Errorf("unable to init z_stream: %d", int(ret))
	}
	defer C.inflateEnd(&stream)

	inputBuf := (*C.uchar)(g.zlibInBuf)
	outputBuf := (*C.uchar)(g.zlibOutBuf)

	crc32Checker := crc32.NewIEEE()
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
			if _, err = g.w.Write(unsafe.Slice((*byte)(inputBuf), readSize)); err != nil {
				return fmt.Errorf("unable to write: %w", err)
			}
			if readSize, err = readToBuf(&stream, br, inputBuf); err != nil {
				return err
			}
		}

		if stream.avail_out < BufSize {
			_, _ = crc32Checker.Write(unsafe.Slice((*byte)(outputBuf), int(BufSize-stream.avail_out)))
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
					if _, err = g.w.Write(unsafe.Slice((*byte)(inputBuf), readSize)); err != nil {
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
		_, _ = crc32Checker.Write(unsafe.Slice((*byte)(outputBuf), int(BufSize-stream.avail_out)))
	}

	pos := stream.data_type & 7
	if _, err = g.w.Write(unsafe.Slice((*byte)(inputBuf), readSize-int(stream.avail_in)-1)); err != nil {
		err = fmt.Errorf("unable to output: %w", err)
		return
	}

	lastByte := unsafe.Slice((*byte)(inputBuf), readSize)[readSize-int(stream.avail_in)-1]

	if pos == 0 || isLastReader {
		if err = g.w.WriteByte(lastByte); err != nil {
			err = fmt.Errorf("unable to output last byte: %w", err)
			return
		}
	} else {
		lastByte &= byte((int(0x100) >> pos) - 1)
		if pos&1 != 0 {
			// odd
			if err = g.w.WriteByte(lastByte); err != nil {
				err = fmt.Errorf("unable to output last byte: %w", err)
				return
			}
			if pos == 1 {
				if err = g.w.WriteByte(0); err != nil {
					err = fmt.Errorf("unable to output last byte: %w", err)
					return
				}
			}
			if _, err = g.w.Write([]byte{0, 0, 255, 255}); err != nil {
				err = fmt.Errorf("unable to output empty block: %w", err)
				return
			}
		} else {
			// even
			switch pos {
			case 6:
				if err = g.w.WriteByte(lastByte | 8); err != nil {
					err = fmt.Errorf("unable to output last byte: %w", err)
					return
				}
				lastByte = 0
				fallthrough
			case 4:
				if err = g.w.WriteByte(lastByte | 0x20); err != nil {
					err = fmt.Errorf("unable to output last byte: %w", err)
					return
				}
				lastByte = 0
				fallthrough
			case 2:
				if err = g.w.WriteByte(lastByte | 0x80); err != nil {
					err = fmt.Errorf("unable to output last byte: %w", err)
					return
				}
				if err = g.w.WriteByte(0); err != nil {
					err = fmt.Errorf("unable to output last byte: %w", err)
					return
				}
			}
		}
	}

	g.crc32Sum = IEEECrc32Combine(g.crc32Sum, crc32Checker.Sum32(), uncompressedSize64)
	g.checkSize32 += uint32(uncompressedSize64)

	if isLastReader {
		trailer := make([]byte, 8)
		binary.LittleEndian.PutUint32(trailer[:4], g.crc32Sum)
		binary.LittleEndian.PutUint32(trailer[4:], g.checkSize32)
		if _, err := g.w.Write(trailer); err != nil {
			return fmt.Errorf("unable to output gzip trailer: %w", err)
		}
		if err = g.w.Flush(); err != nil {
			return fmt.Errorf("unable to flush write buffer: %w", err)
		}
		C.free(g.zlibInBuf)
		C.free(g.zlibOutBuf)
	}

	return nil
}

func readToBuf(stream *C.z_stream, r io.Reader, buf *C.uchar) (int, error) {
	readSize, err := io.ReadFull(r, unsafe.Slice((*byte)(buf), BufSize))
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return readSize, fmt.Errorf("unable to read to buf: %w", err)
	}
	if readSize == 0 {
		return readSize, fmt.Errorf("unable to read deflate data: %w", io.ErrUnexpectedEOF)
	}
	//fmt.Println("read compressed bytes: ", readSize)
	stream.avail_in = C.uint(readSize)
	stream.next_in = buf
	return readSize, nil
}

func (g *gzReader) Read(p []byte) (n int, err error) {
	defer func() {
		if n > 0 {
			g.crc32Sum = crc32.Update(g.crc32Sum, crc32.IEEETable, p[:n])
			g.checkSize32 += uint32(n)
		}
		if errors.Is(err, io.EOF) {
			unRead := io.MultiReader(
				bytes.NewReader(unsafe.Slice((*byte)(g.stream.next_in), int(g.stream.avail_in))),
				g.br,
			)
			trailer := make([]byte, 8)
			if _, ex := io.ReadFull(unRead, trailer); ex != nil {
				err = fmt.Errorf("unable to read gzip trailer: %w", ex)
				return
			}

			trailerCrc32 := binary.LittleEndian.Uint32(trailer[:4])
			if g.crc32Sum != trailerCrc32 {
				err = fmt.Errorf("%w: expect 0x%x, got 0x%x", ErrChecksum, g.crc32Sum, trailerCrc32)
				return
			}
			checkSize := binary.LittleEndian.Uint32(trailer[4:])
			if g.checkSize32 != checkSize {
				err = fmt.Errorf("%w: expect %d, got %d", ErrCheckSize, g.checkSize32, checkSize)
				return
			}
		}
	}()

	for n < len(p) {
		if g.offset > 0 && g.offset >= int(BufSize-g.stream.avail_out) {
			g.offset = 0
			g.stream.next_out = g.outputBuf
			g.stream.avail_out = BufSize
		}

		for !g.inflateEnd && g.offset >= int(BufSize-g.stream.avail_out) {
			if err = g.inflate(); err != nil {
				return 0, fmt.Errorf("inflate: %w", err)
			}
		}

		if g.offset >= int(BufSize-g.stream.avail_out) {
			if n == 0 {
				return 0, io.EOF
			}
			return n, nil
		}

		uncompressed := unsafe.Slice((*byte)(g.outputBuf), int(BufSize-g.stream.avail_out))
		copied := copy(p[n:], uncompressed[g.offset:])
		n += copied
		g.offset += copied
	}
	return
}

var _ io.ReadCloser = (*gzReader)(nil)

func NewGzipReader(r io.Reader) (io.ReadCloser, error) {
	var stream C.z_stream

	if ret := C.initStream(&stream); ret != C.Z_OK {
		return nil, fmt.Errorf("unable to init z_stream: %d", int(ret))
	}

	memIn := C.malloc(BufSize)
	memOut := C.malloc(BufSize)

	if memIn == nil || memOut == nil {
		errMessage := C.errMessage()
		return nil, fmt.Errorf("unable to malloc buffer memory: %s",
			internal.UnsafeString((*byte)(unsafe.Pointer(errMessage)), int(C.strlen(errMessage))))
	}

	br := bufio.NewReader(r)

	stream.avail_out = BufSize
	stream.next_out = (*C.uchar)(memOut)

	gz := &gzReader{
		inflater: inflater{
			stream:    &stream,
			inputBuf:  (*C.uchar)(memIn),
			outputBuf: (*C.uchar)(memOut),
			br:        br,
		},
	}
	if n, err := gz.readHeader(); err != nil {
		_ = gz.Close()
		return nil, fmt.Errorf("unable to read gzip header data: n = %d, %w", n, err)
	}

	return gz, nil
}

func CGOTest() {
}
