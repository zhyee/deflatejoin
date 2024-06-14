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

## Usage
