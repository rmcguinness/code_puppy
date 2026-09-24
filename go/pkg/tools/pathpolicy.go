package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/retail-cortex/code_puppy/pkg/config"
)

// PathMatcher matches absolute paths against blocked-path glob patterns.
//
// Pattern forms (all match the path itself and everything beneath it):
//   - "name" or "*.pem"      (no slash): a file or directory name at any depth
//   - "~/.ssh", "/etc/x"     (absolute): that exact location
//   - "config/prod/*.yaml"   (relative with slash): relative to every sandbox root
//
// "*" matches within one path segment, "**" across segments, "?" one character.
type PathMatcher struct {
	rules []pathRule
}

type pathRule struct {
	pattern string
	re      *regexp.Regexp
	// sbpl is the same expression without Go-only syntax, for macOS sandbox profiles.
	sbpl string
}

// caseInsensitiveFS reports whether the default filesystem ignores case, in
// which case ".ENV" must be treated like ".env".
var caseInsensitiveFS = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// NewPathMatcher compiles patterns; relative patterns containing "/" are
// anchored at each of roots.
func NewPathMatcher(patterns []string, roots []string) (*PathMatcher, error) {
	m := &PathMatcher{}
	for _, raw := range patterns {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if strings.ContainsAny(p, "\"\n") {
			return nil, fmt.Errorf("invalid blocked path pattern %q", raw)
		}
		p = filepath.ToSlash(config.ExpandHome(p))
		p = strings.TrimSuffix(p, "/")

		var exprs []string
		switch {
		case strings.HasPrefix(p, "/"):
			// Match both the spelling given and its symlink-resolved form.
			exprs = append(exprs, "^"+globToRegex(p)+"(/.*)?$")
			if canon := canonicalGlobPrefix(p); canon != p {
				exprs = append(exprs, "^"+globToRegex(canon)+"(/.*)?$")
			}
		case !strings.Contains(p, "/"):
			exprs = append(exprs, "/"+globToRegex(p)+"(/.*)?$")
		default:
			for _, root := range roots {
				exprs = append(exprs, "^"+regexp.QuoteMeta(filepath.ToSlash(root))+"/"+globToRegex(strings.TrimPrefix(p, "./"))+"(/.*)?$")
			}
		}
		for _, expr := range exprs {
			goExpr := expr
			if caseInsensitiveFS {
				goExpr = "(?i)" + expr
			}
			re, err := regexp.Compile(goExpr)
			if err != nil {
				return nil, fmt.Errorf("invalid blocked path pattern %q: %w", raw, err)
			}
			m.rules = append(m.rules, pathRule{pattern: raw, re: re, sbpl: expr})
		}
	}
	return m, nil
}

// Match returns the pattern that blocks abs, if any.
func (m *PathMatcher) Match(abs string) (string, bool) {
	if m == nil {
		return "", false
	}
	p := filepath.ToSlash(abs)
	for _, r := range m.rules {
		if r.re.MatchString(p) {
			return r.pattern, true
		}
	}
	return "", false
}

// sbplRegexes returns the compiled expressions for a sandbox profile.
func (m *PathMatcher) sbplRegexes() []string {
	if m == nil {
		return nil
	}
	out := make([]string, 0, len(m.rules))
	for _, r := range m.rules {
		out = append(out, r.sbpl)
	}
	return out
}

// Patterns returns the configured patterns (deduplicated, in order).
func (m *PathMatcher) Patterns() []string {
	if m == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, r := range m.rules {
		if !seen[r.pattern] {
			seen[r.pattern] = true
			out = append(out, r.pattern)
		}
	}
	return out
}

// globToRegex converts a slash-separated glob to an unanchored regex body using
// only syntax understood by both Go and the macOS sandbox profile language.
func globToRegex(glob string) string {
	var sb strings.Builder
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case c == '*' && i+1 < len(glob) && glob[i+1] == '*':
			i++
			if i+1 < len(glob) && glob[i+1] == '/' {
				i++
				sb.WriteString("(.*/)?")
			} else {
				sb.WriteString(".*")
			}
		case c == '*':
			sb.WriteString("[^/]*")
		case c == '?':
			sb.WriteString("[^/]")
		default:
			sb.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return sb.String()
}

// canonicalGlobPrefix resolves symlinks in the literal (glob-free) directory
// prefix of an absolute pattern, so "/var/x" also matches "/private/var/x".
func canonicalGlobPrefix(p string) string {
	idx := strings.IndexAny(p, "*?")
	literal, rest := p, ""
	if idx >= 0 {
		cut := strings.LastIndex(p[:idx], "/")
		literal, rest = p[:cut], p[cut:]
	}
	if literal == "" {
		return p
	}
	resolved, err := resolveExistingPrefix(filepath.FromSlash(literal))
	if err != nil {
		return p
	}
	return filepath.ToSlash(resolved) + rest
}

// canonicalDir returns the absolute, symlink-resolved form of an existing directory.
func canonicalDir(dir string) (string, error) {
	abs, err := filepath.Abs(config.ExpandHome(dir))
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	return real, nil
}
