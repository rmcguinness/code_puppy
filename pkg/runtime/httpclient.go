package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	openaioption "github.com/openai/openai-go/v3/option"
	"github.com/retail-cortex/code_puppy/pkg/config"
	"google.golang.org/genai"
)

const (
	defaultStallTimeout = 10 * time.Minute
	dialTimeout         = 30 * time.Second
	tlsTimeout          = 15 * time.Second
)

// ErrStalled is returned when a model API stops sending data.
var ErrStalled = errors.New("model API stopped responding")

// retryPolicy is how model clients retry and time out, from [llm] config.
type retryPolicy struct {
	maxRetries int           // retries after the first attempt
	stall      time.Duration // longest wait for headers or between body reads
}

func policyFrom(cfg config.LLMConfig) retryPolicy {
	p := retryPolicy{maxRetries: cfg.MaxRetries, stall: time.Duration(cfg.StallTimeoutSeconds) * time.Second}
	if p.maxRetries < 0 {
		p.maxRetries = 0
	}
	if p.stall <= 0 {
		p.stall = defaultStallTimeout
	}
	return p
}

// httpClient returns the client every model SDK uses. A request fails
// instead of hanging when connecting, the TLS handshake, the wait for
// response headers, or the gap between reads of the (streamed) body takes
// too long. The SDKs' retry logic then applies to connection-level failures.
//
// The stall limit also bounds the wait for headers, which for non-streamed
// requests is the whole generation, so it must exceed the longest
// generation expected (default 10 minutes).
func (p retryPolicy) httpClient() *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.DialContext = (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext
	base.TLSHandshakeTimeout = tlsTimeout
	base.ResponseHeaderTimeout = p.stall
	return &http.Client{Transport: &stallTransport{base: base, stall: p.stall}}
}

// stallTransport cancels a request whose response body goes quiet for
// longer than stall. Each successful read pushes the deadline back, so long
// streams that keep producing tokens are never cut off.
type stallTransport struct {
	base  http.RoundTripper
	stall time.Duration
}

func (t *stallTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(req.Context())
	resp, err := t.base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel(nil)
		return nil, err
	}
	b := &stallBody{rc: resp.Body, ctx: ctx, cancel: cancel, stall: t.stall}
	b.timer = time.AfterFunc(t.stall, func() { cancel(ErrStalled) })
	resp.Body = b
	return resp, nil
}

type stallBody struct {
	rc     io.ReadCloser
	ctx    context.Context
	cancel context.CancelCauseFunc
	stall  time.Duration
	timer  *time.Timer
	once   sync.Once
}

func (b *stallBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 {
		b.timer.Reset(b.stall)
	}
	if err != nil && errors.Is(context.Cause(b.ctx), ErrStalled) {
		return n, fmt.Errorf("%w: no data for %s", ErrStalled, b.stall)
	}
	return n, err
}

func (b *stallBody) Close() error {
	b.once.Do(func() {
		b.timer.Stop()
		b.cancel(nil)
	})
	return b.rc.Close()
}

// Backoff for Gemini, whose SDK doesn't retry unless asked. The Anthropic
// and OpenAI SDKs use their own backoff (0.5 s doubling to 8 s, with
// jitter, honouring Retry-After).
var (
	geminiInitialDelay = 1.0  // seconds
	geminiMaxDelay     = 30.0 // seconds
)

func (p retryPolicy) geminiConfig(apiKey string) *genai.ClientConfig {
	attempts := int32(p.maxRetries + 1)
	return &genai.ClientConfig{
		APIKey:     apiKey,
		HTTPClient: p.httpClient(),
		HTTPOptions: genai.HTTPOptions{RetryOptions: &genai.HTTPRetryOptions{
			Attempts:     &attempts,
			InitialDelay: &geminiInitialDelay,
			MaxDelay:     &geminiMaxDelay,
		}},
	}
}

func (p retryPolicy) anthropicOptions() []anthropicoption.RequestOption {
	return []anthropicoption.RequestOption{
		anthropicoption.WithHTTPClient(p.httpClient()),
		anthropicoption.WithMaxRetries(p.maxRetries),
	}
}

func (p retryPolicy) openAIOptions() []openaioption.RequestOption {
	return []openaioption.RequestOption{
		openaioption.WithHTTPClient(p.httpClient()),
		openaioption.WithMaxRetries(p.maxRetries),
	}
}
