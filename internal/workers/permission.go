package workers

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/retail-cortex/blitz/internal/tools"
)

// Permission is something a worker may do without approval, as written in
// its frontmatter: "<kind>:<pattern>".
//
//	shell:go test ./...     a command, exactly, or a glob ("go list *")
//	write:reports/*.md      creating or editing workspace files (glob)
//	delete:tmp/*            deleting workspace files (glob)
//	web:proxy.golang.org    fetching from a host, or searching with a provider
//	mcp:github:create_issue an MCP tool, as server:tool (glob)
//
// Globs use path.Match, where * doesn't cross "/"; a pattern ending in "/"
// covers everything under that directory. A shell glob never covers a
// command that chains, pipes, redirects or substitutes (see shellMeta):
// "go list *" must not cover "go list x; rm -rf ~". Such a command is
// covered only by a permission that is exactly it. Reading never needs
// permission.
// Anything a worker isn't permitted is refused and recorded.
type Permission struct {
	Kind    string // shell, write, delete, web, mcp
	Pattern string
}

func (p Permission) String() string { return p.Kind + ":" + p.Pattern }

var permissionKinds = map[string]tools.ActionKind{
	"shell":  tools.ActionCommand,
	"write":  tools.ActionWrite,
	"delete": tools.ActionDelete,
	"web":    tools.ActionNetwork,
	"mcp":    tools.ActionMCP,
}

// ParsePermission reads "<kind>:<pattern>".
func ParsePermission(s string) (Permission, error) {
	kind, pattern, ok := strings.Cut(strings.TrimSpace(s), ":")
	kind = strings.ToLower(strings.TrimSpace(kind))
	pattern = strings.TrimSpace(pattern)
	if _, known := permissionKinds[kind]; !ok || !known || pattern == "" {
		return Permission{}, fmt.Errorf(`permission %q: use kind:pattern with kind shell, write, delete, web or mcp`, s)
	}
	if kind == "write" || kind == "delete" {
		if strings.HasPrefix(pattern, "/") || strings.Contains("/"+pattern+"/", "/../") {
			return Permission{}, fmt.Errorf("permission %q: paths are relative to the workspace and can't use \"..\"", s)
		}
	}
	if _, err := path.Match(pattern, ""); err != nil {
		return Permission{}, fmt.Errorf("permission %q: %w", s, err)
	}
	return Permission{Kind: kind, Pattern: pattern}, nil
}

// Allows reports whether the permissions cover the request: its kind has a
// permission matching every one of its targets. A request without targets
// is never covered.
func Allows(perms []Permission, req tools.ApprovalRequest) bool {
	if len(req.Targets) == 0 {
		return false
	}
	for _, target := range req.Targets {
		if !covered(perms, req.Kind, target) {
			return false
		}
	}
	return true
}

// shellMeta are the characters that make one shell command several, or
// run something else: separators, pipes, redirection, substitution,
// escapes and line breaks.
const shellMeta = ";&|`$()<>\\\n\r"

func covered(perms []Permission, kind tools.ActionKind, target string) bool {
	switch kind {
	case tools.ActionWrite, tools.ActionDelete:
		target = filepath.ToSlash(target)
		if target != path.Clean(target) || strings.HasPrefix(target, "/") || target == ".." || strings.HasPrefix(target, "../") {
			return false // outside the workspace, or not as the tools give it
		}
	}
	chained := kind == tools.ActionCommand && strings.ContainsAny(target, shellMeta)
	for _, p := range perms {
		if permissionKinds[p.Kind] != kind {
			continue
		}
		if p.Pattern == target {
			return true
		}
		if chained {
			continue
		}
		if strings.HasSuffix(p.Pattern, "/") && strings.HasPrefix(target, p.Pattern) {
			return true
		}
		if ok, _ := path.Match(p.Pattern, target); ok {
			return true
		}
	}
	return false
}
