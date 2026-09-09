package dfjoin

import "io"

// ConcatZlib joins inputs into one validated zlib stream without recompression.
// Each input must contain exactly one stream, with no preset dictionary or
// trailing bytes. On error, any output already written must be discarded.
func ConcatZlib(w io.Writer, inputs ...io.Reader) error {
	return ConcatZlibWithOptions(w, Options{}, inputs...)
}

// ConcatZlibWithOptions is ConcatZlib with resource limits and cancellation.
func ConcatZlibWithOptions(w io.Writer, opts Options, inputs ...io.Reader) error {
	return concat(w, zlibFormat, opts, inputs)
}

// NewZlibReader reads and validates one zlib stream. Preset dictionaries are not
// supported. It may read ahead in r. Close releases native memory without
// closing r. Read to EOF to validate the checksum. The returned reader implements
// io.WriterTo for direct buffered writes via io.Copy. Read, WriteTo and Close
// must not be called concurrently.
func NewZlibReader(r io.Reader) (io.ReadCloser, error) {
	return NewZlibReaderWithOptions(r, Options{})
}

// NewZlibReaderWithOptions is NewZlibReader with resource limits and cancellation.
func NewZlibReaderWithOptions(r io.Reader, opts Options) (io.ReadCloser, error) {
	return newReader(r, zlibFormat, opts)
}
