package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"sync"
)

// pathLocks serialises read-approve-write sequences per file. The ADK runs
// the tool calls of one model response concurrently, so two edits to the
// same file would otherwise both read the original and the second write
// would silently drop the first.
//
// Each lock is a one-slot channel so waiting respects ctx: a cancelled turn
// never leaves a tool blocked behind an approval that will not come.
type pathLocks struct {
	mu sync.Mutex
	m  map[string]*pathLock
}

type pathLock struct {
	ch   chan struct{}
	refs int // holders plus waiters; the entry is dropped at zero
}

func (p *pathLocks) acquire(ctx context.Context, key string) error {
	p.mu.Lock()
	if p.m == nil {
		p.m = map[string]*pathLock{}
	}
	l := p.m[key]
	if l == nil {
		l = &pathLock{ch: make(chan struct{}, 1)}
		p.m[key] = l
	}
	l.refs++
	p.mu.Unlock()

	select {
	case l.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		p.drop(key, l)
		return ctx.Err()
	}
}

func (p *pathLocks) release(key string) {
	p.mu.Lock()
	l := p.m[key]
	p.mu.Unlock()
	<-l.ch
	p.drop(key, l)
}

func (p *pathLocks) drop(key string, l *pathLock) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if l.refs--; l.refs == 0 {
		delete(p.m, key)
	}
}

// lockPaths locks the given paths (as returned by WritablePath) until the
// returned func is called. Paths are locked in sorted order so overlapping
// multi-file edits cannot deadlock.
func (w *Workspace) lockPaths(ctx context.Context, paths ...string) (unlock func(), err error) {
	keys := slices.Clone(paths)
	slices.Sort(keys)
	keys = slices.Compact(keys)
	var held []string
	unlock = func() {
		for i := len(held) - 1; i >= 0; i-- {
			w.locks.release(held[i])
		}
	}
	for _, k := range keys {
		if err := w.locks.acquire(ctx, k); err != nil {
			unlock()
			return nil, err
		}
		held = append(held, k)
	}
	return unlock, nil
}

// errChangedDuringApproval tells the model to re-read before editing again.
var errChangedDuringApproval = errors.New("changed while waiting for approval (edited outside this tool); nothing was written — read it again and redo the edit")

// unchanged verifies that p still holds before (or, when !existed, that it
// still does not exist). Called under the path lock after approval, it
// catches edits made meanwhile by the user or a shell command, which the
// approved diff would otherwise overwrite.
func (w *Workspace) unchanged(p string, existed bool, before []byte) error {
	data, err := w.ReadFile(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if !existed {
			return nil
		}
	case err != nil:
		return fmt.Errorf("%s: %w", p, err)
	case existed && bytes.Equal(data, before):
		return nil
	}
	return fmt.Errorf("%s %w", p, errChangedDuringApproval)
}
