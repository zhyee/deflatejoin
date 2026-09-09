package dfjoin

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"testing"
)

func TestCorrectnessTerminalState(t *testing.T) {
	for _, f := range regressionFormats {
		for _, bad := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/corrupt=%v", f.name, bad), func(t *testing.T) {
				data := f.pack(t, []byte("abc"), -1)
				if bad {
					offset := 4
					if f.format == gzipFormat {
						offset = 8
					}
					data[len(data)-offset] ^= 1
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				r, err := newReader(bytes.NewReader(data), f.format, Options{Context: ctx})
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				_, err = io.ReadAll(r)
				want := err
				if !bad {
					if err != nil {
						t.Fatal(err)
					}
					want = io.EOF
				} else if err == nil {
					t.Fatal("checksum failure missing")
				}
				cancel()
				for i := 0; i < 2; i++ {
					if n, err := r.Read(make([]byte, 1)); n != 0 || err != want {
						t.Fatalf("terminal error changed: %d %v, want %v", n, err, want)
					}
				}
			})
		}
	}
}

func TestCorrectnessSmallWindowDynamicBlock(t *testing.T) {
	// One dynamic-Huffman block encoding abc repeated 10,000 times, with
	// distance-three matches and a valid 256-byte-window zlib header/trailer.
	data, err := hex.DecodeString("081dedc2411100000c02a0ac6aff0e2bb1271ce9a2aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaafaf1003d0fdef5")
	if err != nil {
		t.Fatal(err)
	}
	plain := bytes.Repeat([]byte("abc"), 10000)
	f := regressionFormats[1]
	assertStandard(t, f, data, plain)
	opts := Options{MaxUncompressedBytes: int64(len(plain)), MaxCompressedBytes: int64(len(data)), MaxMembers: 1}
	got, err := f.read(data, opts)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("dynamic small-window stream: %v", err)
	}
	out, err := f.join(opts, chunkReader{bytes.NewReader(data), 1})
	if err != nil {
		t.Fatal(err)
	}
	assertStandard(t, f, out, plain)
}

func TestCorrectnessPendingErrorBeforeCancellation(t *testing.T) {
	for _, f := range regressionFormats {
		// Valid stored block followed by reserved BTYPE. One inflate call produces
		// abc and reports the error, while the caller initially requests only a.
		data := append(f.format.header(), []byte{0, 3, 0, 252, 255, 'a', 'b', 'c', 7}...)
		ctx, cancel := context.WithCancel(context.Background())
		r, err := newReader(bytes.NewReader(data), f.format, Options{Context: ctx})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		p := make([]byte, 1)
		n, err := r.Read(p)
		if n != 1 || err != nil || p[0] != 'a' {
			cancel()
			r.Close()
			t.Fatalf("first read: %q %v", p, err)
		}
		terminal := r.(*streamReader).err
		if terminal == nil {
			cancel()
			r.Close()
			t.Fatal("expected pending decode error")
		}
		cancel()
		got, err := io.ReadAll(r)
		r.Close()
		if string(got) != "bc" || err != terminal {
			t.Fatalf("pending data/error lost: %q %v", got, err)
		}
	}
}

func TestCorrectnessCancelBufferedData(t *testing.T) {
	for _, f := range regressionFormats {
		ctx, cancel := context.WithCancel(context.Background())
		r, err := newReader(bytes.NewReader(f.pack(t, bytes.Repeat([]byte{'x'}, 100), -1)), f.format, Options{Context: ctx})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if _, err = r.Read(make([]byte, 1)); err != nil {
			cancel()
			r.Close()
			t.Fatal(err)
		}
		cancel()
		for i := 0; i < 2; i++ {
			if n, err := r.Read(make([]byte, 10)); n != 0 || !errors.Is(err, context.Canceled) {
				r.Close()
				t.Fatalf("canceled reader delivered bytes: %d %v", n, err)
			}
		}
		r.Close()
	}
}

type countedSource struct {
	io.Reader
	bytes int
}

func (r *countedSource) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytes += n
	return n, err
}

