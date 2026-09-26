package workers

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retail-cortex/code_puppy/internal/tools"
)

func TestParseSchedule(t *testing.T) {
	for text, want := range map[string]string{
		"0 6 * * *":                 "0 6 * * *",
		"@daily":                    "@daily",
		"@every 90m":                "@every 90m",
		"Every two hours":           "0 */2 * * *",
		"every 5 hours":             "@every 5h",
		"every 15 minutes":          "*/15 * * * *",
		"every 7 minutes":           "@every 7m",
		"every minute":              "*/1 * * * *",
		"hourly":                    "@hourly",
		"Daily":                     "@daily",
		"Daily at 6 AM":             "0 6 * * *",
		"daily at 6:30 pm":          "30 18 * * *",
		"every day at 18:05":        "5 18 * * *",
		"Weekdays at 9:30":          "30 9 * * 1-5",
		"every weekday at 9 am":     "0 9 * * 1-5",
		"weekends at noon":          "0 12 * * 0,6",
		"Every Monday at 8 PM":      "0 20 * * 1",
		"on fridays at midnight":    "0 0 * * 5",
		"every 2 days":              "0 0 */2 * *",
		"daily at 12 am":            "0 0 * * *",
		"Every Sunday at 7:15 a.m.": "15 7 * * 0",
	} {
		s, err := ParseSchedule(text, "")
		if err != nil || s.Cron != want {
			t.Errorf("%q: cron %q, %v; want %q", text, s.Cron, err, want)
		}
	}
	for _, text := range []string{"", "sometimes", "every blue moon", "daily at 25:00", "daily at 13 pm", "every funday at 9", "* * *"} {
		if _, err := ParseSchedule(text, ""); err == nil {
			t.Errorf("%q was accepted", text)
		}
	}
	if _, err := ParseSchedule("@daily", "Mars/Olympus"); err == nil {
		t.Error("an unknown time zone was accepted")
	}
}

func TestScheduleNextUsesItsTimeZone(t *testing.T) {
	s, err := ParseSchedule("daily at 6 AM", "America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	chicago, _ := time.LoadLocation("America/Chicago")
	from := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) // 7 AM in Chicago
	want := time.Date(2026, 9, 27, 6, 0, 0, 0, chicago)
	if got := s.Next(from); !got.Equal(want) {
		t.Errorf("next %v, want %v", got, want)
	}
}

func writeWorker(t *testing.T, root, name, content string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const valid = `---
description: Summarise outdated dependencies
schedule: Daily at 6 AM
timezone: America/Chicago
permissions: ["shell:go list -m -u all", "write:reports/"]
limits: { max_turns: 30, max_cost_usd: 0.5, timeout: 20m }
---
Check for outdated Go modules and write reports/deps.md.
`

func TestLoadWorker(t *testing.T) {
	root := t.TempDir()
	dir := writeWorker(t, root, "deps", valid)
	w, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if w.Name != "deps" || w.Schedule.Cron != "0 6 * * *" || w.Schedule.Location.String() != "America/Chicago" ||
		len(w.Permissions) != 2 || w.Limits.MaxTurns != 30 || w.Limits.Timeout != 20*time.Minute ||
		w.Overlap != "skip" || w.CatchUp != "none" || !strings.HasPrefix(w.Prompt, "Check for outdated") || !strings.HasPrefix(w.Hash, "sha256:") {
		t.Errorf("worker %+v", w)
	}

	// Any change to the worker's files changes its hash.
	before := w.Hash
	os.WriteFile(filepath.Join(dir, "template.md"), []byte("# Report"), 0o644)
	if w2, _ := Load(dir); w2.Hash == before {
		t.Error("adding a file didn't change the hash")
	}
}

func TestLoadRejectsBadWorkers(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"no-frontmatter": "just text",
		"no-schedule":    "---\ndescription: x\n---\ndo it\n",
		"bad-schedule":   "---\nschedule: now and then\n---\ndo it\n",
		"no-workflow":    "---\nschedule: hourly\n---\n",
		"misspelled":     "---\nschedul: hourly\n---\ndo it\n",
		"wrong-name":     "---\nname: other\nschedule: hourly\n---\ndo it\n",
		"bad-permission": "---\nschedule: hourly\npermissions: [\"write:/etc/passwd\"]\n---\ndo it\n",
		"Bad_Name":       "---\nschedule: hourly\n---\ndo it\n",
		"bad-timeout":    "---\nschedule: hourly\nlimits: {timeout: soon}\n---\ndo it\n",
	} {
		_, err := Load(writeWorker(t, root, name, content))
		var invalid *InvalidError
		if err == nil || (!errors.As(err, &invalid) && name != "no-frontmatter" && name != "misspelled") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDiscover(t *testing.T) {
	root := t.TempDir()
	writeWorker(t, root, "deps", valid)
	writeWorker(t, root, "broken", "---\nschedule: whenever\n---\ndo it\n")
	os.MkdirAll(filepath.Join(root, "notes"), 0o755) // no WORKER.md: ignored
	list, err := Discover(root, filepath.Join(root, "missing"))
	if err != nil || len(list) != 2 {
		t.Fatalf("found %d workers, %v", len(list), err)
	}
	invalid := 0
	for _, f := range list {
		if f.Err != nil {
			invalid++
		}
	}
	if invalid != 1 {
		t.Errorf("%d invalid, want 1", invalid)
	}
}

func TestPermissions(t *testing.T) {
	var perms []Permission
	for _, s := range []string{"shell:go list -m -u all", "shell:git log *", "write:reports/", "delete:tmp/*.log", "web:proxy.golang.org", "mcp:github:create_*"} {
		p, err := ParsePermission(s)
		if err != nil {
			t.Fatal(err)
		}
		perms = append(perms, p)
	}
	req := func(kind tools.ActionKind, targets ...string) tools.ApprovalRequest {
		return tools.ApprovalRequest{Kind: kind, Targets: targets}
	}
	for _, c := range []struct {
		req  tools.ApprovalRequest
		want bool
	}{
		{req(tools.ActionCommand, "go list -m -u all"), true},
		{req(tools.ActionCommand, "go list -m -u all && rm -rf ~"), false},
		{req(tools.ActionCommand, "git log --oneline"), true},
		{req(tools.ActionWrite, "reports/deps.md"), true},
		{req(tools.ActionWrite, "reports/2026/deps.md"), true},
		{req(tools.ActionWrite, "reports/deps.md", "main.go"), false}, // every target must be covered
		{req(tools.ActionWrite, "main.go"), false},
		{req(tools.ActionDelete, "tmp/a.log"), true},
		{req(tools.ActionDelete, "tmp/sub/a.log"), false},
		{req(tools.ActionNetwork, "proxy.golang.org"), true},
		{req(tools.ActionNetwork, "evil.example"), false},
		{req(tools.ActionMCP, "github:create_issue"), true},
		{req(tools.ActionMCP, "github:delete_repo"), false},
		{req(tools.ActionWrite), false}, // nothing to match
	} {
		if got := Allows(perms, c.req); got != c.want {
			t.Errorf("%v %v: %v, want %v", c.req.Kind, c.req.Targets, got, c.want)
		}
	}
	for _, bad := range []string{"read:x", "shell", "write:../x", "write:/abs", "web:"} {
		if _, err := ParsePermission(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
