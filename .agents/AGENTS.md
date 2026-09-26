# Working on Blitz

Blitz is an AI coding agent in Go, built on the Google Agent Development Kit (`google.golang.org/adk/v2`). This file is for people and agents changing the code. Start with [NEXT_STEPS.md](NEXT_STEPS.md) (where work stands and what's next); [ROADMAP.md](ROADMAP.md) explains what was built and why, and [MANUAL_VERIFICATION.md](MANUAL_VERIFICATION.md) holds the checks that need a person.

## Layout

The layout follows [golang-standards/project-layout](https://github.com/golang-standards/project-layout): binaries in `cmd/`, implementation in `internal/`. `pkg/` is reserved for packages deliberately published for other modules to import; there are none yet, so new code goes in `internal/`.

| Path | What |
|---|---|
| `cmd/blitz` | The CLI: flags, `--dir`, observability, output modes, one-shot runs, `doctor`, `config`, `serve` |
| `internal/app` | The program without a UI: `app.Open` builds a `Workspace` (registries, tools, sessions, model, engine) and exposes typed operations that return data and never print. Front ends drive it |
| `internal/runtime` | The engine over the ADK runner: models and providers, fallback, per-model settings, compaction, steering, side questions, usage and cost |
| `internal/tools` | Tools and their guardrails: workspace roots, approvals, command policy, OS sandbox, script sandbox (gVisor), skill scripts and their environments, web, MCP, hooks, checkpoints |
| `internal/tui` | The REPL: commands, rendering, input, steering keys |
| `internal/config` | Configuration (`.env.toml` through modenv) and its editing |
| `internal/session` | Saved sessions, snapshots, the ADK event store, transcript search |
| `internal/skills`, `internal/agents` | Skill and agent definitions (built-ins embedded) |
| `internal/i18n` | Message catalogs (`en-US`, `es`, `fr-CA`) |
| `internal/observability` | Diagnostic log and OpenTelemetry |
| `api/blitz/v1` | The service API as protos (`buf.yaml`, `buf.gen.yaml` at the root): `SessionService` (sessions, turns, steering, approvals), `WorkspaceService` (everything else `internal/app` does), `WorkerService` (ROADMAP item 24) |
| `cmd/blitz-desktop` | The desktop app (Wails v2): **its own Go module** (cgo, WebKit), forwarding the page's API calls to the service socket. `make desktop`, `make desktop-check` |
| `web/desktop` | The desktop app's page: React + TypeScript (pnpm), the generated Connect client in `src/gen` |
| `internal/server` | The service: Connect handlers over `app.Workspace` (translation only), the approval/question broker, the Unix socket. `blitz serve` runs it |
| `internal/gen` | Generated Go and Connect code, committed. Never edit it: change the protos and run `make proto` |
| `tools` | A separate module pinning build tools (`buf`, `protoc-gen-go`, `protoc-gen-connect-go`) as `tool` directives, run with `go tool -modfile=tools/go.mod` |
| `.github/workflows` | `ci.yml` (vet, race tests, sandbox and gVisor checks on macOS and Linux; cross-compiled binaries must be identical on both), `release.yml` (GoReleaser, SBOMs, cosign) |

## Commands

```bash
make build                      # ./bin/blitz
make check                      # go vet + go test -race
make proto                      # lint, format and regenerate the API (internal/gen)
make proto-check                # fails if the protos aren't formatted or internal/gen is stale
make desktop                    # build/desktop/bin/Blitz.app (needs pnpm; Linux also webkit2gtk)
make desktop-check              # build the page, vet and test the desktop module
./bin/blitz doctor --online
BLITZ_TELEMETRY=1 BLITZ_LOG_LEVEL=debug ./bin/blitz   # traces to http://localhost:4318; logs in ~/.blitz/logs
BLITZ_PYENV_TESTS=1 go test ./internal/tools -run 'PyEnv|InstallsPackages'   # builds real Python environments (network)
```

## Conventions

- One commit per feature, with a message explaining why.
- New user-facing strings go into all three catalogs in `internal/i18n/locales/` (`en-US`, `es`, `fr-CA`); `i18n_lint_test.go` enforces this.
- A bug fix comes with a test that was confirmed to fail without the fix (temporarily revert, run, restore).
- Each feature updates the README, a ROADMAP item, and a MANUAL_VERIFICATION section (all in `.agents/` except the README).
- Real-terminal behavior was checked with `script` against a local fake provider. Answer the line editor's cursor-position query (`ESC[6n`) with `ESC[1;1R` in the scripted input, or it waits forever.
- gVisor code only builds on Linux (`//go:build linux`, stub elsewhere); check `GOOS=linux go vet ./internal/tools` from macOS. Its tests need `runsc` (`RUNSC_PATH`); CI fails if they're skipped.
- License: Apache 2.0. New files need no header; `NOTICE` credits the original Python Blitz (MIT).
