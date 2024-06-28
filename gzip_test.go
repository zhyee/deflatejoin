package dfjoin

import (
	"bytes"
	"compress/gzip"
	crand "crypto/rand"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

//go:embed testdata/data.txt
var text4Test []byte

func genTestGzipDoc() ([]byte, int) {
	compressed := new(bytes.Buffer)
	gw := gzip.NewWriter(compressed)
	repeatNum := 0
	for repeatNum*len(text4Test) < math.MaxUint32+rand.Intn(65536) {
		if _, err := gw.Write(text4Test); err != nil {
			panic(err)
		}
		repeatNum++
	}
	if err := gw.Close(); err != nil {
		panic(err)
	}

	return compressed.Bytes(), repeatNum * len(text4Test)
}

func TestNoCompressedGzip(t *testing.T) {
	gzOut := new(bytes.Buffer)
	gw, err := gzip.NewWriterLevel(gzOut, gzip.NoCompression)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 65535; i++ {
		if _, err = gw.Write(text4Test); err != nil {
			t.Fatal(err)
		}
	}
	if err = gw.Close(); err != nil {
		t.Fatal(err)
	}

	gr, err := NewGzipReader(gzOut)
	defer func() {
		if err = gr.Close(); err != nil {
			t.Fatalf("unable to close: %v", err)
		}
	}()

	buf := make([]byte, len(text4Test))

	readCount := 0
	for {
		if _, err = io.ReadFull(gr, buf); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		if bytes.Compare(text4Test, buf) != 0 {
			t.Fatalf("expected %s, got %s", text4Test, buf)
		}
		readCount++
	}

	assert.Equal(t, 65535, readCount)
}

func TestCorruptTrailer(t *testing.T) {
	t.Run("corrupt-crc", func(t *testing.T) {
		compressed, size := genTestGzipDoc()
		fmt.Println("len(compressed): ", size)

		compressed[len(compressed)-5] += 1

		gr, err := NewGzipReader(bytes.NewReader(compressed))
		if err != nil {
			t.Fatal(err)
		}

		defer func() {
			if err = gr.Close(); err != nil {
				t.Fatal(err)
			}
		}()

		_, err = io.Copy(io.Discard, gr)
		fmt.Println(err)

		if !errors.Is(err, ErrChecksum) {
			t.Fatalf("expected error type of gzip.ErrChecksum, got %v(%T)", err, err)
		}
	})

	t.Run("corrupt-size", func(t *testing.T) {
		compressed, size := genTestGzipDoc()
		fmt.Println("len(compressed): ", size)

		compressed[len(compressed)-1] += 1

		gr, err := NewGzipReader(bytes.NewReader(compressed))
		if err != nil {
			t.Fatal(err)
		}

		defer func() {
			if err = gr.Close(); err != nil {
				t.Fatal(err)
			}
		}()

		_, err = io.Copy(io.Discard, gr)
		fmt.Println(err)

		if !errors.Is(err, ErrCheckSize) {
			t.Fatalf("expected error type of ErrCheckSize, got %v(%T)", err, err)
		}
	})

}

func TestNewGzipReader(t *testing.T) {
	compressed, size := genTestGzipDoc()
	t.Log("len(compressed): ", size)

	gr, err := NewGzipReader(bytes.NewReader(compressed))

	defer func() {
		if err = gr.Close(); err != nil {
			t.Fatalf("unable to close: %v", err)
		}
	}()

	buf := make([]byte, len(text4Test))

	readNum := 0
	for {
		if _, err = io.ReadFull(gr, buf); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		if bytes.Compare(text4Test, buf) != 0 {
			t.Fatalf("the source input is not equal to uncompressed text")
		}
		readNum++
	}
	assert.Equal(t, size/len(text4Test), readNum)
}

func TestCorruptGzipStream(t *testing.T) {
	out := new(bytes.Buffer)
	gw := gzip.NewWriter(out)
	if _, err := gw.Write(text4Test); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	gb := out.Bytes()
	gb[len(gb)-9]++
	gb[len(gb)-10]--

	gr, err := NewGzipReader(out)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	defer func() {
		if err = gr.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}()

	_, err = io.Copy(io.Discard, gr)
	assert.Error(t, err)
}

func TestConcatGzip(t *testing.T) {
	gz1 := generateGzOut(rand.Intn(1<<26) + math.MaxUint16)
	gz2 := generateGzOut(rand.Intn(1<<26) + math.MaxUint16)
	gz3 := generateGzOut(rand.Intn(1<<25) + math.MaxUint16)
	gz4 := generateGzOut(rand.Intn(1<<25) + math.MaxUint16)

	testDir := os.TempDir()

	zlibJoined, err := os.Create(filepath.Join(testDir, "zlib-concat.gz"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer zlibJoined.Close()

	goJoined, err := os.Create(filepath.Join(testDir, "go-concat.gz"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer goJoined.Close()

	start := time.Now()
	if err = ConcatGzip(zlibJoined, bytes.NewReader(gz1),
		bytes.NewReader(gz2), bytes.NewReader(gz3), bytes.NewReader(gz4)); err != nil {
		t.Fatalf("unable to concat gzip: %v", err)
	}
	t.Log("zlib concat cost: ", time.Now().Sub(start))

	start = time.Now()
	if err = concatGzipGo(goJoined, bytes.NewReader(gz1),
		bytes.NewReader(gz2), bytes.NewReader(gz3), bytes.NewReader(gz4)); err != nil {

	}
	t.Log("go concat cost: ", time.Now().Sub(start))

	zStat, err := zlibJoined.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	gStat, err := goJoined.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	t.Logf("zlib merged gzip size: %d, go merged gzip size: %d", zStat.Size(), gStat.Size())

	if _, err = zlibJoined.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	if _, err = goJoined.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}

	goDecompress, err := gzip.NewReader(goJoined)
	if err != nil {
		t.Fatalf("newReader: %v", err)
	}
	zlibDecompress, err := gzip.NewReader(zlibJoined)
	if err != nil {
		t.Fatalf("new reader; %v", err)
	}

	goBuf := make([]byte, 4096)
	zlibBuf := make([]byte, 4096)

	for {

		n1, err1 := io.ReadFull(goDecompress, goBuf)
		n2, err2 := io.ReadFull(zlibDecompress, zlibBuf)

		if bytes.Compare(goBuf[:n1], zlibBuf[:n2]) != 0 {
			t.Fatalf("uncompressed contents are not consistent")
		}

		if err1 != nil || err2 != nil {
			if !errors.Is(err1, err2) {
				t.Fatalf("different error returned, err1: %v, err2: %v", err1, err2)
			}
			break
		}
	}
}

func concatGzipGo(w io.Writer, inputs ...io.Reader) (err error) {
	gw := gzip.NewWriter(w)
	defer gw.Close()

	buf := make([]byte, 16384)

	for _, reader := range inputs {
		if err = func() (err error) {
			gr, err := gzip.NewReader(reader)
			if err != nil {
				return fmt.Errorf("new: %w", err)
			}

			defer gr.Close()

			if _, err = io.CopyBuffer(gw, gr, buf); err != nil {
				return fmt.Errorf("copy: %w", err)
			}
			return nil
		}(); err != nil {
			return err
		}
	}
	return nil
}

func generateGzOut(uncompressedLen int) []byte {
	gz := new(bytes.Buffer)
	gw := gzip.NewWriter(gz)

	total := 0
	randBytes := make([]byte, 6)
	for total < uncompressedLen {
		cnt, _ := crand.Read(randBytes)

		n, err := gw.Write(text4Test)
		if err != nil {
			panic(err)
		}
		total += n
		text4Test = append(text4Test, randBytes[:cnt]...)
	}
	if err := gw.Close(); err != nil {
		panic(err)
	}

	return gz.Bytes()
}

func BenchmarkConcatGzip(b *testing.B) {
	gz1, err := os.Open("./testdata/1.gz")
	if err != nil {
		b.Fatal(err)
	}
	defer gz1.Close()

	gz2, err := os.Open("./testdata/2.gz")
	if err != nil {
		b.Fatal(err)
	}
	defer gz2.Close()

	gz3, err := os.Open("./testdata/3.gz")
	if err != nil {
		b.Fatal(err)
	}
	defer gz3.Close()

	gz4, err := os.Open("./testdata/4.gz")
	if err != nil {
		b.Fatal(err)
	}
	defer gz4.Close()

	gz5, err := os.Open("./testdata/5.gz")
	if err != nil {
		b.Fatal(err)
	}
	defer gz5.Close()

	gz6, err := os.Open("./testdata/6.gz")
	if err != nil {
		b.Fatal(err)
	}
	defer gz6.Close()

	b.Run("concat-standard-go", func(b *testing.B) {

		for i := 0; i < b.N; i++ {
			gz1.Seek(0, io.SeekStart)
			gz2.Seek(0, io.SeekStart)
			gz3.Seek(0, io.SeekStart)
			gz4.Seek(0, io.SeekStart)
			gz5.Seek(0, io.SeekStart)
			gz6.Seek(0, io.SeekStart)

			if err := concatGzipGo(io.Discard, gz1, gz2, gz3, gz4, gz5, gz6); err != nil {
				b.Fatalf("concat: %v", err)
			}
		}

	})

	b.Run("concat-deflatejoin", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			gz1.Seek(0, io.SeekStart)
			gz2.Seek(0, io.SeekStart)
			gz3.Seek(0, io.SeekStart)
			gz4.Seek(0, io.SeekStart)
			gz5.Seek(0, io.SeekStart)
			gz6.Seek(0, io.SeekStart)

			if err := ConcatGzip(io.Discard, gz1, gz2, gz3, gz4, gz5, gz6); err != nil {
				b.Fatalf("concat: %v", err)
			}
		}
	})

}

func TestCGOTest(t *testing.T) {
	CGOTest()
}
