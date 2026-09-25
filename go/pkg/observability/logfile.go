// Package observability provides the diagnostic log and OpenTelemetry
// export. Both are fed from the goroutines doing the work and written by
// dedicated background goroutines, so logging and tracing never wait on disk
// or network I/O.
package observability

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/retail-cortex/code_puppy/pkg/config"
	"github.com/retail-cortex/code_puppy/pkg/redact"
	"go.opentelemetry.io/otel/trace"
)

const (
	logQueueSize = 1024
	logPrefix    = "code-puppy-"
	logSuffix    = ".jsonl"
)

// ParseLevel maps a config level to slog; ok is false for "off".
func ParseLevel(s string) (level slog.Level, ok bool, err error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "none":
		return 0, false, nil
	case "", "info":
		return slog.LevelInfo, true, nil
	case "debug":
		return slog.LevelDebug, true, nil
	case "warn", "warning":
		return slog.LevelWarn, true, nil
	case "error":
		return slog.LevelError, true, nil
	}
	return 0, false, fmt.Errorf("unknown log level %q (use debug, info, warn, error or off)", s)
}

// fileSink receives encoded log lines and appends them to a daily file from
// a single writer goroutine. Callers never block: when the queue is full the
// line is dropped and counted, and the count is written once there is room.
// Lines queued together are written with one flush (group commit).
type fileSink struct {
	mu     sync.RWMutex // write lock only to close the queue
	closed bool
	queue  chan []byte
	done   chan struct{}

	dropped atomic.Int64

	dir    string
	retain time.Duration
	now    func() time.Time

	// Owned by the writer goroutine.
	day string
	f   *os.File
	w   *bufio.Writer
}

func newFileSink(dir string, retainDays int) (*fileSink, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	s := &fileSink{
		queue: make(chan []byte, logQueueSize),
		done:  make(chan struct{}),
		dir:   dir,
		now:   time.Now,
	}
	if retainDays > 0 {
		s.retain = time.Duration(retainDays) * 24 * time.Hour
	}
	go s.run()
	return s, nil
}

// Write queues one encoded record. slog's JSON handler calls it once per
// record with the complete line, and reuses p afterwards, so it is copied.
func (s *fileSink) Write(p []byte) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return len(p), nil
	}
	select {
	case s.queue <- append([]byte(nil), p...):
	default:
		s.dropped.Add(1)
	}
	return len(p), nil
}

// Close writes everything queued and closes the file.
func (s *fileSink) Close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.queue)
	}
	s.mu.Unlock()
	<-s.done
	return nil
}

func (s *fileSink) run() {
	defer close(s.done)
	defer s.closeFile()
	for line := range s.queue {
		s.write(line)
		if len(s.queue) == 0 {
			s.reportDropped()
			s.flush()
		}
	}
	s.reportDropped()
}

func (s *fileSink) reportDropped() {
	if n := s.dropped.Swap(0); n > 0 {
		s.write(fmt.Appendf(nil, `{"time":%q,"level":"WARN","msg":"log queue full; records dropped","count":%d}`+"\n",
			s.now().Format(time.RFC3339Nano), n))
	}
}

func (s *fileSink) write(line []byte) {
	if err := s.rotate(); err != nil {
		return // logging must never break the session
	}
	_, _ = s.w.Write(line)
}

func (s *fileSink) flush() {
	if s.w != nil {
		_ = s.w.Flush()
	}
}

func (s *fileSink) closeFile() {
	s.flush()
	if s.f != nil {
		_ = s.f.Close()
		s.f, s.w = nil, nil
	}
}

func (s *fileSink) rotate() error {
	day := s.now().Format("2006-01-02")
	if s.f != nil && day == s.day {
		return nil
	}
	s.closeFile()
	f, err := os.OpenFile(filepath.Join(s.dir, logPrefix+day+logSuffix), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	s.f, s.w, s.day = f, bufio.NewWriterSize(f, 32*1024), day
	s.prune()
	return nil
}

// prune deletes log files older than the retention period.
func (s *fileSink) prune() {
	if s.retain <= 0 {
		return
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	cutoff := s.now().Add(-s.retain).Format("2006-01-02")
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, logPrefix) || !strings.HasSuffix(name, logSuffix) {
			continue
		}
		if day := strings.TrimSuffix(strings.TrimPrefix(name, logPrefix), logSuffix); day < cutoff {
			_ = os.Remove(filepath.Join(s.dir, name))
		}
	}
}

// redactAttr masks secrets in string and error values before encoding.
func redactAttr(r *redact.Redactor) func([]string, slog.Attr) slog.Attr {
	return func(_ []string, a slog.Attr) slog.Attr {
		switch a.Value.Kind() {
		case slog.KindString:
			a.Value = slog.StringValue(r.String(a.Value.String()))
		case slog.KindAny:
			switch v := a.Value.Any().(type) {
			case error:
				a.Value = slog.StringValue(r.String(v.Error()))
			case fmt.Stringer:
				a.Value = slog.StringValue(r.String(v.String()))
			}
		}
		return a
	}
}

// traceHandler adds the active trace and span IDs so log lines can be
// matched to exported traces.
type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, rec slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		rec.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, rec)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}

// levelHandler applies one minimum level to handlers that don't take one.
type levelHandler struct {
	slog.Handler
	min slog.Level
}

func (h levelHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.min && h.Handler.Enabled(ctx, l)
}

func (h levelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return levelHandler{h.Handler.WithAttrs(attrs), h.min}
}

func (h levelHandler) WithGroup(name string) slog.Handler {
	return levelHandler{h.Handler.WithGroup(name), h.min}
}

// OpenLog returns a logger writing JSON lines to cfg.Dir (secrets masked)
// and to each extra handler (e.g. Telemetry.LogHandler), plus a func that
// flushes and closes the file. With level "off" only the extra handlers
// receive records, at info level.
func OpenLog(cfg config.LogConfig, r *redact.Redactor, extra ...slog.Handler) (*slog.Logger, io.Closer, error) {
	level, on, err := ParseLevel(cfg.Level)
	if err != nil {
		return nil, nil, err
	}
	var handlers []slog.Handler
	closer := io.Closer(nopCloser{})
	if on {
		sink, err := newFileSink(config.ExpandHome(cfg.Dir), cfg.RetainDays)
		if err != nil {
			return nil, nil, err
		}
		closer = sink
		handlers = append(handlers, traceHandler{slog.NewJSONHandler(sink, &slog.HandlerOptions{
			Level:       level,
			ReplaceAttr: redactAttr(r),
		})})
	} else {
		level = slog.LevelInfo
	}
	for _, h := range extra {
		if h != nil {
			handlers = append(handlers, levelHandler{h, level})
		}
	}
	switch len(handlers) {
	case 0:
		return slog.New(slog.DiscardHandler), closer, nil
	case 1:
		return slog.New(handlers[0]), closer, nil
	}
	return slog.New(slog.NewMultiHandler(handlers...)), closer, nil
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
