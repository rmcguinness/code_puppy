package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ShellSandboxMode selects OS-level sandboxing for shell commands.
type ShellSandboxMode string

const (
	// SandboxAuto sandboxes shell commands when the platform supports it.
	SandboxAuto ShellSandboxMode = "auto"
	// SandboxRequired refuses to start unless shell commands can be sandboxed.
	SandboxRequired ShellSandboxMode = "required"
	// SandboxOff runs shell commands without an OS sandbox.
	SandboxOff ShellSandboxMode = "off"
)

// OSSandboxSpec describes what sandboxed commands may do. Reads are allowed
// everywhere except blocked paths; writes only under WritableDirs.
type OSSandboxSpec struct {
	Mode         ShellSandboxMode
	WritableDirs []string // canonical absolute directories
	ReadOnlyDirs []string // never writable, even when inside a writable dir
	Blocked      *PathMatcher
	AllowNetwork bool
}

// OSSandbox wraps command argv so they run under the platform sandbox.
type OSSandbox struct {
	mode   ShellSandboxMode
	active bool
	reason string // why it is inactive
	wrapFn func(argv []string) []string
	spec   OSSandboxSpec
}

// sandboxWrapper rewrites a command's argv to run it sandboxed.
type sandboxWrapper = func(argv []string) []string

// platformSandbox returns a wrapper that sandboxes commands, or an error if
// this platform can't. Replaced in tests.
var platformSandbox = nativeSandbox

// prefixWrapper returns a wrapper that prepends a fixed argv prefix.
func prefixWrapper(prefix ...string) sandboxWrapper {
	return func(argv []string) []string {
		return append(append([]string(nil), prefix...), argv...)
	}
}

// NewOSSandbox sets up the shell sandbox. In "required" mode an unavailable
// sandbox is an error, so the CLI fails closed at startup rather than
// silently running commands unconfined.
func NewOSSandbox(spec OSSandboxSpec) (*OSSandbox, error) {
	switch spec.Mode {
	case "":
		spec.Mode = SandboxAuto
	case SandboxAuto, SandboxRequired, SandboxOff:
	default:
		return nil, fmt.Errorf("invalid sandbox.shell mode %q (use auto, required, or off)", spec.Mode)
	}
	s := &OSSandbox{mode: spec.Mode, spec: spec}
	if spec.Mode == SandboxOff {
		s.reason = "disabled by configuration"
		return s, nil
	}
	wrap, err := platformSandbox(spec)
	if err != nil {
		if spec.Mode == SandboxRequired {
			return nil, fmt.Errorf("sandbox.shell is \"required\" but the OS sandbox is unavailable: %w", err)
		}
		s.reason = err.Error()
		return s, nil
	}
	s.active, s.wrapFn = true, wrap
	return s, nil
}

// Active reports whether shell commands run sandboxed.
func (s *OSSandbox) Active() bool { return s != nil && s.active }

// Status describes the sandbox state for users.
func (s *OSSandbox) Status() string {
	switch {
	case s == nil:
		return "off"
	case s.active:
		net := "network blocked"
		if s.spec.AllowNetwork {
			net = "network allowed"
		}
		return fmt.Sprintf("on (writes limited to %d dirs, %s)", len(s.spec.WritableDirs), net)
	default:
		return "off: " + s.reason
	}
}

func (s *OSSandbox) wrap(argv []string) []string {
	if !s.Active() {
		return argv
	}
	return s.wrapFn(argv)
}

// DefaultShellWritableDirs returns scratch locations commands commonly need:
// the temp dirs and the user cache dir (e.g. the Go build cache).
func DefaultShellWritableDirs() []string {
	var dirs []string
	add := func(d string) {
		if d == "" {
			return
		}
		if real, err := canonicalDir(d); err == nil {
			dirs = append(dirs, real)
		}
	}
	tmp := os.TempDir()
	add(tmp)
	// macOS per-user temp lives in /var/folders/xx/yyy/T; its siblings (C, 0)
	// hold per-user caches that many tools write to.
	if strings.HasPrefix(tmp, "/var/folders/") || strings.HasPrefix(tmp, "/private/var/folders/") {
		add(filepath.Dir(filepath.Clean(tmp)))
	}
	add("/tmp")
	add("/var/tmp")
	if cache, err := os.UserCacheDir(); err == nil {
		add(cache)
	}
	return dirs
}

// seatbeltProfile renders a macOS sandbox profile for spec. Rule order
// matters: later rules override earlier ones, so blocked paths come last.
func seatbeltProfile(spec OSSandboxSpec) string {
	var sb strings.Builder
	sb.WriteString("(version 1)\n(allow default)\n(deny file-write*)\n(allow file-write*\n")
	for _, d := range spec.WritableDirs {
		fmt.Fprintf(&sb, "  (subpath %s)\n", sbplString(d))
	}
	sb.WriteString("  (literal \"/dev/null\") (literal \"/dev/zero\") (literal \"/dev/tty\") (literal \"/dev/dtracehelper\")\n")
	sb.WriteString("  (regex #\"^/dev/ttys[0-9]+$\") (regex #\"^/dev/fd/\"))\n")
	if len(spec.ReadOnlyDirs) > 0 {
		sb.WriteString("(deny file-write*\n")
		for _, d := range spec.ReadOnlyDirs {
			fmt.Fprintf(&sb, "  (subpath %s)\n", sbplString(d))
		}
		sb.WriteString(")\n")
	}
	if regexes := spec.Blocked.sbplRegexes(); len(regexes) > 0 {
		sb.WriteString("(deny file-read* file-write*\n")
		for _, re := range regexes {
			fmt.Fprintf(&sb, "  (regex #\"%s\")\n", strings.ReplaceAll(re, `"`, `\"`))
		}
		sb.WriteString(")\n")
	}
	if !spec.AllowNetwork {
		sb.WriteString("(deny network*)\n(allow network* (remote unix-socket))\n")
	}
	return sb.String()
}

func sbplString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
