package session

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Match is a transcript message that mentions a search's terms.
type Match struct {
	Index   int // position in the transcript
	Message Message
	Hits    int    // distinct terms found
	Excerpt string // the text around the first hit
}

// excerptRadius is how much text Search keeps on each side of a hit.
const excerptRadius = 300

var quotedRE = regexp.MustCompile(`"([^"]+)"`)

// SearchTerms splits a query into words and "quoted phrases".
func SearchTerms(query string) []string {
	var terms []string
	for _, m := range quotedRE.FindAllStringSubmatch(query, -1) {
		if t := strings.TrimSpace(m[1]); t != "" {
			terms = append(terms, t)
		}
	}
	terms = append(terms, strings.Fields(quotedRE.ReplaceAllString(query, " "))...)
	seen := map[string]bool{}
	return slices.DeleteFunc(terms, func(t string) bool {
		k := strings.ToLower(t)
		dup := seen[k]
		seen[k] = true
		return dup
	})
}

// Search finds the messages that mention any of query's terms, case
// insensitively, and returns at most limit of them: those with the most
// distinct terms first, then the most recent, presented oldest first.
// Earlier /search commands are skipped. total counts every match.
func Search(msgs []Message, query string, limit int) (matches []Match, total int) {
	var res []*regexp.Regexp
	for _, t := range SearchTerms(query) {
		res = append(res, regexp.MustCompile(`(?i)`+regexp.QuoteMeta(t)))
	}
	if len(res) == 0 {
		return nil, 0
	}
	for i, m := range msgs {
		if strings.HasPrefix(m.Content, "/search ") {
			continue
		}
		hits, first := 0, []int(nil)
		for _, re := range res {
			if loc := re.FindStringIndex(m.Content); loc != nil {
				hits++
				if first == nil || loc[0] < first[0] {
					first = loc
				}
			}
		}
		if hits > 0 {
			matches = append(matches, Match{Index: i, Message: m, Hits: hits, Excerpt: excerpt(m.Content, first[0], first[1])})
		}
	}
	total = len(matches)
	slices.SortStableFunc(matches, func(a, b Match) int {
		if a.Hits != b.Hits {
			return b.Hits - a.Hits
		}
		return b.Index - a.Index
	})
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	slices.SortFunc(matches, func(a, b Match) int { return a.Index - b.Index })
	return matches, total
}

// excerpt returns s around s[start:end], cut at rune boundaries, with "…"
// where text was left out.
func excerpt(s string, start, end int) string {
	from, to := max(0, start-excerptRadius), min(len(s), end+excerptRadius)
	for from > 0 && !utf8.RuneStart(s[from]) {
		from--
	}
	for to < len(s) && !utf8.RuneStart(s[to]) {
		to++
	}
	out := strings.TrimSpace(s[from:to])
	if from > 0 {
		out = "…" + out
	}
	if to < len(s) {
		out += "…"
	}
	return out
}
