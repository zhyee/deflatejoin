package dfjoin

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/adler32"
	"hash/crc32"
	"io"
	"math/rand"
	"testing"
	"time"
)

type regressionFormat struct {
	name   string
	format streamFormat
}

var regressionFormats = []regressionFormat{{"gzip", gzipFormat}, {"zlib", zlibFormat}}

func (f regressionFormat) pack(t testing.TB, plain []byte, level int) []byte {
	t.Helper()
	var out bytes.Buffer
	var w io.WriteCloser
	var err error
	if f.format == gzipFormat {
		w, err = gzip.NewWriterLevel(&out, level)
	} else {
		w, err = zlib.NewWriterLevel(&out, level)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func (f regressionFormat) wrap(raw, plain []byte) []byte {
	sum := adler32.Checksum(plain)
	if f.format == gzipFormat {
		sum = crc32.ChecksumIEEE(plain)
	}
	data := append(f.format.header(), raw...)
	return append(data, f.format.trailer(sum, uint32(len(plain)))...)
}

func (f regressionFormat) standard(r io.Reader) (io.ReadCloser, error) {
	if f.format == gzipFormat {
		return gzip.NewReader(r)
	}
	return zlib.NewReader(r)
}

func (f regressionFormat) read(data []byte, opts Options) ([]byte, error) {
	r, err := newReader(bytes.NewReader(data), f.format, opts)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func (f regressionFormat) join(opts Options, inputs ...io.Reader) ([]byte, error) {
	var out bytes.Buffer
	err := concat(&out, f.format, opts, inputs)
	return out.Bytes(), err
}

func assertStandard(t testing.TB, f regressionFormat, data, want []byte) {
	t.Helper()
	r, err := f.standard(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("standard decode: got %d bytes want %d, err=%v", len(got), len(want), err)
	}
}

func TestRegressionFinalBlocks(t *testing.T) {
	cases := []struct {
		name       string
		raw, plain []byte
	}{
		{"stored", []byte{1, 3, 0, 252, 255, 'a', 'b', 'c'}, []byte("abc")},
		{"fixed", []byte{0x4b, 0x4c, 0x4a, 0x06, 0}, []byte("abc")},
		{"empty-stored", []byte{1, 0, 0, 255, 255}, nil},
		{"empty-fixed", []byte{3, 0}, nil},
	}
	for _, f := range regressionFormats {
		for _, c := range cases {
			t.Run(f.name+"/"+c.name, func(t *testing.T) {
				data := f.wrap(c.raw, c.plain)
				assertStandard(t, f, data, c.plain)
				got, err := f.read(data, Options{})
				if err != nil || !bytes.Equal(got, c.plain) {
					t.Fatalf("read=%q err=%v", got, err)
				}
				out, err := f.join(Options{}, bytes.NewReader(data), bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				assertStandard(t, f, out, append(append([]byte{}, c.plain...), c.plain...))
			})
		}
	}
}

func TestRegressionIntegrity(t *testing.T) {
	for _, f := range regressionFormats {
		t.Run(f.name, func(t *testing.T) {
			data := f.pack(t, []byte("hello"), flate.DefaultCompression)
			tail := 4
			sumErr := ErrZlibSum
			if f.format == gzipFormat {
				tail = 8
				sumErr = ErrChecksum
			}
			bad := append([]byte{}, data...)
			bad[len(bad)-tail] ^= 0xff
			for _, count := range []int{1, 2} {
				inputs := []io.Reader{bytes.NewReader(bad)}
				if count == 2 {
					inputs = append(inputs, bytes.NewReader(data))
				}
				if _, err := f.join(Options{}, inputs...); !errors.Is(err, sumErr) {
					t.Fatalf("concat %d: %v", count, err)
				}
			}
			if _, err := f.read(bad, Options{}); !errors.Is(err, sumErr) {
				t.Fatal(err)
			}
			// Every prefix is an incomplete stream, including missing whole trailers.
			for cut := 0; cut < len(data); cut++ {
				if _, err := f.read(data[:cut], Options{}); err == nil {
					t.Fatalf("accepted reader prefix %d", cut)
				}
				if _, err := f.join(Options{}, bytes.NewReader(data[:cut])); err == nil {
					t.Fatalf("accepted concat prefix %d", cut)
				}
			}
			if f.format == gzipFormat {
				bad = append([]byte{}, data...)
				bad[len(bad)-1] ^= 1
				if _, err := f.read(bad, Options{}); !errors.Is(err, ErrCheckSize) {
					t.Fatal(err)
				}
				if _, err := f.join(Options{}, bytes.NewReader(bad)); !errors.Is(err, ErrCheckSize) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestRegressionMembersAndHeaders(t *testing.T) {
	f := regressionFormats[0]
	a, b, c := f.pack(t, []byte("A"), -1), f.pack(t, []byte("B"), -1), f.pack(t, []byte("C"), -1)
	ab := append(append([]byte{}, a...), b...)
	out, err := f.join(Options{}, bytes.NewReader(ab), bytes.NewReader(c))
	if err != nil {
		t.Fatal(err)
	}
	assertStandard(t, f, out, []byte("ABC"))
	got, err := f.read(ab, Options{})
	if err != nil || string(got) != "AB" {
		t.Fatalf("%q %v", got, err)
	}
	// Header with extra field, name, comment and a correct FHCRC.
	header := append([]byte{}, a[:10]...)
	header[3] = 4 | 8 | 16 | 2
	header = append(header, 3, 0, 1, 2, 3, 'f', 0, 'c', 0)
	var sum [2]byte
	binary.LittleEndian.PutUint16(sum[:], uint16(crc32.ChecksumIEEE(header)))
	header = append(header, sum[:]...)
	good := append(append([]byte{}, header...), a[10:]...)
	assertStandard(t, f, good, []byte("A"))
	if _, err = f.read(good, Options{}); err != nil {
		t.Fatal(err)
	}
	if out, err = f.join(Options{}, bytes.NewReader(good)); err != nil {
		t.Fatal(err)
	} else {
		assertStandard(t, f, out, []byte("A"))
	}
	good[len(header)-1] ^= 1
	if _, err = f.read(good, Options{}); !errors.Is(err, ErrHeader) {
		t.Fatal(err)
	}
	if _, err = f.join(Options{}, bytes.NewReader(good)); !errors.Is(err, ErrHeader) {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{append(append([]byte{}, a...), 0x1f), append(append([]byte{}, ab...), []byte("junk")...)} {
		if _, err = f.read(bad, Options{}); err == nil {
			t.Fatal("accepted trailing garbage")
		}
		if _, err = f.join(Options{}, bytes.NewReader(bad)); err == nil {
			t.Fatal("accepted trailing garbage")
		}
	}
	z := regressionFormats[1]
	zl := z.pack(t, []byte("x"), -1)
	if _, err = z.join(Options{}, bytes.NewReader(append(zl, 0))); !errors.Is(err, ErrTrailingData) {
		t.Fatal(err)
	}
	dict := []byte{0x78, 0x20, 0, 0, 0, 1}
	if _, err = z.read(dict, Options{}); !errors.Is(err, ErrDictionary) {
		t.Fatal(err)
	}
	if _, err = z.join(Options{}, bytes.NewReader(dict)); !errors.Is(err, ErrDictionary) {
		t.Fatal(err)
	}
}

func TestRegressionReaderLifecycle(t *testing.T) {
	for _, f := range regressionFormats {
		t.Run(f.name, func(t *testing.T) {
			data := f.pack(t, bytes.Repeat([]byte{'x'}, 128), -1)
			r, err := newReader(bytes.NewReader(data), f.format, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if n, err := r.Read(make([]byte, 1)); n != 1 || err != nil {
				t.Fatalf("%d %v", n, err)
			}
			for i := 0; i < 2; i++ {
				if err = r.Close(); err != nil {
					t.Fatal(err)
				}
			}
			for _, p := range [][]byte{nil, make([]byte, 16)} {
				if n, err := r.Read(p); n != 0 || !errors.Is(err, ErrClosed) {
					t.Fatalf("read after close: %d %v", n, err)
				}
			}
			bad := append(f.format.header(), []byte{0, 3, 0, 252, 255, 'a', 'b', 'c', 7}...)
			r, err = newReader(bytes.NewReader(bad), f.format, Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			got, readErr := io.ReadAll(r)
			if string(got) != "abc" || readErr == nil {
				t.Fatalf("partial=%q err=%v", got, readErr)
			}
			if n, err := r.Read(make([]byte, 32)); n != 0 || err != readErr {
				t.Fatalf("unstable error: %d %v", n, err)
			}
			r, err = newReader(bytes.NewReader(data), f.format, Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if _, err = io.Copy(io.Discard, r); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if n, err := r.Read(make([]byte, 32)); n != 0 || err != io.EOF {
					t.Fatalf("unstable EOF: %d %v", n, err)
				}
			}
		})
	}
}

type chunkReader struct {
	r    io.Reader
	size int
}

func (r chunkReader) Read(p []byte) (int, error) {
	if len(p) > r.size {
		p = p[:r.size]
	}
	return r.r.Read(p)
}

type finalErrorReader struct {
	data []byte
	err  error
}

func (r *finalErrorReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		return n, r.err
	}
	return n, nil
}

type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, nil }

func TestRegressionStreaming(t *testing.T) {
	for _, f := range regressionFormats {
		t.Run(f.name, func(t *testing.T) {
			data := f.pack(t, []byte("hello"), -1)
			pr, pw := io.Pipe()
			defer pr.Close()
			defer pw.Close()
			go func() { _, _ = pw.Write(data) }()
			done := make(chan error, 1)
			go func() {
				r, err := newReader(pr, f.format, Options{})
				if err != nil {
					done <- err
					return
				}
				defer r.Close()
				p := make([]byte, 1)
				n, err := r.Read(p)
				if n != 1 || p[0] != 'h' {
					err = fmt.Errorf("read %d bytes", n)
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				pr.Close()
				t.Fatal("waited for source EOF before returning data")
			}
			for _, chunk := range []int{1, 2, 7, 31} {
				out, err := f.join(Options{}, chunkReader{bytes.NewReader(data), chunk}, chunkReader{bytes.NewReader(data), chunk})
				if err != nil {
					t.Fatalf("chunk %d: %v", chunk, err)
				}
				assertStandard(t, f, out, []byte("hellohello"))
			}
			out, err := f.join(Options{}, &finalErrorReader{data: append([]byte{}, data...), err: io.EOF})
			if err != nil {
				t.Fatal(err)
			}
			assertStandard(t, f, out, []byte("hello"))
			sentinel := errors.New("transport failure")
			if _, err = f.join(Options{}, &finalErrorReader{data: data[:len(data)-1], err: sentinel}); !errors.Is(err, sentinel) {
				t.Fatal(err)
			}
			if _, err = newReader(emptyReader{}, f.format, Options{}); !errors.Is(err, io.ErrNoProgress) {
				t.Fatal(err)
			}
		})
	}
}

func TestRegressionLimits(t *testing.T) {
	for _, f := range regressionFormats {
		t.Run(f.name, func(t *testing.T) {
			data := f.pack(t, []byte("hello"), -1)
			if _, err := f.read(data, Options{MaxUncompressedBytes: 5}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.read(data, Options{MaxUncompressedBytes: 4}); !errors.Is(err, ErrLimitExceeded) {
				t.Fatal(err)
			}
			if _, err := f.join(Options{MaxUncompressedBytes: 9}, bytes.NewReader(data), bytes.NewReader(data)); !errors.Is(err, ErrLimitExceeded) {
				t.Fatal(err)
			}
			if _, err := f.join(Options{MaxUncompressedBytes: 10}, bytes.NewReader(data), bytes.NewReader(data)); err != nil {
				t.Fatal(err)
			}
			if _, err := f.read(data, Options{MaxHeaderBytes: 1}); !errors.Is(err, ErrLimitExceeded) {
				t.Fatal(err)
			}
			if _, err := f.read(data, Options{MaxUncompressedBytes: -1}); !errors.Is(err, ErrInvalidOptions) {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := f.read(data, Options{Context: ctx}); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if _, err := f.join(Options{Context: ctx}, bytes.NewReader(data)); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestRegressionNegativeChecksumLengths(t *testing.T) {
	for _, fn := range []func(uint32, uint32, int64) uint32{IEEECrc32Combine, Adler32Combine} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("negative length accepted")
				}
			}()
			fn(0, 0, -1)
		}()
	}
}

func TestRegressionBlockBoundaries(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for _, f := range regressionFormats {
		for _, level := range []int{0, 1, 6, 9, flate.HuffmanOnly} {
			for _, size := range []int{0, 1, 32754, 32758, 32768, 65535, 65536, 100000} {
				t.Run(fmt.Sprintf("%s/%d/%d", f.name, level, size), func(t *testing.T) {
					plain := make([]byte, size)
					rng.Read(plain)
					data := f.pack(t, plain, level)
					got, err := f.read(data, Options{})
					if err != nil || !bytes.Equal(got, plain) {
						t.Fatalf("reader size %d err=%v", len(got), err)
					}
					out, err := f.join(Options{}, chunkReader{bytes.NewReader(data), 257}, bytes.NewReader(data))
					if err != nil {
						t.Fatal(err)
					}
					assertStandard(t, f, out, append(append([]byte{}, plain...), plain...))
				})
			}
		}
	}
}

func TestRegressionIndependentReaders(t *testing.T) {
	for i := 0; i < 8; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			for _, f := range regressionFormats {
				plain := bytes.Repeat([]byte("parallel"), 1000)
				data := f.pack(t, plain, -1)
				got, err := f.read(data, Options{})
				if err != nil || !bytes.Equal(got, plain) {
					t.Fatal(err)
				}
			}
		})
	}
}

func FuzzConcatRoundTrip(f *testing.F) {
	f.Add([]byte("hello"), uint8(6), uint16(1))
	f.Add([]byte{}, uint8(0), uint16(257))
	f.Add(bytes.Repeat([]byte("abc"), 1000), uint8(9), uint16(31))
	f.Fuzz(func(t *testing.T, plain []byte, level uint8, chunk uint16) {
		if len(plain) > 65536 {
			t.Skip()
		}
		for _, format := range regressionFormats {
			data := format.pack(t, plain, int(level)%10)
			out, err := format.join(Options{MaxUncompressedBytes: 131072}, chunkReader{bytes.NewReader(data), int(chunk) + 1}, bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			assertStandard(t, format, out, append(append([]byte{}, plain...), plain...))
		}
	})
}

func FuzzRawDeflate(f *testing.F) {
	f.Add([]byte{3, 0})
	f.Add([]byte{1, 0, 0, 255, 255})
	f.Add([]byte{1, 3, 0, 252, 255, 'a', 'b', 'c'})
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 65536 {
			t.Skip()
		}
		for _, format := range regressionFormats {
			data := format.wrap(raw, nil)
			opts := Options{MaxUncompressedBytes: 1 << 20, MaxHeaderBytes: 65536}
			_, _ = format.read(data, opts)
			out, err := format.join(opts, chunkReader{bytes.NewReader(data), 17})
			if err == nil {
				r, ex := format.standard(bytes.NewReader(data))
				if ex != nil {
					t.Fatal(ex)
				}
				want, ex := io.ReadAll(io.LimitReader(r, (1<<20)+1))
				r.Close()
				if ex != nil {
					t.Fatal(ex)
				}
				assertStandard(t, format, out, want)
			}
		}
	})
}

type regressionWriter struct {
	err   error
	short bool
}

func (w regressionWriter) Write(p []byte) (int, error) {
	if w.short {
		return len(p) - 1, nil
	}
	return 0, w.err
}

func TestRegressionWriterErrors(t *testing.T) {
	sentinel := errors.New("write failure")
	plain := make([]byte, 100000)
	rand.New(rand.NewSource(7)).Read(plain)
	for _, f := range regressionFormats {
		for _, size := range []int{8, len(plain)} {
			data := f.pack(t, plain[:size], 0)
			for _, w := range []regressionWriter{{err: sentinel}, {short: true}} {
				want := sentinel
				if w.short {
					want = io.ErrShortWrite
				}
				if err := concat(w, f.format, Options{}, []io.Reader{bytes.NewReader(data)}); !errors.Is(err, want) {
					t.Fatalf("%s size %d: %v", f.name, size, err)
				}
			}
		}
	}
}

func TestRegressionCancellationAndMemberLimits(t *testing.T) {
	for _, f := range regressionFormats {
		data := f.pack(t, []byte("hello"), -1)
		ctx, cancel := context.WithCancel(context.Background())
		r, err := newReader(bytes.NewReader(data), f.format, Options{Context: ctx})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		if _, err = r.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		r.Close()
	}
	f := regressionFormats[0]
	member := f.pack(t, []byte("hello"), -1)
	data := append(append([]byte{}, member...), member...)
	if _, err := f.read(data, Options{MaxUncompressedBytes: 9}); !errors.Is(err, ErrLimitExceeded) {
		t.Fatal(err)
	}
	if _, err := f.join(Options{MaxUncompressedBytes: 9}, bytes.NewReader(data)); !errors.Is(err, ErrLimitExceeded) {
		t.Fatal(err)
	}
}

func TestRegressionPartialDataAndTransportError(t *testing.T) {
	sentinel := errors.New("truncated transport")
	for _, f := range regressionFormats {
		data := append(f.format.header(), []byte{0, 3, 0, 252, 255, 'a', 'b', 'c'}...)
		r, err := newReader(&finalErrorReader{data: data, err: sentinel}, f.format, Options{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		r.Close()
		if string(got) != "abc" || !errors.Is(err, sentinel) {
			t.Fatalf("%s: %q %v", f.name, got, err)
		}
	}
}
