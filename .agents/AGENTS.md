# Working on Code Puppy

Code Puppy is an AI coding agent in Go, built on the Google Agent Development Kit (`google.golang.org/adk/v2`). This file is for people and agents changing the code. Start with [NEXT_STEPS.md](NEXT_STEPS.md) (where work stands and what's next); [ROADMAP.md](ROADMAP.md) explains what was built and why, and [MANUAL_VERIFICATION.md](MANUAL_VERIFICATION.md) holds the checks that need a person.

## Layout

The layout follows [golang-standards/project-layout](https://github.com/golang-standards/project-layout): binaries in `cmd/`, implementation in `internal/`. `pkg/` is reserved for packages deliberately published for other modules to import; there are none yet, so new code goes in `internal/`.

| Path | What |
|---|---|
| `cmd/code-puppy` | The CLI: flags, `--dir`, observability, output modes, one-shot runs, `doctor`, `config` |
| `internal/app` | The program without a UI: `app.Open` builds a `Workspace` (registries, tools, sessions, model, engine) and exposes typed operations that return data and never print. Front ends drive it |
| `internal/runtime` | The engine over the ADK runner: models and providers, fallback, per-model settings, compaction, steering, side questions, usage and cost |
| `internal/tools` | Tools and their guardrails: workspace roots, approvals, command policy, OS sandbox, script sandbox (gVisor), skill scripts and their environments, web, MCP, hooks, checkpoints |
| `internal/tui` | The REPL: commands, rendering, input, steering keys |
| `internal/config` | Configuration (`.env.toml` through modenv) and its editing |
| `internal/session` | Saved sessions, snapshots, the ADK event store, transcript search |
| `internal/skills`, `internal/agents` | Skill and agent definitions (built-ins embedded) |
| `internal/i18n` | Message catalogs (`en-US`, `es`, `fr-CA`) |
| `internal/observability` | Diagnostic log and OpenTelemetry |
| `api/codepuppy/v1` | The service API as protos (`buf.yaml`, `buf.gen.yaml` at the root): `SessionService` (sessions, turns, steering, approvals), `WorkspaceService` (everything else `internal/app` does), `WorkerService` (ROADMAP item 24) |
| `internal/gen` | Generated Go and Connect code, committed. Never edit it: change the protos and run `make proto` |
| `tools` | A separate module pinning build tools (`buf`, `protoc-gen-go`, `protoc-gen-connect-go`) as `tool` directives, run with `go tool -modfile=tools/go.mod` |
| `.github/workflows` | `ci.yml` (vet, race tests, sandbox and gVisor checks on macOS and Linux; cross-compiled binaries must be identical on both), `release.yml` (GoReleaser, SBOMs, cosign) |

## Commands

```bash
make build                      # ./bin/code-puppy
make check                      # go vet + go test -race
make proto                      # lint, format and regenerate the API (internal/gen)
make proto-check                # fails if the protos aren't formatted or internal/gen is stale
./bin/code-puppy doctor --online
CODE_PUPPY_TELEMETRY=1 CODE_PUPPY_LOG_LEVEL=debug ./bin/code-puppy   # traces to http://localhost:4318; logs in ~/.code_puppy/logs
CODE_PUPPY_PYENV_TESTS=1 go test ./internal/tools -run 'PyEnv|InstallsPackages'   # builds real Python environments (network)
```

## Conventions

- One commit per feature, with a message explaining why.
- New user-facing strings go into all three catalogs in `internal/i18n/locales/` (`en-US`, `es`, `fr-CA`); `i18n_lint_test.go` enforces this.
- A bug fix comes with a test that was confirmed to fail without the fix (temporarily revert, run, restore).
- Each feature updates the README, a ROADMAP item, and a MANUAL_VERIFICATION section (all in `.agents/` except the README).
- Real-terminal behavior was checked with `script` against a local fake provider. Answer the line editor's cursor-position query (`ESC[6n`) with `ESC[1;1R` in the scripted input, or it waits forever.
- gVisor code only builds on Linux (`//go:build linux`, stub elsewhere); check `GOOS=linux go vet ./internal/tools` from macOS. Its tests need `runsc` (`RUNSC_PATH`); CI fails if they're skipped.
- License: Apache 2.0. New files need no header; `NOTICE` credits the original Python Code Puppy (MIT).
