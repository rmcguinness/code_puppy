package tools

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

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
	Error      string `json:"error,omitempty"`
}

// NewReadFileTool creates an ADK tool for reading files.
func NewReadFileTool(workspaceDir string) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "read_file",
			Description: "Read contents of a file with line numbers and optional line slicing",
		},
		func(ctx agent.Context, input ReadFileInput) (ReadFileOutput, error) {
			targetPath := resolveSafePath(workspaceDir, input.Path)
			data, err := os.ReadFile(targetPath)
			if err != nil {
				return ReadFileOutput{Path: input.Path, Error: fmt.Sprintf("failed to read file: %v", err)}, nil
			}

			lines := strings.Split(string(data), "\n")
			totalLines := len(lines)

			start := 1
			if input.StartLine > 1 {
				start = input.StartLine
			}
			end := totalLines
			if input.EndLine > 0 && input.EndLine < totalLines {
				end = input.EndLine
			}

			if start > totalLines {
				start = totalLines
			}
			if end < start {
				end = start
			}

			var sb strings.Builder
			for i := start; i <= end; i++ {
				sb.WriteString(fmt.Sprintf("%4d: %s\n", i, lines[i-1]))
			}

			return ReadFileOutput{
				Path:       input.Path,
				Content:    sb.String(),
				TotalLines: totalLines,
			}, nil
		},
	)
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
func NewListFilesTool(workspaceDir string) (tool.Tool, error) {
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
			resolvedDir := resolveSafePath(workspaceDir, dirPath)

			max := input.MaxEntries
			if max <= 0 || max > 500 {
				max = 200
			}

			var files []FileEntry
			walkErr := filepath.WalkDir(resolvedDir, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if path == resolvedDir {
					return nil
				}

				name := d.Name()
				if strings.HasPrefix(name, ".") && name != "." && name != ".." && name != ".env.toml" {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}

				rel, _ := filepath.Rel(workspaceDir, path)
				info, err := d.Info()
				var size int64 = 0
				if err == nil {
					size = info.Size()
				}

				files = append(files, FileEntry{
					Path:  rel,
					IsDir: d.IsDir(),
					Size:  size,
				})

				if len(files) >= max {
					return fs.SkipAll
				}

				if !input.Recursive && d.IsDir() {
					return filepath.SkipDir
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
func NewCreateFileTool(workspaceDir string) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "create_file",
			Description: "Create a new file with the specified content",
		},
		func(ctx agent.Context, input CreateFileInput) (CreateFileOutput, error) {
			targetPath := resolveSafePath(workspaceDir, input.Path)

			if !input.Overwrite {
				if _, err := os.Stat(targetPath); err == nil {
					return CreateFileOutput{
						Path:    input.Path,
						Success: false,
						Error:   fmt.Sprintf("file '%s' already exists; set overwrite=true to overwrite", input.Path),
					}, nil
				}
			}

			dir := filepath.Dir(targetPath)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return CreateFileOutput{Path: input.Path, Success: false, Error: fmt.Sprintf("failed to create directories: %v", err)}, nil
			}

			if err := os.WriteFile(targetPath, []byte(input.Content), 0644); err != nil {
				return CreateFileOutput{Path: input.Path, Success: false, Error: fmt.Sprintf("failed to write file: %v", err)}, nil
			}

			return CreateFileOutput{
				Path:         input.Path,
				Success:      true,
				BytesWritten: len([]byte(input.Content)),
			}, nil
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
func NewDeleteFileTool(workspaceDir string) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "delete_file",
			Description: "Delete a file from the workspace",
		},
		func(ctx agent.Context, input DeleteFileInput) (DeleteFileOutput, error) {
			targetPath := resolveSafePath(workspaceDir, input.Path)
			if err := os.Remove(targetPath); err != nil {
				return DeleteFileOutput{Path: input.Path, Success: false, Error: fmt.Sprintf("failed to delete: %v", err)}, nil
			}
			return DeleteFileOutput{Path: input.Path, Success: true}, nil
		},
	)
}

func resolveSafePath(workspaceDir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	if workspaceDir == "" || workspaceDir == "." {
		return p
	}
	return filepath.Join(workspaceDir, p)
}

// CountLines helper
func countLines(data []byte) int {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	count := 0
	for scanner.Scan() {
		count++
	}
	return count
}
