package tools

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Verdict is the outcome of evaluating a command against the policy.
type Verdict int

const (
	// VerdictNeedsApproval means the user must approve (subject to global auto-approve settings).
	VerdictNeedsApproval Verdict = iota
	// VerdictAutoApprove means every command matched an auto_approve pattern.
	VerdictAutoApprove
	// VerdictDeny means the command must not run.
	VerdictDeny
)

// CommandDecision explains a policy verdict.
type CommandDecision struct {
	Verdict  Verdict
	Reason   string   // why it was denied or not auto-approved
	Commands []string // every simple command found, as evaluated
}

// CommandPolicyConfig lists command patterns. A pattern is matched against a
// simple command with its words joined by single spaces: "*" matches any
// text, "?" one character, and a trailing " *" also matches the bare command
// ("git *" matches "git" and "git status"). A command given by path also
// matches by its base name ("/bin/rm -rf x" matches "rm *").
type CommandPolicyConfig struct {
	Allow       []string // if non-empty, only matching commands may run
	Deny        []string // never run; wins over Allow and AutoApprove
	AutoApprove []string // run without prompting
}

// CommandPolicy decides whether shell commands may run. Every simple command
// is checked: pipelines, lists, subshells, command substitutions, functions,
// "bash -c" strings, find -exec, and wrappers such as env, nohup, timeout and
// xargs are all unpacked.
//
// This is a guardrail, not a security boundary: an allowed interpreter or
// script can still do anything. Use it with the OS sandbox.
type CommandPolicy struct {
	allow, deny, auto []cmdPattern
}

type cmdPattern struct {
	raw string
	re  *regexp.Regexp
}

// NewCommandPolicy compiles the configured patterns.
func NewCommandPolicy(cfg CommandPolicyConfig) (*CommandPolicy, error) {
	compile := func(list []string) ([]cmdPattern, error) {
		var out []cmdPattern
		for _, raw := range list {
			p := strings.Join(strings.Fields(raw), " ")
			if p == "" {
				continue
			}
			base, anyArgs := p, false
			if strings.HasSuffix(p, " *") {
				base, anyArgs = strings.TrimSuffix(p, " *"), true
			}
			var sb strings.Builder
			sb.WriteString("^")
			for _, r := range base {
				switch r {
				case '*':
					sb.WriteString(".*")
				case '?':
					sb.WriteString(".")
				default:
					sb.WriteString(regexp.QuoteMeta(string(r)))
				}
			}
			if anyArgs {
				sb.WriteString("( .*)?")
			}
			sb.WriteString("$")
			re, err := regexp.Compile(sb.String())
			if err != nil {
				return nil, fmt.Errorf("invalid command pattern %q: %w", raw, err)
			}
			out = append(out, cmdPattern{raw: raw, re: re})
		}
		return out, nil
	}
	var p CommandPolicy
	var err error
	if p.allow, err = compile(cfg.Allow); err != nil {
		return nil, err
	}
	if p.deny, err = compile(cfg.Deny); err != nil {
		return nil, err
	}
	if p.auto, err = compile(cfg.AutoApprove); err != nil {
		return nil, err
	}
	return &p, nil
}

// Describe summarises the policy for display.
func (p *CommandPolicy) Describe() []string {
	if p == nil {
		return nil
	}
	join := func(ps []cmdPattern) string {
		var raw []string
		for _, x := range ps {
			raw = append(raw, x.raw)
		}
		return strings.Join(raw, ", ")
	}
	var lines []string
	if len(p.allow) > 0 {
		lines = append(lines, "allow only: "+join(p.allow))
	} else {
		lines = append(lines, "allow: any command not denied (with approval)")
	}
	if len(p.deny) > 0 {
		lines = append(lines, "deny: "+join(p.deny))
	}
	if len(p.auto) > 0 {
		lines = append(lines, "auto-approve: "+join(p.auto))
	}
	return lines
}

func matchAny(ps []cmdPattern, c simpleCommand) (string, bool) {
	for _, p := range ps {
		for _, s := range c.forms() {
			if p.re.MatchString(s) {
				return p.raw, true
			}
		}
	}
	return "", false
}

// simpleCommand is one command invocation extracted from a script.
type simpleCommand struct {
	words       []string
	dyn         []bool // per word: depends on runtime expansion
	unverified  string // non-empty if the command's identity can't be verified
	transparent bool   // a wrapper whose inner command is evaluated separately
}

