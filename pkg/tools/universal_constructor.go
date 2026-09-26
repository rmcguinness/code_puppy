package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

const (
	ucRunTimeout  = 60 * time.Second
	ucOutputLimit = 100 * 1024
)

// ucToolNamePattern restricts forged tool names so they cannot contain path
// separators or traversal sequences.
var ucToolNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ucLanguages maps accepted language names to a canonical name and file extension.
var ucLanguages = map[string]struct{ canonical, ext string }{
	"bash":   {"bash", ".sh"},
	"sh":     {"bash", ".sh"},
	"python": {"python", ".py"},
	"py":     {"python", ".py"},
	"go":     {"go", ".go"},
}

// UCToolMetadata holds metadata about a dynamically forged tool.
type UCToolMetadata struct {
	Name        string `json:"name"`
	Language    string `json:"language"`
	Description string `json:"description"`
	Path        string `json:"path"`
}

// UniversalConstructorInput defines arguments for the universal constructor.
type UniversalConstructorInput struct {
	Action      string `json:"action" jsonschema:"Action to perform: create, run, list, or delete"`
	ToolName    string `json:"tool_name,omitempty" jsonschema:"Name of tool to create or run (letters, digits, '-' and '_')"`
	Language    string `json:"language,omitempty" jsonschema:"Programming language (go, python, bash)"`
	Code        string `json:"code,omitempty" jsonschema:"Source code of tool to create"`
	Description string `json:"description,omitempty" jsonschema:"Description of what tool does"`
	Args        string `json:"args,omitempty" jsonschema:"Whitespace-separated arguments to pass when running tool"`
}

