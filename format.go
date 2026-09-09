package dfjoin

import (
	"compress/gzip"
	stdzlib "compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/adler32"
	"hash/crc32"
	"io"
)

var (
	ErrHeader       = gzip.ErrHeader
	ErrChecksum     = gzip.ErrChecksum
	ErrCheckSize    = errors.New("gzip: invalid trailer size")
	ErrZlibHeader   = errors.New("zlib: invalid header")
	ErrZlibSum      = errors.New("zlib: invalid checksum")
	ErrDictionary   = stdzlib.ErrDictionary
	ErrTrailingData = errors.New("zlib: trailing data after stream")
)

type streamFormat bool

const (
	zlibFormat streamFormat = false
	gzipFormat streamFormat = true
)

func (f streamFormat) digest() hash.Hash32 {
	if f == gzipFormat {
		return crc32.NewIEEE()
	}
	return adler32.New()
}

func (f streamFormat) header() []byte {
	if f == gzipFormat {
		return []byte{31, 139, 8, 0, 0, 0, 0, 0, 0, 255}
	}
	return []byte{120, 156}
}

type headerReader struct {
	in    *compressedInput
	count int64
	crc   uint32
}

func (h *headerReader) read(p []byte) error {
	limit := h.in.budget.MaxHeaderBytes
	if limit > 0 && int64(len(p)) > limit-h.count {
		return ErrLimitExceeded
	}
	n, err := io.ReadFull(h.in, p)
	h.count += int64(n)
	h.crc = crc32.Update(h.crc, crc32.IEEETable, p[:n])
	return required(err)
}

func (h *headerReader) skip(n int) error {
	var scratch [256]byte
	for n > 0 {
		count := len(scratch)
		if n < count {
			count = n
		}
		if err := h.read(scratch[:count]); err != nil {
			return err
		}
		n -= count
	}
	return nil
}

func (h *headerReader) text() error {
	var b [1]byte
	for {
		if err := h.read(b[:]); err != nil {
			return err
		}
		if b[0] == 0 {
			return nil
		}
	}
}

func (f streamFormat) readHeader(in *compressedInput) error {
	if err := in.budget.nextMember(); err != nil {
		return err
	}
	h := headerReader{in: in}
	in.windowBits = 15
	if f == zlibFormat {
		var p [2]byte
		if err := h.read(p[:]); err != nil {
			return err
		}
		if p[0]&15 != 8 || p[0]>>4 > 7 || binary.BigEndian.Uint16(p[:])%31 != 0 {
			return ErrZlibHeader
		}
		if p[1]&32 != 0 {
			return ErrDictionary
		}
		in.windowBits = 8 + int(p[0]>>4)
		return nil
	}
	var p [10]byte
	if err := h.read(p[:]); err != nil {
		return err
	}
	if p[0] != 31 || p[1] != 139 || p[2] != 8 || p[3]&0xe0 != 0 {
		return ErrHeader
	}
	flags := p[3]
	if flags&4 != 0 {
		if err := h.read(p[:2]); err != nil {
			return err
		}
		if err := h.skip(int(binary.LittleEndian.Uint16(p[:2]))); err != nil {
			return err
		}
	}
	if flags&8 != 0 {
		if err := h.text(); err != nil {
			return err
		}
	}
	if flags&16 != 0 {
		if err := h.text(); err != nil {
			return err
		}
	}
	if flags&2 != 0 {
		want := uint16(h.crc)
		if err := h.read(p[:2]); err != nil {
			return err
		}
		if binary.LittleEndian.Uint16(p[:2]) != want {
			return ErrHeader
		}
	}
	return nil
}

func (f streamFormat) readTrailer(in *compressedInput, sum uint32, size int64) error {
	var p [8]byte
	n := 4
	if f == gzipFormat {
		n = 8
	}
	if _, err := io.ReadFull(in, p[:n]); err != nil {
		return fmt.Errorf("read trailer: %w", required(err))
	}
	if f == gzipFormat {
		if binary.LittleEndian.Uint32(p[:4]) != sum {
			return ErrChecksum
		}
		if binary.LittleEndian.Uint32(p[4:]) != uint32(size) {
			return ErrCheckSize
		}
	} else if binary.BigEndian.Uint32(p[:4]) != sum {
		return ErrZlibSum
	}
	return nil
}

func (f streamFormat) trailer(sum, size uint32) []byte {
	if f == gzipFormat {
		p := make([]byte, 8)
		binary.LittleEndian.PutUint32(p[:4], sum)
		binary.LittleEndian.PutUint32(p[4:], size)
		return p
	}
	p := make([]byte, 4)
	binary.BigEndian.PutUint32(p, sum)
	return p
}
