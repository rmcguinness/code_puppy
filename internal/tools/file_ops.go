package tools

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// maxReadOutputBytes caps how much file content read_file returns in one call.
const maxReadOutputBytes = 512 * 1024

// ReadFileInput defines arguments for reading a file.
type ReadFileInput struct {
	Path      string `json:"path" jsonschema:"The absolute or workspace-relative path to the file"`
	StartLine int    `json:"start_line,omitempty" jsonschema:"Optional 1-indexed starting line number"`
	EndLine   int    `json:"end_line,omitempty" jsonschema:"Optional 1-indexed ending line number"`
}

// ReadFileOutput holds the result of reading a file.
type ReadFileOutput struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	TotalLines int    `json:"total_lines"`
	Truncated  bool   `json:"truncated,omitempty"`
	Error      string `json:"error,omitempty"`
}

// NewReadFileTool creates an ADK tool for reading files.
func NewReadFileTool(ws *Workspace) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "read_file",
			Description: "Read contents of a file with line numbers and optional line slicing",
		},
		func(ctx agent.Context, input ReadFileInput) (ReadFileOutput, error) {
			out, err := readFileLines(ws, input)
			if err != nil {
				return ReadFileOutput{Path: input.Path, Error: fmt.Sprintf("failed to read file: %v", err)}, nil
			}
			return out, nil
		},
	)
}

// readFileLines streams the file once, keeping only the requested line range.
func readFileLines(ws *Workspace, input ReadFileInput) (ReadFileOutput, error) {
	rel, err := ws.Rel(input.Path)
	if err != nil {
		return ReadFileOutput{}, err
	}
	f, err := ws.Open(rel)
	if err != nil {
		return ReadFileOutput{}, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return ReadFileOutput{}, err
	}
	if info.IsDir() {
		return ReadFileOutput{}, fmt.Errorf("%s is a directory", input.Path)
	}
	if info.Size() > ws.MaxFileSize() {
		return ReadFileOutput{}, fmt.Errorf("file is %d bytes, exceeding the %d byte limit; use grep or run_shell_command with head/tail instead", info.Size(), ws.MaxFileSize())
	}

	start := 1
	if input.StartLine > 1 {
		start = input.StartLine
	}
	end := input.EndLine // 0 means "to EOF"
	if end > 0 && end < start {
		end = start
	}

	var (
		sb        strings.Builder
		lineNo    int
		truncated bool
		numBuf    []byte
		lastLine  string
	)
	writeLine := func(n int, text string) bool {
		if sb.Len()+len(text)+8 > maxReadOutputBytes {
			return false
		}
		numBuf = strconv.AppendInt(numBuf[:0], int64(n), 10)
		for pad := 4 - len(numBuf); pad > 0; pad-- {
			sb.WriteByte(' ')
		}
		sb.Write(numBuf)
		sb.WriteString(": ")
		sb.WriteString(text)
		sb.WriteByte('\n')
		return true
	}
	r := bufio.NewReaderSize(io.LimitReader(f, ws.MaxFileSize()), 64*1024)
	for {
		line, readErr := r.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			return ReadFileOutput{}, readErr
		}
		// A trailing "\n" yields a final empty line, matching strings.Split semantics.
		lineNo++
		text := strings.TrimSuffix(line, "\n")
		inRange := lineNo >= start && (end <= 0 || lineNo <= end)
		if inRange && !truncated && !writeLine(lineNo, text) {
			truncated = true
		}
		if readErr == io.EOF {
			lastLine = text
			break
		}
	}
	totalLines := lineNo

	// Out-of-range starts clamp to the last line.
	if start > totalLines && !writeLine(totalLines, lastLine) {
		truncated = true
	}

	content := sb.String()
	if truncated {
		content += fmt.Sprintf("\n... [output truncated at %d bytes; use start_line/end_line to page]\n", maxReadOutputBytes)
	}
	return ReadFileOutput{
		Path:       input.Path,
		Content:    content,
		TotalLines: totalLines,
		Truncated:  truncated,
	}, nil
}

// FileEntry represents a file in directory listing.
type FileEntry struct {
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size_bytes"`
}

// ListFilesInput defines arguments for listing directory files.
type ListFilesInput struct {
	Directory  string `json:"directory,omitempty" jsonschema:"Directory path relative to workspace or absolute"`
	Recursive  bool   `json:"recursive,omitempty" jsonschema:"Whether to list files recursively"`
	MaxEntries int    `json:"max_entries,omitempty" jsonschema:"Maximum number of file entries to return"`
}

// ListFilesOutput holds listed files.
type ListFilesOutput struct {
	Directory  string      `json:"directory"`
	Files      []FileEntry `json:"files"`
	TotalCount int         `json:"total_count"`
	Error      string      `json:"error,omitempty"`
}

// NewListFilesTool creates an ADK tool for listing directory files.
func NewListFilesTool(ws *Workspace) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "list_files",
			Description: "List files and subdirectories within a directory path",
		},
		func(ctx agent.Context, input ListFilesInput) (ListFilesOutput, error) {
			dirPath := input.Directory
			if dirPath == "" {
				dirPath = "."
			}
			max := input.MaxEntries
			if max <= 0 || max > 500 {
				max = 200
			}

			files := make([]FileEntry, 0, 32)
			walkErr := ws.Walk(dirPath, func(e WalkEntry) error {
				if e.IsRoot {
					return nil
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				d := e.Entry
				name := d.Name()
				if strings.HasPrefix(name, ".") {
					if d.IsDir() {
						return fs.SkipDir
					}
					return nil
				}

				var size int64
				if info, err := d.Info(); err == nil {
					size = info.Size()
				}
				files = append(files, FileEntry{Path: e.Path, IsDir: d.IsDir(), Size: size})

				if len(files) >= max {
					return fs.SkipAll
				}
				if !input.Recursive && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			})

			if walkErr != nil {
				return ListFilesOutput{Directory: dirPath, Error: fmt.Sprintf("failed to list directory: %v", walkErr)}, nil
			}

			return ListFilesOutput{
				Directory:  dirPath,
				Files:      files,
				TotalCount: len(files),
			}, nil
		},
	)
}

