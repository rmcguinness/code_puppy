# Code Puppy Go — Where to Pick Up

Written 2026-09-24 at commit `7e211697` on `main`; updated 2026-09-25. Read this first when resuming. [ROADMAP.md](ROADMAP.md) has the detail behind each finished item, and [MANUAL_VERIFICATION.md](MANUAL_VERIFICATION.md) has the checks that need a person.

## State

Roadmap items 1–22 are done and committed, and item 23 (the desktop app) is in progress; each has its own commit:

| Commit | Change |
|---|---|
| `383ab847` | Per-file locks for parallel edits; refuse to write a file that changed during approval |
| `ae509e39` | Earlier work: Anthropic provider, workspace-scoped sessions, `/compact`, MCP prefixes/agents, saved forged tools, web search, pricing, i18n, images |
| `bd0a0bb5` | `slog` diagnostic log + opt-in OpenTelemetry (content stripped), chained turn traces |
| `04bd68dc` | `post_tool` hooks on an ordered background worker |
| `b13ba38d` | Retries for every provider, stall timeouts, MCP stdio restart + circuit breaker, parallel tool cap |
| `72acd1c4` | Steering a running turn (type or Ctrl+T) |
| `4d1f31c5` | `!cmd`, enforced `/plan` and `--plan`, `/tools`, `/show` |
| `042263dd` | Ordered model fallback across providers; Ollama base-URL fix |
| `7a06b7a6` | Default Gemini model → `gemini-3.8-flash` |
| `7e211697` | Per-agent models (`[agent_models]`, `/pin_model`, `/unpin`) |
| `b652f263` | Per-model settings (`[model_settings]`, `/model_settings`) |
| `36c2758b` | Removed the committed editor swap file; `*.swp` ignored |
| `957c08b9` | Named session snapshots (`/session save`, `/session load <name>`, `--resume=<name>`) |
| `9eeab5e6` | Google search via Gemini grounding; `/search web`, `/search session` |
| `155d595b`, `a5f34180`, `e2f02959` | Item 22: Castor skill definitions and `[skills.policy]`; the gVisor/OS script sandbox; environments, `run_skill_script`, `/envs` |
| (the commit removing `python/`) | Go-only repository: Python removed (tag `python-final`), Go at the root, workflows `ci.yml` and `release.yml`, Apache 2.0 with `NOTICE`, planning docs in `.agents/` |
| `1370b80f` | Side questions (`/btw`) |
| `56a870a4`, `dce9baa9`, `c239dfb8`, `f318498d` | Dependabot and a macOS release note; pinned release tools, `GOTOOLCHAIN=local`, a cross-host reproducibility check; project-layout with code in `internal/`; actions upgraded (checkout v7, setup-go v7, Node 24) |
| `8ce5c642` | ROADMAP item 23, phase 2: `app.Open` and `app.Workspace` replace `cmd`'s `buildEnv` |
| `75630f18` | Phase 3: `Workspace.Run` and `Steer`, one turn lifecycle for the REPL and one-shot runs |
| `e657a7bd` | Phase 4, agents and models: typed operations for `/agents`, `/agent`, `/model`, `/pin_model`, `/unpin`, `/model_settings`, `/set` |
| `ef91966a` | Phase 4, sessions: `/session list/new/load/save`, `/resume`, `/rename` |
| `8bbe0b94` | Phase 4, checkpoints and approvals: `/undo`, `/checkpoints`, `/diff`, `/approvals` |
| `5bf56f4c` | Phase 4, skills, envs, MCP and tools: `/skills`, `/envs`, `/mcp`, `/tools` |
| `4ddbb783` | Phase 4, the rest: `/cost`, `/context`, `/compact`, `/memory`, `/locale`, `/sandbox`, `/attach`, `/paste`, `/search`; `tui.App` reduced to the workspace and terminal state |
| (the commit adding `internal/session/title_test.go`) | Session names from the first prompt, `/rename`, terminal title, resume hint on exit |

`go vet ./...` and `go test -race ./...` pass.

## Dated reminders

- **2027-01-01: update the `gemini-3.8-flash` price** in `internal/config/features.go` (`DefaultPricing`) from the introductory $0.75 / $3.75 / $0.075 to $1.50 / $7.50 / $0.15 per 1M tokens. Until then, `/cost` shows about half the real cost for this model. Check https://ai.google.dev/gemini-api/docs/pricing first.

## Open work, in suggested order