func newSimpleCommand(words []string, dyn []bool) simpleCommand {
	c := simpleCommand{words: words, dyn: dyn}
	if len(dyn) > 0 && dyn[0] {
		c.unverified = "the command name is computed at runtime"
	}
	return c
}

func (c simpleCommand) dynamic() bool {
	for _, d := range c.dyn {
		if d {
			return true
		}
	}
	return false
}

func (c simpleCommand) String() string { return strings.Join(c.words, " ") }

// forms returns the strings patterns are matched against.
func (c simpleCommand) forms() []string {
	s := c.String()
	if len(c.words) > 0 && strings.Contains(c.words[0], "/") {
		base := append([]string{filepath.Base(c.words[0])}, c.words[1:]...)
		return []string{s, strings.Join(base, " ")}
	}
	return []string{s}
}

// Evaluate checks a shell script against the policy. A nil policy requires approval.
func (p *CommandPolicy) Evaluate(script string) CommandDecision {
	cmds, parseErr := extractCommands(script, 0)
	var rendered []string
	for _, c := range cmds {
		rendered = append(rendered, c.String())
	}
	d := CommandDecision{Verdict: VerdictNeedsApproval, Commands: rendered}
	if p == nil {
		return d
	}
	allowActive := len(p.allow) > 0

	if parseErr != nil {
		if allowActive {
			return CommandDecision{Verdict: VerdictDeny, Reason: "could not parse the command to check it against the allow-list: " + parseErr.Error(), Commands: rendered}
		}
		d.Reason = "could not parse command: " + parseErr.Error()
		return d
	}

	// Deny always wins, including for wrappers themselves.
	for _, c := range cmds {
		if pat, ok := matchAny(p.deny, c); ok {
			return CommandDecision{Verdict: VerdictDeny, Reason: fmt.Sprintf("%q matches deny rule %q", c.String(), pat), Commands: rendered}
		}
	}

	autoOK := len(p.auto) > 0
	for _, c := range cmds {
		if c.transparent {
			continue
		}
		if c.unverified != "" {
			if allowActive {
				return CommandDecision{Verdict: VerdictDeny, Reason: fmt.Sprintf("cannot verify %q against the allow-list: %s", c.String(), c.unverified), Commands: rendered}
			}
			autoOK = false
			d.Reason = c.unverified
			continue
		}
		if allowActive {
			if _, ok := matchAny(p.allow, c); !ok {
				return CommandDecision{Verdict: VerdictDeny, Reason: fmt.Sprintf("%q is not in the allow-list", c.String()), Commands: rendered}
			}
		}
		if autoOK {
			if c.dynamic() {
				autoOK = false
				d.Reason = fmt.Sprintf("%q uses runtime expansion", c.String())
			} else if _, ok := matchAny(p.auto, c); !ok {
				autoOK = false
			}
		}
	}
	if autoOK && len(cmds) > 0 {
		d.Verdict = VerdictAutoApprove
		d.Reason = ""
	}
	return d
}

const maxNestedShellDepth = 3

func extractCommands(script string, depth int) ([]simpleCommand, error) {
	if depth > maxNestedShellDepth {
		return []simpleCommand{{words: []string{"<nested shell>"}, unverified: "shell nesting too deep"}}, nil
	}
	// Parsers carry state and aren't safe for concurrent use; tools may run in parallel.
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), "")
	if err != nil {
		return nil, err
	}
	var cmds []simpleCommand
	var walkErr error
	syntax.Walk(file, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		words := make([]string, len(call.Args))
		dyn := make([]bool, len(call.Args))
		for i, w := range call.Args {
			words[i], dyn[i] = wordLiteral(w)
			if dyn[i] {
				words[i] = printWord(w)
			}
		}
		c := newSimpleCommand(words, dyn)
		inner, err := expandCommand(&c, depth)
		if err != nil {
			walkErr = err
			return false
		}
		cmds = append(cmds, c)
		cmds = append(cmds, inner...)
		return true // keep walking into command substitutions etc.
	})
	return cmds, walkErr
}

// interpreterEval lists builtins whose argument is code that can't be checked.
var interpreterEval = map[string]string{
	"eval":   "eval runs dynamically built code",
	"source": "source runs a script file",
	".":      "'.' runs a script file",
}

var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true}

