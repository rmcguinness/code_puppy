package tools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/retail-cortex/code_puppy/internal/audit"
	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/skills"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

const (
	scriptOutputLimit = 64 << 10 // per stream returned to the model
	scriptOutputFiles = 50       // files listed from the output directory
	// SkillOutputDir is where scripts write, relative to the workspace.
	SkillOutputDir = ".code_puppy/skill-output"
)

// SkillScripts runs skills' scripts: each is checked against
// skills.policy, approved by its tier, given its Python environment, and run
// in the script sandbox. Scripts can read the workspace but never write it;
// they write into their own output directory, and the agent applies any
// changes with the file tools (so diffs, approvals and /undo still work).
type SkillScripts struct {
	provider *skills.Provider
	policy   config.SkillPolicy
	ws       *Workspace
	hooks    *Hooks
	envs     *PyEnvs
	boxCfg   ScriptBoxConfig

	boxOnce sync.Once
	box     ScriptBox
	boxErr  error
}

// NewSkillScripts sets up script runs for the skills of provider.
func NewSkillScripts(provider *skills.Provider, policy config.SkillPolicy, ws *Workspace, hooks *Hooks, envs *PyEnvs, boxCfg ScriptBoxConfig) *SkillScripts {
	return &SkillScripts{provider: provider, policy: policy, ws: ws, hooks: hooks, envs: envs, boxCfg: boxCfg}
}

// Box returns the script sandbox, set up on first use (a gVisor test run
// takes a moment).
func (s *SkillScripts) Box() (ScriptBox, error) {
	s.boxOnce.Do(func() { s.box, _, s.boxErr = NewScriptBox(s.boxCfg) })
	return s.box, s.boxErr
}

// Envs is the environment manager.
func (s *SkillScripts) Envs() *PyEnvs { return s.envs }

// RunSkillScriptInput is the run_skill_script request.
type RunSkillScriptInput struct {
	Skill  string   `json:"skill" jsonschema:"Skill name"`
	Script string   `json:"script" jsonschema:"Script name, as listed by activate_skill"`
	Args   []string `json:"args,omitempty" jsonschema:"Command-line arguments for the script"`
}

