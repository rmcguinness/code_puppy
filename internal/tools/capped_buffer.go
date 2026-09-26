package tools

import (
	"fmt"
	"sync"

	"github.com/retail-cortex/code_puppy/internal/textutil"
)

// cappedBuffer is a concurrency-safe io.Writer that keeps at most limit bytes
// and silently discards the rest, so runaway output cannot exhaust memory.
type cappedBuffer struct {
	mu      sync.Mutex
	buf     []byte
	limit   int
	dropped int64
}

func newCappedBuffer(limit int) *cappedBuffer {
	return &cappedBuffer{limit: limit, buf: make([]byte, 0, min(limit, 4096))}
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	room := c.limit - len(c.buf)
	n := 0
	if room > 0 {
		n = min(room, len(p))
		c.buf = append(c.buf, p[:n]...)
	}
	c.dropped += int64(len(p) - n)
	return len(p), nil
}

// String returns the captured output, with a notice when bytes were dropped.
func (c *cappedBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := string(c.buf)
	if c.dropped == 0 {
		return s
	}
	return textutil.TrimPartialRune(s) +
		fmt.Sprintf("\n\n... [output truncated: %d bytes over the %d byte limit were discarded]", c.dropped, c.limit)
}

// Truncated reports whether any output was discarded.
func (c *cappedBuffer) Truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped > 0
}
