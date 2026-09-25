# Code Puppy Go — Gap Implementation Plan

Status key: ✅ done · 🔜 next · 📋 planned · 🔍 needs investigation first

Each item lists the problem, the approach, where the change lands, how it is tested, and a rough size (S ≤ half a day, M ≈ 1–2 days, L ≈ 3+ days).

---

## 1. Anthropic provider — ✅ done (S/M)

**Problem.** `llm.provider = "anthropic"` was accepted by config but `runtime.NewModel` had no Anthropic branch, so startup failed. The ADK ships no Anthropic model.

**Approach.** A `model.LLM` adapter over the official `anthropic-sdk-go` (`pkg/runtime/anthropic.go`):
- genai ⇄ Messages API translation: system instruction → cached `system` block; function declarations → tools (JSON Schema, including conversion of genai `Schema` enums); `FunctionCall`/`FunctionResponse` ⇄ `tool_use`/`tool_result` (parallel results merged into one user turn); thinking blocks round-tripped unchanged (text + signature in `Thought`/`ThoughtSignature`).
- Streaming (partial text events, then one aggregated final event, matching the Gemini path) and non-streaming.
- Usage mapped to genai usage metadata (cache reads counted as cached input) so `/cost` works.
- Sampling parameters only sent to models that accept them (current models reject `temperature` with a 400).
- Refusals surfaced as a safety finish reason; server-side refusal fallback enabled by default on `claude-opus-5` / `claude-fable-5-1` (`llm.anthropic.fallback_model`, empty to disable).
- Credentials: `api_key`, else the SDK's own resolution (`ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ant auth login` profile). Optional `base_url` for gateways.

Also fixed: the model name now resolves per provider (`code_puppy.default_model` if set, else `llm.<provider>.model`), which also affected OpenAI/Ollama.

**Tests.** Translation unit tests + an `httptest` fake of the Messages API covering tool loops, streaming SSE, thinking round trip, usage, refusal, and API errors. No paid calls in the suite.

---

## 2. Workspace-scoped sessions — ✅ done (S)

**Problem.** `--continue`, `--resume` (no ID), `/session list` and `/resume` completion used the most recent session from *any* project.

**Approach.** Sessions record their canonical workspace path. `--continue`/`--resume` pick the latest session for the current workspace; `/session list` shows this workspace (`/session list --all` shows everything with its workspace). An explicit `--resume <id>` from another workspace still works but warns. Sessions saved before this change have no workspace and only appear under `--all`.

**Tests.** Storage filtering, legacy records, `selectSession` scoping, REPL listing.

---

## 3. Manual `/compact` — ✅ done (M)

**Problem.** Compaction only runs when a prompt crosses `context.token_threshold`.

**Approach.** Investigate whether the ADK exposes a compaction entry point outside the runner. If not: run `compaction.LLMSummarizer` over the session's events and append an event carrying `EventActions.Compaction` via the session service (the runner already honours these on replay). Add `/compact [focus text]` and a `--compact` flag for resumed sessions.

**Files.** `pkg/runtime/engine.go` (new `Engine.Compact`), `pkg/tui/commands_extra.go`.
**Tests.** Mock LLM summarizer; assert the next request's contents shrink and contain the summary; persisted sessions replay the compaction.
**Outcome.** The ADK only honours compaction events when the runner has a compaction config, so one is now always set (with an unreachable threshold when automatic compaction is off). The cut is made at a user-turn boundary so a tool call is never separated from its result, and earlier summaries are fed to the summarizer so rolling compactions don't lose history. No `--compact` flag was added; `/compact` covers resumed sessions.

---

## 4. MCP tools for sub-agents, and name prefixes — ✅ done (M)

**Problem.** MCP tools are only offered to the primary agent, and a server's tool names can't be namespaced (collisions are skipped with a warning).

**Approach.**
- Per-server `agents = ["code-puppy", "qa-kitten"]` (default: primary agent only) — pass the matching toolsets to `newLLMAgent` for sub-agents and `InvokeSubagent`.
- Per-server `prefix = "gh"`: wrap each MCP tool in a delegating tool that renames it (`gh__create_issue`). Needs the ADK's function-tool interfaces (`Declaration()`, `Run()`); confirm they're implementable outside the module, otherwise build the declaration from the MCP tool schema and call the MCP client directly.

