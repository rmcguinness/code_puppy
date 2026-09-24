package tools

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

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
func NewGrepTool(workspaceDir string) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "grep",
			Description: "Search for pattern or literal string across workspace files",
		},
		func(ctx agent.Context, input GrepInput) (GrepOutput, error) {
			if input.Query == "" {
				return GrepOutput{Error: "query must not be empty"}, nil
			}

			searchPath := input.Path
			if searchPath == "" {
				searchPath = "."
			}
			resolvedPath := resolveSafePath(workspaceDir, searchPath)

			max := input.MaxMatches
			if max <= 0 || max > 500 {
				max = 100
			}

			var re *regexp.Regexp
			var err error
			pattern := input.Query
			if input.CaseInsensitive {
				pattern = "(?i)" + pattern
			}

			if input.IsRegex {
				re, err = regexp.Compile(pattern)
			} else {
				re, err = regexp.Compile(regexp.QuoteMeta(pattern))
			}

			if err != nil {
				return GrepOutput{Error: fmt.Sprintf("invalid regex pattern: %v", err)}, nil
			}

			var matches []GrepMatch
			walkErr := filepath.WalkDir(resolvedPath, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					if d != nil && d.IsDir() {
						name := d.Name()
						if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "target" || name == "__pycache__" {
							return filepath.SkipDir
						}
					}
					return nil
				}

				// Check file size limit (skip files > 5MB)
				info, err := d.Info()
				if err != nil || info.Size() > 5*1024*1024 {
					return nil
				}

				f, err := os.Open(path)
				if err != nil {
					return nil
				}
				defer f.Close()

				scanner := bufio.NewScanner(f)
				lineNum := 1
				relPath, _ := filepath.Rel(workspaceDir, path)

				for scanner.Scan() {
					line := scanner.Text()
					if re.MatchString(line) {
						matches = append(matches, GrepMatch{
							File:       relPath,
							LineNumber: lineNum,
							Content:    strings.TrimSpace(line),
						})
						if len(matches) >= max {
							return fs.SkipAll
						}
					}
					lineNum++
				}
				return nil
			})

			if walkErr != nil {
				return GrepOutput{Error: fmt.Sprintf("search error: %v", walkErr)}, nil
			}

			return GrepOutput{
				Matches:      matches,
				TotalMatches: len(matches),
			}, nil
		},
	)
}
