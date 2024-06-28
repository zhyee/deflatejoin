# deflatejoin
A go package used to more efficiently concat(join) multi gzip/zlib files, 
which benefits from cgo and zlib.
It's a golang port(wrapper) of Mark Adler's [gzjoin.c](https://github.com/madler/zlib/blob/develop/examples/gzjoin.c).
Compared to decompressing all files
and then compressing them again
by using go builtin gzip package, it only decompresses all files once and with no any compressions.

## Prerequisites
- GCC/Clang/MinGW

## Install

```shell
go get github.com/zhyee/deflatejoin
```

## Example

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

## Benchmarks

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

It's recommended to build executable binaries on Docker for various platforms, or
you can use the gcc cross-compilation toolchains for the specified target to build 
zlib and your project, for example, on an Ubuntu 22.04 would be like:

```shell
# build zlib static library for linux/arm64
CC=aarch64-linux-gnu-gcc AR=aarch64-linux-gnu-ar ./configure --prefix=/usr/local/zlib-arm64 --static && make clean && make && make install
# build your project for linux/arm64
CC=aarch64-linux-gnu-gcc CGO_ENABLED='1' CGO_CFLAGS='-O2 -g -I/usr/local/zlib-arm64/include' CGO_LDFLAGS='-O2 -g -L/usr/local/zlib-arm64/lib' GOOS=linux GOARCH=arm64 go build

# build zlib static library for linux/amd64
CC=x86_64-linux-gnu-gcc AR=x86_64-linux-gnu-ar ./configure --prefix=/usr/local/zlib-x64 --static && make clean && make && make install
# build your project for linux/amd64
CC=x86_64-linux-gnu-gcc CGO_ENABLED='1' CGO_CFLAGS='-O2 -g -I/usr/local/zlib-x64/include' CGO_LDFLAGS='-O2 -g -L/usr/local/zlib-x64/lib' GOOS=linux GOARCH=amd64 go build

# build zlib static library for windows/x86-64
CC=x86_64-w64-mingw32-gcc AR=x86_64-w64-mingw32-ar ./configure --prefix=/usr/local/zlib-win64 --static && make clean && make && make install
# build your project for windows/x86-64
CC=x86_64-w64-mingw32-gcc CGO_ENABLED='1' CGO_CFLAGS='-O2 -g -I/usr/local/zlib-win64/include' CGO_LDFLAGS='-O2 -g -L/usr/local/zlib-win64/lib' GOOS=windows GOARCH=amd64 go build

# build zlib static library for windows/x86
CC=i686-w64-mingw32-gcc AR=i686-w64-mingw32-ar ./configure --prefix=/usr/local/zlib-win32 --static && make clean && make && make install
# build your project for windows/x86
CC=i686-w64-mingw32-gcc CGO_ENABLED='1' CGO_CFLAGS='-O2 -g -I/usr/local/zlib-win32/include' CGO_LDFLAGS='-O2 -g -L/usr/local/zlib-win32/lib' GOOS=windows GOARCH=386 go build
```