**Files.** `pkg/tools/mcp.go`, `pkg/runtime/engine.go`, `pkg/config/features.go`.
**Tests.** In-memory MCP servers: sub-agent sees tools; prefixed names route to the right server; approvals keyed by the original name.

---

## 5. Persist forged tools (`universal_constructor`) — ✅ done (S)

**Problem.** Tools are written to `~/.code_puppy/uc_tools` but the registry of names/languages/descriptions is in memory, so they vanish on restart.

**Approach.** Write a `<name>.json` manifest beside each script; load manifests at startup (validate the name pattern, language allow-list, and that the script is inside the tools dir). Add `action: "delete"`.

**Tests.** Create → new registry instance lists and runs it; tampered manifest (path escape, bad language) is ignored.

---

## 6. Web search — ✅ done (M, option b)

**Problem.** Only `web_fetch` exists.

**Approach.** A `web_search` tool behind a provider interface. Options: (a) Anthropic's server-side `web_search` tool when the provider is Anthropic (no extra key, results cited); (b) a pluggable HTTP backend (Brave/Tavily/SearXNG) with `web.search_provider` + key for other providers. Results go through the same approval-per-host and SSRF rules for any follow-up fetch.

**Tests.** Fake backends via `httptest`; approval and deny-domain behaviour.
**Outcome.** Implemented option (b) — Brave, Tavily and SearXNG — which works with every provider. Anthropic's server-side search (option a) is not implemented: it needs server-tool result blocks round-tripped through the adapter.

---

## 7. Pricing accuracy — ✅ done (S)

**Problem.** Built-in prices are estimates for a handful of models and go stale.

**Approach.** Keep config overrides as the source of truth; show "estimated" in `/cost`; account for cache-write premiums (`cache_write_per_mtok`, reported by the Anthropic adapter); price by the model that actually served the response (refusal fallbacks); add a `code-puppy doctor` warning when the active model has no price.
**Outcome.** Done. Defaults cover the three default Gemini/OpenAI models plus Claude Opus 5, Sonnet 5 and Haiku 4.5; other models need `[pricing]` entries rather than guessed prices.

---

## 8. Docs — ✅ done (S)

`COMPARISON.md` predates the sandbox, approvals, sessions and release work — rewrite it against the Python version; keep README's config snippets in sync with `config init`.

---

## 10. Diagnostic log and OpenTelemetry — ✅ done (M)

**Problem.** No application log (warnings only reached stderr) and no tracing. The ADK already creates OpenTelemetry spans for agents, model calls and tools, but no provider was installed, so they were discarded.

**Approach.** `pkg/observability`:
- **Log:** `slog` JSON lines to `~/.code_puppy/logs`. `Write` never blocks: records go into a bounded channel, and a single writer goroutine appends them, flushing once per batch. Drops are counted and reported. Secrets are masked, and trace and span IDs are added. It is the process's `slog` default and is forwarded to OTel when telemetry is on.
- **Telemetry:** off by default; `[telemetry]` or `CODE_PUPPY_TELEMETRY=1`. Code Puppy builds the OTLP/HTTP trace and log providers itself rather than calling `adk/telemetry.New`, which would add its own unfiltered exporter from `OTEL_*` variables. The ADK attaches tool arguments and results to every `execute_tool` span even with content capture off, so a filtering exporter removes content attributes (or masks secrets in them with `capture_content`) on the batch goroutine before export. `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` is forced off unless content capture is on. The default endpoint is `http://localhost:4318` (the Go exporter would use https). Shutdown is bounded to 3 s.
- **Spans:** `turn` (in `Engine.Execute`: agent, model, tokens, cost), `approval` (separates user wait from tool time), `hook <event>`, `compact`.
- **Sessions:** each turn is its own trace root. One trace per session would fail: spans only export when they end, sessions last days, and `--resume` continues in another process. Turns carry `gen_ai.conversation.id` (the key the ADK uses) and `turn.index`, plus a span link to the previous turn. That turn's `traceparent` is saved in the session's metadata (`last_turn`), so the chain survives resume. Nothing is saved with telemetry off.

