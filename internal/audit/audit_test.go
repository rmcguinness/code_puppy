package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retail-cortex/code_puppy/internal/redact"
)

func readEntries(t *testing.T, path string) []Entry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		out = append(out, e)
	}
	return out
}

func TestAuditLogWritesRedactedEntries(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	l, err := Open(dir, redact.New("super-secret-value"))
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return fixed }
	l.SetContext("sess-1", "/ws")

	l.Log(Entry{Kind: KindToolCall, Tool: "run_shell_command", Args: map[string]any{"command": "curl -H 'Authorization: super-secret-value'"}})
	l.Log(Entry{Kind: KindDenial, Tool: "delete_file", Detail: "token=abcdefghij", Decision: "deny"})
	l.Close()

	path := filepath.Join(dir, "audit-2026-09-23.jsonl")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("audit file mode %v", info.Mode().Perm())
	}
	if d, _ := os.Stat(dir); d.Mode().Perm() != 0o700 {
		t.Errorf("audit dir mode %v", d.Mode().Perm())
	}
	entries := readEntries(t, path)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Session != "sess-1" || entries[0].Workspace != "/ws" {
		t.Errorf("context not recorded: %+v", entries[0])
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "super-secret-value") || strings.Contains(string(raw), "abcdefghij") {
		t.Errorf("secret written to audit log: %s", raw)
	}
}

func TestAuditRotatesDaily(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir, nil)
	day := time.Date(2026, 1, 1, 23, 59, 0, 0, time.UTC)
	l.now = func() time.Time { return day }
	l.Log(Entry{Kind: KindPrompt})
	day = day.Add(2 * time.Minute)
	l.Log(Entry{Kind: KindPrompt})
	l.Close()
	for _, name := range []string{"audit-2026-01-01.jsonl", "audit-2026-01-02.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing %s", name)
		}
	}
}

func TestNilLoggerIsNoOp(t *testing.T) {
	var l *Logger
	l.SetContext("a", "b")
	l.Log(Entry{Kind: KindPrompt})
	if err := l.Close(); err != nil {
		t.Error(err)
	}
	// Negative: an unwritable location fails at Open.
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o600)
	if _, err := Open(filepath.Join(file, "sub"), nil); err == nil {
		t.Error("expected error for unwritable dir")
	}
}
