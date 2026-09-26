package textutil

import (
	"testing"
	"unicode/utf8"
)

func TestTruncateUTF8(t *testing.T) {
	// Positive: ASCII shorter than limit is untouched; longer is cut exactly.
	if got := TruncateUTF8("hello", 10); got != "hello" {
		t.Errorf("expected unchanged, got %q", got)
	}
	if got := TruncateUTF8("hello", 3); got != "hel" {
		t.Errorf("expected 'hel', got %q", got)
	}

	// Negative: cutting inside a multi-byte rune must back up to a rune boundary.
	s := "ab🐶cd" // 🐶 is 4 bytes at offset 2
	for n := 3; n <= 5; n++ {
		got := TruncateUTF8(s, n)
		if !utf8.ValidString(got) {
			t.Errorf("n=%d produced invalid UTF-8 %q", n, got)
		}
		if got != "ab" {
			t.Errorf("n=%d expected 'ab', got %q", n, got)
		}
	}
	if got := TruncateUTF8(s, 0); got != "" {
		t.Errorf("expected empty for n=0, got %q", got)
	}
}

func TestEllipsize(t *testing.T) {
	if got := Ellipsize("short", 10); got != "short" {
		t.Errorf("expected unchanged, got %q", got)
	}
	got := Ellipsize("🐶🐶🐶🐶🐶", 10)
	if !utf8.ValidString(got) || len(got) > 10 || got[len(got)-3:] != "..." {
		t.Errorf("bad ellipsized value %q", got)
	}
}

func TestSanitizeTerminal(t *testing.T) {
	// Positive: ordinary text, newlines, tabs and emoji survive untouched.
	plain := "line1\n\tline2 🐶 ✅"
	if got := SanitizeTerminal(plain); got != plain {
		t.Errorf("expected plain text unchanged, got %q", got)
	}

	// Negative: escape sequences and control characters are stripped.
	cases := map[string]string{
		"\x1b]52;c;ZXZpbA==\x07ok": "]52;c;ZXZpbA==ok", // OSC 52 clipboard write
		"\x1b[2J\x1b[Hcleared":     "[2J[Hcleared",
		"fake\rreal":               "fakereal",
		"bell\x07":                 "bell",
		"c1\u009bcontrol":          "c1control",
		"del\x7f":                  "del",
		"bad\xffbyte":              "bad�byte",
	}
	for in, want := range cases {
		if got := SanitizeTerminal(in); got != want {
			t.Errorf("SanitizeTerminal(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTrimPartialRune(t *testing.T) {
	// Positive: complete strings are untouched.
	for _, s := range []string{"", "abc", "ab🐶", "é"} {
		if got := TrimPartialRune(s); got != s {
			t.Errorf("TrimPartialRune(%q) = %q, want unchanged", s, got)
		}
	}
	// Negative: a rune cut mid-sequence is removed.
	dog := "ab🐶"
	for cut := 3; cut < len(dog); cut++ {
		if got := TrimPartialRune(dog[:cut]); got != "ab" {
			t.Errorf("TrimPartialRune(%q) = %q, want 'ab'", dog[:cut], got)
		}
	}
}