**Constraint.** Install telemetry once per process: the ADK binds its tracer to the first global provider.
**Tests.** Log: concurrency, masking, drops without blocking, pruning, `off`, trace IDs. Exporter: content dropped or masked, log records masked and linked. Real OTLP/HTTP export to an `httptest` collector. An ADK run whose tool span nests under `turn` without file content; three turns chained across a simulated resume. Also smoke-tested with the binary against a local collector.
**Cost.** About 1 MB of binary; the stripped darwin/arm64 build is 59.4 MB.

---

## 11. `post_tool` hooks off the hot path — ✅ done (S)

**Problem.** `afterTool` ran `post_tool` hooks inline, so each tool call waited for them (up to 30 s per hook) even though they can't change the result.

**Approach.** `PostTool` encodes the event right away (a snapshot, so later changes to the result maps can't race with the hook), then queues it for one worker goroutine. The worker starts only when a hook first matches, and a single worker keeps events in order. The queued job drops the turn's cancellation but keeps its trace, so hooks run after the turn ends or is interrupted and their spans still sit under the tool. The queue is bounded: when full, events are dropped (one warning, logged and audited) rather than blocking the agent. `Registry.Close` drains the queue for up to 5 s, then kills what is still running. `pre_tool` and `prompt_submit` stay synchronous because they can block.

**Tests.** Returns immediately with a slow hook; order and snapshot over 20 events; runs after the turn context is cancelled; worker only for matching tools; `Close` drains, stops hung hooks, and is idempotent; a full queue drops without blocking and warns once.

---

## 12. Resilience — ✅ done (M)

**Findings.**
- The Gemini SDK doesn't retry unless configured, and it wasn't: one 429 or 503 failed the turn. The Anthropic and OpenAI SDKs retry twice by default.
- No provider had timeouts, so a hung connection or stalled stream blocked a turn forever. In CI there's no Ctrl+C to recover.
- A crashed MCP stdio server could never reconnect. The SDK's `CommandTransport` wraps one `exec.Cmd`, which can only be started once (`exec: Stdout already set`). It also bypassed the process guard's startup step.
- A down MCP server was contacted, with no timeout, before every model call, and printed a warning each time.
- The ADK runs all tool calls of a response at once, with no upper limit.

**Changes.**
- **Model requests** (`pkg/runtime/httpclient.go`): one retry budget for all providers (`llm.max_retries`, default 3). Gemini gets `HTTPRetryOptions` (backoff from 1 s up to 30 s); the other two SDKs keep their own backoff and `Retry-After` handling. A shared HTTP client sets 30 s dial and 15 s TLS timeouts, and a stall timeout (`llm.stall_timeout_seconds`, default 600) on response headers and on gaps between body reads, so steady streams are never cut off.
- **MCP**: a stdio transport that starts a fresh guarded process on each connect and kills the previous one. A circuit breaker per server: it opens after 2 consecutive failures, skips the server for 15 s doubling to 5 min, then allows one trial call. Tool errors reported by the server don't count against it. Listing times out after 30 s, calls after `timeout_seconds` (default 300). Warnings go out on the first failure, on pause and on recovery.
- **Parallel tool cap**: an ADK `TaskRunner` caps each batch of tool calls (`tools.max_parallel`, default 8). The cap is per batch, so a sub-agent's calls running inside a parent's call can't deadlock.
- **Hook worker**: a panic is contained to one event and logged.

**Not changed.** A stream that fails mid-response isn't retried (the text is already on screen). There is no model fallback (Python's round-robin), and session files aren't fsynced (a crash loses nothing the OS already has; a power loss can lose the last event).

**Tests.** Stall, steady stream and header timeouts. For each provider: a server that fails twice with 529/503/429, then 3 attempts with `max_retries = 2`, 1 attempt with 0, and no retry on 400. Breaker state machine on a fake clock. A down server is skipped after 2 attempts and recovers after the cooldown. The test binary runs as a real stdio MCP server: a crash is followed by a new process, a slow call times out, and a tool error keeps the server healthy. The restart test was confirmed to fail with the old transport. Runner: concurrency cap, every task runs, nested batches don't deadlock, and the engine really serialises with `max_parallel = 1`. Worker survives a panic.

