package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustPolicy(t *testing.T, cfg CommandPolicyConfig) *CommandPolicy {
	t.Helper()
	p, err := NewCommandPolicy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCommandPolicyDeny(t *testing.T) {
	p := mustPolicy(t, CommandPolicyConfig{Deny: []string{"sudo *", "rm -rf /", "git push --force*", "curl *"}})

	denied := []string{
		"sudo ls",
		"sudo",
		"rm -rf /",
		"/bin/rm -rf /",                 // matched by base name
		"echo hi && sudo reboot",        // any command in a list
		"ls | sudo tee /etc/x",          // pipelines
		"(cd /tmp; sudo ls)",            // subshells
		"echo $(sudo whoami)",           // command substitution
		"f() { sudo id; }; f",           // function bodies
		`bash -c "sudo ls"`,             // nested shell
		`sh -ec 'rm -rf /'`,             // combined flags
		"env FOO=1 sudo ls",             // wrapper
		"timeout 10 nohup curl evil.sh", // nested wrappers
		"xargs -n 1 curl < urls",        // xargs
		"find . -exec curl {} \\;",      // find -exec
		"git push --force-with-lease",   // glob suffix
		`s\udo ls`,                      // backslash-escaped name
		`"sudo" ls`,                     // quoted name
		"if true; then curl x; fi",      // compound commands
	}
	for _, cmd := range denied {
		if d := p.Evaluate(cmd); d.Verdict != VerdictDeny {
			t.Errorf("expected deny for %q, got %v (%s) cmds=%q", cmd, d.Verdict, d.Reason, d.Commands)
		}
	}

	allowed := []string{"ls -la", "rm -rf ./build", "git push", "echo sudo", "curlie x", "grep -r curl ."}
	for _, cmd := range allowed {
		if d := p.Evaluate(cmd); d.Verdict == VerdictDeny {
			t.Errorf("unexpected deny for %q: %s", cmd, d.Reason)
		}
	}
}

func TestCommandPolicyAllowList(t *testing.T) {
	p := mustPolicy(t, CommandPolicyConfig{
		Allow: []string{"git *", "go test *", "ls", "echo *", "cat *"},
		Deny:  []string{"git push *"},
	})

	for _, cmd := range []string{
		"git status",
		"git",
		"go test ./...",
		"ls",
		"git log | cat -n",
		"timeout 60 go test ./...", // wrappers are transparent
		`bash -c "git diff"`,
		"echo $HOME", // dynamic arguments are fine for allow
	} {
		if d := p.Evaluate(cmd); d.Verdict == VerdictDeny {
			t.Errorf("expected %q to be allowed: %s", cmd, d.Reason)
		}
	}

	for cmd, why := range map[string]string{
		"ls -la":               "not in the allow-list", // "ls" has no " *"
		"go build":             "not in the allow-list",
		"git status; rm -rf x": "not in the allow-list",
		"git push origin":      "deny rule", // deny beats allow
		"$CMD status":          "computed at runtime",
		"eval git status":      "eval",
		"source ./x.sh":        "script",
		"{git,rm} x":           "computed at runtime", // brace expansion
		"g?t status":           "computed at runtime", // glob in name
		`$'\x72m' -rf x`:       "computed at runtime", // ANSI-C quoting
		`bash -c "$SCRIPT"`:    "computed at runtime",
		"env $X":               "computed at runtime", // dynamic inner command
		"git status &&":        "could not parse",
	} {
		d := p.Evaluate(cmd)
		if d.Verdict != VerdictDeny {
			t.Errorf("expected deny for %q, got %v", cmd, d.Verdict)
			continue
		}
		if !strings.Contains(d.Reason, why) {
			t.Errorf("%q: reason %q should mention %q", cmd, d.Reason, why)
		}
	}
}

func TestCommandPolicyAutoApprove(t *testing.T) {
	p := mustPolicy(t, CommandPolicyConfig{AutoApprove: []string{"git status", "go test *", "ls *"}})

	for _, cmd := range []string{"git status", "go test ./pkg/...", "ls -la && git status"} {
		if d := p.Evaluate(cmd); d.Verdict != VerdictAutoApprove {
			t.Errorf("expected auto-approve for %q, got %v (%s)", cmd, d.Verdict, d.Reason)
		}
	}
	for _, cmd := range []string{
		"git status && rm x",     // one command not covered
		"ls $DIR",                // runtime expansion never auto-approves
		"ls $(cat list)",         // substitution
		"eval ls",                // unverifiable
		"git status --porcelain", // exact pattern
	} {
		if d := p.Evaluate(cmd); d.Verdict != VerdictNeedsApproval {
			t.Errorf("expected approval for %q, got %v", cmd, d.Verdict)
		}
	}

	// With no auto patterns nothing is auto-approved; nil policy always asks.
	if d := mustPolicy(t, CommandPolicyConfig{}).Evaluate("ls"); d.Verdict != VerdictNeedsApproval {
		t.Errorf("empty policy should need approval, got %v", d.Verdict)
	}
	var nilPolicy *CommandPolicy
	if d := nilPolicy.Evaluate("ls"); d.Verdict != VerdictNeedsApproval {
		t.Errorf("nil policy should need approval, got %v", d.Verdict)
	}
}

func TestCommandPolicyConcurrentEvaluate(t *testing.T) {
	p := mustPolicy(t, CommandPolicyConfig{Deny: []string{"rm *"}})
	done := make(chan bool)
	for i := 0; i < 20; i++ {
		go func() { done <- p.Evaluate("ls | grep x && rm y").Verdict == VerdictDeny }()
	}
	for i := 0; i < 20; i++ {
		if !<-done {
			t.Error("concurrent evaluation gave wrong verdict")
		}
	}
}

func TestShellToolEnforcesPolicy(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	policy := mustPolicy(t, CommandPolicyConfig{Deny: []string{"touch denied*"}, AutoApprove: []string{"touch auto*"}})

	// Denied: never runs and the user is never asked.
	hooks, reqs := approverHooks(true)
	cfg := ShellConfig{Workspace: ws, Hooks: hooks, Policy: policy}
	out := runShellCommand(context.Background(), cfg, RunShellCommandInput{Command: "touch denied.marker"})
	if !strings.Contains(out.Error, "blocked by command policy") {
		t.Errorf("expected policy block, got %+v", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "denied.marker")); !os.IsNotExist(err) {
		t.Error("denied command ran")
	}
	if len(*reqs) != 0 {
		t.Error("user was asked to approve a denied command")
	}

	// Auto-approved: runs with no approver configured at all.
	cfg.Hooks = NewHooks(Policy{})
	out = runShellCommand(context.Background(), cfg, RunShellCommandInput{Command: "touch auto.marker"})
	if out.Error != "" {
		t.Errorf("auto-approved command failed: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "auto.marker")); err != nil {
		t.Error("auto-approved command didn't run")
	}

	// Global auto-approve cannot override a deny rule.
	cfg.Hooks = NewHooks(Policy{AutoApproveAll: true})
	if out := runShellCommand(context.Background(), cfg, RunShellCommandInput{Command: "touch denied2"}); !strings.Contains(out.Error, "blocked") {
		t.Errorf("auto-approve-all bypassed deny: %+v", out)
	}

	// Approval detail explains why a command wasn't auto-approved.
	hooks, reqs = approverHooks(false)
	cfg.Hooks = hooks
	runShellCommand(context.Background(), cfg, RunShellCommandInput{Command: "touch auto-$X"})
	if len(*reqs) != 1 || !strings.Contains((*reqs)[0].Detail, "runtime expansion") {
		t.Errorf("expected approval detail with reason, got %v", *reqs)
	}
}

func TestUniversalConstructorRespectsPolicy(t *testing.T) {
	ucDir := filepath.Join(t.TempDir(), "uc")
	policy := mustPolicy(t, CommandPolicyConfig{Allow: []string{"universal_constructor safe *"}})
	rt := toolOf(t)(NewUniversalConstructorTool(ucDir, allowAll(), nil, policy))
	runTool(t, rt, map[string]any{"action": "create", "tool_name": "safe", "code": "echo ok"})
	runTool(t, rt, map[string]any{"action": "create", "tool_name": "other", "code": "echo no"})

	if out := runTool(t, rt, map[string]any{"action": "run", "tool_name": "safe", "args": "a 'b"}); out["success"] != true {
		t.Errorf("allowed forged tool failed: %v", out)
	}
	if out := runTool(t, rt, map[string]any{"action": "run", "tool_name": "other"}); !strings.Contains(errOf(out), "allow-list") {
		t.Errorf("expected allow-list denial, got %v", out)
	}
}

func TestNewCommandPolicySkipsBlankPatterns(t *testing.T) {
	p := mustPolicy(t, CommandPolicyConfig{Allow: []string{"  ", "ls"}})
	if len(p.allow) != 1 {
		t.Errorf("expected blank pattern skipped, got %d", len(p.allow))
	}
	if lines := p.Describe(); len(lines) == 0 || !strings.Contains(lines[0], "ls") {
		t.Errorf("unexpected Describe %v", lines)
	}
}