// CreateFileInput defines arguments for creating a file.
type CreateFileInput struct {
	Path      string `json:"path" jsonschema:"The path to create"`
	Content   string `json:"content" jsonschema:"The complete content to write"`
	Overwrite bool   `json:"overwrite,omitempty" jsonschema:"Whether to overwrite if file already exists"`
}

// CreateFileOutput holds file creation result.
type CreateFileOutput struct {
	Path         string `json:"path"`
	Success      bool   `json:"success"`
	BytesWritten int    `json:"bytes_written"`
	Error        string `json:"error,omitempty"`
}

// NewCreateFileTool creates an ADK tool for creating files.
func NewCreateFileTool(ws *Workspace, hooks *Hooks) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "create_file",
			Description: "Create a new file with the specified content",
		},
		func(ctx agent.Context, input CreateFileInput) (CreateFileOutput, error) {
			fail := func(msg string) (CreateFileOutput, error) {
				return CreateFileOutput{Path: input.Path, Error: msg}, nil
			}
			rel, err := ws.WritablePath(input.Path)
			if err != nil {
				return fail(err.Error())
			}
			if rel == "." {
				return fail("path must name a file")
			}

			unlock, err := ws.lockPaths(ctx, rel)
			if err != nil {
				return fail(err.Error())
			}
			defer unlock()

			verb, before := "Create", ""
			existing, readErr := ws.ReadFile(rel)
			existed := readErr == nil
			if existed {
				if !input.Overwrite {
					return fail(fmt.Sprintf("file '%s' already exists; set overwrite=true to overwrite", input.Path))
				}
				verb, before = "Overwrite", string(existing)
			}
			if err := hooks.Approve(ctx, writeApproval(ws, "create_file", fmt.Sprintf("%s %s (%d bytes)", verb, rel, len(input.Content)),
				unifiedDiff(rel, before, input.Content))); err != nil {
				return fail(err.Error())
			}
			if input.Overwrite {
				if err := ws.unchanged(rel, existed, existing); err != nil {
					return fail(err.Error())
				}
			}

			data := []byte(input.Content)
			if input.Overwrite {
				err = ws.WriteFileAtomic(rel, data)
			} else {
				err = ws.CreateExclusive(rel, data)
				if errors.Is(err, fs.ErrExist) {
					return fail(fmt.Sprintf("file '%s' already exists; set overwrite=true to overwrite", input.Path))
				}
			}
			if err != nil {
				return fail(fmt.Sprintf("failed to write file: %v", err))
			}

			return CreateFileOutput{Path: input.Path, Success: true, BytesWritten: len(data)}, nil
		},
	)
}

// DeleteFileInput defines arguments for deleting a file.
type DeleteFileInput struct {
	Path string `json:"path" jsonschema:"Path of file to delete"`
}

// DeleteFileOutput holds result of deleting a file.
type DeleteFileOutput struct {
	Path    string `json:"path"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// NewDeleteFileTool creates an ADK tool for deleting files.
func NewDeleteFileTool(ws *Workspace, hooks *Hooks) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "delete_file",
			Description: "Delete a file from the workspace",
		},
		func(ctx agent.Context, input DeleteFileInput) (DeleteFileOutput, error) {
			rel, err := ws.WritablePath(input.Path)
			if err != nil {
				return DeleteFileOutput{Path: input.Path, Error: err.Error()}, nil
			}
			unlock, err := ws.lockPaths(ctx, rel)
			if err != nil {
				return DeleteFileOutput{Path: input.Path, Error: err.Error()}, nil
			}
			defer unlock()
			var diff string
			data, readErr := ws.ReadFile(rel)
			if readErr == nil {
				diff = unifiedDiff(rel, string(data), "")
			}
			if err := hooks.Approve(ctx, ApprovalRequest{
				Tool:     "delete_file",
				Kind:     ActionDelete,
				Detail:   "Delete " + rel,
				Diff:     diff,
				Key:      "delete:" + ws.Dir(),
				KeyLabel: "file deletions in " + ws.Dir(),
			}); err != nil {
				return DeleteFileOutput{Path: input.Path, Error: err.Error()}, nil
			}
			if readErr == nil {
				if err := ws.unchanged(rel, true, data); err != nil {
					return DeleteFileOutput{Path: input.Path, Error: err.Error()}, nil
				}
			}
			if err := ws.RemoveFile(rel); err != nil {
				return DeleteFileOutput{Path: input.Path, Error: fmt.Sprintf("failed to delete: %v", err)}, nil
			}
			return DeleteFileOutput{Path: input.Path, Success: true}, nil
		},
	)
}

// writeApproval builds an approval request for a file edit. Remembered
// approvals cover all edits within the workspace.
func writeApproval(ws *Workspace, tool, detail, diff string) ApprovalRequest {
	return ApprovalRequest{
		Tool:     tool,
		Kind:     ActionWrite,
		Detail:   detail,
		Diff:     diff,
		Key:      "write:" + ws.Dir(),
		KeyLabel: "file edits in " + ws.Dir(),
	}
}
