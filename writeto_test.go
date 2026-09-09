package dfjoin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"testing"
)

type writeToFunc func([]byte) (int, error)

func (w writeToFunc) Write(p []byte) (int, error) { return w(p) }

func writerToReader(t testing.TB, f regressionFormat, data []byte, opts Options) *streamReader {
	t.Helper()
	var r io.ReadCloser
	var err error
	if f.format == gzipFormat {
		r, err = NewGzipReaderWithOptions(bytes.NewReader(data), opts)
	} else {
		r, err = NewZlibReaderWithOptions(bytes.NewReader(data), opts)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	if _, ok := r.(io.WriterTo); !ok {
		t.Fatal("reader does not implement io.WriterTo")
	}
	return r.(*streamReader)
}

type directBufferWriter struct {
	t *testing.T
	r *streamReader
	bytes.Buffer
	calls int
}

func (w *directBufferWriter) Write(p []byte) (int, error) {
	w.calls++
	if len(p) == 0 || len(p) != w.r.available-w.r.offset || &p[0] != &w.r.output[w.r.offset] {
		w.t.Fatal("Write did not receive the unread native output buffer")
	}
	return w.Buffer.Write(p)
}

func (w *directBufferWriter) ReadFrom(io.Reader) (int64, error) {
	w.t.Fatal("io.Copy selected destination ReaderFrom instead of source WriterTo")
	return 0, nil
}

func TestReaderWriteToDirectBuffer(t *testing.T) {
	for _, f := range regressionFormats {
		for _, size := range []int{0, 1, BufSize - 1, BufSize, BufSize + 1, 3*BufSize + 17} {
			for _, prefix := range []int{0, 1} {
				if prefix > size {
					continue
				}
				t.Run(fmt.Sprintf("%s/size=%d/prefix=%d", f.name, size, prefix), func(t *testing.T) {
					plain := bytes.Repeat([]byte("abc"), size/3+1)[:size]
					r := writerToReader(t, f, f.pack(t, plain, -1), Options{})
					p := make([]byte, prefix)
					if _, err := io.ReadFull(r, p); err != nil || !bytes.Equal(p, plain[:prefix]) {
						t.Fatalf("prefix read: %q %v", p, err)
					}
					w := &directBufferWriter{t: t, r: r}
					// Even a one-byte CopyBuffer must be bypassed by WriterTo.
					n, err := io.CopyBuffer(w, r, make([]byte, 1))
					if err != nil || n != int64(size-prefix) || !bytes.Equal(w.Bytes(), plain[prefix:]) {
						t.Fatalf("copy: %d bytes, %v", n, err)
					}
					if size > prefix && w.calls > (size+BufSize-1)/BufSize {
						t.Fatalf("small writes: %d calls for %d bytes", w.calls, size)
					}
					if n, err = r.WriteTo(w); n != 0 || err != nil {
						t.Fatalf("repeated WriteTo: %d %v", n, err)
					}
					if n, err := r.Read(make([]byte, 1)); n != 0 || err != io.EOF {
						t.Fatalf("Read after WriteTo: %d %v", n, err)
					}
					if err := r.Close(); err != nil {
						t.Fatal(err)
					}
					if n, err = r.WriteTo(w); n != 0 || err != ErrClosed {
						t.Fatalf("WriteTo after Close: %d %v", n, err)
					}
				})
			}
		}
	}
}

func TestReaderWriteToWriterErrors(t *testing.T) {
	sentinel := errors.New("destination failure")
	for _, f := range regressionFormats {
		plain := bytes.Repeat([]byte("abcdefgh"), 3*BufSize/8+17)
		data := f.pack(t, plain, -1)
		for _, tc := range []struct {
			name string
			n    int // -2 means a full write; -3 means larger than the buffer.
			err  error
		}{
			{"zero", 0, nil}, {"short", 7, nil},
			{"error", 0, sentinel}, {"partial-error", 7, sentinel},
			{"full-error", -2, sentinel}, {"writer-eof", 7, io.EOF},
			{"negative", -1, nil}, {"too-large", -3, nil},
			{"invalid-with-error", -3, sentinel},
		} {
			t.Run(f.name+"/"+tc.name, func(t *testing.T) {
				r := writerToReader(t, f, data, Options{})
				var got bytes.Buffer
				calls := 0
				n, err := r.WriteTo(writeToFunc(func(p []byte) (int, error) {
					calls++
					if calls == 1 {
						return got.Write(p)
					}
					if calls > 2 {
						t.Fatal("WriteTo retried a failed write")
					}
					n := tc.n
					if n == -2 {
						n = len(p)
					} else if n == -3 {
						n = len(p) + 1
					}
					if n >= 0 && n <= len(p) {
						got.Write(p[:n])
					}
					return n, tc.err
				}))
				if err == nil || n != int64(got.Len()) || calls != 2 {
					t.Fatalf("failed write: count=%d accepted=%d calls=%d err=%v", n, got.Len(), calls, err)
				}
				if tc.err != nil && err != tc.err {
					t.Fatalf("writer error replaced: %v, want %v", err, tc.err)
				}
				if tc.err == nil && tc.n >= 0 && err != io.ErrShortWrite {
					t.Fatalf("short write: %v", err)
				}
				// Both APIs must resume exactly after the bytes accepted by the writer.
				p := make([]byte, 13)
				if _, err := io.ReadFull(r, p); err != nil {
					t.Fatal(err)
				}
				got.Write(p)
				remaining := len(plain) - got.Len()
				n, err = io.Copy(&got, r)
				if err != nil || n != int64(remaining) || !bytes.Equal(got.Bytes(), plain) {
					t.Fatalf("resume lost or repeated data: %d %v", n, err)
				}
			})
		}
	}
}

func TestReaderWriteToValidation(t *testing.T) {
	for _, f := range regressionFormats {
		plain := bytes.Repeat([]byte("payload"), 10000)
		data := f.pack(t, plain, -1)
		badSum := append([]byte(nil), data...)
		trailerSize := 4
		if f.format == gzipFormat {
			trailerSize = 8
		}
		badSum[len(badSum)-trailerSize] ^= 1
		type validationCase struct {
			name string
			data []byte
			opts Options
		}
		cases := []validationCase{
			{"checksum", badSum, Options{}},
			{"truncated-trailer", data[:len(data)-1], Options{}},
			{"truncated-body", data[:len(data)-trailerSize-2], Options{}},
			{"decode-error", append(f.format.header(), 0, 3, 0, 252, 255, 'a', 'b', 'c', 7), Options{}},
			{"compressed-limit", data, Options{MaxCompressedBytes: int64(len(data) - 1)}},
			{"uncompressed-limit", data, Options{MaxUncompressedBytes: BufSize + 7}},
			{"exact-limits", data, Options{MaxCompressedBytes: int64(len(data)), MaxUncompressedBytes: int64(len(plain)), MaxMembers: 1}},
		}
		if f.format == gzipFormat {
			members := append(append(append([]byte(nil), data...), f.pack(t, nil, -1)...), data...)
			cases = append(cases,
				validationCase{"members", members, Options{}},
				validationCase{"member-limit", members, Options{MaxMembers: 2}})
		} else {
			smallWindow, _ := windowFixture(8, 256, false)
			cases = append(cases, validationCase{"small-window", smallWindow, Options{}})
		}
		for _, tc := range cases {
			t.Run(f.name+"/"+tc.name, func(t *testing.T) {
				want, wantErr := f.read(tc.data, tc.opts)
				r := writerToReader(t, f, tc.data, tc.opts)
				var got bytes.Buffer
				n, err := io.Copy(&got, r)
				if n != int64(len(want)) || !bytes.Equal(got.Bytes(), want) || fmt.Sprint(err) != fmt.Sprint(wantErr) {
					t.Fatalf("WriteTo=(%d,%v), Read=(%d,%v)", n, err, len(want), wantErr)
				}
				if n, again := r.WriteTo(io.Discard); n != 0 || again != err {
					t.Fatalf("unstable terminal state: %d %v, want %v", n, again, err)
				}
			})
		}
	}
}

func TestReaderWriteToCancellation(t *testing.T) {
	for _, f := range regressionFormats {
		for _, savedError := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/saved-error=%v", f.name, savedError), func(t *testing.T) {
				data := f.pack(t, []byte("abc"), -1)
				if savedError {
					data = append(f.format.header(), 0, 3, 0, 252, 255, 'a', 'b', 'c', 7)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				r := writerToReader(t, f, data, Options{Context: ctx})
				if n, err := r.Read(make([]byte, 1)); n != 1 || err != nil {
					t.Fatalf("first Read: %d %v", n, err)
				}
				terminal := r.err
				cancel()
				var got bytes.Buffer
				if savedError {
					writeErr := errors.New("destination failure before decode error")
					n, err := r.WriteTo(writeToFunc(func(p []byte) (int, error) {
						got.Write(p[:1])
						return 1, writeErr
					}))
					if n != 1 || err != writeErr {
						t.Fatalf("writer error lost: %d %v", n, err)
					}
				}
				n, err := r.WriteTo(&got)
				if savedError {
					if terminal == nil || n != 1 || got.String() != "bc" || err != terminal {
						t.Fatalf("pending data/error lost: %d %q %v", n, got.String(), err)
					}
				} else if n != 0 || got.Len() != 0 || err != context.Canceled {
					t.Fatalf("canceled buffered data delivered: %d %q %v", n, got.String(), err)
				}
				if n, again := r.WriteTo(io.Discard); n != 0 || again != err {
					t.Fatalf("unstable cancellation: %d %v", n, again)
				}
			})
		}
		t.Run(f.name+"/cancel-during-write", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := writerToReader(t, f, f.pack(t, bytes.Repeat([]byte{'x'}, 3*BufSize), -1), Options{Context: ctx})
			calls := 0
			n, err := r.WriteTo(writeToFunc(func(p []byte) (int, error) {
				calls++
				cancel()
				return len(p), nil
			}))
			if n != BufSize || err != context.Canceled || calls != 1 {
				t.Fatalf("cancellation between writes: %d %v calls=%d", n, err, calls)
			}
		})
		for _, corrupt := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancel-after-terminal/corrupt=%v", f.name, corrupt), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				data := f.pack(t, []byte("abc"), -1)
				if corrupt {
					data[len(data)-1] ^= 1
				}
				r := writerToReader(t, f, data, Options{Context: ctx})
				n, terminal := r.WriteTo(io.Discard)
				if n != 3 || (terminal != nil) != corrupt {
					t.Fatalf("terminal state: %d %v", n, terminal)
				}
				cancel()
				if n, err := r.WriteTo(io.Discard); n != 0 || err != terminal {
					t.Fatalf("terminal error overwritten: %d %v, want %v", n, err, terminal)
				}
			})
		}
	}
}

