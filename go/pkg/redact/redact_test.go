package redact

import (
	"strings"
	"testing"
)

func TestRedactPatterns(t *testing.T) {
	r := New()
	secrets := []string{
		"sk-proj-abcdefghijklmnopqrstuvwxyz123456",
		"sk-ant-api03-abcdefghijklmnopqrstuvwxyz",
		"AKIAIOSFODNN7EXAMPLE",
		"AIzaSyA1234567890abcdefghijklmnopqrstuv",
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"xoxb-1234567890-abcdefghij",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----",
	}
	for _, s := range secrets {
		out := r.String("value: " + s + " end")
		if strings.Contains(out, s) || !strings.Contains(out, mask) {
			t.Errorf("secret not masked: %q -> %q", s, out)
		}
	}
	// Assignment style keeps the key, masks the value.
	if got := r.String(`export DB_PASSWORD="hunter2secret"`); got != `export DB_PASSWORD="`+mask+`"` {
		t.Errorf("assignment masking = %q", got)
	}
	// Negative: ordinary text is untouched.
	for _, s := range []string{"go test ./...", "the token count is 12", "password=", "sk-short", ""} {
		if got := r.String(s); got != s {
			t.Errorf("false positive: %q -> %q", s, got)
		}
	}
}

func TestRedactValuesAndEnv(t *testing.T) {
	t.Setenv("MY_SERVICE_API_KEY", "custom-secret-value-123")
	t.Setenv("SHORT_API_KEY", "abc") // too short to redact safely
	r := FromEnv([]string{"*_API_KEY"}, "another-literal-secret")

	in := "a custom-secret-value-123 b another-literal-secret c abc"
	out := r.String(in)
	if strings.Contains(out, "custom-secret-value-123") || strings.Contains(out, "another-literal-secret") {
		t.Errorf("literal secrets leaked: %q", out)
	}
	if !strings.HasSuffix(out, " abc") {
		t.Errorf("short value should not be redacted: %q", out)
	}

	nested := r.Value(map[string]any{"cmd": "curl -H custom-secret-value-123", "n": 3, "list": []any{"x", "another-literal-secret"}}).(map[string]any)
	if strings.Contains(nested["cmd"].(string), "custom-secret") || nested["n"] != 3 || nested["list"].([]any)[1] != mask {
		t.Errorf("nested redaction failed: %v", nested)
	}
	var nilR *Redactor
	if got := nilR.String("key sk-proj-abcdefghijklmnopqrstuvwxyz123456"); strings.Contains(got, "sk-proj") {
		t.Error("nil redactor should still apply built-in patterns")
	}
}
