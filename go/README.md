# 🐶 Code Puppy Go (Google ADK Edition)

> An AI coding agent in a single static binary, built on the **Google Agent Development Kit (`google.golang.org/adk/v2`)**, with a sandbox, approvals, undo, and scriptable output.

---

## 🚀 Quick Start

```bash
make build                      # -> ./bin/code-puppy
./bin/code-puppy config init    # writes a commented ~/.code_puppy/.env.toml (mode 600)
export GEMINI_API_KEY=...       # or set it in the config file
./bin/code-puppy doctor         # checks config, credentials, sandbox, MCP, hooks
./bin/code-puppy                # interactive session
```

Configuration is read only from `~/.code_puppy/.env.toml`, `$MODENV_PREFIX`, or `--config DIR`. A `.env.toml` inside a project is **ignored** unless you pass `--config .` — a cloned repository must not be able to redirect your API key or turn off approvals.

---

## 💻 Usage

```bash
code-puppy                                   # interactive REPL in the current directory
code-puppy -d ~/src/app                      # ...in another workspace
code-puppy "fix the failing test"            # run once and exit
git diff | code-puppy review this change     # piped input becomes part of the prompt
code-puppy -p - < task.md                    # prompt from stdin
code-puppy --continue "now add docs"         # continue the most recent session
code-puppy --resume session-2026…            # resume a specific session (or -r for latest)
code-puppy --output-format json "…"          # one JSON result object on stdout
code-puppy --output-format stream-json "…"   # one JSON object per event, then the result
code-puppy --max-turns 20 "…"                # cap model calls in a one-shot run
```

| Exit code | Meaning |
|---|---|
| 0 | success |
| 1 | runtime or model error |
| 2 | invalid flags/arguments |
| 3 | `--max-turns` reached |
| 4 | prompt blocked by a `prompt_submit` hook |
| 130 | interrupted |

Subcommands: `doctor [--online]`, `config init|show|path`, `completion bash|zsh|fish|powershell`.

---

## ⌨️ Interactive Session