func TestReaderWriteToStreaming(t *testing.T) {
	sentinel := errors.New("transport failure")
	for _, f := range regressionFormats {
		plain := []byte("abc")
		data := f.pack(t, plain, -1)
		for _, tc := range []struct {
			name    string
			source  io.Reader
			wantErr error
		}{
			{"one-byte-reads", chunkReader{bytes.NewReader(data), 1}, nil},
			{"data-with-eof", &finalErrorReader{data: data, err: io.EOF}, nil},
			{"data-with-error", &finalErrorReader{data: append(f.format.header(), 0, 3, 0, 252, 255, 'a', 'b', 'c'), err: sentinel}, sentinel},
		} {
			t.Run(f.name+"/"+tc.name, func(t *testing.T) {
				r, err := newReader(tc.source, f.format, Options{})
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				var got bytes.Buffer
				n, err := io.Copy(&got, r)
				if n != int64(len(plain)) || !bytes.Equal(got.Bytes(), plain) || !errors.Is(err, tc.wantErr) {
					t.Fatalf("streaming write: %d %q %v, want %v", n, got.String(), err, tc.wantErr)
				}
			})
		}
	}
}

type writeToReaderOnly struct{ io.Reader }

type writeToDiscard struct{ calls int64 }

func (w *writeToDiscard) Write(p []byte) (int, error) {
	w.calls++
	return len(p), nil
}

