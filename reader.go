package dfjoin

import (
	"errors"
	"fmt"
	"hash"
	"io"
)

type streamReader struct {
	*nativeStream
	format                  streamFormat
	digest                  hash.Hash32
	size                    int64
	offset, available       int
	bodyDone, drain, closed bool
	err                     error
}

var _ io.WriterTo = (*streamReader)(nil)

func newReader(r io.Reader, format streamFormat, opts Options) (io.ReadCloser, error) {
	b, err := newBudget(opts)
	if err != nil {
		return nil, err
	}
	if err = b.check(); err != nil {
		return nil, err
	}
	s, err := newNative(r, b)
	if err != nil {
		return nil, err
	}
	if err = format.readHeader(&s.input); err != nil {
		s.Close()
		return nil, fmt.Errorf("read header: %w", err)
	}
	if err = s.reset(); err != nil {
		s.Close()
		return nil, err
	}
	return &streamReader{nativeStream: s, format: format, digest: format.digest()}, nil
}

func (r *streamReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		if r.closed {
			return 0, ErrClosed
		}
		return 0, nil
	}
	if err := r.fill(); err != nil {
		return 0, err
	}
	n := copy(p, r.output[r.offset:r.available])
	r.offset += n
	return n, nil
}

// WriteTo writes the remaining decompressed data directly from the native
// output buffer. It returns nil at EOF after validating all trailers. A writer
// error leaves any unwritten data available for subsequent Read or WriteTo
// calls. WriteTo does not close either the reader or the writer.
func (r *streamReader) WriteTo(w io.Writer) (written int64, err error) {
	for {
		if err = r.fill(); err != nil {
			if err == io.EOF {
				err = nil
			}
			return written, err
		}
		p := r.output[r.offset:r.available]
		n, err := w.Write(p)
		if n < 0 || n > len(p) {
			if err == nil {
				err = errors.New("deflatejoin: invalid writer count")
			}
			return written, err
		}
		r.offset += n
		written += int64(n)
		if err != nil {
			return written, err
		}
		if n != len(p) {
			return written, io.ErrShortWrite
		}
	}
}

// fill preserves unread output and only decodes again once it is consumed.
// Read and WriteTo share terminal errors, cancellation and member validation.
func (r *streamReader) fill() error {
	if r.closed {
		return ErrClosed
	}
	for {
		// Preserve a decoded terminal error and its pending output. A later
		// cancellation must not replace the error or discard those bytes.
		if r.err == nil {
			if err := r.input.budget.check(); err != nil {
				r.err = err
				r.offset = r.available
				return err
			}
		}
		if r.offset < r.available {
			return nil
		}
		if r.err != nil {
			return r.err
		}
		if r.bodyDone {
			if err := r.format.readTrailer(&r.input, r.digest.Sum32(), r.size); err != nil {
				r.err = err
				continue
			}
			if r.format == zlibFormat {
				r.err = io.EOF
				continue
			}
			if err := r.input.ensure(); err != nil {
				r.err = err
				continue
			}
			if err := r.format.readHeader(&r.input); err != nil {
				r.err = err
				continue
			}
			if err := r.reset(); err != nil {
				r.err = err
				continue
			}
			r.digest.Reset()
			r.size = 0
			r.bodyDone, r.drain = false, false
		}
		if r.input.pos == r.input.end && !r.drain {
			if err := r.input.ensure(); err != nil {
				r.err = required(err)
				continue
			}
		}
		n, _, last, full, _, err := r.step(false)
		r.drain = full
		if errors.Is(err, io.ErrNoProgress) && r.input.pos == r.input.end {
			r.drain = false
			continue
		}
		if ex := r.input.budget.add(n); ex != nil {
			r.err = ex
			continue
		}
		r.digest.Write(r.output[:n])
		r.size += int64(n)
		r.offset, r.available = 0, n
		r.bodyDone, r.err = last, err
	}
}

func (r *streamReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.offset, r.available = 0, 0
	r.err = ErrClosed
	return r.nativeStream.Close()
}
