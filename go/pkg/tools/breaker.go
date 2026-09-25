package tools

import (
	"sync"
	"time"
)

// breaker is a per-dependency circuit breaker. After failThreshold
// consecutive failures it opens: calls are refused immediately for a
// cooldown that doubles on each failed trial (up to maxCooldown). When the
// cooldown ends, one caller is let through as a trial; success closes the
// breaker, failure reopens it.
//
// It keeps an unreachable MCP server from costing a connection attempt (for
// stdio servers, a process start) and a timeout on every model call.
type breaker struct {
	mu        sync.Mutex
	failures  int
	open      bool
	trial     bool // a half-open trial call is in flight
	openUntil time.Time
	cooldown  time.Duration
	now       func() time.Time
}

const (
	failThreshold   = 2
	initialCooldown = 15 * time.Second
	maxCooldown     = 5 * time.Minute
)

func newBreaker() *breaker { return &breaker{now: time.Now} }

// allow reports whether a call may proceed; if not, retryIn says when the
// next trial is due.
func (b *breaker) allow() (ok bool, retryIn time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.open {
		return true, 0
	}
	if now := b.now(); now.Before(b.openUntil) || b.trial {
		return false, max(b.openUntil.Sub(now), 0)
	}
	b.trial = true
	return true, 0
}

// success records a successful call; recovered is true when it closed an
// open breaker.
func (b *breaker) success() (recovered bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	recovered = b.open
	b.failures, b.open, b.trial, b.cooldown = 0, false, false, 0
	return recovered
}

// failure records a failed call. streak is the number of consecutive
// failures; opened is true when this failure opened (or reopened) the
// breaker, with the cooldown that now applies.
func (b *breaker) failure() (streak int, opened bool, cooldown time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	switch {
	case b.open && b.trial: // failed trial: back off further
		b.cooldown = min(b.cooldown*2, maxCooldown)
	case !b.open && b.failures >= failThreshold:
		b.cooldown = initialCooldown
	default:
		return b.failures, false, 0
	}
	b.open, b.trial = true, false
	b.openUntil = b.now().Add(b.cooldown)
	return b.failures, true, b.cooldown
}