// RunSkillScriptOutput is how the script ran.
type RunSkillScriptOutput struct {
	ExitCode  int      `json:"exit_code"`
	Stdout    string   `json:"stdout,omitempty"`
	Stderr    string   `json:"stderr,omitempty"`
	TimedOut  bool     `json:"timed_out,omitempty"`
	Sandbox   string   `json:"sandbox,omitempty"`
	Tier      string   `json:"tier,omitempty"`
	OutputDir string   `json:"output_dir,omitempty"` // workspace-relative; read results with read_file
	Files     []string `json:"output_files,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// NewRunSkillScriptTool creates run_skill_script.
func NewRunSkillScriptTool(s *SkillScripts) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name: "run_skill_script",
			Description: "Run a script that a skill ships (see activate_skill for its scripts). It runs sandboxed, " +
				"with its own Python packages; it can read the workspace but writes only to its output directory, " +
				"whose files you can read and then apply with the file tools.",
		},
		func(ctx agent.Context, in RunSkillScriptInput) (RunSkillScriptOutput, error) {
			return s.Run(ctx, in), nil
		},
	)
}

// Run runs one script.
func (s *SkillScripts) Run(ctx context.Context, in RunSkillScriptInput) (out RunSkillScriptOutput) {
	fail := func(format string, a ...any) RunSkillScriptOutput {
		out.Error = fmt.Sprintf(format, a...)
		return out
	}
	skill, ok := s.provider.Get(in.Skill)
	if !ok {
		return fail("no skill named %q", in.Skill)
	}
	idx := slices.IndexFunc(skill.Scripts, func(sc skills.ScriptDefinition) bool { return sc.Name == in.Script })
	if idx < 0 {
		return fail("skill %q has no script %q", in.Skill, in.Script)
	}
	sc := skill.Scripts[idx]
	ev := skills.Evaluate(skill, s.policy)
	verdict := ev.Scripts[idx]
	if !verdict.Allowed {
		return fail("skills.policy doesn't allow this script: %s", strings.Join(verdict.Reasons, "; "))
	}
	if sc.Language != skills.LanguagePython {
		return fail("%s scripts aren't supported yet", sc.Language)
	}
	out.Tier = ev.Tier.String()

	box, err := s.Box()
	if err != nil {
		return fail("%v", err)
	}
	out.Sandbox = box.Name()
	python, err := SystemPython()
	if err != nil {
		return fail("%v", err)
	}

	// Approval by tier: 3 asks every time and can't be remembered.
	detail := fmt.Sprintf("Run %s from skill %s (%s)%s", sc.Name, skill.Name, ev.Tier, argsNote(in.Args))
	switch {
	case ev.Bypass:
		s.audit(detail, "bypass")
	case ev.Tier >= skills.Tier3MandatoryApproval:
		if err := s.hooks.Approve(ctx, ApprovalRequest{Tool: "run_skill_script", Kind: ActionCommand, Detail: detail}); err != nil {
			return fail("%v", err)
		}
	default:
		s.audit(detail, "auto-"+strings.ToLower(ev.Tier.String()))
	}

	interp := python
	readOnly := append([]string{s.ws.Dir()}, MountsFor(python)...)
	if len(sc.Dependencies) > 0 {
		env, ready := s.envs.Lookup(python, sc.Dependencies)
		if !ready {
			// Installing reaches the network and runs installers: approved on
			// its own, rememberable for exactly this package list.
			if err := s.hooks.Approve(ctx, ApprovalRequest{
				Tool: "run_skill_script", Kind: ActionNetwork,
				Detail: fmt.Sprintf("Install packages for skill %s into an isolated environment (%s): %s", skill.Name, box.Name(), s.envs.InstallCommands(sc.Dependencies)),
				Key:    "pyenv:" + env.Key, KeyLabel: "installing exactly these packages",
			}); err != nil {
				return fail("%v", err)
			}
		}
		if env, err = s.envs.Ensure(ctx, box, python, skill.Name, sc.Dependencies); err != nil {
			return fail("setting up the environment: %v", err)
		}
		interp = env.Interpreter()
		readOnly = append(readOnly, env.Dir)
	}

	scriptPath, scriptDir, cleanup, err := materializeScript(skill, sc)
	if err != nil {
		return fail("%v", err)
	}
	defer cleanup()
	readOnly = append(readOnly, scriptDir)

	var writable []string
	env := []string{"PYTHONDONTWRITEBYTECODE=1", "PYTHONNOUSERSITE=1", "SKILL_DIR=" + scriptDir}
	if ev.Tier >= skills.Tier2AuditedWrite || ev.Bypass {
		dir := filepath.Join(s.ws.Dir(), SkillOutputDir, skill.Name, fmt.Sprintf("%s-%s", sc.Name, time.Now().Format("20060102-150405.000")))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fail("%v", err)
		}
		writable = append(writable, dir)
		out.OutputDir, _ = filepath.Rel(s.ws.Dir(), dir)
		env = append(env, "SKILL_OUTPUT="+dir)
	}
	for _, name := range ev.Env {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	for k, v := range sc.EnvironmentVariables {
		env = append(env, k+"="+v)
	}

	argv := []string{interp, scriptPath}
	if sc.EntryPoint != "" {
		// Call the named function, with the script's module run under a
		// name other than __main__.
		argv = []string{interp, "-c",
			"import runpy,sys; sys.argv=sys.argv[1:]; ns=runpy.run_path(sys.argv[0], run_name='__skill__'); r=ns[" + pyQuote(sc.EntryPoint) + "](); sys.exit(r if isinstance(r, int) else 0)",
			scriptPath}
	}
	argv = append(argv, in.Args...)

	stdout, stderr := newCappedBuffer(scriptOutputLimit), newCappedBuffer(scriptOutputLimit)
	res, err := box.Run(ctx, ScriptRequest{
		Argv: argv, Dir: s.ws.Dir(), Env: env, Network: ev.Network,
		ReadOnly: readOnly, Writable: writable, Stdout: stdout, Stderr: stderr,
		Timeout: time.Duration(verdict.TimeoutSeconds) * time.Second,
	})
	out.Stdout, out.Stderr = stdout.String(), stderr.String()
	if err != nil {
		return fail("%v", err)
	}
	out.ExitCode, out.TimedOut = res.ExitCode, res.TimedOut
	if res.TimedOut {
		out.Error = fmt.Sprintf("stopped after %ds (timeout)", verdict.TimeoutSeconds)
	}
	for _, w := range writable {
		out.Files = listFiles(s.ws.Dir(), w, scriptOutputFiles)
	}
	return out
}

func (s *SkillScripts) audit(detail, decision string) {
	s.hooks.Audit().Log(audit.Entry{Kind: audit.KindApproval, Tool: "run_skill_script", Detail: detail, Decision: decision})
}

// materializeScript returns the script's host path and the directory to
// mount for it: the skill's own directory for skills on disk (so a script
// can import its neighbours), else a temporary copy.
func materializeScript(skill *skills.Skill, sc skills.ScriptDefinition) (path, dir string, cleanup func(), err error) {
	cleanup = func() {}
	if sc.RelativePath != "" && skill.HostDir() != "" {
		path, err = skill.ScriptPath(sc.RelativePath)
		return path, skill.HostDir(), cleanup, err
	}
	tmp, err := os.MkdirTemp("", "code-puppy-skill-*")
	if err != nil {
		return "", "", cleanup, err
	}
	cleanup = func() { os.RemoveAll(tmp) }
	switch {
	case sc.InlineCode != "":
		path = filepath.Join(tmp, sc.Name+".py")
		err = os.WriteFile(path, []byte(sc.InlineCode), 0o600)
	case sc.RelativePath != "":
		if err = skill.CopyTo(tmp); err == nil {
			path = filepath.Join(tmp, filepath.FromSlash(sc.RelativePath))
		}
	default:
		err = errors.New("the script has no source")
	}
	if err != nil {
		cleanup()
		return "", "", func() {}, err
	}
	if c, err := filepath.EvalSymlinks(tmp); err == nil { // macOS: /var -> /private/var
		path = filepath.Join(c, strings.TrimPrefix(path, tmp))
		tmp = c
	}
	return path, tmp, cleanup, nil
}

// listFiles lists up to max regular files under dir, relative to ws.
func listFiles(ws, dir string, max int) []string {
	var files []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		if len(files) == max {
			return filepath.SkipAll
		}
		if rel, err := filepath.Rel(ws, p); err == nil {
			files = append(files, rel)
		}
		return nil
	})
	return files
}

func argsNote(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return " with arguments " + shellJoin(args)
}

// pyQuote quotes s as a Python string literal.
func pyQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`).Replace(s) + "'"
}