---

## 13. Steering mid-turn — ✅ done (M)

**Problem.** A long turn couldn't be corrected without Ctrl+C, which throws the turn's work away. Python lets you press Ctrl+T and type a message that the model sees before its next call.

**Approach.**
- **Delivery (`runtime.Engine.Steer`):** messages are queued per session and attached to the next tool result as `message_from_user`. The obvious alternative, adding a user message in a before-model callback, doesn't work: ADK callback contexts return nil from `Session()`, so the message would reach one request and then vanish from history. Tool results are recorded by the ADK in order, so a steer is seen by every later call, survives compaction and `--resume`, and avoids provider rules about message order. A failed tool's error is kept. Sub-agent tools (other sessions) don't take the parent's messages. Anything left when the turn ends is returned by `TakeSteers`.
- **Keyboard (`pkg/tui/keywatch*.go`):** the line editor can't abandon a read, so a key watcher owns the terminal between prompts during a turn. It switches off line buffering and echo but keeps signals, so Ctrl+C works as before, and disables macOS's Ctrl+T STATUS key. It uses `select(2)` with a 50 ms timeout, since `poll(2)` doesn't work on macOS terminals. Typing or Ctrl+T opens a steer prompt pre-filled with what was typed, with printer output held back until Enter. The spinner returns only if it was showing. Approval and question prompts pause the watcher, and an open steer prompt finishes first. The editor only reads stdin when a prompt asks, so the two never race. Other platforms: the watcher exits and turns behave as before.
- **REPL:** steer messages pass `prompt_submit` hooks and are audited and recorded like prompts. A message that arrives after the model's last tool call is sent as the next prompt, or reported as not sent if the turn was interrupted.

**Not done.** The shared event stream proposed alongside this: steering didn't need it, and audit, hooks and traces are already fed from engine callbacks.

**Tests.** Engine: delivery on the next tool result, history across turns and in session events, failed-tool errors kept, leftovers returned, scoping to the session and away from sub-agents. Watcher (a channel-backed fake terminal): triggers, ignored keys (Enter, arrow keys), pausing for prompts with no lost keys, pause waiting for an open steer prompt or giving up on its context, and the terminal mode restored. Printer: output held and flushed in order; spinner rule. REPL with a real engine and a fake keyboard: mid-turn delivery, a late message becoming the next prompt (recorded once), and a hook blocking a message. Also driven for real through a pseudo-terminal (`script`) against a local fake OpenAI server: the second request carried the message.

---

## 9. Real-environment verification — 🔜 needs a person; see [MANUAL_VERIFICATION.md](MANUAL_VERIFICATION.md)

Not automatable here; run once and record results in this file:

| Check | How |
|---|---|
| Gemini end-to-end | Real key: streaming, tool loop, `/cost` numbers, compaction at a low `token_threshold` |
| Anthropic end-to-end | Same, plus a thinking + tool-use turn and `--resume` of that session |
| OpenAI / Ollama | Tool calls, including Ollama's text-encoded calls |
| Terminal UX | History, Ctrl+R, Tab (`/`, `@path`), multi-line, Ctrl+C at prompt/approval/turn, Markdown, spinner, resize |
| Real MCP server | `npx @modelcontextprotocol/server-github` (or filesystem) via stdio, sandboxed |
| Windows | Build runs; shell tools need Git Bash or WSL; document what's unsupported (no OS sandbox / process guard) |
| First tagged release | Keyless cosign verify, SBOM attached, draft published |

---

## Accepted limitations (documented, not planned)

- The command allow/deny lists are a guardrail; the OS sandbox is the boundary.
- Shell commands can read anything outside `blocked_paths`.
- On Linux, a blocked-name file created by a command is visible to that same command (bubblewrap masks paths that exist at start).
- File-tool symlink checks are resolved at call time; a concurrent swap between check and open is theoretically possible (`os.Root` still prevents escaping the roots).
