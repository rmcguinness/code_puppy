package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/retail-cortex/blitz/internal/server"
)

// apiPrefix is where the service's API lives; the page's requests there go
// to the service.
const apiPrefix = "/blitz.v1."

// serviceProxy forwards the page's API requests to the service's Unix
// socket (a web view can't open one itself), streaming responses as they
// come. Anything else is not found: the page's own files are served by
// Wails.
func serviceProxy(socket string) http.Handler {
	target, _ := url.Parse(server.BaseURL)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = target.Host
		},
		Transport:     server.Client(socket).Transport,
		FlushInterval: -1, // turns stream their events
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			// Connect's JSON error shape, so the page's client reads it.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"code":"unavailable","message":"the Blitz service isn't answering"}`))
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, apiPrefix) {
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}
