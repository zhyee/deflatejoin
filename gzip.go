package dfjoin

import "io"

// ConcatGzip joins all members of inputs into one validated gzip member without
// recompressing their deflate data. Even a single input is validated. On error,
// the writer may contain incomplete output, which must be discarded.
func ConcatGzip(w io.Writer, inputs ...io.Reader) error {
	return ConcatGzipWithOptions(w, Options{}, inputs...)
}

// ConcatGzipWithOptions is ConcatGzip with resource limits and cancellation.
func ConcatGzipWithOptions(w io.Writer, opts Options, inputs ...io.Reader) error {
	return concat(w, gzipFormat, opts, inputs)
}

// NewGzipReader reads and validates all gzip members in r. Close releases native
// memory without closing r. Reading to EOF is required to validate all trailers.
// The returned reader implements io.WriterTo for direct buffered writes via
// io.Copy. A reader must not be used concurrently, including Read, WriteTo and Close.
func NewGzipReader(r io.Reader) (io.ReadCloser, error) {
	return NewGzipReaderWithOptions(r, Options{})
}

// NewGzipReaderWithOptions is NewGzipReader with resource limits and cancellation.
func NewGzipReaderWithOptions(r io.Reader, opts Options) (io.ReadCloser, error) {
	return newReader(r, gzipFormat, opts)
}

// CGOTest is retained for compatibility.
func CGOTest() {}
