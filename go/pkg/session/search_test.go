package session

import (
	"slices"
	"strings"
	"testing"
)

func TestSearchTerms(t *testing.T) {
	got := SearchTerms(`retry "circuit breaker" Retry  backoff`)
	if !slices.Equal(got, []string{"circuit breaker", "retry", "backoff"}) {
		t.Fatalf("%q", got)
	}
}

func TestSearchRanksAndExcerpts(t *testing.T) {
	long := strings.Repeat("é", 400) + " the Circuit Breaker opens " + strings.Repeat("x", 400)
	msgs := []Message{
		{Role: "user", Content: "add a circuit breaker"},                  // 0: one term
		{Role: "model", Content: "done: breaker plus retry with backoff"}, // 1: breaker, retry, backoff
		{Role: "user", Content: "/search session breaker retry"},          // 2: an earlier search, skipped
		{Role: "user", Content: "unrelated"},                              // 3
		{Role: "model", Content: long},                                    // 4: one phrase hit
		{Role: "user", Content: "RETRY later"},                            // 5: one term
	}
	got, total := Search(msgs, `"circuit breaker" retry backoff`, 3)
	if total != 4 {
		t.Fatalf("total %d", total)
	}
	// Best first (1 has two terms), then the most recent single hits (5, 4),
	// shown oldest first.
	idx := []int{}
	for _, m := range got {
		idx = append(idx, m.Index)
	}
	if !slices.Equal(idx, []int{1, 4, 5}) {
		t.Fatalf("indexes %v", idx)
	}
	ex := got[1].Excerpt
	if !strings.HasPrefix(ex, "…") || !strings.HasSuffix(ex, "…") || !strings.Contains(ex, "Circuit Breaker opens") || !utf8Valid(ex) {
		t.Fatalf("excerpt %q", ex)
	}
	if len(ex) > 700*2 {
		t.Fatalf("excerpt too long: %d bytes", len(ex))
	}
	if got, total := Search(msgs, "   ", 5); got != nil || total != 0 {
		t.Fatal("empty query matched")
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }
