package dfjoin

import (
	"context"
	"errors"
	"math"
)

var (
	ErrClosed         = errors.New("deflatejoin: reader is closed")
	ErrLimitExceeded  = errors.New("deflatejoin: resource limit exceeded")
	ErrInvalidOptions = errors.New("deflatejoin: negative resource limit")
)

// Options controls optional limits. Zero limits mean unlimited. Limits cover
// the entire operation, except MaxHeaderBytes, which applies to each header.
// Context is checked between reads, writes and inflate calls; it cannot interrupt a
// blocked underlying Read or Write. Network callers must also set deadlines.
// A nil Context is equivalent to context.Background().
// MaxCompressedBytes counts source bytes, including headers, trailers and read
// ahead. MaxMembers counts gzip members or zlib streams across all inputs.
// Compressed and uncompressed limits each permit at most one extra byte to
// distinguish EOF from excess data. A probe byte is never decoded (compressed
// limit) or returned to the caller (uncompressed limit).
type Options struct {
	Context              context.Context
	MaxUncompressedBytes int64
	MaxHeaderBytes       int64
	MaxCompressedBytes   int64
	MaxMembers           int64
}

type budget struct {
	Options
	total      int64
	compressed int64
	members    int64
}

func newBudget(opts Options) (*budget, error) {
	if opts.MaxUncompressedBytes < 0 || opts.MaxHeaderBytes < 0 ||
		opts.MaxCompressedBytes < 0 || opts.MaxMembers < 0 {
		return nil, ErrInvalidOptions
	}
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	return &budget{Options: opts}, nil
}

func (b *budget) check() error { return b.Context.Err() }

// allowance returns the next buffer size and whether it is an EOF probe.
// Counters also remain bounded when the configured limit is unlimited.
func allowance(limit, used int64, capacity int) (int, bool) {
	if limit == 0 {
		limit = math.MaxInt64
	}
	remaining := limit - used
	if remaining <= 0 {
		return 1, true
	}
	if remaining < int64(capacity) {
		return int(remaining), false
	}
	return capacity, false
}

func (b *budget) nextMember() error {
	if b.members == math.MaxInt64 || (b.MaxMembers > 0 && b.members >= b.MaxMembers) {
		return ErrLimitExceeded
	}
	b.members++
	return nil
}

func (b *budget) add(n int) error {
	if int64(n) > math.MaxInt64-b.total ||
		(b.MaxUncompressedBytes > 0 && int64(n) > b.MaxUncompressedBytes-b.total) {
		return ErrLimitExceeded
	}
	b.total += int64(n)
	return nil
}
