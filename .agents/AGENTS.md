# Working on Code Puppy

Code Puppy is an AI coding agent in Go, built on the Google Agent Development Kit (`google.golang.org/adk/v2`). This file is for people and agents changing the code. Start with [NEXT_STEPS.md](NEXT_STEPS.md) (where work stands and what's next); [ROADMAP.md](ROADMAP.md) explains what was built and why, and [MANUAL_VERIFICATION.md](MANUAL_VERIFICATION.md) holds the checks that need a person.

## Layout

| Path | What |
|---|---|
| `cmd/code-puppy` | The CLI: flags, setup, one-shot runs, `doctor`, `config` |
| `pkg/runtime` | The engine over the ADK runner: models and providers, fallback, per-model settings, compaction, steering, side questions, usage and cost |
| `pkg/tools` | Tools and their guardrails: workspace roots, approvals, command policy, OS sandbox, script sandbox (gVisor), skill scripts and their environments, web, MCP, hooks, checkpoints |
| `pkg/tui` | The REPL: commands, rendering, input, steering keys |
| `pkg/config` | Configuration (`.env.toml` through modenv) and its editing |
| `pkg/session` | Saved sessions, snapshots, the ADK event store, transcript search |
| `pkg/skills`, `pkg/agents` | Skill and agent definitions (built-ins embedded) |
| `pkg/i18n` | Message catalogs (`en-US`, `es`, `fr-CA`) |
| `pkg/observability` | Diagnostic log and OpenTelemetry |
| `.github/workflows` | `ci.yml` (vet, race tests, sandbox and gVisor checks on macOS and Linux), `release.yml` (GoReleaser, SBOMs, cosign) |

## Commands

```bash
make build                      # ./bin/code-puppy
make check                      # go vet + go test -race
./bin/code-puppy doctor --online
CODE_PUPPY_TELEMETRY=1 CODE_PUPPY_LOG_LEVEL=debug ./bin/code-puppy   # traces to http://localhost:4318; logs in ~/.code_puppy/logs
CODE_PUPPY_PYENV_TESTS=1 go test ./pkg/tools -run 'PyEnv|InstallsPackages'   # builds real Python environments (network)
```

## Conventions

- One commit per feature, with a message explaining why.
- New user-facing strings go into all three catalogs in `pkg/i18n/locales/` (`en-US`, `es`, `fr-CA`); `i18n_lint_test.go` enforces this.
- A bug fix comes with a test that was confirmed to fail without the fix (temporarily revert, run, restore).
- Each feature updates the README, a ROADMAP item, and a MANUAL_VERIFICATION section (all in `.agents/` except the README).
- Real-terminal behavior was checked with `script` against a local fake provider. Answer the line editor's cursor-position query (`ESC[6n`) with `ESC[1;1R` in the scripted input, or it waits forever.
- gVisor code only builds on Linux (`//go:build linux`, stub elsewhere); check `GOOS=linux go vet ./pkg/tools` from macOS. Its tests need `runsc` (`RUNSC_PATH`); CI fails if they're skipped.
- License: Apache 2.0. New files need no header; `NOTICE` credits the original Python Code Puppy (MIT).
