// Package redact masks secrets in text before it is written to disk.
package redact

import (
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
)

const mask = "[REDACTED]"

// patterns match common credential formats. Each is replaced whole, except
// assignment-style matches, where only the value is masked.
var patterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                                            // AWS access key id
	regexp.MustCompile(`\bsk-(?:ant-|proj-)?[A-Za-z0-9_\-]{20,}`),                         // OpenAI / Anthropic
	regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`),                                      // Google API key
	regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{30,}\b`),                    // GitHub tokens
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{40,}\b`),                                // GitHub fine-grained
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9\-]{10,}`),                                 // Slack
	regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`), // JWT
}

// assignment masks the value in "password=...", "api_key: ...", etc.
var assignment = regexp.MustCompile(`(?i)\b([A-Za-z0-9_\-]*(?:password|passwd|secret|api[_\-]?key|access[_\-]?key|token)[A-Za-z0-9_\-]*)(\s*[:=]\s*["']?)([^\s"',;]{6,})`)

// Redactor masks known secret formats plus specific secret values.
type Redactor struct {
	values []string // exact secrets, longest first
}

// New returns a redactor that also masks the given literal values (ignoring
// ones shorter than 8 characters, which would cause false positives).
func New(values ...string) *Redactor {
	r := &Redactor{}
	seen := map[string]bool{}
	for _, v := range values {
		if len(v) >= 8 && !seen[v] {
			seen[v] = true
			r.values = append(r.values, v)
		}
	}
	sort.Slice(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
	return r
}

// FromEnv returns a redactor for the values of environment variables whose
// names match any of the glob patterns (case-insensitive), e.g. "*_API_KEY".
func FromEnv(namePatterns []string, extra ...string) *Redactor {
	var values []string
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		for _, p := range namePatterns {
			if ok, _ := path.Match(strings.ToUpper(p), strings.ToUpper(name)); ok {
				values = append(values, value)
				break
			}
		}
	}
	return New(append(values, extra...)...)
}

// String masks secrets in s. A nil Redactor still applies the built-in patterns.
func (r *Redactor) String(s string) string {
	if s == "" {
		return s
	}
	if r != nil {
		for _, v := range r.values {
			s = strings.ReplaceAll(s, v, mask)
		}
	}
	for _, re := range patterns {
		s = re.ReplaceAllString(s, mask)
	}
	return assignment.ReplaceAllString(s, "${1}${2}"+mask)
}

// Value masks secrets inside strings nested in maps and slices (e.g. tool
// arguments), returning a copy.
func (r *Redactor) Value(v any) any {
	switch t := v.(type) {
	case string:
		return r.String(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = r.Value(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = r.Value(x)
		}
		return out
	default:
		return v
	}
}
