package dfjoin

import (
	"crypto/rand"
	"hash/adler32"
	"hash/crc32"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCrc32Combine(t *testing.T) {
	s1 := make([]byte, 12345)
	s2 := make([]byte, 56789)

	if _, err := io.ReadFull(rand.Reader, s1); err != nil {
		t.Fatal(err)
	}

	if _, err := io.ReadFull(rand.Reader, s2); err != nil {
		t.Fatal(err)
	}

	crc1 := crc32.ChecksumIEEE(s1)
	crc2 := crc32.ChecksumIEEE(s2)
	crc3 := crc32.ChecksumIEEE(append(s1, s2...))
	crcCombine := IEEECrc32Combine(crc1, crc2, int64(len(s2)))

	assert.Equal(t, crc3, crcCombine)
}

func TestAdler32Combine(t *testing.T) {
	s1 := make([]byte, 12345)
	s2 := make([]byte, 56789)

	if _, err := io.ReadFull(rand.Reader, s1); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(rand.Reader, s2); err != nil {
		t.Fatal(err)
	}

	a1 := adler32.Checksum(s1)
	a2 := adler32.Checksum(s2)

	ac := Adler32Combine(a1, a2, int64(len(s2)))
	a3 := adler32.Checksum(append(s1, s2...))

	assert.Equal(t, a3, ac)
}
