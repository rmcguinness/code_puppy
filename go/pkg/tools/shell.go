package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// BackgroundProcess represents a command running in background.
type BackgroundProcess struct {
	ID        int
	Command   string
	StartTime time.Time
	Cmd       *exec.Cmd
	Buffer    *bytes.Buffer
}

var (
	procMu      sync.Mutex
	procCounter int
	processes   = make(map[int]*BackgroundProcess)
)

// RunShellCommandInput defines arguments for executing shell commands.
type RunShellCommandInput struct {
	Command        string `json:"command" jsonschema:"The shell command to execute"`
	Cwd            string `json:"cwd,omitempty" jsonschema:"Optional working directory"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"Timeout in seconds (default 120)"`
	Background     bool   `json:"background,omitempty" jsonschema:"Whether to run command asynchronously in the background"`
}

// RunShellCommandOutput holds command execution results.
type RunShellCommandOutput struct {
	Output       string `json:"output"`
	ExitCode     int    `json:"exit_code"`
	DurationMs   int64  `json:"duration_ms"`
	IsBackground bool   `json:"is_background"`
	ProcessID    int    `json:"process_id,omitempty"`
	Error        string `json:"error,omitempty"`
}

// NewRunShellCommandTool creates an ADK tool for running shell commands.
func NewRunShellCommandTool(workspaceDir string, defaultTimeout int) (tool.Tool, error) {
	if defaultTimeout <= 0 {
		defaultTimeout = 120
	}

	return functiontool.New(
		functiontool.Config{
			Name:        "run_shell_command",
			Description: "Execute a shell command with timeout, output capture, and optional background execution",
		},
		func(ctx agent.Context, input RunShellCommandInput) (RunShellCommandOutput, error) {
			if input.Command == "" {
				return RunShellCommandOutput{Error: "command cannot be empty"}, nil
			}

			cwd := workspaceDir
			if input.Cwd != "" {
				cwd = resolveSafePath(workspaceDir, input.Cwd)
			}

			if input.Background {
				procMu.Lock()
				defer procMu.Unlock()

				procCounter++
				pid := procCounter

				cmd := exec.Command("bash", "-c", input.Command)
				cmd.Dir = cwd
				buf := new(bytes.Buffer)
				cmd.Stdout = buf
				cmd.Stderr = buf

				if err := cmd.Start(); err != nil {
					return RunShellCommandOutput{
						Error: fmt.Sprintf("failed to start background command: %v", err),
					}, nil
				}

				bp := &BackgroundProcess{
					ID:        pid,
					Command:   input.Command,
					StartTime: time.Now(),
					Cmd:       cmd,
					Buffer:    buf,
				}
				processes[pid] = bp

				go func() {
					_ = cmd.Wait()
				}()

				return RunShellCommandOutput{
					Output:       fmt.Sprintf("Process started in background with ID %d", pid),
					IsBackground: true,
					ProcessID:    pid,
				}, nil
			}

			// Synchronous run
			timeout := defaultTimeout
			if input.TimeoutSeconds > 0 {
				timeout = input.TimeoutSeconds
			}

			start := time.Now()
			cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
			defer cancel()

			cmd := exec.CommandContext(cmdCtx, "bash", "-c", input.Command)
			cmd.Dir = cwd

			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			runErr := cmd.Run()
			duration := time.Since(start).Milliseconds()

			exitCode := 0
			if runErr != nil {
				if exitErr, ok := runErr.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				} else if cmdCtx.Err() == context.DeadlineExceeded {
					return RunShellCommandOutput{
						Output:     stdout.String(),
						ExitCode:   -1,
						DurationMs: duration,
						Error:      fmt.Sprintf("command timed out after %d seconds", timeout),
					}, nil
				} else {
					exitCode = 1
				}
			}

			combined := stdout.String()
			if stderr.Len() > 0 {
				if len(combined) > 0 && !bytes.HasSuffix([]byte(combined), []byte("\n")) {
					combined += "\n"
				}
				combined += stderr.String()
			}

			// Truncate output if excessively large (> 100KB)
			if len(combined) > 100*1024 {
				combined = combined[:100*1024] + "\n\n... [Output truncated after 100KB]"
			}

			return RunShellCommandOutput{
				Output:     combined,
				ExitCode:   exitCode,
				DurationMs: duration,
			}, nil
		},
	)
}