// UniversalConstructorOutput holds result of universal constructor.
type UniversalConstructorOutput struct {
	Success bool     `json:"success"`
	Result  string   `json:"result"`
	Tools   []string `json:"tools,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type ucRegistry struct {
	mu     sync.RWMutex
	dir    string
	tools  map[string]*UCToolMetadata
	exec   *ExecEnv
	policy *CommandPolicy
}

// NewUniversalConstructorTool creates the Helios Universal Constructor tool.
// Forged tools are stored in toolsDir (default ~/.code_puppy/uc_tools), which
// is created lazily on the first create. Runs go through the command policy
// as "universal_constructor <tool> <args...>" and the OS sandbox.
func NewUniversalConstructorTool(toolsDir string, hooks *Hooks, env *ExecEnv, policy *CommandPolicy) (tool.Tool, error) {
	if toolsDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home directory: %w", err)
		}
		toolsDir = filepath.Join(home, ".code_puppy", "uc_tools")
	}
	reg := &ucRegistry{dir: toolsDir, tools: make(map[string]*UCToolMetadata), exec: env, policy: policy}
	reg.load()

	return functiontool.New(
		functiontool.Config{
			Name:        "universal_constructor",
			Description: "Helios power: forge, run, and list dynamic executable tools and scripts",
		},
		func(ctx agent.Context, input UniversalConstructorInput) (UniversalConstructorOutput, error) {
			switch input.Action {
			case "list":
				return reg.list(), nil
			case "create":
				return reg.create(ctx, hooks, input), nil
			case "run":
				return reg.run(ctx, hooks, input), nil
			case "delete":
				return reg.delete(ctx, hooks, input), nil
			default:
				return UniversalConstructorOutput{Error: "unknown action; use 'create', 'run', 'list', or 'delete'"}, nil
			}
		},
	)
}

func (r *ucRegistry) list() UniversalConstructorOutput {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name, meta := range r.tools {
		names = append(names, fmt.Sprintf("%s (%s): %s", name, meta.Language, meta.Description))
	}
	sort.Strings(names)
	return UniversalConstructorOutput{
		Success: true,
		Result:  fmt.Sprintf("%d custom tools available", len(names)),
		Tools:   names,
	}
}

func (r *ucRegistry) create(ctx context.Context, hooks *Hooks, input UniversalConstructorInput) UniversalConstructorOutput {
	if input.ToolName == "" || input.Code == "" {
		return UniversalConstructorOutput{Error: "tool_name and code are required to create a tool"}
	}
	if !ucToolNamePattern.MatchString(input.ToolName) {
		return UniversalConstructorOutput{Error: "tool_name may only contain letters, digits, '-' and '_' (max 64 chars)"}
	}
	langName := strings.ToLower(input.Language)
	if langName == "" {
		langName = "bash"
	}
	lang, ok := ucLanguages[langName]
	if !ok {
		return UniversalConstructorOutput{Error: fmt.Sprintf("unsupported language %q; use bash, python, or go", input.Language)}
	}

	if err := hooks.Approve(ctx, ApprovalRequest{
		Tool:   "universal_constructor",
		Kind:   ActionWrite,
		Diff:   unifiedDiff(input.ToolName+lang.ext, "", input.Code),
		Detail: fmt.Sprintf("Forge %s tool %q in %s (%d bytes)", lang.canonical, input.ToolName, r.dir, len(input.Code)),
	}); err != nil {
		return UniversalConstructorOutput{Error: err.Error()}
	}

	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return UniversalConstructorOutput{Error: fmt.Sprintf("failed to create tools directory: %v", err)}
	}
	filePath := filepath.Join(r.dir, input.ToolName+lang.ext)
	if err := os.WriteFile(filePath, []byte(input.Code), 0o700); err != nil {
		return UniversalConstructorOutput{Error: fmt.Sprintf("failed to save tool: %v", err)}
	}

	meta := &UCToolMetadata{
		Name:        input.ToolName,
		Language:    lang.canonical,
		Description: input.Description,
		Path:        filePath,
	}
	if err := r.saveManifest(meta); err != nil {
		return UniversalConstructorOutput{Error: fmt.Sprintf("saved the script but not its manifest: %v", err)}
	}
	r.mu.Lock()
	r.tools[input.ToolName] = meta
	r.mu.Unlock()

	return UniversalConstructorOutput{
		Success: true,
		Result:  fmt.Sprintf("Tool '%s' successfully forged and saved to %s", input.ToolName, filePath),
	}
}

func (r *ucRegistry) run(ctx context.Context, hooks *Hooks, input UniversalConstructorInput) UniversalConstructorOutput {
	r.mu.RLock()
	meta, exists := r.tools[input.ToolName]
	r.mu.RUnlock()
	if !exists {
		return UniversalConstructorOutput{Error: fmt.Sprintf("tool '%s' not found", input.ToolName)}
	}

	args := strings.Fields(input.Args)
	// Script contents can't be inspected, so the policy sees a synthetic
	// command; allow-lists must opt in with e.g. "universal_constructor *".
	decision := r.policy.Evaluate(shellJoin(append([]string{"universal_constructor", meta.Name}, args...)))
	if decision.Verdict == VerdictDeny {
		return UniversalConstructorOutput{Error: "blocked by command policy: " + decision.Reason}
	}
	if decision.Verdict != VerdictAutoApprove {
		if err := hooks.Approve(ctx, ApprovalRequest{
			Tool:     "universal_constructor",
			Kind:     ActionCommand,
			Detail:   fmt.Sprintf("Run forged tool %q with args %q", meta.Name, args),
			Key:      "uc-run:" + meta.Name + "\x00" + strings.Join(args, "\x00"),
			KeyLabel: "this forged tool with these arguments",
		}); err != nil {
			return UniversalConstructorOutput{Error: err.Error()}
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, ucRunTimeout)
	defer cancel()

	var argv []string
	switch meta.Language {
	case "python":
		argv = append([]string{"python3", meta.Path}, args...)
	case "go":
		argv = append([]string{"go", "run", meta.Path}, args...)
	default:
		argv = append([]string{"bash", meta.Path}, args...)
	}
	cmd, err := r.exec.command(runCtx, argv)
	if err != nil {
		return UniversalConstructorOutput{Error: fmt.Sprintf("failed to prepare tool: %v", err)}
	}
	out := newCappedBuffer(ucOutputLimit)
	cmd.Stdout = out
	cmd.Stderr = out

	err = cmd.Run()
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return UniversalConstructorOutput{Result: out.String(), Error: fmt.Sprintf("tool timed out after %s", ucRunTimeout)}
	}
	if err != nil {
		return UniversalConstructorOutput{Result: out.String(), Error: fmt.Sprintf("tool execution failed: %v", err)}
	}
	return UniversalConstructorOutput{Success: true, Result: out.String()}
}

// shellJoin quotes words so the policy parser sees them as literals.
func shellJoin(words []string) string {
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

// ucManifest is the persisted description of a forged tool. The script path
// is never stored: it is recomputed from the validated name and language.
type ucManifest struct {
	Name        string    `json:"name"`
	Language    string    `json:"language"`
	Description string    `json:"description,omitempty"`
	Created     time.Time `json:"created"`
}

func (r *ucRegistry) manifestPath(name string) string {
	return filepath.Join(r.dir, name+".json")
}

func (r *ucRegistry) saveManifest(meta *UCToolMetadata) error {
	data, err := json.MarshalIndent(ucManifest{Name: meta.Name, Language: meta.Language, Description: meta.Description, Created: time.Now().UTC()}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.manifestPath(meta.Name), data, 0o600)
}

// load restores forged tools from manifests. Manifests are untrusted input:
// the name and language are re-validated, and the script must be a regular
// file (not a symlink) at the path derived from them.
func (r *ucRegistry) load() {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.dir, e.Name()))
		if err != nil {
			continue
		}
		var m ucManifest
		if json.Unmarshal(data, &m) != nil || !ucToolNamePattern.MatchString(m.Name) || m.Name+".json" != e.Name() {
			continue
		}
		lang, ok := ucLanguages[m.Language]
		if !ok || lang.canonical != m.Language {
			continue
		}
		script := filepath.Join(r.dir, m.Name+lang.ext)
		info, err := os.Lstat(script)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		r.tools[m.Name] = &UCToolMetadata{Name: m.Name, Language: lang.canonical, Description: m.Description, Path: script}
	}
}

func (r *ucRegistry) delete(ctx context.Context, hooks *Hooks, input UniversalConstructorInput) UniversalConstructorOutput {
	r.mu.RLock()
	meta, ok := r.tools[input.ToolName]
	r.mu.RUnlock()
	if !ok {
		return UniversalConstructorOutput{Error: fmt.Sprintf("tool '%s' not found", input.ToolName)}
	}
	if err := hooks.Approve(ctx, ApprovalRequest{
		Tool: "universal_constructor", Kind: ActionDelete,
		Detail: fmt.Sprintf("Delete forged tool %q (%s)", meta.Name, meta.Path),
	}); err != nil {
		return UniversalConstructorOutput{Error: err.Error()}
	}
	for _, p := range []string{meta.Path, r.manifestPath(meta.Name)} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return UniversalConstructorOutput{Error: fmt.Sprintf("failed to delete %s: %v", p, err)}
		}
	}
	r.mu.Lock()
	delete(r.tools, meta.Name)
	r.mu.Unlock()
	return UniversalConstructorOutput{Success: true, Result: fmt.Sprintf("Deleted tool '%s'", meta.Name)}
}
