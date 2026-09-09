# deflatejoin
A go package used to more efficiently concat(join) multi gzip/zlib files, 
which benefits from cgo and zlib.
It's a golang port(wrapper) of Mark Adler's [gzjoin.c](https://github.com/madler/zlib/blob/develop/examples/gzjoin.c).
Compared to decompressing all files
and then Immediately compressing them again
by using go builtin gzip package, it only decompresses all files once and with no any compressions.

## Prerequisites
- GCC/Clang/MinGW

## Install

```shell
go get github.com/zhyee/deflatejoin
```

## Example

The main apis are pretty simple:

`func ConcatGzip(w io.Writer, inputs ...io.Reader) error`

`func ConcatZlib(w io.Writer, inputs ...io.Reader) error`

```go
package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"log"
	"os"
	"strings"

	"github.com/zhyee/deflatejoin"
)

func gzCompress(s string) io.Reader {
	out := new(bytes.Buffer)
	gw := gzip.NewWriter(out)
	if _, err := io.WriteString(gw, s); err != nil {
		panic(err)
	}

	if err := gw.Close(); err != nil {
		panic(err)
	}
	return out
}

func main() {
	gz1 := gzCompress(strings.Repeat("hello world\n", 10))
	gz2 := gzCompress(strings.Repeat("hello deflate\n", 10))
	gz3 := gzCompress(strings.Repeat("hello gzip\n", 10))

	joined := new(bytes.Buffer)
	// for zlib files concatenating you can use dfjoin.ConcatZlib instead.
	if err := dfjoin.ConcatGzip(joined, gz1, gz2, gz3); err != nil {
		log.Fatalf("unable to concat gzip files: %v", err)
	}

	gr, err := gzip.NewReader(joined)
	if err != nil {
		log.Fatalf("unable to decompress: %v", err)
	}
	defer gr.Close()

	if _, err = io.Copy(os.Stdout, gr); err != nil {
		log.Fatalf("decompress: %v", err)
	}
}
```

## Validation and streaming behavior

- All inputs are validated, including calls with only one input. gzip header
  CRC, data CRC and ISIZE, and zlib Adler-32 are checked. Truncated streams fail.
- `ConcatGzip` includes every member of every input. It produces one gzip member.
  `NewGzipReader` reads all members, like Go's default gzip reader.
- zlib back-reference distances must fit the window declared by CINFO. Streams
  declaring less than 32 KiB use a stricter native decoding path, since ordinary
  zlib builds can otherwise accept excessive distances within an output buffer.
  This path can be slower; gzip and zlib's usual 32 KiB window retain bulk decoding.
- Each `ConcatZlib` input must contain exactly one zlib stream. Trailing bytes
  and preset dictionaries are rejected (`ErrTrailingData`, `ErrDictionary`).
  `NewZlibReader` reads one stream and may read ahead in its underlying reader.
- Output headers are normalized, so names, comments and timestamps are not
  preserved. Compressed output bytes can change even for a single input. An
  empty final deflate block completes the output; data is never recompressed.
- An error can occur after output has been written. Discard incomplete output;
  callers needing atomic file replacement should write to a temporary file.
- Read to EOF to validate all data. Closing early releases resources without
  validating unread data. Close is idempotent, does not close the source, and
  subsequent reads return `ErrClosed`. A reader must not be used concurrently.
  Independent readers and concatenations can run concurrently.
- Readers also implement `io.WriterTo`: `io.Copy(dst, reader)` writes directly
  from the internal decompression buffer, avoiding an intermediate copy through
  `Read`. `Read` and `WriteTo` can be interleaved sequentially. `WriteTo` validates
  trailers and returns nil at EOF; after a destination error, unwritten buffered
  data remains available. A short write without an error returns `io.ErrShortWrite`.
  `WriteTo` does not close the reader or destination; callers still need `Close`
  to release native memory. Calling `WriteTo` after `Close` returns `ErrClosed`.
- A saved terminal error or EOF remains stable if the context is later canceled.
  Bytes produced together with a decoding error are returned before that error.
  Cancellation during an active read stops delivery of otherwise buffered data.
- Reads can return available data without filling a 32 KiB input buffer. gzip
  readers need source EOF to know there are no more members; concatenation
  requires EOF on each input to detect additional members or trailing bytes.
- `IEEECrc32Combine` and `Adler32Combine` require a nonnegative second-input
  length. They panic on a negative length rather than loop indefinitely.

### Optional resource limits

Existing APIs have unlimited defaults. For external input, the `WithOptions`
variants accept a context, total compressed- and uncompressed-byte limits,
a member-count limit, and a per-header byte limit. Zero means unlimited;
negative limits are rejected. For example:

```go
err := dfjoin.ConcatGzipWithOptions(dst, dfjoin.Options{
    Context: ctx,
    MaxUncompressedBytes: 128 << 20,
    MaxCompressedBytes: 32 << 20,
    MaxMembers: 1000,
    MaxHeaderBytes: 64 << 10,
}, inputs...)
```

`NewGzipReaderWithOptions`, `NewZlibReaderWithOptions`, and
`ConcatZlibWithOptions` accept the same options. Limits apply across all inputs
and members in an operation; exceeding a limit returns `ErrLimitExceeded`.
`MaxMembers` counts each gzip member, including empty members, or each zlib
input stream. `MaxCompressedBytes` includes headers, trailers and source
read-ahead; `NewZlibReader` still reads only one stream, so trailing bytes already
read into its buffer count toward that limit as well. `MaxHeaderBytes` is per
header, rather than a total across members.

Input reads and inflate output buffers are sized to the remaining byte budget.
At an exact byte limit, at most one additional byte may be read or inflated to
distinguish a complete stream from excess data. A compressed probe byte is not
decoded, and an uncompressed probe byte is not returned by a Reader. Failed
concatenations still require callers to discard any partial compressed output.
An exactly sized, valid input succeeds, including any empty final blocks or
members that fit the other limits. Like all input reads, an EOF probe can block.

Context cancellation is checked between reads, writes and native inflate calls, but
cannot interrupt a blocked underlying read or write. Configure transport
timeouts/deadlines as well when using network streams.

### Verification

```sh
go test ./...
go vet ./...
go test -race ./... -run 'TestRegression|TestCorrectness|TestReaderWriteTo'
go test . -run '^$' -fuzz '^FuzzRawDeflate$' -fuzztime=30s
go test . -run '^$' -fuzz '^FuzzConcatRoundTrip$' -fuzztime=30s
go test . -run '^$' -fuzz '^FuzzDeclaredWindow$' -fuzztime=30s
```

The regression suite covers block and buffer boundaries, short reads, header
and trailer corruption, multiple members, resource limits, writer failures,
partial data on errors, and closed-reader behavior. Some legacy tests expand
more than 4 GiB of data to check gzip ISIZE wrapping and can take longer.
For bundled zlib provenance and updates, see [zlib/README.md](zlib/README.md).

## Benchmarks

To compare Reader's direct `WriteTo` path with reusable 8 KiB and 32 KiB
`Read` buffers on 1 MiB inputs, including the number of destination writes:

```sh
go test . -run '^$' -bench '^BenchmarkReaderCopy$' -benchmem
```

Below is the benchmark result for concatenating 6 gzip files which sizes range from tens of KiB to 300 KiB,
on my MacBook Air M2 with 8GB RAM

```shell
goos: darwin
goarch: arm64
pkg: github.com/zhyee/deflatejoin
BenchmarkConcatGzip/concat-standard-go-8                       9         123559972 ns/op         1257826 B/op       1261 allocs/op
BenchmarkConcatGzip/concat-deflatejoin-8                     100          10784015 ns/op           30289 B/op         41 allocs/op
```


## Cross compilation

It's recommended to build on Docker for various targets, or
you can use the gcc cross-compilation toolchains for the specified target to build 
zlib and your project, for example, on an Ubuntu 22.04 would be like:

- for target linux/arm64
```shell
# install gcc cross compilation toolchain for specific target
apt -y install aarch64-linux-gnu-gcc

# build zlib static library for linux/arm64
CC=aarch64-linux-gnu-gcc \
AR=aarch64-linux-gnu-ar \
RANLIB=aarch64-linux-gnu-ranlib \
./configure --prefix=/usr/local/zlib-arm64 --static \
&& make clean && make && make install

# build your project for linux/arm64
CC=aarch64-linux-gnu-gcc \
CGO_ENABLED='1' \
CGO_CFLAGS='-O2 -g -I/usr/local/zlib-arm64/include' \
CGO_LDFLAGS='-O2 -g -L/usr/local/zlib-arm64/lib' \
GOOS=linux \
GOARCH=arm64 go build
```

- for target linux/amd64
```shell
apt -y install x86_64-linux-gnu-gcc

# build zlib static library for linux/amd64
CC=x86_64-linux-gnu-gcc \
AR=x86_64-linux-gnu-ar \
RANLIB=x86_64-linux-gnu-ranlib \
./configure --prefix=/usr/local/zlib-x64 --static \
&& make clean && make && make install

# build your project for linux/amd64
CC=x86_64-linux-gnu-gcc \
CGO_ENABLED='1' \
CGO_CFLAGS='-O2 -g -I/usr/local/zlib-x64/include' \
CGO_LDFLAGS='-O2 -g -L/usr/local/zlib-x64/lib' \
GOOS=linux \
GOARCH=amd64 go build
```

- for target windows/x86-64
```shell
apt -y install x86_64-w64-mingw32-gcc

# build zlib static library for windows/x86-64
CC=x86_64-w64-mingw32-gcc \
AR=x86_64-w64-mingw32-ar \
RANLIB=x86_64-w64-mingw32-ranlib \
./configure --prefix=/usr/local/zlib-win64 --static \
&& make clean && make && make install

# build your project for windows/x86-64
CC=x86_64-w64-mingw32-gcc \
CGO_ENABLED='1' \
CGO_CFLAGS='-O2 -g -I/usr/local/zlib-win64/include' \
CGO_LDFLAGS='-O2 -g -L/usr/local/zlib-win64/lib' \
GOOS=windows \
GOARCH=amd64 go build
```

- for target windows/x86
```shell
apt -y install i686-w64-mingw32-gcc

# build zlib static library for windows/x86
CC=i686-w64-mingw32-gcc \
AR=i686-w64-mingw32-ar \
RANLIB=i686-w64-mingw32-ranlib \
./configure --prefix=/usr/local/zlib-win32 --static \
&& make clean && make && make install

# build your project for windows/x86
CC=i686-w64-mingw32-gcc \
CGO_ENABLED='1' \
CGO_CFLAGS='-O2 -g -I/usr/local/zlib-win32/include' \
CGO_LDFLAGS='-O2 -g -L/usr/local/zlib-win32/lib' \
GOOS=windows \
GOARCH=386 go build
```
All other targets would be similar, also for darwin/amd64, darwin/arm64, 
linux/386, linux/adm64, linux/arm64, linux/mips64, linux/mips64le, 
linux/ppc64, linux/ppc64le, windows/386, windows/amd64 targets, 
prebuilt zlib static libraries have been bundled into this package, 
so you have no need to build it by yourself, refer to [zlib](./zlib) 
directory for details, see also [osxcross](https://github.com/tpoechtrager/osxcross)
(a toolchain on linux targeting for macOS) and [llvm-mingw](https://github.com/mstorsjo/llvm-mingw)
(a toolchain targeting for windows on arm/x86).
