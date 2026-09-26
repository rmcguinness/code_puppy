package images

import (
	"regexp"
	"strings"
)

// mentionRE matches @path or @"path with spaces" at the start of the text or
// after whitespace (so e-mail addresses don't count).
var mentionRE = regexp.MustCompile(`(?:^|\s)@(?:"([^"]+)"|(\S+))`)

// Mentions returns the image files referenced as @path in a prompt, in
// order and without duplicates. Other @paths are left for the model.
func Mentions(prompt string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range mentionRE.FindAllStringSubmatch(prompt, -1) {
		p := m[1]
		if p == "" {
			p = strings.TrimRight(m[2], ".,;:!?)")
		}
		if IsImagePath(p) && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
