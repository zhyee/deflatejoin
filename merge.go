package dfjoin

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

func concat(w io.Writer, format streamFormat, opts Options, inputs []io.Reader) error {
	if len(inputs) == 0 {
		return errors.New("empty sources")
	}
	b, err := newBudget(opts)
	if err != nil {
		return err
	}
	if err = b.check(); err != nil {
		return err
	}
	s, err := newNative(nil, b)
	if err != nil {
		return err
	}
	defer s.Close()
	out := bufio.NewWriter(w)
	if _, err = out.Write(format.header()); err != nil {
		return err
	}
	var sum, size uint32
	if format == zlibFormat {
		sum = 1
	}
	for i, input := range inputs {
		s.input.r = input
		s.input.pos, s.input.end, s.input.pending = 0, 0, nil
		for member := 0; ; member++ {
			if err = format.readHeader(&s.input); err != nil {
				return fmt.Errorf("input %d member %d header: %w", i, member, err)
			}
			memberSum, memberSize, ex := s.copyMember(out, format)
			if ex != nil {
				return fmt.Errorf("input %d member %d: %w", i, member, ex)
			}
			if format == gzipFormat {
				sum = IEEECrc32Combine(sum, memberSum, memberSize)
			} else {
				sum = Adler32Combine(sum, memberSum, memberSize)
			}
			size += uint32(memberSize)
			err = s.input.ensure()
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("input %d: %w", i, err)
			}
			if format == zlibFormat {
				return ErrTrailingData
			}
		}
	}
	// Every copied member is non-final and byte aligned. Finish with an empty
	// stored block, so no look-ahead or buffering of an entire member is needed.
	if _, err = out.Write([]byte{1, 0, 0, 255, 255}); err != nil {
		return err
	}
	if _, err = out.Write(format.trailer(sum, size)); err != nil {
		return err
	}
	if err = b.check(); err != nil {
		return err
	}
	return out.Flush()
}

func (s *nativeStream) copyMember(w *bufio.Writer, format streamFormat) (uint32, int64, error) {
	if err := s.reset(); err != nil {
		return 0, 0, err
	}
	in := &s.input
	if err := in.ensure(); err != nil {
		return 0, 0, required(err)
	}
	start := in.pos
	lastBlock := in.data[in.pos]&1 != 0
	in.data[in.pos] &^= 1
	digest := format.digest()
	var size int64
	drain := false
	for {
		if in.pos == in.end && !drain {
			if _, err := w.Write(in.data[start:in.pos]); err != nil {
				return 0, 0, err
			}
			if err := in.ensure(); err != nil {
				return 0, 0, required(err)
			}
			start = in.pos
		}
		n, boundary, ended, full, bits, err := s.step(true)
		drain = full
		if errors.Is(err, io.ErrNoProgress) && in.pos == in.end {
			drain = false
			continue
		}
		if err != nil {
			return 0, 0, err
		}
		if err = in.budget.add(n); err != nil {
			return 0, 0, err
		}
		digest.Write(s.output[:n])
		size += int64(n)
		if !boundary {
			if ended {
				return 0, 0, errors.New("deflatejoin: missing final block boundary")
			}
			continue
		}
		if lastBlock {
			if in.pos <= start {
				return 0, 0, errors.New("deflatejoin: invalid final block position")
			}
			if _, err = w.Write(in.data[start : in.pos-1]); err != nil {
				return 0, 0, err
			}
			if err = alignMember(w, in.data[in.pos-1], bits); err != nil {
				return 0, 0, err
			}
			if err = format.readTrailer(in, digest.Sum32(), size); err != nil {
				return 0, 0, err
			}
			return digest.Sum32(), size, nil
		}
		if bits != 0 {
			if in.pos == 0 {
				return 0, 0, errors.New("deflatejoin: invalid block position")
			}
			mask := byte(int(0x100) >> bits)
			lastBlock = in.data[in.pos-1]&mask != 0
			in.data[in.pos-1] &^= mask
		} else {
			if in.pos == in.end {
				if _, err = w.Write(in.data[start:in.pos]); err != nil {
					return 0, 0, err
				}
				if err = in.ensure(); err != nil {
					return 0, 0, required(err)
				}
				start = in.pos
			}
			lastBlock = in.data[in.pos]&1 != 0
			in.data[in.pos] &^= 1
		}
	}
}

// Append empty non-final blocks to align the next member to a byte boundary.
// This is the block alignment algorithm from Mark Adler's gzjoin example.
func alignMember(w *bufio.Writer, last byte, bits int) error {
	if bits == 0 {
		return w.WriteByte(last)
	}
	last &= byte((int(0x100) >> bits) - 1)
	if bits&1 != 0 {
		if err := w.WriteByte(last); err != nil {
			return err
		}
		if bits == 1 {
			if err := w.WriteByte(0); err != nil {
				return err
			}
		}
		_, err := w.Write([]byte{0, 0, 255, 255})
		return err
	}
	for ; bits >= 2; bits -= 2 {
		if err := w.WriteByte(last | byte(1<<(9-bits))); err != nil {
			return err
		}
		last = 0
	}
	return w.WriteByte(0)
}