- **Line editing** with history (`~/.code_puppy/history`, owner-only), Ctrl+R search, and **Tab completion** for `/commands`, their arguments, and `@path` file references.
- **Multi-line input**: end a line with `\`, or put a block between two lines of `"""`.
- **Streaming Markdown** rendering, a progress spinner, and a per-turn usage line: `↳ 12.4k in · 1.2k out · context 12.3k · $0.0023`.
- **Ctrl+C** cancels the running turn; at the prompt it exits (see *Background processes*).

| Command | |
|---|---|
| `/undo [--force]` | Revert the file changes made in the last turn |
| `/checkpoints` | Turns that changed files |
| `/diff [git]` | Everything tools changed this session (or `git diff`) |
| `/cost`, `/context` | Token usage, estimated cost, context size vs. compaction threshold |
| `/memory [reload\|add <note>]` | Project instructions (`AGENTS.md`, `PUPPY.md`) |
| `/approvals [revoke <n>\|clear]` | Remembered approval rules |
| `/session list\|new\|load <id>`, `/resume <id>` | Saved sessions |
| `/agents`, `/agent <name>`, `/model <name>` | Personas and models |
| `/sandbox`, `/mcp` | Active policy; MCP servers |
| `/skills`, `/set`, `/clear`, `/exit` | |

---

## 🛡️ Safety Model

**Approvals.** File edits show a colored diff before you approve. Answers: `y` once, `s` for the rest of the session, `a` always (saved to `~/.code_puppy/approvals.json`), `n` no. Commands are remembered by exact text within a workspace; edits per workspace; web requests per host; MCP tools per server/tool. Ctrl+C at an approval prompt cancels the whole turn. With no terminal to ask, sensitive actions are denied unless auto-approved in config.

**File sandbox.** File tools only reach the workspace plus `sandbox.allowed_paths` (read-write) and `sandbox.read_only_paths`, enforced with `os.Root` (no `..` or symlink escapes). `sandbox.blocked_paths` (default: `.env`, keys, `~/.ssh`, cloud credentials, …) are never readable or writable — including through symlinks, `grep`, and `list_files`.

**Command policy.** Every shell command is parsed and each sub-command checked (pipes, `$(…)`, `bash -c`, `find -exec`, `env`/`xargs`/`timeout` wrappers; disguises like `s\udo` or `{r,}m` are caught). `sandbox.commands.deny` always wins; if `allow` is set, only matching commands run; `auto_approve` skips the prompt. This is a guardrail — the OS sandbox is the boundary.

**OS sandbox** (`sandbox.shell = auto|required|off`). Shell commands, forged tools and stdio MCP servers run under **Seatbelt** (macOS) or **bubblewrap** (Linux): writes only to writable roots, temp/cache dirs and `shell_writable_paths`; blocked paths unreadable; network off when `allow_network = false`. `required` refuses to start without it.

On Linux, install `bubblewrap` and allow unprivileged user namespaces. Ubuntu 24.04 restricts them through AppArmor (`kernel.apparmor_restrict_unprivileged_userns`), and Docker's default seccomp profile blocks them. In `auto` mode Code Puppy then runs unsandboxed and `/sandbox` or `doctor` shows why. bubblewrap can only hide paths that exist when a command starts, so a file matching `blocked_paths` that a command creates is visible to that same command; macOS blocks it immediately.

**Background processes never outlive the CLI.** Exiting with processes running asks to kill or wait; a second Ctrl+C force-quits. Each process group is also guarded so it is killed if Code Puppy dies, even by `SIGKILL`.

**Secrets.** Child processes don't inherit credential variables (`sandbox.scrub_env`, default `*_API_KEY`, `*_SECRET`, …). The audit log masks secrets. Sessions, history, approvals and audit files are owner-only.

**Audit log.** `~/.code_puppy/audit/audit-YYYY-MM-DD.jsonl` records prompts, tool calls and results, approvals, denials, hook decisions, and undos.

**Web.** `web_fetch` only reaches public addresses (checked after DNS resolution and on every redirect — no `localhost`, private ranges, or cloud metadata), needs approval per host unless in `web.allow_domains`, and caps response size.

---

## 🔌 Extending

**Project memory** — `AGENTS.md` / `PUPPY.md` from the repository root down to the workspace, plus `~/.code_puppy/PUPPY.md`, are added to the agents' instructions. They can't grant permissions.

**MCP servers**
```toml
[[mcp.servers]]
name    = "github"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-github"]
env     = { GITHUB_PERSONAL_ACCESS_TOKEN = "..." }
# url = "https://example.com/mcp"   # streamable HTTP instead of stdio
# tools = ["create_issue"]          # optional allow-list
# auto_approve = false
# sandbox = true                    # stdio servers run in the OS sandbox
```
MCP tools need approval per server/tool unless `auto_approve = true`, and can't shadow built-in tools.

**Hooks** receive a JSON event on stdin (`event`, `tool`, `args`, `result`, `prompt`, `session_id`, `workspace`). Exit `2` blocks (stderr is the reason) or print `{"decision":"block","reason":"…"}`; other failures warn unless `fail_closed = true`. Hooks are your own code from trusted config, so they run outside the OS sandbox with your full environment (they are still killed with Code Puppy).
```toml
[[hooks.pre_tool]]
match   = "run_shell_command"   # tool-name glob
command = "~/.code_puppy/hooks/check.sh"
[[hooks.post_tool]]
command = "jq -c . >> ~/tool-log.jsonl"
[[hooks.prompt_submit]]
command = "grep -qv 'password' || { echo 'no secrets' >&2; exit 2; }"
```

**Context & cost** — history is compacted once a prompt reaches `context.token_threshold` tokens. Estimated prices for the default models are built in; override or add them under `[pricing."model-name"]`.

---

## 🤖 Built-In Agent Personas

| Agent | Role |
|---|---|
| `code-puppy` | Primary autonomous coding agent |
| `helios` | Universal Constructor; builds and runs custom tools |
| `qa-kitten` | Test loops, edge cases, regression suites |
| `web-retriever` | Documentation and web research (`web_fetch`) |
| `planning-agent` | Requirement decomposition and roadmaps |
| `agent-creator` | Creates custom agent specs and skills |
| `model-judge` | Model comparisons |

Tools: `read_file`, `list_files`, `grep`, `create_file`, `replace_in_file`/`edit`, `delete_snippet`, `apply_patch` (unified diff or `*** Begin Patch`, atomic, multi-file), `delete_file`, `run_shell_command`, `manage_background_process`, `web_fetch`, `ask_user_question`, skills, `list_agents`/`invoke_agent`, `universal_constructor`, and MCP tools.

---

## 📦 Build, Test, Release

```bash
make build          # bin/code-puppy (version from git describe)
make check          # go vet + go test -race
make cross-compile  # darwin/linux amd64+arm64, windows amd64
make snapshot       # local GoReleaser build into dist/
```

Tagging `v*` runs `.github/workflows/go-release.yml`: reproducible builds, archives, SPDX SBOMs, and a cosign-signed checksum file (keyless, via GitHub OIDC). Verify a release:

```bash
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp 'https://github.com/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
sha256sum --ignore-missing -c checksums.txt
```

CI (`go-ci.yml`) runs vet and race tests on macOS and Linux; the Linux job installs bubblewrap and fails if the sandbox enforcement test is skipped.
