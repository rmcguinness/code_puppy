// Package textutil holds small string helpers shared by tools and the TUI.
package textutil

import (
	"strings"
	"unicode/utf8"
)

// TruncateUTF8 returns s cut to at most n bytes without splitting a multi-byte rune.
func TruncateUTF8(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// TrimPartialRune drops an incomplete multi-byte rune from the end of s, as
// left behind when a byte stream is cut at an arbitrary offset.
func TrimPartialRune(s string) string {
	for i := len(s) - 1; i >= 0 && i >= len(s)-utf8.UTFMax; i-- {
		if utf8.RuneStart(s[i]) {
			if !utf8.FullRuneInString(s[i:]) {
				return s[:i]
			}
			return s
		}
	}
	return s
}

// Ellipsize truncates s to at most n bytes (UTF-8 safe), appending "..." when cut.
func Ellipsize(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return TruncateUTF8(s, n)
	}
	return TruncateUTF8(s, n-3) + "..."
}

// SanitizeTerminal strips control characters that a terminal would interpret
// (ESC sequences, BEL, backspace, carriage return, C1 controls) so untrusted
// text from models, files, or tool output cannot rewrite the screen, change the
// window title, or write to the clipboard via OSC 52. Newlines and tabs are kept.
// Invalid UTF-8 is replaced with U+FFFD.
func SanitizeTerminal(s string) string {
	clean := true
	for _, r := range s {
		if isUnsafeControl(r) || r == utf8.RuneError {
			clean = false
			break
		}
	}
	if clean {
		return s
	}

	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		switch {
		case r == utf8.RuneError:
			sb.WriteRune('�')
		case isUnsafeControl(r):
			// drop
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func isUnsafeControl(r rune) bool {
	if r == '\n' || r == '\t' {
		return false
	}
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}
