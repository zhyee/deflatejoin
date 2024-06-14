package dfjoin

import (
	"bytes"
	"compress/zlib"
	crand "crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"testing"
	"time"
)

func TestNewZlibReader(t *testing.T) {
	t.Run("default-compression", func(t *testing.T) {
		zlibOut := new(bytes.Buffer)
		zw := zlib.NewWriter(zlibOut)

		if _, err := zw.Write(text4Test); err != nil {
			t.Fatal(err)
		}

		_ = zw.Close()

		zr, err := NewZlibReader(zlibOut)
		if err != nil {
			t.Fatal(err)
		}

		defer func() {
			if err = zr.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
		}()

		decompress, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal("decompress: ", err)
		}

		if bytes.Compare(text4Test, decompress) != 0 {
			t.Fatalf("the decompressed out is not equal to input")
		}
	})

	t.Run("no-compression", func(t *testing.T) {
		randInput := make([]byte, rand.Intn(1<<25))
		t.Logf("input length: %d", len(randInput))
		if _, err := io.ReadFull(crand.Reader, randInput); err != nil {
			t.Fatal("rand read: ", err)
		}

		zlibOut := new(bytes.Buffer)
		zw := zlib.NewWriter(zlibOut)
		if _, err := zw.Write(randInput); err != nil {
			t.Fatal("compress: ", err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal("close: ", err)
		}

		zr, err := NewZlibReader(zlibOut)
		if err != nil {
			t.Fatal("new: ", err)
		}

		defer func() {
			if err = zr.Close(); err != nil {
				t.Fatal("close: ", err)
			}
		}()

		plainOut, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}

		if bytes.Compare(randInput, plainOut) != 0 {
			t.Fatalf("the uncompressed out is not equal to source input")
		}
	})
}

func generateZlibOut(decompressLen int) []byte {
	out := new(bytes.Buffer)
	zw := zlib.NewWriter(out)

	total := 0
	randBytes := make([]byte, 6)
	for total < decompressLen {
		cnt, _ := crand.Read(randBytes)

		n, err := zw.Write(text4Test)
		if err != nil {
			panic(err)
		}
		total += n
		text4Test = append(text4Test, randBytes[:cnt]...)
	}

	if err := zw.Close(); err != nil {
		panic(err)
	}
	return out.Bytes()
}

func zlibConcatGo(w io.Writer, inputs ...io.Reader) error {
	zw := zlib.NewWriter(w)
	defer zw.Close()

	for _, r := range inputs {
		var err error
		func() {
			var zr io.ReadCloser
			zr, err = zlib.NewReader(r)
			if err != nil {
				return
			}
			defer zr.Close()
			if _, err = io.Copy(zw, zr); err != nil {
				return
			}
		}()
		if err != nil {
			return fmt.Errorf("unable to decompress: %w", err)
		}
	}
	return nil
}

func TestConcatZlib(t *testing.T) {
	zl1 := generateZlibOut(rand.Intn(1<<26) + math.MaxUint16)
	zl2 := generateZlibOut(rand.Intn(1<<26) + math.MaxUint16)
	zl3 := generateZlibOut(rand.Intn(1<<26) + math.MaxUint16)
	zl4 := generateZlibOut(rand.Intn(1<<26) + math.MaxUint16)

	goJoined := new(bytes.Buffer)
	zlibJoined := new(bytes.Buffer)

	start := time.Now()
	if err := zlibConcatGo(goJoined, bytes.NewReader(zl1), bytes.NewReader(zl2),
		bytes.NewReader(zl3), bytes.NewReader(zl4)); err != nil {
		t.Fatalf("go concate: %v", err)
	}
	t.Log("go concat cost: ", time.Now().Sub(start))

	start = time.Now()
	if err := ConcatZlib(zlibJoined, bytes.NewReader(zl1), bytes.NewReader(zl2),
		bytes.NewReader(zl3), bytes.NewReader(zl4)); err != nil {
		t.Fatalf("zlib concat: %v", err)
	}
	t.Log("zlib concat cost: ", time.Now().Sub(start))
	t.Logf("go concat length: %d, zlib concat length: %d", goJoined.Len(), zlibJoined.Len())

	goDecompress, err := zlib.NewReader(goJoined)
	if err != nil {
		t.Fatalf("go decompress: %v", err)
	}
	defer goDecompress.Close()

	zlibDecompress, err := zlib.NewReader(zlibJoined)
	if err != nil {
		t.Fatalf("zlib decompress: %v", err)
	}
	defer zlibDecompress.Close()

	goBuf := make([]byte, 4096)
	zlibBuf := make([]byte, 4096)

	decompressionLen := 0

	for {
		n1, err1 := io.ReadFull(goDecompress, goBuf)
		n2, err2 := io.ReadFull(zlibDecompress, zlibBuf)

		if bytes.Compare(goBuf[:n1], zlibBuf[:n2]) != 0 {
			t.Fatalf("uncompressed contents are different")
		}

		decompressionLen += n1

		if err1 != nil || err2 != nil {
			if !errors.Is(err1, err2) {
				t.Fatalf("expected same error: %v", errors.Join(err1, err2))
			}
			break
		}
	}

	t.Logf("decompression length: %d", decompressionLen)
}

func BenchmarkConcatZlib(b *testing.B) {
	zl1 := generateZlibOut(rand.Intn(1<<26) + math.MaxUint16)
	zl2 := generateZlibOut(rand.Intn(1<<26) + math.MaxUint16)
	zl3 := generateZlibOut(rand.Intn(1<<26) + math.MaxUint16)
	zl4 := generateZlibOut(rand.Intn(1<<26) + math.MaxUint16)

	b.Run("concat-by-go", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if err := zlibConcatGo(io.Discard, bytes.NewReader(zl1), bytes.NewReader(zl2),
				bytes.NewReader(zl3), bytes.NewReader(zl4)); err != nil {
				b.Fatalf("go concate: %v", err)
			}
		}
	})

	b.Run("concat-by-zlib", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if err := ConcatZlib(io.Discard, bytes.NewReader(zl1), bytes.NewReader(zl2),
				bytes.NewReader(zl3), bytes.NewReader(zl4)); err != nil {
				b.Fatalf("zlib concat: %v", err)
			}
		}
	})

}
