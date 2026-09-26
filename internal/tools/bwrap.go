package tools

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// bwrapArgs builds a bubblewrap command line mirroring the Seatbelt profile:
// the filesystem is read-only except WritableDirs, ReadOnlyDirs stay
// read-only even inside writable ones, blocked files are replaced by
// /dev/null and blocked directories by an empty read-only tmpfs, and the
// network namespace is unshared when network access is off. The returned
// slice ends with "--"; append the command to run.
func bwrapArgs(bwrap string, spec OSSandboxSpec, blockedFiles, blockedDirs []string) []string {
	args := []string{bwrap, "--die-with-parent", "--ro-bind", "/", "/", "--dev-bind", "/dev", "/dev"}
	for _, d := range existing(spec.WritableDirs) {
		args = append(args, "--bind", d, d)
	}
	for _, d := range existing(spec.ReadOnlyDirs) {
		args = append(args, "--ro-bind", d, d)
	}
	for _, f := range blockedFiles {
		args = append(args, "--ro-bind", "/dev/null", f)
	}
	for _, d := range blockedDirs {
		args = append(args, "--tmpfs", d, "--remount-ro", d)
	}
	if !spec.AllowNetwork {
		args = append(args, "--unshare-net")
	}
	return append(args, "--")
}

func existing(dirs []string) []string {
	var out []string
	for _, d := range dirs {
		if _, err := os.Stat(d); err == nil {
			out = append(out, d)
		}
	}
	return out
}

// Limits on how much of the filesystem is scanned to find blocked paths
// before each sandboxed command.
const (
	blockedScanMaxEntries = 50_000
	blockedScanMaxDepth   = 12
)

var blockedScanSkip = map[string]bool{".git": true, "node_modules": true, "vendor": true, "target": true, "__pycache__": true}

// expandBlocked resolves blocked-path patterns to concrete existing paths,
// since bubblewrap can only mask paths, not patterns. Absolute patterns are
// globbed directly; name patterns (".env", "*.pem") are found by scanning the
// writable and read-only roots.
func expandBlocked(spec OSSandboxSpec) (files, dirs []string) {
	m := spec.Blocked
	if m == nil || len(m.rules) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	add := func(p string, isDir bool) {
		if seen[p] {
			return
		}
		seen[p] = true
		if isDir {
			dirs = append(dirs, p)
		} else {
			files = append(files, p)
		}
	}

	for _, pat := range m.Patterns() {
		p := filepath.ToSlash(expandHomePattern(pat))
		if !strings.HasPrefix(p, "/") || strings.Contains(p, "**") {
			continue
		}
		matches, _ := filepath.Glob(filepath.FromSlash(p))
		for _, match := range matches {
			if info, err := os.Lstat(match); err == nil {
				add(match, info.IsDir())
			}
		}
	}

	count := 0
	roots := append(append([]string(nil), spec.WritableDirs...), spec.ReadOnlyDirs...)
	for _, root := range roots {
		base := strings.Count(root, string(filepath.Separator))
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			count++
			if count > blockedScanMaxEntries {
				return fs.SkipAll
			}
			if d.IsDir() && path != root && (blockedScanSkip[d.Name()] || strings.Count(path, string(filepath.Separator))-base > blockedScanMaxDepth) {
				return fs.SkipDir
			}
			if _, blocked := m.Match(path); blocked && path != root {
				add(path, d.IsDir())
				if d.IsDir() {
					return fs.SkipDir
				}
			}
			return nil
		})
	}
	sort.Strings(files)
	sort.Strings(dirs)
	return files, dirs
}

func expandHomePattern(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
