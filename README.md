# deflatejoin
A go package used to more efficiently concat(join) multi gzip/zlib compressed files, 
which benefits from cgo and zlib.
It's a golang port(wrapper) of Mark Adler's [gzjoin.c](https://github.com/madler/zlib/blob/develop/examples/gzjoin.c).
Compared to decompressing all files
and then compressing them again
by using go builtin gzip package, it only decompresses all files once and with no any compressions.

## Prerequisites
- zlib
- GCC/Clang/MinGW


**Install zlib on macOS**

```shell
brew install zlib
```

**Install zlib on Debian/Ubuntu**

```shell
sudo apt -y install zlib1g zlib1g-dev
```

**Install zlib on RedHat/CentOS**

```shell
sudo yum install -y zlib zlib-devel
```

For other platforms or install zlib from source, you may google it.

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

## Cross compilation

It's recommended to build executable binaries on Docker for various platforms, 
that will make your life easier.