package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"runtime"
	"strings"

	"github.com/retail-cortex/code_puppy/pkg/textutil"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

const (
	grepMaxFileSize    = 5 * 1024 * 1024 // files larger than this are skipped
	grepBinarySniffLen = 8000            // bytes inspected for NUL to detect binaries
	grepMaxLineContent = 500             // bytes of a matching line returned to the model
)

// grepSkipDirs are directory names never descended into (in addition to dot-dirs).
var grepSkipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"__pycache__":  true,
}

// GrepMatch holds a single search match.
type GrepMatch struct {
	File       string `json:"file"`
	LineNumber int    `json:"line_number"`
	Content    string `json:"content"`
}

// GrepInput defines arguments for grep search.
type GrepInput struct {
	Query           string `json:"query" jsonschema:"The search term or regex pattern"`
	Path            string `json:"path,omitempty" jsonschema:"Optional directory or file path to search in"`
	IsRegex         bool   `json:"is_regex,omitempty" jsonschema:"Whether query is a regex pattern"`
	CaseInsensitive bool   `json:"case_insensitive,omitempty" jsonschema:"Whether search is case insensitive"`
	MaxMatches      int    `json:"max_matches,omitempty" jsonschema:"Max results to return"`
}

// GrepOutput holds grep search results.
type GrepOutput struct {
	Matches      []GrepMatch `json:"matches"`
	TotalMatches int         `json:"total_matches"`
	Error        string      `json:"error,omitempty"`
}

// NewGrepTool creates an ADK tool for workspace grep searching.
func NewGrepTool(ws *Workspace) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "grep",
			Description: "Search for pattern or literal string across workspace files",
		},
		func(ctx agent.Context, input GrepInput) (GrepOutput, error) {
			matches, err := grepWorkspace(ctx, ws, input)
			if err != nil {
				return GrepOutput{Error: err.Error()}, nil
			}
			return GrepOutput{Matches: matches, TotalMatches: len(matches)}, nil
		},
	)
}

// lineMatcher reports whether a line matches, with an optional whole-file
// prefilter that lets files without any match be skipped cheaply.
type lineMatcher struct {
	match     func(line []byte) bool
	prefilter func(data []byte) bool
}

func newLineMatcher(input GrepInput) (*lineMatcher, error) {
	if !input.IsRegex && !input.CaseInsensitive {
		needle := []byte(input.Query)
		contains := func(b []byte) bool { return bytes.Contains(b, needle) }
		return &lineMatcher{match: contains, prefilter: contains}, nil
	}

	pattern := input.Query
	if !input.IsRegex {
		pattern = regexp.QuoteMeta(pattern)
	}
	if input.CaseInsensitive {
		// Must be added after quoting, otherwise the flag itself gets escaped.
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %v", err)
	}
	m := &lineMatcher{match: re.Match}
	if !input.IsRegex {
		// Literal patterns have no anchors, so a whole-file check is equivalent.
		m.prefilter = re.Match
	}
	return m, nil
}

type grepFileResult struct {
	matches []GrepMatch
}

func grepWorkspace(ctx context.Context, ws *Workspace, input GrepInput) ([]GrepMatch, error) {
	if input.Query == "" {
		return nil, fmt.Errorf("query must not be empty")
	}
	searchPath := input.Path
	if searchPath == "" {
		searchPath = "."
	}
	max := input.MaxMatches
	if max <= 0 || max > 500 {
		max = 100
	}
	matcher, err := newLineMatcher(input)
	if err != nil {
		return nil, err
	}

	// Collect candidate files first; walking is cheap relative to reading.
	// Blocked paths are filtered out by Walk.
	var files []WalkEntry
	walkErr := ws.Walk(searchPath, func(e WalkEntry) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d := e.Entry
		if d.IsDir() {
			// Never skip the starting directory itself (its name may be ".").
			if !e.IsRoot && (strings.HasPrefix(d.Name(), ".") || grepSkipDirs[d.Name()]) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			files = append(files, e)
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("search error: %v", walkErr)
	}

	// Scan files in parallel, but consume results in walk order so the first
	// max matches are deterministic. The producer hands per-file result
	// channels to the consumer through a bounded queue, so at most a small
	// window of files is in flight regardless of repository size. Result
	// channels are buffered so workers never block; returning early cancels
	// scanCtx, which unblocks the producer and stops outstanding workers.
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	workers := min(runtime.GOMAXPROCS(0), 8)
	sem := make(chan struct{}, workers)
	order := make(chan chan grepFileResult, workers*2)

	go func() {
		defer close(order)
		for _, entry := range files {
			ch := make(chan grepFileResult, 1)
			select {
			case order <- ch:
			case <-scanCtx.Done():
				return
			}
			select {
			case sem <- struct{}{}:
			case <-scanCtx.Done():
				close(ch)
				return
			}
			go func(entry WalkEntry) {
				defer func() { <-sem }()
				defer close(ch)
				if scanCtx.Err() != nil {
					return
				}
				ch <- grepFileResult{matches: grepFile(scanCtx, entry, matcher, max)}
			}(entry)
		}
	}()

	matches := make([]GrepMatch, 0, 16)
	for ch := range order {
		res, ok := <-ch
		if !ok {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		for _, m := range res.matches {
			matches = append(matches, m)
			if len(matches) >= max {
				return matches, nil
			}
		}
	}
	return matches, nil
}

// grepFile returns up to max matches from a single file. Unreadable, oversized,
// and binary files yield no matches.
func grepFile(ctx context.Context, entry WalkEntry, m *lineMatcher, max int) []GrepMatch {
	f, err := entry.Open()
	if err != nil {
		return nil
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() > grepMaxFileSize {
		return nil
	}
	// Reading the whole (bounded) file avoids bufio.Scanner's line-length limit.
	data, err := io.ReadAll(io.LimitReader(f, grepMaxFileSize))
	if err != nil {
		return nil
	}
	if bytes.IndexByte(data[:min(len(data), grepBinarySniffLen)], 0) >= 0 {
		return nil
	}
	if m.prefilter != nil && !m.prefilter(data) {
		return nil
	}

	var out []GrepMatch
	lineNum := 0
	for len(data) > 0 {
		lineNum++
		if lineNum%4096 == 0 && ctx.Err() != nil {
			return out
		}
		var line []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			line, data = data, nil
		}
		line = bytes.TrimSuffix(line, []byte("\r"))
		if m.match(line) {
			out = append(out, GrepMatch{
				File:       entry.Path,
				LineNumber: lineNum,
				Content:    textutil.Ellipsize(strings.TrimSpace(string(line)), grepMaxLineContent),
			})
			if len(out) >= max {
				return out
			}
		}
	}
	return out
}