// expandCommand marks wrappers transparent and returns the commands they run.
func expandCommand(c *simpleCommand, depth int) ([]simpleCommand, error) {
	if c.unverified != "" || len(c.words) == 0 {
		return nil, nil
	}
	name := filepath.Base(c.words[0])
	args, argDyn := c.words[1:], c.dyn[1:]

	if why, ok := interpreterEval[name]; ok {
		c.unverified = why
		return nil, nil
	}

	if shells[name] {
		for i, a := range args {
			if a == "-c" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "c")) {
				if i+1 >= len(args) {
					return nil, nil
				}
				if argDyn[i+1] {
					c.unverified = "the -c script is computed at runtime"
					return nil, nil
				}
				c.transparent = true
				return extractCommands(args[i+1], depth+1)
			}
		}
		return nil, nil
	}

	if name == "find" {
		var inner []simpleCommand
		for i := 0; i < len(args); i++ {
			switch args[i] {
			case "-exec", "-execdir", "-ok", "-okdir":
				j := i + 1
				for j < len(args) && args[j] != ";" && args[j] != "+" {
					j++
				}
				if j > i+1 {
					inner = append(inner, newSimpleCommand(args[i+1:j], argDyn[i+1:j]))
				}
				i = j
			}
		}
		return inner, nil
	}

	skip, ok := unwrap(name, args)
	if !ok || skip >= len(args) {
		return nil, nil
	}
	c.transparent = true
	inner := newSimpleCommand(args[skip:], argDyn[skip:])
	nested, err := expandCommand(&inner, depth)
	if err != nil {
		return nil, err
	}
	return append([]simpleCommand{inner}, nested...), nil
}

// unwrap returns the index in args where a known wrapper's command starts.
func unwrap(name string, args []string) (int, bool) {
	// Options that consume the following argument, per wrapper.
	withValue := map[string]string{
		"env":     "uSC",
		"nice":    "n",
		"timeout": "sk",
		"xargs":   "IinPLdEsa",
		"stdbuf":  "ioe",
		"exec":    "a",
		"sudo":    "ugUCDhprt",
		"doas":    "uC",
	}
	switch name {
	case "env", "nohup", "command", "builtin", "exec", "time", "nice", "timeout", "xargs", "stdbuf", "sudo", "doas", "caffeinate":
	default:
		return 0, false
	}
	takes := withValue[name]
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if name == "env" && strings.Contains(a, "=") && !strings.HasPrefix(a, "-") {
			i++
			continue
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			break
		}
		// "-n" "10" style: the option letter is last and takes a value.
		if len(a) == 2 && strings.ContainsRune(takes, rune(a[1])) {
			i += 2
			continue
		}
		i++
	}
	if name == "timeout" && i < len(args) {
		i++ // the duration
	}
	return i, true
}

// wordLiteral returns a word's value if it is fully static. Brace expansion
// and unquoted globs count as dynamic, since they change what actually runs.
func wordLiteral(w *syntax.Word) (string, bool) {
	var sb strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			if strings.ContainsAny(p.Value, "*?[") || hasBraceExpansion(p.Value) {
				return "", true
			}
			sb.WriteString(unescapeUnquoted(p.Value))
		case *syntax.SglQuoted:
			if p.Dollar {
				return "", true // $'..' escapes such as \x72
			}
			sb.WriteString(p.Value)
		case *syntax.DblQuoted:
			if p.Dollar {
				return "", true
			}
			for _, inner := range p.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return "", true
				}
				sb.WriteString(unescapeDoubleQuoted(lit.Value))
			}
		default:
			return "", true
		}
	}
	return sb.String(), false
}

func hasBraceExpansion(s string) bool {
	open := strings.IndexByte(s, '{')
	return open >= 0 && strings.IndexByte(s[open:], '}') > 0 &&
		(strings.Contains(s[open:], ",") || strings.Contains(s[open:], ".."))
}

func unescapeUnquoted(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			if s[i] == '\n' {
				continue
			}
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

func unescapeDoubleQuoted(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("$`\"\\\n", s[i+1]) >= 0 {
			i++
			if s[i] == '\n' {
				continue
			}
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

func printWord(w *syntax.Word) string {
	var sb strings.Builder
	if err := syntax.NewPrinter().Print(&sb, w); err != nil {
		return "<expansion>"
	}
	return sb.String()
}
