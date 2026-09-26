package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/retail-cortex/code_puppy/internal/config"
)

// The service listens on a Unix socket that only its user can reach: it
// runs shell commands, so it is never on a network port by default.

// BaseURL is the URL clients use over the socket (the host is ignored).
const BaseURL = "http://code-puppy"

// ErrRunning reports a service already answering on the socket.
var ErrRunning = errors.New("a Code Puppy service is already running")

// DefaultSocket is where the per-user service listens:
// $CODE_PUPPY_SOCKET, or ~/.code_puppy/run/code-puppy.sock.
func DefaultSocket() string {
	if p := os.Getenv("CODE_PUPPY_SOCKET"); p != "" {
		return config.ExpandHome(p)
	}
	return config.ExpandHome("~/.code_puppy/run/code-puppy.sock")
}

// Listen opens the Unix socket at path for this user only. It refuses when
// a service already answers there, and replaces a socket left by one that
// died.
func Listen(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// The directory, not only the socket, keeps other users out: there is
	// no window between creating the socket and restricting it.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		if Running(path) {
			return nil, fmt.Errorf("%w (%s)", ErrRunning, path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("removing stale socket: %w", err)
		}
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// Running reports whether a service answers on the socket.
func Running(path string) bool {
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// Serve serves h on l (HTTP/1.1, and HTTP/2 without TLS for gRPC clients)
// until ctx is done, then stops accepting and waits up to grace for calls
// in progress.
func Serve(ctx context.Context, l net.Listener, h http.Handler, grace time.Duration) error {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	srv.Protocols = new(http.Protocols)
	srv.Protocols.SetHTTP1(true)
	srv.Protocols.SetUnencryptedHTTP2(true)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	err := srv.Shutdown(sctx)
	if errors.Is(err, context.DeadlineExceeded) {
		err = srv.Close() // turns still running are cut off
	}
	<-done
	return err
}

// Client returns an HTTP client that reaches the service on the socket;
// use it with BaseURL.
func Client(path string) *http.Client {
	var d net.Dialer
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.DialContext(ctx, "unix", path)
		},
	}}
}