func BenchmarkReaderCopy(b *testing.B) {
	plain := make([]byte, 1<<20)
	rand.New(rand.NewSource(7)).Read(plain)
	for _, f := range regressionFormats {
		for _, entropy := range []bool{false, true} {
			payload := plain
			if !entropy {
				payload = bytes.Repeat([]byte("abcdefgh"), len(plain)/8)
			}
			data := f.pack(b, payload, -1)
			for _, mode := range []struct {
				name   string
				buffer int
			}{
				{"WriteTo", 0}, {"Read8KiB", 8 << 10}, {"Read32KiB", 32 << 10},
			} {
				b.Run(fmt.Sprintf("%s/entropy=%v/%s", f.name, entropy, mode.name), func(b *testing.B) {
					buf := make([]byte, mode.buffer)
					w := &writeToDiscard{}
					b.SetBytes(int64(len(payload)))
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						r, err := newReader(bytes.NewReader(data), f.format, Options{})
						if err != nil {
							b.Fatal(err)
						}
						var n int64
						if mode.buffer == 0 {
							n, err = io.Copy(w, r)
						} else {
							n, err = io.CopyBuffer(w, writeToReaderOnly{r}, buf)
						}
						closeErr := r.Close()
						if err != nil || closeErr != nil || n != int64(len(payload)) {
							b.Fatalf("copy: %d %v, close: %v", n, err, closeErr)
						}
					}
					b.ReportMetric(float64(w.calls)/float64(b.N), "writes/op")
				})
			}
		}
	}
}
