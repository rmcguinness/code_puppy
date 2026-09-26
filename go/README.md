# 🐶 Code Puppy Go (Google ADK Edition)

> An AI coding agent in a single static binary, built on the **Google Agent Development Kit (`google.golang.org/adk/v2`)**, with a sandbox, approvals, undo, and scriptable output.

---

## 🚀 Quick Start

```bash
make build                      # -> ./bin/code-puppy
./bin/code-puppy config init    # writes a commented ~/.code_puppy/.env.toml (mode 600)
export GEMINI_API_KEY=...       # or ANTHROPIC_API_KEY / OPENAI_API_KEY; or set it in the config file
./bin/code-puppy doctor         # checks config, credentials, sandbox, MCP, hooks
./bin/code-puppy                # interactive session
```

**Providers.** Set `llm.provider` to `gemini` (default), `anthropic`, `openai`, or `ollama`; the model comes from `llm.<provider>.model` unless `code_puppy.default_model` or `--model` overrides it. Anthropic defaults to `claude-opus-5` with streaming, prompt caching of the system prompt, thinking preserved across tool calls, and server-side refusal fallback (`llm.anthropic.fallbacks = "default"`, or `"off"`). Without `api_key` it uses `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, or an `ant auth login` profile.

Configuration is read only from `~/.code_puppy/.env.toml`, `$MODENV_PREFIX`, or `--config DIR`. A `.env.toml` inside a project is **ignored** unless you pass `--config .` — a cloned repository must not be able to redirect your API key or turn off approvals.

---

## 💻 Usage

```bash
code-puppy                                   # interactive REPL in the current directory
code-puppy -d ~/src/app                      # ...in another workspace
code-puppy "fix the failing test"            # run once and exit
git diff | code-puppy review this change     # piped input becomes part of the prompt
code-puppy -p - < task.md                    # prompt from stdin
code-puppy --continue "now add docs"         # continue this directory's most recent session
code-puppy --resume session-2026…            # resume a specific session (or -r for this directory's latest)
code-puppy --resume=before-refactor          # start a new session from a snapshot saved with /session save
code-puppy --output-format json "…"          # one JSON result object on stdout
code-puppy --output-format stream-json "…"   # one JSON object per event, then the result
code-puppy --max-turns 20 "…"                # cap model calls in a one-shot run
code-puppy --plan "add rate limiting"         # a plan only: reads and searches, no edits or commands
code-puppy --image ui.png "why is this misaligned?"   # attach images (repeatable; @ui.png in the prompt works too)
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
- **Steering**: while the agent is working, start typing (or press Ctrl+T) to send it a message, e.g. "use tabs" or "skip the tests". Output pauses while you type. The message reaches the agent with its next tool result, so nothing is interrupted, and it is kept in the conversation history. If the agent finishes without another tool call, the message is sent as your next prompt. `prompt_submit` hooks apply to these messages too. (macOS and Linux.)
- **Ctrl+C** cancels the running turn; at the prompt it exits (see *Background processes*).

