package tools

import (
	"strings"

	"github.com/aymanbagabas/go-udiff"
)

// unifiedDiff renders a unified diff of a file change ("" when unchanged).
func unifiedDiff(path, before, after string) string {
	if before == after {
		return ""
	}
	oldName, newName := "a/"+path, "b/"+path
	if before == "" {
		oldName = "/dev/null"
	}
	if after == "" {
		newName = "/dev/null"
	}
	return udiff.Unified(oldName, newName, before, after)
}

// diffStats counts added and removed lines in a unified diff.
func diffStats(diff string) (added, removed int) {
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}
