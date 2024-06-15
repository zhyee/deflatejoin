package internal

import (
	"github.com/stretchr/testify/assert"
	"runtime"
	"testing"
)

func getString() string {
	b := []byte("foobar")
	return UnsafeString(&b[0], len(b))
}

func TestUnsafeString(t *testing.T) {
	b := []byte("Hello world")
	s := UnsafeString(&b[0], len(b))
	t.Logf("%q", s)

	assert.Equal(t, string(b), s)

	s = UnsafeString(&b[6], len(b[6:]))
	assert.Equal(t, string(b[6:]), s)
	t.Logf("%q", s)

	s = getString()
	runtime.GC()
	t.Logf("%q", s)
}