1. **Desktop app: ROADMAP item 23.** Phases 2–4 are done: `internal/app` is a UI-independent core with typed operations, and `tui` only parses and renders. Decided: one service per user, CLI attaches when available, Connect + buf. Next is phase 5 (`app.Event`), then 5b (per-workspace state), then the protos. The phases and decisions are in the ROADMAP item.
2. **Manual verification (needs a person).** Nothing in `MANUAL_VERIFICATION.md` has been run yet. It covers real providers (💲 = paid calls), terminal behavior, the macOS and Linux sandboxes, MCP, steering, fallback and pinning. Record results in the file; any failure becomes the next task.
3. **Optional: notarize the macOS binaries** (needs an Apple Developer account). Until then, the release notes (GoReleaser `release.footer`) and the README explain clearing the quarantine flag. Dependabot (`.github/dependabot.yml`) keeps the pinned action SHAs current. `~/.gnupg/gpg-agent.conf` now points at GPG Suite's `pinentry-mac`; the next tag will show whether signing works.
4. **Linux sandbox startup cost.** Before every sandboxed command, `expandBlocked` (`internal/tools/bwrap.go`) scans the writable roots, including the temp and cache directories (e.g. the Go build cache), for blocked names, up to 50,000 entries. Measured in a Linux container: about 0.1 s per command normally, 3.4 s under `-race`. Worth reducing (e.g. skip cache directories for name patterns, or reuse a recent scan), keeping in mind that a file created between scans could then escape masking. CI's parallel-cap test runs with the sandbox off because of this.
5. **Skill scripts, follow-ups** (ROADMAP item 22 is done: Castor definitions, `[skills.policy]`, the gVisor/OS `ScriptBox`, environments, `run_skill_script`, `/envs`):
   - TypeScript scripts;
   - scripts that write the workspace directly, with snapshots;
   - `requires-python` with uv-managed interpreters;
   - `storage_uri` and resources;
   - running the opt-in real-install tests in CI (`CODE_PUPPY_PYENV_TESTS=1`);
   - adding a network field to Castor's proto, instead of `custom_hints.network`.
6. **Optional: reasoning settings per model.** Python's `/model_settings` also sets `reasoning_effort`, extended thinking and budgets. The Go wrapper (`internal/runtime/settings.go`) is where they'd go, mapped to genai `ThinkingConfig`, which each adapter translates differently.
7. **Optional, from the Antigravity review (ROADMAP, "Antigravity CLI review"):** `/copy` (last reply to the clipboard, with OSC 52 over SSH), `--add-dir <path>` at startup, `/grill-me` (the agent interviews you before coding), and `/fork [n]` (branch a new session from an earlier turn).

## Decisions already made (don't redo without a reason)

- **No shared event bus.** Considered for steering and dropped: audit, hooks and traces are fed from engine callbacks, and steering didn't need a bus.
- **Steering rides on tool results** (`message_from_user`), because ADK model callbacks can't add session events (`Session()` returns nil there).
- **Each turn is its own trace root.** Turns are linked to the previous turn and tagged `gen_ai.conversation.id`; the previous turn's traceparent is kept in session metadata as `last_turn`. A session is never one long trace.
- **Content is never exported by default.** The ADK puts tool arguments and results on every `execute_tool` span, so `internal/observability` filters them before export. OTel providers are built here, not with `adk/telemetry.New`, which adds its own unfiltered exporter.
- **The Python implementation is gone** (2026-09-26): the Go code is the repository root, and `python-final` tags the last commit with Python. Don't reintroduce a second implementation.
- **Not ported from Python:** `/cd` (the workspace is the sandbox root; use `-d`), `/truncate` (use `/compact`) and `/tutorial`. Reasons are in ROADMAP item 14.
- **`fallback_models` switches only before any output** and never on cancellation. Breakers are asked just before each attempt (regression tests guard this).
- **Model settings are applied per built model, not per agent.** `newProviderModel` wraps every model, so each member of a fallback chain uses its own `[model_settings]`. Applying them in `newLLMAgent` would give fallbacks the primary's settings.
- **Snapshots are never continued in place.** Loading one (by name or ID, `/session load`, `/resume`, `--resume=`) copies it to a new session, as Python's `/load_context` does, and `--continue` skips snapshots.
- **No Anthropic server-side web search** (dropped by the user). **No Google Custom Search JSON API**: it shuts down on 2027-01-01, so `google` means Gemini grounding.
- **`/search web` pre-approves exactly the URLs it hands over, for that turn only** (`tools.WithFetchGrants`); everything else the agent fetches still asks.
- **Telemetry and OTel need one provider per process:** the ADK binds its tracer to the first global provider.
- **No Bazel** (2026-09-26). Go modules, `CGO_ENABLED=0` and GoReleaser already give reproducible CLI builds, and the planned Wails app needs host cgo libraries (WebKit, webkit2gtk) that Bazel can't make hermetic. Close the gaps by pinning tools instead; reconsider only for a multi-language monorepo or a need for remote caching. Make stays as a thin entry point.
- **Layout follows golang-standards/project-layout** (2026-09-26): `cmd/code-puppy` (CLI/TUI, pure Go) and `cmd/code-puppy-desktop` (Wails, cgo, built on each OS); code in `internal/`, `pkg/` only for deliberately public APIs; frontend in `web/desktop`, packaging in `build/`. Slash-command logic moves from `tui` to a UI-agnostic `internal/app` shared by both UIs.
- **One engine service per user** (2026-09-26, replacing "a process per tab"): `code-puppy serve` hosts every workspace over Connect on a Unix socket; the CLI attaches when it's running, else runs in-process. Batch functions come later. See ROADMAP item 23.
- **The module path stays `github.com/retail-cortex/code_puppy`** even though the repository is `rmcguinness/code_puppy`.

Conventions, layout and commands: [AGENTS.md](AGENTS.md).
