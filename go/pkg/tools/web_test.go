package tools

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestPublicAddr(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "::1", "fe80::1",
		"0.0.0.0", "100.64.0.1", "224.0.0.1", "::ffff:127.0.0.1", "fc00::1", "0.1.2.3"} {
		if publicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !publicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func TestMatchDomain(t *testing.T) {
	globs := []string{"*.go.dev", "example.com"}
	for host, want := range map[string]bool{
		"pkg.go.dev": true, "go.dev": true, "EXAMPLE.com": true, "example.com.": true,
		"evilgo.dev": false, "go.dev.evil.com": false, "sub.example.com": false,
	} {
		if got := matchDomain(globs, host); got != want {
			t.Errorf("matchDomain(%q) = %v, want %v", host, got, want)
		}
	}
}

func testFetcher(cfg WebFetchConfig) *webFetcher {
	cfg.AllowNetwork = true
	return newWebFetcher(cfg)
}

func TestWebFetchBlocksPrivateByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret internal")) }))
	defer srv.Close()

	out := testFetcher(WebFetchConfig{}).fetch(context.Background(), allowAll(), srv.URL)
	if !strings.Contains(out.Error, "not a public address") || strings.Contains(out.Content, "secret") {
		t.Errorf("expected loopback to be blocked: %+v", out)
	}
	// "localhost" resolves to loopback and is blocked at dial time too.
	u, _ := url.Parse(srv.URL)
	out = testFetcher(WebFetchConfig{}).fetch(context.Background(), allowAll(), "http://localhost:"+u.Port())
	if out.Error == "" {
		t.Error("localhost should be blocked")
	}
}

func TestWebFetchContent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><title>T</title><style>.x{}</style><script>alert(1)</script></head>
<body><h1>Heading</h1><p>Hello <b>world</b>.</p><ul><li>one</li><li>two</li></ul>
<a href="https://go.dev/doc">docs</a><a href="/rel">relative</a></body></html>`))
	})
	mux.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(strings.Repeat("a", 5000)))
	})
	mux.HandleFunc("/bin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte{0, 1, 2})
	})
	mux.HandleFunc("/404", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", 404) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	f := testFetcher(WebFetchConfig{AllowPrivate: true, MaxBytes: 1000})
	ctx := context.Background()

	out := f.fetch(ctx, allowAll(), srv.URL+"/page")
	if out.Error != "" {
		t.Fatal(out.Error)
	}
	for _, want := range []string{"# Heading", "Hello world .", "- one", "- two", "docs (https://go.dev/doc)"} {
		if !strings.Contains(out.Content, want) {
			t.Errorf("html text missing %q:\n%s", want, out.Content)
		}
	}
	if strings.Contains(out.Content, "alert") || strings.Contains(out.Content, ".x{}") {
		t.Errorf("script/style leaked: %s", out.Content)
	}
	if out := f.fetch(ctx, allowAll(), srv.URL+"/json"); out.Content != `{"ok":true}` {
		t.Errorf("json: %+v", out)
	}
	if out := f.fetch(ctx, allowAll(), srv.URL+"/big"); !out.Truncated || len(out.Content) != 1000 {
		t.Errorf("size cap: truncated=%v len=%d", out.Truncated, len(out.Content))
	}
	if out := f.fetch(ctx, allowAll(), srv.URL+"/bin"); !strings.Contains(out.Error, "unsupported content type") {
		t.Errorf("binary: %+v", out)
	}
	if out := f.fetch(ctx, allowAll(), srv.URL+"/404"); out.Status != 404 || out.Error != "HTTP 404" {
		t.Errorf("404: %+v", out)
	}
}

func TestWebFetchValidationAndApproval(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	ctx := context.Background()
	f := testFetcher(WebFetchConfig{AllowPrivate: true, DenyDomains: []string{"*.evil.test"}})

	for _, bad := range []string{"file:///etc/passwd", "ftp://x.org/f", "http://", "http://user:pw@example.com/", "https://a.evil.test/x"} {
		if out := f.fetch(ctx, allowAll(), bad); out.Error == "" {
			t.Errorf("%q should be rejected", bad)
		}
	}

	// Non-allow-listed hosts need approval, keyed per host.
	h, reqs := approverHooks(false)
	if out := f.fetch(ctx, h, srv.URL); !strings.Contains(out.Error, "not approved") {
		t.Errorf("expected approval denial: %+v", out)
	}
	if len(*reqs) != 1 || (*reqs)[0].Key != "web:127.0.0.1" || (*reqs)[0].Kind != ActionNetwork {
		t.Errorf("unexpected approval request %+v", *reqs)
	}
	// Allow-listed hosts don't prompt.
	allowed := testFetcher(WebFetchConfig{AllowPrivate: true, AllowDomains: []string{"127.0.0.1"}})
	h, reqs = approverHooks(false)
	if out := allowed.fetch(ctx, h, srv.URL); out.Content != "ok" || len(*reqs) != 0 {
		t.Errorf("allow-listed fetch: %+v prompts=%d", out, len(*reqs))
	}

	// Network disabled by sandbox.
	off := newWebFetcher(WebFetchConfig{AllowPrivate: true})
	if out := off.fetch(ctx, allowAll(), srv.URL); !strings.Contains(out.Error, "disabled") {
		t.Errorf("expected network disabled: %+v", out)
	}
}

func TestWebFetchRedirects(t *testing.T) {
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("metadata-secret")) }))
	defer internal.Close()
	internalPort := netip.MustParseAddrPort(strings.TrimPrefix(internal.URL, "http://")).Port()

	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-internal":
			http.Redirect(w, r, internal.URL+"/", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/to-self":
			http.Redirect(w, r, "/final", http.StatusFound)
		default:
			w.Write([]byte("final page"))
		}
	}))
	defer public.Close()

	// Treat the "internal" server's port as a private destination.
	f := testFetcher(WebFetchConfig{AllowPrivate: true})
	f.allowAddr = func(ap netip.AddrPort) bool { return ap.Port() != internalPort }

	out := f.fetch(context.Background(), allowAll(), public.URL+"/to-internal")
	if out.Error == "" || strings.Contains(out.Content, "metadata-secret") {
		t.Errorf("redirect to internal address followed: %+v", out)
	}
	if !errors.Is(errors.New(out.Error), ErrBlockedAddress) && !strings.Contains(out.Error, "not a public address") && !strings.Contains(out.Error, "redirected") {
		t.Errorf("unexpected error: %s", out.Error)
	}
	if out := f.fetch(context.Background(), allowAll(), public.URL+"/loop"); !strings.Contains(out.Error, "redirects") {
		t.Errorf("redirect loop: %+v", out)
	}
	if out := f.fetch(context.Background(), allowAll(), public.URL+"/to-self"); out.Content != "final page" || !strings.HasSuffix(out.FinalURL, "/final") {
		t.Errorf("same-host redirect: %+v", out)
	}
}

func TestHTMLToText(t *testing.T) {
	got := htmlToText("<div>a</div><div>b</div><p></p><p></p><p>c</p><noscript>x</noscript>")
	if got != "a\nb\nc" {
		t.Errorf("htmlToText = %q", got)
	}
	if got := htmlToText("not html at all"); got != "not html at all" {
		t.Errorf("plain text = %q", got)
	}
}
