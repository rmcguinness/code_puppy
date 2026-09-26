// Package breaker is a small circuit breaker for dependencies that fail
// persistently (MCP servers, model APIs): after a run of failures calls
// are refused for a cooldown that grows with each failed trial.
package breaker

import (
	"sync"
	"time"
)

// Breaker opens after Threshold consecutive failures: calls are refused
// immediately for a cooldown that doubles on each failed trial (from
// InitialCooldown up to MaxCooldown). When the cooldown ends, one caller is
// let through as a trial; success closes the breaker, failure reopens it.
// It is safe for concurrent use.
type Breaker struct {
	threshold int

	mu        sync.Mutex
	failures  int
	open      bool
	trial     bool // a half-open trial call is in flight
	openUntil time.Time
	cooldown  time.Duration
	now       func() time.Time // for tests
}

const (
	InitialCooldown = 15 * time.Second
	MaxCooldown     = 5 * time.Minute
)

// New returns a closed breaker that opens after threshold consecutive
// failures (minimum 1).
func New(threshold int) *Breaker {
	return &Breaker{threshold: max(threshold, 1), now: time.Now}
}

// Allow reports whether a call may proceed; if not, retryIn says when the
// next trial is due.
func (b *Breaker) Allow() (ok bool, retryIn time.Duration) {
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

// Success records a successful call; recovered is true when it closed an
// open breaker.
func (b *Breaker) Success() (recovered bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	recovered = b.open
	b.failures, b.open, b.trial, b.cooldown = 0, false, false, 0
	return recovered
}

// Failure records a failed call. streak is the number of consecutive
// failures; opened is true when this failure opened (or reopened) the
// breaker, with the cooldown that now applies.
func (b *Breaker) Failure() (streak int, opened bool, cooldown time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	switch {
	case b.open && b.trial: // failed trial: back off further
		b.cooldown = min(b.cooldown*2, MaxCooldown)
	case !b.open && b.failures >= b.threshold:
		b.cooldown = InitialCooldown
	default:
		return b.failures, false, 0
	}
	b.open, b.trial = true, false
	b.openUntil = b.now().Add(b.cooldown)
	return b.failures, true, b.cooldown
}

// Abandon releases a call Allow let through without recording an outcome
// (it was cancelled), so a half-open breaker can admit another trial.
func (b *Breaker) Abandon() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.trial = false
}

// SetClock replaces the time source (tests).
func (b *Breaker) SetClock(now func() time.Time) { b.now = now }

// Failures returns the current run of consecutive failures.
func (b *Breaker) Failures() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failures
}
