package tui

import (
	"context"
	"errors"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	ctrlT          = 0x14
	keyPollTimeout = 50 * time.Millisecond
)

// keyTerm is the terminal access a keyWatcher needs; platform files provide
// the real one and tests a pipe.
type keyTerm interface {
	// enter switches to key-at-a-time input without echo (signals such as
	// Ctrl+C still work); leave restores the previous mode.
	enter() error
	leave() error
	// ready waits up to timeout for input to read.
	ready(timeout time.Duration) (bool, error)
	read(p []byte) (int, error)
}

var errKeysUnsupported = errors.New("key watching is not supported on this platform")

// keyWatcher notices typing while a turn runs, so the user can steer the
// agent. It owns the terminal between prompts: Ctrl+T or any printable key
// calls onKey (with the typed text, to pre-fill the steer prompt). Prompts
// shown during the turn (approvals) pause it first, so it never competes
// with them for input.
type keyWatcher struct {
	term  keyTerm
	onKey func(prefill string)

	pauseReq chan chan struct{}
	resume   chan struct{}
	stop     chan struct{}
	done     chan struct{}
}

func startKeyWatcher(term keyTerm, onKey func(prefill string)) *keyWatcher {
	w := &keyWatcher{
		term:     term,
		onKey:    onKey,
		pauseReq: make(chan chan struct{}),
		resume:   make(chan struct{}),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go w.run()
	return w
}

func (w *keyWatcher) run() {
	defer close(w.done)
	if w.term.enter() != nil {
		return // not a terminal we can drive: no steering, nothing else changes
	}
	active := true
	defer func() {
		if active {
			w.term.leave()
		}
	}()
	buf := make([]byte, 256)
	for {
		select {
		case <-w.stop:
			return
		case ack := <-w.pauseReq:
			w.term.leave()
			active = false
			close(ack)
			select {
			case <-w.resume:
			case <-w.stop:
				return
			}
			if w.term.enter() != nil {
				return
			}
			active = true
			continue
		default:
		}

		ok, err := w.term.ready(keyPollTimeout)
		if err != nil {
			return
		}
		if !ok {
			continue
		}
		n, err := w.term.read(buf)
		if err != nil || n == 0 {
			return
		}
		prefill, trigger := steerTrigger(buf[:n])
		if !trigger {
			continue
		}
		w.term.leave()
		active = false
		w.onKey(prefill)
		if w.term.enter() != nil {
			return
		}
		active = true
	}
}

// pause hands the terminal back (restoring its mode) until the returned
// func is called. It waits for an open steer prompt to finish.
func (w *keyWatcher) pause(ctx context.Context) (resume func(), err error) {
	ack := make(chan struct{})
	select {
	case w.pauseReq <- ack:
	case <-w.done:
		return func() {}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	<-ack
	return func() {
		select {
		case w.resume <- struct{}{}:
		case <-w.done:
		}
	}, nil
}

// close stops watching and restores the terminal. If a steer prompt is
// open it waits for the user to finish it.
func (w *keyWatcher) close() {
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
	<-w.done
}

// steerTrigger decides whether input read during a turn opens the steer
// prompt: Ctrl+T does, and so does typing text, which pre-fills the prompt.
// Enter, escape sequences (arrow keys) and other control keys are ignored.
func steerTrigger(b []byte) (prefill string, trigger bool) {
	if len(b) > 0 && b[0] == 0x1b {
		return "", false
	}
	var out []rune
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		b = b[size:]
		switch {
		case r == ctrlT:
			trigger = true
		case r != utf8.RuneError && unicode.IsPrint(r):
			out = append(out, r)
		}
	}
	return string(out), trigger || len(out) > 0
}
