package tui

import (
	"fmt"
	"io"
	"sync"
	"time"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner shows an animated status line while waiting. It is a no-op when
// disabled (e.g. output is not a terminal). Start and Stop may be called in
// any order and repeatedly.
type Spinner struct {
	mu      sync.Mutex
	out     io.Writer
	enabled bool
	stop    chan struct{}
	done    chan struct{}
}

// NewSpinner returns a spinner writing to out.
func NewSpinner(out io.Writer, enabled bool) *Spinner {
	return &Spinner{out: out, enabled: enabled}
}

// Start shows label with an elapsed-time counter until Stop.
func (s *Spinner) Start(label string) {
	if s == nil || !s.enabled {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		return
	}
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	stop, done := s.stop, s.done
	go func() {
		defer close(done)
		start := time.Now()
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for i := 0; ; i++ {
			fmt.Fprintf(s.out, "\r\033[2K%s%s %s %s(%.0fs)%s", Cyan, spinnerFrames[i%len(spinnerFrames)], label, Dim, time.Since(start).Seconds(), Reset)
			select {
			case <-stop:
				fmt.Fprint(s.out, "\r\033[2K")
				return
			case <-t.C:
			}
		}
	}()
}

// Stop clears the spinner line.
func (s *Spinner) Stop() {
	if s == nil || !s.enabled {
		return
	}
	s.mu.Lock()
	stop, done := s.stop, s.done
	s.stop, s.done = nil, nil
	s.mu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
}