| Command | |
|---|---|
| `/undo [--force]` | Revert the file changes made in the last turn |
| `/checkpoints` | Turns that changed files |
| `/diff [git]` | Everything tools changed this session (or `git diff`) |
| `/cost`, `/context` | Token usage (including cache reads and writes), estimated cost, context size vs. compaction threshold |
| `/compact [focus]` | Summarize everything before the latest turn now; the focus says what to keep |
| `/memory [reload\|add <note>]` | Project instructions (`AGENTS.md`, `PUPPY.md`) |
| `/approvals [revoke <n>\|clear]` | Remembered approval rules |
| `/session list [--all]\|new\|load <id\|name>`, `/resume <id\|name>` | Saved sessions — scoped to the current workspace; `--all` shows every directory |
| `/session save <name> [--force]` | Save a snapshot of this session (📸 in `/session list`). Loading it by name starts a new session from that point and leaves the snapshot unchanged, so you can return to it again. `--continue` skips snapshots |
| `/agents`, `/agent <name>`, `/model <name>` | Personas and models; `/model anthropic/claude-sonnet-5` can switch provider |
| `/pin_model [<agent> <model>]`, `/unpin <agent>` | Run an agent on its own model (e.g. qa-kitten on a cheaper one); saved under `[agent_models]` |
| `/model_settings [<model> [key=value…\|reset]]` | Show or set one model's temperature, max_tokens, top_p or seed (`key=` clears one); saved under `[model_settings."<model>"]` |
| `/sandbox`, `/mcp`, `/tools` | Active policy; MCP servers; the tools the active agent can use |
| `/plan <goal>` | Ask for a plan without changing anything. The agent can read, search and delegate, but edits, commands and MCP tools are refused for that turn |
| `!<command>` | Run a command yourself, like in your own terminal: in the workspace, with your environment, outside the agent's sandbox and approvals. The agent doesn't see it; the audit log records it |
| `/attach [path\|clear]`, `/paste` | Queue an image (or the clipboard's) for your next message |
| `/locale [code]` | Interface language (see below) |
| `/skills`, `/set` (`/show`), `/clear`, `/exit` | |

### Images

Mention an image in a prompt (`what's wrong with @screenshots/login.png?`, or `@"with spaces.png"`), queue one with `/attach <path>`, or paste a screenshot with `/paste`; the model also has a `view_image` tool for images it finds in the workspace. PNG, JPEG, GIF and WebP work with Gemini, Anthropic and OpenAI-compatible models (for Ollama, pick a vision model).

- Files are read through the workspace sandbox: blocked paths and anything outside the workspace are refused.
- Pictures larger than `[images] max_dimension` (1568 px) or 3.75 MB are scaled down and re-encoded; headers are checked before decoding, so oversized "decompression bomb" files are rejected.
- Session files store a short reference, not the image; the picture lives once in `~/.code_puppy/images` (owner-only, named by SHA-256) and is deleted after `retain_days` (30) unused. The audit log records the path and hash.
- `/paste` uses `osascript` on macOS, `wl-paste` or `xclip` on Linux, and PowerShell on Windows.

### Language

The interface speaks English (`en-US`, the default), Spanish (`es`) and Canadian French (`fr-CA`). `/locale` shows the current language; `/locale es` switches and saves `[ui] locale = "es"` to `~/.code_puppy/.env.toml`. Codes are forgiving: `es-ES`, `es_MX`, `ES-sp`, `spanish` and `español` all work. Any other language (`/locale ja`) changes the language the model replies in, while menus stay in English until someone adds a catalog. Code, paths, commands and tool output are never translated, and `doctor`, `--help` and CLI errors stay in English so they can be shared in bug reports. To add or correct a language, drop a JSON catalog in `~/.code_puppy/locales/` — see [docs/TRANSLATING.md](docs/TRANSLATING.md).

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

**Diagnostic log.** `~/.code_puppy/logs/code-puppy-YYYY-MM-DD.jsonl` (owner-only, secrets masked, kept `log.retain_days` = 14) records warnings, failed turns and errors, with trace IDs when telemetry is on. `log.level` (or `CODE_PUPPY_LOG_LEVEL`) is `debug`, `info`, `warn`, `error` or `off`. A background goroutine writes it, so logging never waits on the disk.

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
# prefix = "gh"                     # expose tools as gh__create_issue
# agents = ["code-puppy", "qa-kitten"]  # who gets these tools; default: primary agent; "*" = all
```
MCP tools need approval per server/tool unless `auto_approve = true`, and can't shadow built-in tools (use `prefix` to avoid clashes). Listing a server's tools times out after 30 s and each call after `timeout_seconds` (default 300). A stdio server that crashes is restarted on the next call. After two failures in a row a server is paused (its tools disappear from the model's list) for 15 s, doubling up to 5 minutes, and then one call is let through as a trial. You get one warning when a server fails, one when it's paused, and one when it recovers.

**Hooks** receive a JSON event on stdin (`event`, `tool`, `args`, `result`, `prompt`, `session_id`, `workspace`). Exit `2` blocks (stderr is the reason) or print `{"decision":"block","reason":"…"}`; other failures warn unless `fail_closed = true`. `pre_tool` and `prompt_submit` hooks run before the action and can block it. `post_tool` hooks only observe, so they run in the background, in order, and never delay the agent. Each event is captured when the tool finishes. If hooks fall far behind (256 queued), further events are dropped with a warning, and at exit queued hooks get up to 5 s to finish. Hooks are your own code from trusted config, so they run outside the OS sandbox with your full environment (they are still killed with Code Puppy).
```toml
[[hooks.pre_tool]]
match   = "run_shell_command"   # tool-name glob
command = "~/.code_puppy/hooks/check.sh"
[[hooks.post_tool]]
command = "jq -c . >> ~/tool-log.jsonl"
[[hooks.prompt_submit]]
command = "grep -qv 'password' || { echo 'no secrets' >&2; exit 2; }"
```

**Web search** — set `web.search_provider` to `brave`, `tavily` (key in `web.search_api_key` or `BRAVE_API_KEY` / `TAVILY_API_KEY`) or `searxng` (`web.search_url`). Each search needs approval (rememberable per provider) because the query leaves your machine; results from `web.deny_domains` are dropped.

**Forged tools** — tools built by `universal_constructor` are saved with a manifest in `~/.code_puppy/uc_tools` and reloaded on start; `action: "delete"` removes one.

**Telemetry (OpenTelemetry)** — off by default, and nothing is sent unless you turn it on. With `[telemetry] enabled = true` (or `CODE_PUPPY_TELEMETRY=1`), traces and logs are exported over OTLP/HTTP to `telemetry.endpoint`, else `OTEL_EXPORTER_OTLP_ENDPOINT`, else `http://localhost:4318`. Other `OTEL_EXPORTER_OTLP_*` settings (headers, timeouts) apply. Each prompt is one trace: a `turn` span (agent, model, token counts, cost) containing the ADK's agent, model-call and tool spans, plus `approval` (time spent waiting on you), `hook` and `compact` spans. A session is a chain of turns, not one long trace, because sessions last days and resume in new processes. Every span carries the session as `gen_ai.conversation.id`, and each `turn` has a `turn.index` and a span link to the previous turn, even after `--resume`. Search by `gen_ai.conversation.id` to list a session, or follow the links turn by turn. Prompts, replies, tool arguments and tool results are **not** exported. The ADK attaches tool arguments and results to every tool span, so Code Puppy removes them before export. Set `capture_content = true` to include them, with secrets masked. Export runs on background goroutines and gives up after 3 s at exit if the collector is unreachable.
```toml
[telemetry]
enabled = true
endpoint = "http://localhost:4318"
# capture_content = false
```

**Resilience** — model requests are retried on rate limits, overload, 5xx responses and dropped connections, with exponential backoff that honours `Retry-After` (`llm.max_retries`, default 3; `0` disables retries). A request that sends nothing for `llm.stall_timeout_seconds` (default 600: no response headers, or a stream that goes quiet) fails instead of hanging the turn. Keep this above your longest non-streamed generation. A stream that fails partway through is not retried, because the text has already been shown. At most `tools.max_parallel` (default 8) tool calls from one model response run at once. Each sub-agent's calls are capped separately.

**Fallback models** — if the model fails before answering (after its retries: an outage, rate limit or bad credentials), the next one in `llm.fallback_models` answers instead:
```toml
[llm]
provider = "gemini"
fallback_models = ["anthropic/claude-sonnet-5", "gemini-3.5-flash-lite"]   # "provider/model", or a model of the same provider
```
Each provider uses its own credentials section. A failed model is skipped for 15 s, doubling up to 5 minutes, then tried again. You see one notice when a fallback takes over and one when the primary is back. Cost is priced by the model that answered. A model that fails after it started answering isn't replaced, because part of the answer is already on screen. For OpenRouter names that contain a slash, write the provider first: `openai/anthropic/claude-sonnet-5`. `code-puppy doctor --online` checks each model separately.

**Per-agent models** — agents can run on different models, e.g. a cheap one for reviews:
```toml
[agent_models]
qa-kitten = "anthropic/claude-haiku-4-5"
```
A pin wins over an agent's own `default_model` (agent frontmatter), and both win over the configured model. Pinned agents keep the `fallback_models` chain, and each agent's tokens are priced by its own model. `/pin_model` and `/unpin` change pins in the session and in the config file, keeping its comments. `doctor` checks each pinned model.

**Per-model settings** — generation settings for one model, which win over the global `code_puppy.temperature` and `max_tokens`:
```toml
[model_settings."gpt-5"]
temperature = 0.3
top_p = 0.9
max_tokens = 4096
seed = 7
```
The key is the model name; a `provider/` prefix is ignored (for OpenRouter names that contain a slash, write the provider first, as in `fallback_models`). Every model uses its own settings: the main model, pinned agents, and each model in `fallback_models`. `/model_settings gpt-5 temperature=0.3` changes them from the next model call and saves them, keeping the file's comments. A setting the provider doesn't accept is left out of the request (and `/model_settings` warns): OpenAI and Ollama have no `seed`; Anthropic takes only `max_tokens`, plus `temperature` on older models.

**Context & cost** — history is compacted automatically once a prompt reaches `context.token_threshold` tokens, or on demand with `/compact`. Estimated list prices for the default models are built in; override or add them under `[pricing."model-name"]` (`input_per_mtok`, `output_per_mtok`, `cached_input_per_mtok`, `cache_write_per_mtok`). Costs use the model that actually answered, so refusal fallbacks are priced correctly; `doctor` warns when the active model has no price.

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

Tools: `read_file`, `list_files`, `grep`, `create_file`, `replace_in_file`/`edit`, `delete_snippet`, `apply_patch` (unified diff or `*** Begin Patch`, atomic, multi-file), `delete_file`, `run_shell_command`, `manage_background_process`, `web_fetch`, `web_search` (when configured), `ask_user_question`, skills, `list_agents`/`invoke_agent`, `universal_constructor`, and MCP tools.

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

**Project docs:** [docs/ROADMAP.md](docs/ROADMAP.md) (what was built and why), [docs/MANUAL_VERIFICATION.md](docs/MANUAL_VERIFICATION.md) (checks that need a person), [docs/NEXT_STEPS.md](docs/NEXT_STEPS.md) (where to pick up), [docs/TRANSLATING.md](docs/TRANSLATING.md).