func TestCorrectnessCompressedLimit(t *testing.T) {
	for _, f := range regressionFormats {
		t.Run(f.name, func(t *testing.T) {
			data := f.pack(t, []byte("payload"), -1)
			for _, chunk := range []int{1, 7, BufSize} {
				for _, limit := range []int{1, len(data) - 1, len(data), len(data) + 1} {
					source := &countedSource{Reader: chunkReader{bytes.NewReader(data), chunk}}
					r, err := newReader(source, f.format, Options{MaxCompressedBytes: int64(limit)})
					if err == nil {
						_, err = io.Copy(io.Discard, r)
						r.Close()
					}
					if limit < len(data) {
						if !errors.Is(err, ErrLimitExceeded) {
							t.Fatalf("chunk=%d limit=%d: %v", chunk, limit, err)
						}
					} else if err != nil {
						t.Fatal(err)
					}
					if source.bytes > limit+1 {
						t.Fatalf("read %d source bytes with limit %d", source.bytes, limit)
					}
					source = &countedSource{Reader: chunkReader{bytes.NewReader(data), chunk}}
					out, err := f.join(Options{MaxCompressedBytes: int64(limit)}, source)
					if limit < len(data) {
						if !errors.Is(err, ErrLimitExceeded) {
							t.Fatalf("concat limit=%d: %v", limit, err)
						}
					} else {
						if err != nil {
							t.Fatal(err)
						}
						assertStandard(t, f, out, []byte("payload"))
					}
					if source.bytes > limit+1 {
						t.Fatalf("concat read %d bytes with limit %d", source.bytes, limit)
					}
				}
			}
			// Preserve a terminal error returned together with the final source bytes.
			sentinel := errors.New("transport error")
			for _, endErr := range []error{io.EOF, sentinel} {
				_, err := f.join(Options{MaxCompressedBytes: int64(len(data))}, &finalErrorReader{data: append([]byte{}, data...), err: endErr})
				if endErr == io.EOF {
					if err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, sentinel) {
					t.Fatal(err)
				}
			}
			for _, limit := range []int{len(data)*2 - 1, len(data) * 2} {
				a := &countedSource{Reader: bytes.NewReader(data)}
				b := &countedSource{Reader: bytes.NewReader(data)}
				_, err := f.join(Options{MaxCompressedBytes: int64(limit)}, a, b)
				if limit < len(data)*2 {
					if !errors.Is(err, ErrLimitExceeded) {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if a.bytes+b.bytes > limit+1 {
					t.Fatal("compressed limit reset across inputs")
				}
			}
		})
	}
	f := regressionFormats[0]
	member := f.pack(t, nil, -1)
	data := bytes.Repeat(member, 3)
	for _, limit := range []int{len(data) - 1, len(data)} {
		_, err := f.read(data, Options{MaxCompressedBytes: int64(limit)})
		if limit < len(data) {
			if !errors.Is(err, ErrLimitExceeded) {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

func TestCorrectnessMemberLimit(t *testing.T) {
	f := regressionFormats[0]
	empty := f.pack(t, nil, -1)
	for _, count := range []int{1, 2, 10000} {
		data := bytes.Repeat(empty, count)
		_, err := f.read(data, Options{MaxMembers: 1, MaxUncompressedBytes: 1, MaxHeaderBytes: 10})
		if count > 1 {
			if !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("members=%d: %v", count, err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		_, err = f.join(Options{MaxMembers: 1}, bytes.NewReader(data))
		if count > 1 {
			if !errors.Is(err, ErrLimitExceeded) {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range regressionFormats {
		data := f.pack(t, nil, -1)
		for _, limit := range []int64{1, 2} {
			_, err := f.join(Options{MaxMembers: limit}, bytes.NewReader(data), bytes.NewReader(data))
			if limit == 1 {
				if !errors.Is(err, ErrLimitExceeded) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, opts := range []Options{{MaxMembers: -1}, {MaxCompressedBytes: -1}} {
		if _, err := newBudget(opts); !errors.Is(err, ErrInvalidOptions) {
			t.Fatal(err)
		}
	}
}

func TestCorrectnessInflateLimitProbe(t *testing.T) {
	for _, f := range regressionFormats {
		for _, limit := range []int{1, 7, BufSize, BufSize + 1} {
			for _, extra := range []int{0, 100000} {
				data := f.pack(t, bytes.Repeat([]byte{'x'}, limit+extra), -1)
				r, err := newReader(bytes.NewReader(data), f.format, Options{MaxUncompressedBytes: int64(limit)})
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(r)
				decoded := uint64(r.(*streamReader).stream.total_out)
				r.Close()
				if extra == 0 {
					if err != nil || len(got) != limit {
						t.Fatalf("exact limit: %d %v", len(got), err)
					}
				} else if !errors.Is(err, ErrLimitExceeded) {
					t.Fatal(err)
				}
				if len(got) > limit || decoded > uint64(limit+1) {
					t.Fatalf("limit %d: returned %d, actually inflated %d", limit, len(got), decoded)
				}
				_, err = f.join(Options{MaxUncompressedBytes: int64(limit)}, bytes.NewReader(data))
				if extra == 0 {
					if err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, ErrLimitExceeded) {
					t.Fatal(err)
				}
			}
		}
	}
}

// Build exact fixed-Huffman distance fixtures, independent of a compressor's
// choice of matches. Huffman codes are reversed into deflate's bit packing.
type fixtureBits struct {
	data  []byte
	count uint
}

func (w *fixtureBits) put(value uint, n uint) {
	for i := uint(0); i < n; i++ {
		if w.count%8 == 0 {
			w.data = append(w.data, 0)
		}
		w.data[w.count/8] |= byte((value>>i)&1) << (w.count % 8)
		w.count++
	}
}
func (w *fixtureBits) symbol(s uint) {
	var code, n uint
	switch {
	case s < 144:
		code, n = 48+s, 8
	case s < 256:
		code, n = 0x190+s-144, 9
	case s < 280:
		code, n = s-256, 7
	default:
		code, n = 0xc0+s-280, 8
	}
	w.put(uint(bits.Reverse16(uint16(code)))>>(16-n), n)
}
func windowFixture(windowBits, distance int, stored bool) ([]byte, []byte) {
	plain := make([]byte, distance)
	for i := range plain {
		plain[i] = byte(i % 251)
	}
	var raw []byte
	w := fixtureBits{}
	w.put(3, 3)
	if stored {
		n := len(plain)
		raw = append(raw, 0, byte(n), byte(n>>8), ^byte(n), ^byte(n>>8))
		raw = append(raw, plain...)
	} else {
		for _, b := range plain {
			w.symbol(uint(b))
		}
	}
	w.symbol(257) // match length three
	bases := []int{1, 2, 3, 4, 5, 7, 9, 13, 17, 25, 33, 49, 65, 97, 129, 193, 257, 385, 513, 769, 1025, 1537, 2049, 3073, 4097, 6145, 8193, 12289, 16385, 24577}
	extra := []uint{0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7, 8, 8, 9, 9, 10, 10, 11, 11, 12, 12, 13, 13}
	for code, base := range bases {
		if distance <= base+(1<<extra[code])-1 {
			w.put(uint(bits.Reverse8(uint8(code)))>>3, 5)
			w.put(uint(distance-base), extra[code])
			break
		}
	}
	w.symbol(256)
	raw = append(raw, w.data...)
	plain = append(plain, plain[:3]...)
	data := regressionFormats[1].wrap(raw, plain)
	data[0] = byte((windowBits-8)<<4) | 8
	data[1] = byte((31 - (int(data[0])<<8)%31) % 31)
	return data, plain
}

func TestCorrectnessDeclaredWindow(t *testing.T) {
	f := regressionFormats[1]
	for window := 8; window <= 15; window++ {
		for _, stored := range []bool{false, true} {
			for _, excess := range []int{0, 1} {
				if window == 15 && excess == 1 {
					continue
				}
				t.Run(fmt.Sprintf("window=%d/stored=%v/excess=%d", window, stored, excess), func(t *testing.T) {
					data, plain := windowFixture(window, (1<<window)+excess, stored)
					// The larger default history decodes the fixture and verifies its trailer.
					assertStandard(t, f, data, plain)
					for _, chunk := range []int{1, 31, BufSize} {
						r, err := newReader(chunkReader{bytes.NewReader(data), chunk}, zlibFormat, Options{})
						if err != nil {
							t.Fatal(err)
						}
						got, err := io.ReadAll(r)
						r.Close()
						if excess == 0 {
							if err != nil || !bytes.Equal(got, plain) {
								t.Fatalf("reader chunk %d: %v", chunk, err)
							}
						} else if err == nil {
							t.Fatalf("reader accepted distance beyond declared window, chunk=%d", chunk)
						}
						out, err := f.join(Options{}, chunkReader{bytes.NewReader(data), chunk})
						if excess == 0 {
							if err != nil {
								t.Fatalf("concat chunk %d: %v", chunk, err)
							}
							assertStandard(t, f, out, plain)
						} else if err == nil {
							t.Fatalf("concat accepted distance beyond declared window, chunk=%d", chunk)
						}
					}
				})
			}
		}
	}
	// Reuse one native stream across different window sizes.
	a, pa := windowFixture(8, 256, true)
	b, pb := windowFixture(15, 32768, false)
	for _, reverse := range []bool{false, true} {
		if reverse {
			a, b = b, a
			pa, pb = pb, pa
		}
		out, err := f.join(Options{}, bytes.NewReader(a), bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		assertStandard(t, f, out, append(append([]byte{}, pa...), pb...))
	}
}

func TestCorrectnessCompressedLimitAcrossBuffers(t *testing.T) {
	for _, f := range regressionFormats {
		data := f.pack(t, bytes.Repeat([]byte{'x'}, 3*BufSize+11), 0)
		for _, chunk := range []int{257, BufSize} {
			for _, limit := range []int{BufSize - 1, BufSize, BufSize + 1, len(data) - 1, len(data)} {
				for _, joining := range []bool{false, true} {
					src := &countedSource{Reader: chunkReader{bytes.NewReader(data), chunk}}
					var err error
					if joining {
						_, err = f.join(Options{MaxCompressedBytes: int64(limit)}, src)
					} else {
						var r io.ReadCloser
						r, err = newReader(src, f.format, Options{MaxCompressedBytes: int64(limit)})
						if err == nil {
							_, err = io.Copy(io.Discard, r)
							r.Close()
						}
					}
					if limit < len(data) {
						if !errors.Is(err, ErrLimitExceeded) {
							t.Fatalf("%s limit %d joining %v: %v", f.name, limit, joining, err)
						}
					} else if err != nil {
						t.Fatal(err)
					}
					if src.bytes > limit+1 {
						t.Fatalf("source read %d bytes for limit %d", src.bytes, limit)
					}
				}
			}
		}
	}
}

func FuzzDeclaredWindow(f *testing.F) {
	f.Add(uint8(0), uint16(256), true, uint8(7))
	f.Add(uint8(0), uint16(257), false, uint8(0))
	f.Add(uint8(7), uint16(32768), false, uint8(255))
	f.Fuzz(func(t *testing.T, window uint8, distance uint16, stored bool, chunk uint8) {
		if distance < 3 || distance > 32768 {
			t.Skip()
		}
		w := 8 + int(window%8)
		data, plain := windowFixture(w, int(distance), stored)
		format := regressionFormats[1]
		opts := Options{MaxCompressedBytes: 1 << 17, MaxUncompressedBytes: 1 << 16, MaxMembers: 1}
		r, err := newReader(chunkReader{bytes.NewReader(data), int(chunk) + 1}, zlibFormat, opts)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		r.Close()
		valid := int(distance) <= 1<<w
		if valid {
			if err != nil || !bytes.Equal(got, plain) {
				t.Fatalf("reader: %v", err)
			}
		} else if err == nil {
			t.Fatal("reader accepted excessive distance")
		}
		out, err := format.join(opts, chunkReader{bytes.NewReader(data), int(chunk) + 1})
		if valid {
			if err != nil {
				t.Fatal(err)
			}
			assertStandard(t, format, out, plain)
		} else if err == nil {
			t.Fatal("concat accepted excessive distance")
		}
	})
}

func TestCorrectnessExactLimitsWithEmptyMembers(t *testing.T) {
	f := regressionFormats[0]
	payload := f.pack(t, []byte("abc"), -1)
	empty := f.pack(t, nil, -1)
	data := append(append([]byte{}, payload...), empty...)
	opts := Options{MaxUncompressedBytes: 3, MaxCompressedBytes: int64(len(data)), MaxMembers: 2}
	got, err := f.read(data, opts)
	if err != nil || string(got) != "abc" {
		t.Fatalf("exact limits with empty member: %q %v", got, err)
	}
	out, err := f.join(opts, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	assertStandard(t, f, out, []byte("abc"))
}
