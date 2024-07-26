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
