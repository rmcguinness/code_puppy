# Code Puppy Go — Gap Implementation Plan

Status key: ✅ done · 🔜 next · 📋 planned · 🔍 needs investigation first

Items 10–22 were added after the first plan and appear before item 9, which stays last because it needs a person. Open work and resume notes: [NEXT_STEPS.md](NEXT_STEPS.md).

Each item lists the problem, the approach, where the change lands, how it is tested, and a rough size (S ≤ half a day, M ≈ 1–2 days, L ≈ 3+ days).

---

## 1. Anthropic provider — ✅ done (S/M)

**Problem.** `llm.provider = "anthropic"` was accepted by config but `runtime.NewModel` had no Anthropic branch, so startup failed. The ADK ships no Anthropic model.

**Approach.** A `model.LLM` adapter over the official `anthropic-sdk-go` (`internal/runtime/anthropic.go`):
- genai ⇄ Messages API translation: system instruction → cached `system` block; function declarations → tools (JSON Schema, including conversion of genai `Schema` enums); `FunctionCall`/`FunctionResponse` ⇄ `tool_use`/`tool_result` (parallel results merged into one user turn); thinking blocks round-tripped unchanged (text + signature in `Thought`/`ThoughtSignature`).
- Streaming (partial text events, then one aggregated final event, matching the Gemini path) and non-streaming.
- Usage mapped to genai usage metadata (cache reads counted as cached input) so `/cost` works.
- Sampling parameters only sent to models that accept them (current models reject `temperature` with a 400).
- Refusals surfaced as a safety finish reason; server-side refusal fallback enabled by default on `claude-opus-5` / `claude-fable-5-1` (`llm.anthropic.fallbacks`: `default`, a model ID, or `off`).
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

**Files.** `internal/runtime/engine.go` (new `Engine.Compact`), `internal/tui/commands_extra.go`.
**Tests.** Mock LLM summarizer; assert the next request's contents shrink and contain the summary; persisted sessions replay the compaction.
**Outcome.** The ADK only honours compaction events when the runner has a compaction config, so one is now always set (with an unreachable threshold when automatic compaction is off). The cut is made at a user-turn boundary so a tool call is never separated from its result, and earlier summaries are fed to the summarizer so rolling compactions don't lose history. No `--compact` flag was added; `/compact` covers resumed sessions.

---

## 4. MCP tools for sub-agents, and name prefixes — ✅ done (M)

**Problem.** MCP tools are only offered to the primary agent, and a server's tool names can't be namespaced (collisions are skipped with a warning).

**Approach.**
- Per-server `agents = ["code-puppy", "qa-kitten"]` (default: primary agent only) — pass the matching toolsets to `newLLMAgent` for sub-agents and `InvokeSubagent`.
- Per-server `prefix = "gh"`: wrap each MCP tool in a delegating tool that renames it (`gh__create_issue`). Needs the ADK's function-tool interfaces (`Declaration()`, `Run()`); confirm they're implementable outside the module, otherwise build the declaration from the MCP tool schema and call the MCP client directly.

**Files.** `internal/tools/mcp.go`, `internal/runtime/engine.go`, `internal/config/features.go`.
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
**Outcome.** Implemented option (b) — Brave, Tavily and SearXNG — which works with every provider. Anthropic's server-side search (option a) was later dropped as not needed; Google search came instead (item 19).

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

**Approach.** `internal/observability`:
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
- **Model requests** (`internal/runtime/httpclient.go`): one retry budget for all providers (`llm.max_retries`, default 3). Gemini gets `HTTPRetryOptions` (backoff from 1 s up to 30 s); the other two SDKs keep their own backoff and `Retry-After` handling. A shared HTTP client sets 30 s dial and 15 s TLS timeouts, and a stall timeout (`llm.stall_timeout_seconds`, default 600) on response headers and on gaps between body reads, so steady streams are never cut off.
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
- **Keyboard (`internal/tui/keywatch*.go`):** the line editor can't abandon a read, so a key watcher owns the terminal between prompts during a turn. It switches off line buffering and echo but keeps signals, so Ctrl+C works as before, and disables macOS's Ctrl+T STATUS key. It uses `select(2)` with a 50 ms timeout, since `poll(2)` doesn't work on macOS terminals. Typing or Ctrl+T opens a steer prompt pre-filled with what was typed, with printer output held back until Enter. The spinner returns only if it was showing. Approval and question prompts pause the watcher, and an open steer prompt finishes first. The editor only reads stdin when a prompt asks, so the two never race. Other platforms: the watcher exits and turns behave as before.
- **REPL:** steer messages pass `prompt_submit` hooks and are audited and recorded like prompts. A message that arrives after the model's last tool call is sent as the next prompt, or reported as not sent if the turn was interrupted.

**Not done.** The shared event stream proposed alongside this: steering didn't need it, and audit, hooks and traces are already fed from engine callbacks.

**Tests.** Engine: delivery on the next tool result, history across turns and in session events, failed-tool errors kept, leftovers returned, scoping to the session and away from sub-agents. Watcher (a channel-backed fake terminal): triggers, ignored keys (Enter, arrow keys), pausing for prompts with no lost keys, pause waiting for an open steer prompt or giving up on its context, and the terminal mode restored. Printer: output held and flushed in order; spinner rule. REPL with a real engine and a fake keyboard: mid-turn delivery, a late message becoming the next prompt (recorded once), and a hook blocking a message. Also driven for real through a pseudo-terminal (`script`) against a local fake OpenAI server: the second request carried the message.

---

## 14. Python command parity — ✅ done (S), with deliberate gaps

**Done.**
- **`!<command>`** runs a command as the user: bash (or sh; `cmd` on Windows) in the workspace, with the terminal attached and the full environment. It isn't an agent action, so the sandbox, command policy and approvals don't apply. It's recorded in the audit log (`user_shell`) and the diagnostic log, and isn't shown to the agent (as in Python). Ctrl+C reaches the command; the REPL swallows its own copy of the signal so it doesn't count as "exit" at the next prompt.
- **`/plan <goal>`** and **`--plan`** (one-shot). Python only asks the model not to use tools. Here `runtime.WithPlanOnly` refuses every tool outside a read-only list (read, list, grep, view_image, web_fetch, web_search, list_agents, invoke_agent, skills, ask_user_question). Sub-agents run within the same run state, so they're restricted too; MCP tools are refused because their effects are unknown. The transcript records `/plan <goal>`, and a goal beginning with `/` is sent to the agent, not run as a command.
- **`/tools`** lists the active agent's tools (● marks those allowed in `/plan`) and the MCP servers offered to it. **`/show`** is an alias for `/set` with no arguments.

**Not ported, and why.**
- `/cd`: in Go the workspace is the sandbox root (`os.Root`, Seatbelt/bubblewrap profile, blocked paths), plus project memory and session scoping. Changing it mid-session means rebuilding all of those; `code-puppy -d <dir>` does it safely at start.
- `/truncate N`: deletes history. `/compact [focus]` shrinks the context without losing what was decided.
- `/dump_context`, `/load_context`: added later as `/session save` and `/session load <name>` (item 18).
- `/tutorial`, `/add_model`, `/refresh_models`: belong with a model catalog, which isn't planned. (`/pin_model` and `/unpin` came with item 16, `/model_settings` with item 17.)

**Tests.** Plan mode refuses edits and still reads, covers sub-agents (the sub-agent's refused `create_file` is checked, not just the missing file), and leaves normal turns alone. `/plan` usage, goal recording, a goal that starts with `/`, `--plan` in JSON output and its flag validation. `!` runs in the workspace, reports exit codes, and isn't recorded as a prompt. A stale Ctrl+C after `!` must not end the session; this test was confirmed to fail without the fix once input arrives with realistic timing. `/tools` marking and MCP scoping; `/show` equals `/set`.

---

## 15. Model fallback — ✅ done (M)

**Config.** `llm.fallback_models = ["provider/model", …]`, tried in order. A bare name uses the primary's provider. The prefix must be a known provider (`gemini`, `anthropic`, `openai`, `ollama`), so OpenRouter names are written `openai/anthropic/…`. It's named `fallback_models` so it isn't confused with `llm.anthropic.fallbacks`, Anthropic's server-side refusal fallback.

**Approach.** `NewModel` builds the primary and wraps it in a `fallbackModel` when fallbacks are configured, so `/model <name>` keeps the chain. A fallback that can't be built (e.g. no credentials) is logged and left out; `doctor` shows why.
- **When to switch:** only on an error before any output, and never on cancellation. Errors aren't classified further, because SDK error types differ and failures such as unknown-model 404s or schema quirks are provider-specific. An error after output was yielded ends the call as before.
- **Breakers:** each model gets a circuit breaker (threshold 1, since SDK retries already ran). It's moved from `internal/tools` to `internal/breaker` so MCP and models share it, with `Abandon` added for cancelled trials. `Allow` is asked just before each attempt, because asking all breakers up front would reserve and strand a half-open trial on a model never tried. If every breaker is open, all models are tried rather than none.
- **Responses:** a fallback's responses carry `ModelVersion` and `code_puppy_fallback_from`. The engine prices usage by the answering model and sends one notice on switching and one on recovery (`WithNotice`).
- **`doctor`:** checks the primary and each fallback on its own, since the chain would make a dead primary look healthy. A broken fallback is a warning.

**Also fixed.** `provider = "ollama"` without `base_url` sent requests to `https://api.openai.com/v1`, the `[llm.openai]` default, with the key "ollama". Ollama now uses localhost unless a different `base_url` is set, and never receives the OpenAI key.

**Not done.** Per-agent model pinning (`/pin_model`) and per-model settings (`/model_settings`), from Python; done later as items 16 and 17.

**Tests.** Takeover, cooldown, trial and recovery on a fake clock. No switch after output or on cancellation. Everything failing, and a retry with every breaker open. Two regression tests, each confirmed to fail against the bug it guards: a cancelled trial is released, and an untried backup's trial isn't stranded. `ParseModelRef`, including OpenRouter and Ollama tags. The Ollama endpoint fix. A real `NewModel` chain from a failing OpenAI server to an Anthropic one. Engine notices once per change.

---

## 16. Per-agent models — ✅ done (S/M)

**Approach.**
- **Precedence:** a pin in `[agent_models]` (agent → `"provider/model"`) wins over the agent's own `default_model` (frontmatter, which was parsed but never used before), and both win over the configured model. Each model is built by `NewModel`, so pinned agents keep the fallback chain. A pin naming an unknown agent, or a model that can't be set up, is warned about and ignored.
- **Engine:** `WithAgentModel` / `PinModel` / `Unpin` keep a model per agent, and `newLLMAgent` uses it for the root agent, sub-agents and `invoke_agent`. `ModelName` reports the active agent's model. Usage without a reported model version is priced by the calling agent's model (`ctx.AgentName()`), not the active agent's, so a pinned sub-agent's tokens are billed right.
- **Provider-aware references everywhere:** `NewModel` parses `"provider/model"` for `/model`, `default_model` and pins, so `/model anthropic/claude-sonnet-5` switches provider.
- **REPL:** `/pin_model [<agent> <model>]` (no arguments lists pins), `/unpin <agent>` (returns to the agent's `default_model` if it has one), a 📌 marker in `/agents`, a note from `/model` when the active agent is pinned, and completion for both commands. Pins are saved with `config.SaveAgentModel`, which shares the new comment-preserving `editConfigFile` with `SaveUILocale` and can remove keys. `doctor` checks each pin.

**Not done.** Python's `/model_settings`; added as item 17.

**Tests.** Pin, replace, unpin and quoted names in the config file, with comments and mode kept. Engine: a pinned sub-agent runs on its model, the main model isn't called for it, and its cost uses its own price (the old pricing path would have used Gemini's). Pin and unpin the active agent. Provider-qualified `/model` and `default_model`. REPL: pin, list, `/agents` marker, unpin saved, unknown agent, usage, the `/model` note, unpin back to `default_model`. Startup precedence and unknown-agent warning.

---

## 17. Per-model settings — ✅ done (S)

**Approach.**
- **Config:** `[model_settings."<model>"]` with `temperature`, `max_tokens`, `top_p` and `seed`, keyed by model name (a `provider/` prefix is dropped, by the same rule as `ParseModelRef`). A set value wins over the global `code_puppy.temperature` / `max_tokens`; an unset one inherits them.
- **Where it's applied:** not in `newLLMAgent`, as first suggested. That would give every model in a fallback chain the primary's settings. Instead `newProviderModel` wraps each model it builds (`settingsModel`), so each chain member, pinned agent and `/model` switch applies its own entry on every call. The wrapper copies the request before changing it, because the fallback chain passes the same request to each model. The engine keeps the settings behind a mutex and puts a lookup in the context of `Execute`, `Compact` and `InvokeSubagent`, so `/model_settings` changes apply from the next call with no runner rebuild. Without a lookup (`doctor`) or an entry, the request passes through unchanged.
- **Provider limits:** the ADK's OpenAI adapter fails the whole request when `Seed` is set, and current Anthropic models reject sampling parameters. `SettingSupported` drops those per provider (OpenAI/Ollama: no seed; Anthropic: `max_tokens`, plus `temperature` on models `supportsSampling` accepts; `top_p` isn't mapped because some Claude models reject it alongside temperature). The provider is taken from the built model's type, so `provider = ""` works too. Dropped settings are logged at debug level, and `/model_settings` warns when they're set.
- **REPL:** `/model_settings` lists models with settings. `/model_settings <model>` shows each key, or where its value comes from (global, provider default). `/model_settings <model> k=v …` sets several at once and changes nothing if one is invalid; `k=` clears one key and `reset` clears them all. Saved with `config.SaveModelSettings`, which uses `editConfigFile` and removes the table when it's empty. Completion offers the models in use and those with settings.

**Not done.** Python's reasoning and thinking settings (`reasoning_effort`, `extended_thinking`, `budget_tokens`, …) and per-model retry strategies. There's no per-model way to unset a global value (e.g. to send no temperature to one model while others get 0.2).

**Tests.** Validation ranges, and nothing changes on error. Save, replace and remove with comments and file mode kept, quoted names (`gpt-4.1`, `qwen2.5-coder:7b`), an empty table removed, and a real `Load` through modenv. A fallback chain where each model gets its own settings, the backup keeps the global temperature, and the shared request config is unchanged. That test was confirmed to fail when the wrapper edits the request in place. Passthrough without a lookup or entry. `SettingSupported` per provider. The engine applies startup settings (including a `provider/` key), picks up a change on the next call, and removes settings. A sub-agent called with no run context gets its settings; confirmed to fail without the lookup in `InvokeSubagent`. The real OpenAI adapter sends temperature, top_p and max_output_tokens to an `httptest` server and no seed. REPL: list, set several, provider warning, atomic failure, bad pair, clear one, show with inherited values, reset saved. A hand-written `"openai/gpt-5"` table is edited rather than duplicated, and when both a bare and a prefixed key exist the bare one always wins. Also smoke-tested with the binary against a local fake OpenAI server: the model's temperature replaced the global one, and the seed wasn't sent.

---

## 18. Named session snapshots — ✅ done (S)

**Approach.**
- **Save:** `/session save <name> [--force]` copies the active session's transcript (`<id>.jsonl`), metadata and the model's event log (`<id>.events.jsonl`) to a new session ID. The new metadata carries `name` and `from` (the source ID). Data files are written first (owner-only, `O_EXCL`) and the metadata last, since the metadata is what makes a session visible, so an interrupted copy is never listed. The source stays active and unchanged. Names follow the ID character rules, are unique across workspaces, and can't start with `session-` or be `latest`, so a name is never mistaken for an ID or for `--resume`'s default. An existing name is refused unless `--force`, which deletes the old snapshot once the new one is written. An empty session is refused. Sessions in the old single-file format can be saved too.
- **Load:** `Storage.Open` takes an ID or a name. A snapshot, whether given by name or by its ID, is never continued in place. Opening one copies it to a new, unnamed session (`from` = the snapshot) in the current workspace and makes that active. This follows Python, where `/load_context` rotates to a fresh session so the snapshot stays a fixed point. The ADK session service replays the copied event log on first access, so the model sees the snapshot's history, including tool calls and compaction summaries. An ordinary ID resumes in place, as before. `/session load`, `/resume` and `--resume=<name>` all use `Open`.
- **`--continue` skips snapshots.** Found in the binary smoke test: a snapshot is the newest session in its workspace, so `--continue` resumed it and appended to it.
- **REPL:** `/session list` marks snapshots with 📸 and their name. `/resume` completion offers snapshot names.

**Not done.** No `/session delete`; `--force` replaces a snapshot, and files can be removed from the sessions directory. Usage and cost (`/cost`) start at zero in a session loaded from a snapshot, as they do after `--resume`.

**Tests.** Copying: files and permissions, name and `from`, the source stays active and keeps growing on its own. Unique names, and `--force` replacing (the old files are gone). Bad names, empty and missing sessions. Opening by name starts a new session with the transcript, and a new `PersistentService` replays the copied events. Continuing that session doesn't touch the snapshot, and a second load starts another session from the same point. A snapshot opened by ID starts a new session; an ordinary ID resumes in place. A legacy source. That the event log copy matters was confirmed by breaking it (the replay test fails). REPL, with a real engine on a persistent session service: the model's request after `/session load` contains the snapshot's turn and not a turn added to the original afterwards; save errors (no session, empty, usage, taken, bad name); list marker; `/resume <name>`; `/resume <id>` in place; `--force`. CLI: `--resume <name>` starts a new session, and `--continue` skips a newer snapshot. Both regression tests were confirmed to fail without their fixes. Binary smoke test against a local fake OpenAI server: save from a piped REPL, continue the original, then `--resume=fruit` twice. Both requests contained only the snapshot's history.

---

## 19. Google search and `/search` — ✅ done (M)

**Backend.** Google's Custom Search JSON API is closed to new customers and shuts down on 2027-01-01, so `search_provider = "google"` uses Gemini's grounding with Google Search instead. It calls `generateContent` with the `google_search` tool over REST (like the other providers, and testable against a fake server), using the Gemini key (`search_api_key`, `[llm.gemini] api_key`, `GEMINI_API_KEY`) and `search_model`. Results are the grounding chunks. Their links are Google redirects (`/grounding-api-redirect/…`), which `web_fetch` won't follow to another host, so each is resolved in parallel by reading its `Location` without following it. One that can't be resolved is kept, and `/search web` skips it. A chunk's snippet is the answer text it supports, and Gemini's answer (thoughts left out) comes back as `answer`. Open alternatives were weighed: SearXNG (already supported) can include Google results without a key; Marginalia and YaCy were left out.

**`/search web <terms>`.** Runs the configured provider as the user, with no approval prompt, since the user typed the query; it goes to the audit log as `user_search`. It asks for 10 results and keeps the first five readable ones: http(s), not a document or binary by extension, not an unresolved redirect, no duplicates (fragment ignored). The list is shown, then a turn starts with a prompt listing them (and the engine's summary, marked unverified). `tools.WithFetchGrants` puts exactly those URLs in the turn's context, so `web_fetch` reads them without asking (audited as `user-selected`). Any other URL, including another page on the same host, still asks. The transcript records the command, not the prompt.

**`/search session <terms>`.** `session.Search` matches words and "quoted phrases" case-insensitively in the stored transcript, which keeps what compaction dropped from the model's context. Earlier `/search` lines are skipped. It ranks by distinct terms, then recency, keeps 12, and sends them oldest first with ±300-character excerpts cut at rune boundaries. With no match, the agent is still asked, with that stated.

**Both** run as read-only turns (`runtime.WithReadOnly("search")`: plan mode's tool list, with its own refusal message). `doctor` reports the provider, or why it can't be used, and `--online` runs a test search. Prompt builders live in `internal/runtime` (`WebSearchPrompt`, `SessionSearchPrompt`) beside `PlanPrompt`.

**Not done.** Google search charges aren't in `/cost`. Search Suggestions (Google's HTML widget) aren't shown in the terminal. Vertex AI (project/location, no API key) isn't supported for search.

**Tests.** Google: request path, key header, `google_search` tool, answer without thoughts, redirect resolved, a denied target dropped after resolution, an unresolvable link kept, snippets from supports; key and model config. User search with no approver, and unconfigured. Fetch grants: the exact URL (fragment ignored) passes without asking, another page on the host asks, and no grant asks. Transcript search: terms and phrases, ranking, skipping earlier searches, excerpts on UTF-8. Viable links. REPL with a real engine and no auto-approval: usage; five links shown and sent (no PDF, no duplicate, not the sixth); the handed-over page is fetched with no approver while another is refused; `create_file` is refused as read-only; the command is recorded. Session search with and without matches. The grant test was confirmed to fail without the grant. Binary smoke test against a fake SearXNG and OpenAI server: both commands reach the model with the expected prompts.

---

## 20. Side questions (`/btw`) — ✅ done (S/M)

From the Antigravity CLI review (below): ask something without adding it to the conversation.

**Approach.** `Engine.Aside` copies the session's events (through JSON, so nothing is shared) into a new in-memory session service and runs the question there with the same agent tree, through a separate runner. Compaction summaries in the copy are honoured, but the copy never compacts. The copy is dropped afterwards, so neither the persistent event log nor the next turn sees the question. The run state carries the real session ID, so usage is billed to it and hooks see it. It is read-only (`btw is read-only` refusals, via plan mode's tool list). In the REPL, `runTurn` with `aside` skips the transcript, checkpoints and steering, and leaves `/attach` images for the next real prompt. `prompt_submit` hooks and the audit log still see the question.

**Not done.** `/btw` can't be asked while a turn is running (steering covers that case), and answers can't be kept afterwards; ask again normally to keep one.

**Tests.** Engine, with a persistent session service: the side question's request has the history and the question; `create_file` is refused as read-only and writes nothing; the saved event log is byte-for-byte unchanged; usage counts the side calls; the next turn's request has the history but not the question or answer. That test was confirmed to fail when the side question runs in the real session. A side question on a session with no turns. REPL: usage, the notice, the request, the next prompt, and a transcript without the side question (confirmed to fail when it is recorded).

---

## 21. Session names and the resume hint — ✅ done (S)

Also from the Antigravity review. Every interactive session used to be titled "Interactive Session", so `/session list` was hard to scan.

**Approach.** `CreateSession` with an empty title leaves it empty, and `AddMessage` names the session after the first user prompt (`session.TitleFrom`: its first non-empty line, whitespace collapsed, at most 60 characters). This applies to the REPL, `/session new` and one-shot runs; an explicit title is kept. `/rename <name>` sets it (`Storage.Rename`, saved to the metadata). Unnamed sessions show as "(untitled)". With `ui.terminal_title` (default on, TTY only), the REPL sets the window title to "🐶 <name>" before each prompt when it changes, with control characters removed, and clears it on exit. Leaving a session that has messages prints `code-puppy --resume=<id>`. The unused `session.interactive_title` / `session.new_title` strings and `firstLine` are gone.

**Tests.** `TitleFrom` (blank lines, whitespace, long UTF-8). Naming from the first user prompt only, an explicit title kept, rename saved and reloaded, an empty name refused. REPL: "(untitled)", the prompt-derived name with escape characters removed, `/rename` usage and result, the window-title sequences and their reset, and the resume hint; none of them when the title is off and the session is empty.

---

## 22. Isolated Python environments, sandboxed by gVisor — ✅ done (M–L)

**Goal.** Skills that ship Python scripts, and forged Python tools, get their own dependencies (`requirements.txt` / `pyproject.toml`) without touching the system Python. Where available, installs and runs happen inside gVisor, whose user-space kernel protects against kernel exploits, which bubblewrap and Seatbelt don't.

**Spike (2026-09-25, gVisor `release-20260921.0`, Ubuntu 24.04 arm64 VM, rootless).**
- **Install:** releases are now a tarball (`gvisor.tar.bz2`/`.zstd` plus `.sha512`) holding `runsc` and a `gvisor-bin/` directory that must stay beside it. The old per-binary URLs return 404.
- **`runsc do` is unusable:** it's documented as for testing only. It wraps every mount, volumes included, in an in-memory overlay (`Overlay: all:memory`), so workspace writes never reach the host. A volume's destination must already exist on the host, and mounting over a secret directory doesn't hide it.
- **`runsc --rootless run` with a generated OCI config works** (rootless `create` isn't supported, but `run` is). The working layout:
  - The root is an empty per-run skeleton directory, with `--overlay2=root:memory`. `/usr`, `/etc`, `/bin`, `/lib`, `/lib64`, `/sbin` and the environment are bound read-only; `/tmp` is a tmpfs; only the workspace (run) or the environment and `uv` cache (install) are bound read-write.
  - Home directories and secrets aren't mounted at all, so they don't exist inside. `/etc/shadow` is refused, and nothing written elsewhere reached the host.
  - Using the host's `/` as the root doesn't work: a read-only root made the workspace bind read-only too, and a writable root let writes escape to the host.
- **Network:** rootless allows only `none` or `host`; gVisor's isolated network needs root. With `host`, the config must drop the `network` namespace that `runsc spec` adds, or the sandbox gets an empty namespace ("Network is unreachable").
- **Timings:**
  - Sandbox start: 40–70 ms.
  - `uv venv` plus a wheels-only install of numpy and requests: 2.6 s from a cold cache, 0.31 s warm (0.06 s without a sandbox).
  - Running a script that imports numpy: about 280 ms versus about 90 ms bare. Almost all of it is file access during imports (185 ms versus 68 ms); computation runs at native speed.
  - `uv` downloaded a managed Python 3.13 inside the sandbox in 3.2 s, leaving the system Python untouched.
- **Enforced at run time:** the environment was read-only ("Read-only file system") and the network was off.
- **Cleanup:** state doesn't accumulate after normal exits, and killing `runsc run` stops its sandbox, leaving only a "stopped" record until `runsc delete`. **But** the sandbox processes aren't children of `runsc`: when only the parent that started `runsc` died, the sandbox kept running. The process guard must reach them (their process group or a cgroup), or use `runsc kill` / `delete` on exit and timeout.
- **`runsc bwrap`** (bubblewrap-compatible CLI) exists, but it lacks `--dev-bind`, `--remount-ro` and `--die-with-parent`, and rootless it needs `newuidmap` (the `uidmap` package). It's not a drop-in for Code Puppy's bubblewrap arguments.
- **`runsc sandboxexec`** takes a `SandboxOptions` proto, and there's a Go package (`gvisor.dev/gvisor/sandboxexec/sandbox`, untagged, still needs `runsc`). Not tried; the OCI route is enough.

**Plan.**
1. Environments in `~/.code_puppy/envs/<hash of requirements and Python version>` via `uv` (managed Python for `requires-python`), falling back to `python3 -m venv` and `pip`.
2. Installs are wheels-only by default (`--only-binary :all:`, so no package code runs at install), with an approval that shows the package list, and hash-checked when requirements are pinned.
3. A sandbox backend interface with gVisor (Linux, when `runsc` is found and a test run passes) and the existing Seatbelt/bubblewrap, selected by `python.sandbox = auto | gvisor | os`. `doctor` reports which is active.
4. `run_skill_script(skill, script, args)` for skills, and a `requirements` field for forged tools.
5. `/envs` and `/envs prune`.
6. CI installs `runsc` on the Linux job.

The same gVisor backend could later run `run_shell_command` and stdio MCP servers (`sandbox.shell = "gvisor"`). macOS and Windows keep the OS sandbox.

**Still to check:** an x86_64 GitHub runner (the spike was arm64); how the process guard and cgroups interact with runsc's sandbox processes; Python versions with no wheels for a package.

**Design direction (2026-09-25): Castor skill definitions and host guardrails.**

*gVisor driven from Go.* Use `gvisor.dev/gvisor/sandboxexec/sandbox` (`New` / `Exec` / `Close`). Each script run is a goroutine that owns its sandbox, with a context tied to the turn, a timeout, and a deferred `Close` on success, error, Ctrl+C or timeout. That fixes the orphaned-sandbox problem the spike found. Caveats: the package still starts the `runsc` binary (gVisor's kernel is its own process), so the process guard must still cover it if Code Puppy is killed. The package is untagged, so pin a commit and keep the proven OCI-config route as the fallback.

*Skills declare, the config caps.* Skill frontmatter follows Castor's `castor.skills.v1.SkillDefinition` (https://github.com/retail-cortex/castor/blob/main/docs/content/architecture/skill.proto.md). For any setting, the stricter of the skill's request and the host config applies; a skill can never widen what the config permits.

| Castor field | Declared by the skill | Capped by the host config |
|---|---|---|
| `scripts[]` (language, `relative_path`, `entry_point`, `dependencies`, `timeout_seconds`, `environment_variables`) | what runs, with which packages | allowed languages; package allow/deny and index URL; wheels only; required hashes; maximum timeout |
| `tool_requirements[]` (name, `scopes` like `git:*`, rationale) | tools and command patterns needed | global allow/deny on tools and scopes, fed into the command policy |
| `execution_hints.environment_variables` | variables it wants | passthrough allowlist; nothing reaches a sandbox unless listed (the scrubbed-variable rules still apply) |
| `execution_hints.hitl_tier`, `allow_hitl_bypass` | how risky it says it is | a minimum tier; bypass never allowed by default |
| network (not in the proto yet) | whether scripts need it | off by default; allowlist of skills |
| `compiled_reference.sha256_hash` | hash of its contents | trust and pin skills by hash; refuse ones changed since approval |

The approval tiers map onto existing mechanisms:
- **Tier 1 (read-only):** runs automatically and is logged (plan mode's tool list).
- **Tier 2 (audited write):** runs automatically with a checkpoint (`/undo`) and an audit entry.
- **Tier 3 (mandatory approval):** asks every time, with no "always allow".
- **Tier 0 (bypass):** only honoured when the config allows it; otherwise treated as tier 3.

Config sketch (names illustrative):
```toml
[skills.policy]
min_hitl_tier       = 2
allow_hitl_bypass   = false
languages           = ["python"]
sandbox             = "auto"            # gvisor | os | auto
network             = "none"            # none | allowlist
network_allow       = ["gh-issues"]
env_passthrough     = ["GITHUB_TOKEN"]
max_timeout_seconds = 300
trusted_hashes      = []

[skills.policy.packages]
index          = "https://pypi.org/simple"
wheels_only    = true
require_hashes = false
deny           = ["*-nightly"]
```

*Frontmatter compatibility.* Keep the Agent Skills spec fields unchanged (`name`, `description`, `license`, `compatibility`, `allowed-tools`, `metadata`); today's parser only reads `name`, `description`, `tags`, `version` and `author`. Castor's fields sit alongside under the proto's JSON names. `github.com/retail-cortex/castor` is a public Go module (v1.0.0, v1.1.0), so frontmatter can be decoded into the generated types (YAML to JSON, then `protojson`), keeping Castor the single source of truth. Still to confirm: the generated package is in the v1.1.0 tag, and what its module pulls in.

*Out of scope at first:* remote `storage_uri` (`gs://…`), Gemini Files API resources, TypeScript scripts (they can reuse the environment design later).

*Order.*
1. ✅ Frontmatter parsing, including the spec fields, and `[skills.policy]` with its merge rules, fully unit-tested (S–M). Done 2026-09-26; see "Step 1 outcome" below.
2. ✅ The gVisor backend through the Go package, with the goroutine lifecycle and cleanup, plus the OS-sandbox fallback (M). Done 2026-09-26; see "Step 2 outcome" below.
3. ✅ Environments, `run_skill_script`, `/envs` (M). Done 2026-09-26; see "Step 3 outcome" below.

*Step 3 outcome.*
- **Scripts never write the workspace.** `Checkpoints` only records changes made through the file tools, so direct writes would bypass diffs, approvals and `/undo`. Instead a script reads the workspace (mounted read-only) and, from tier 2 up, writes to `.code_puppy/skill-output/<skill>/<script>-<time>/` (`$SKILL_OUTPUT`); the agent applies changes with the file tools. Tiers:
  - **Tier 1:** runs automatically and is audited; no writable path.
  - **Tier 2:** the same, plus the output directory.
  - **Tier 3:** `Approve` with no key, so it asks every time.
  - **Tier 0:** a granted bypass, audited as such.

  A mode where scripts write the workspace directly would need workspace-wide snapshots; it's left for later.
- **`PyEnvs`** (`internal/tools/pyenv.go`). The key is a hash of the interpreter's real path, the sorted and deduplicated requirements, the index and wheels-only. The environment is built inside the `ScriptBox`: network on, writable only the environment and a shared cache.
  - **With `uv`:** `uv venv` and `uv pip install --index-url … [--only-binary :all:] -- <deps>`, with `UV_NO_CONFIG=1` (the user's `uv.toml` can't redirect it) and `UV_PYTHON_DOWNLOADS=never`. Without `uv`: `venv` and `pip`.
  - **Markers.** A marker file, written atomically and last, marks an environment usable; one without a marker is deleted and rebuilt. The marker also records the skills using the environment and when it was last used.
  - **Mounts.** `MountsFor` adds the interpreter's prefix when it lives outside `/usr` (e.g. Homebrew, a uv-managed Python).
- **`run_skill_script`** (`internal/tools/skillscript.go`):
  - **Order of checks:** the policy verdict, then the tier approval, then a separate install approval (`ActionNetwork`, key `pyenv:<key>`, showing the exact install command), then the run.
  - **Where the script comes from.** A script on disk is located through `Skill.ScriptPath` (`os.Root`, so no `..` or symlink escapes) and its skill directory is mounted, so it can import its neighbours. Built-in and inline scripts are copied to a temporary directory.
  - **Environment:** `PYTHONDONTWRITEBYTECODE=1`, `PYTHONNOUSERSITE=1`, `$SKILL_DIR`, `$SKILL_OUTPUT`, the passed host variables and the script's own variables. `entry_point` runs the module under `__skill__` through `runpy` and calls the function; its return value is the exit code.
  - **Output:** stdout and stderr are capped at 64 KB each, and up to 50 output files are listed.
  - **`activate_skill`** now lists the scripts, whether each is allowed, and why not.
- **The gVisor backend now hides blocked paths** inside mounted directories: files are covered with an empty read-only file and directories with an empty tmpfs, reusing `expandBlocked`. Before, a workspace `.env` would have been readable by a script under gVisor. A test guards it and was confirmed to fail without the masking.
- **The OS backend lists a read-only path only if it's inside a writable one.** Seatbelt lets read-only win over nested writable paths, which blocked the output directory inside the read-only workspace; the tests caught it.
- **`/envs`** lists environments (size, packages, users, last use, incomplete ones), prunes those no allowed script needs, and removes one by key.
- **Tests:**
  - **Tier 2:** arguments, reading the workspace, and the environment (passed, withheld and host secrets); the workspace write is refused and the output file lands.
  - **Tier 1 and entry points:** tier 1 has no output directory; an entry point's return value is the exit code; inline scripts run.
  - **Refusals and tier 3:** tier 3 asks with no key; TypeScript, missing scripts and unknown skills are refused; the install approval asks separately; timeouts stop the script.
  - **`/envs` and `ScriptPath`:** list, prune and remove; `ScriptPath` refuses a symlink escape.

  Run on macOS (Seatbelt), in a Linux container (bubblewrap) and on an arm64 VM (gVisor). The opt-in tests (`CODE_PUPPY_PYENV_TESTS=1`, which need the network) build a real environment from PyPI and run a script that imports it, on Seatbelt and gVisor. They aren't in CI.
- **Follow-ups:**
  - TypeScript scripts;
  - scripts that write the workspace, with snapshots;
  - `requires-python` with uv-managed interpreters (the proto has no field for it);
  - `storage_uri` and resources;
  - running the opt-in tests in CI (they depend on PyPI).

*Step 2 outcome.*
- **`tools.ScriptBox`** runs one command in isolation from a `ScriptRequest`: read-only and writable host paths (mounted at the same path), network on/off, environment, timeout, stdio. `NewScriptBox` picks `gvisor`, `os` or `auto`; there's no unsandboxed fallback (`ErrNoScriptBox`).
- **Environment.** Both backends give a script only `req.Env` plus `PATH`, a private `HOME`/`TMPDIR` and `LANG`. That's stricter than shell commands, which inherit a scrubbed copy of Code Puppy's environment.
- **gVisor backend** (`scriptbox_gvisor_linux.go`, `gvisor.dev/gvisor/sandboxexec/sandbox` pinned at `v0.0.0-20260926034703-66ed76bcb72f`; the package only builds on Linux, so there's a stub elsewhere):
  - **Layout.** Each run gets its own sandbox: `New`, then `Exec`, then a deferred `Close` on `context.WithoutCancel` with a 15 s bound, so cleanup runs on success, error, timeout or cancel. The root is empty, the host's binaries and libraries are mounted by sandboxexec, and `/etc` read-only, `/tmp` tmpfs, and the request's paths are added. Rootless comes from sandboxexec's single-UID user namespace, with no `newuidmap`.
  - **Timeouts needed a kill.** Killing the `runsc exec` client left the command running inside and its output pipes open, so a timed-out `Exec` waited for the command to finish (30 s in the test). On timeout or cancel, the sandbox is now killed with `runsc kill`.
  - **Orphans.** Sandboxes are named `cp-<pid>-<n>` in a shared state directory (`~/.code_puppy/sandboxes`), and setup sweeps ones whose process is gone. This covers a Code Puppy killed before `Close` ran.
  - **Setup checks.** `runsc` is found via `RUNSC_PATH` (which must exist), `PATH` or `~/.code_puppy/bin`. A `/bin/true` test run, cached per `runsc` path, decides whether gVisor is usable.
- **OS backend** builds a Seatbelt/bubblewrap sandbox per request, with the request's writable paths plus a private temp directory, and the blocked paths hidden.
- **Tests.** The same behaviour for both backends:
  - read the read-only path, write the writable one;
  - writes refused to the read-only path and to a path outside;
  - no inherited secrets, and a private `HOME`/`TMPDIR`;
  - timeouts and cancellation return promptly.

  gVisor also: unmounted paths (including `$HOME`) don't exist, it runs a gVisor kernel, no sandbox or bundle is left behind, and the sweep removes a dead process's sandbox but not a live one. Selection is tested too.
- **Where tested.** gVisor tests ran on an arm64 Ubuntu 24.04 VM. The OS backend ran on macOS and in a Linux container with bubblewrap. CI's Linux job now installs a pinned gVisor (20260921.0, SHA-512 in the workflow) and fails if the gVisor tests are skipped. Mutation checks: without the kill, the timeout test failed; without `Close`, ten sandboxes were left behind; and the OS backend's cancellation bug (a killed process read as a normal exit) was found by the tests.
- **`doctor`** reports the script sandbox and, in `auto`, why gVisor isn't used.
- **Not yet:** anything that uses it. That's step 3.

*Step 1 outcome.*
- **Types, not a dependency.** Castor publishes only `.proto` source. Its Go code is generated by Bazel and not committed; its v1.1.0 tag declares the old module path (`github.com/retail-cortex/skills`); and the module pulls in gin, gorm and Postgres. So `internal/skills/definition.go` mirrors `skill.proto` (Castor commit `1ce880f5`) with the proto field names as YAML keys.
- **Tiers use names, not numbers.** The proto numbers its tiers one higher (tier 2 is enum value 3), so bare numbers are refused as ambiguous. The Go constants match the proto values, so an omitted tier is `TierUnspecified`, never a bypass. An early draft got this wrong; a test now guards it.
- **Validation.** A problem (a `relative_path` escaping the skill directory, not exactly one source, a duplicate or invalid name, an unsupported `storage_uri`, a missing language, a bad environment variable name) keeps the skill's instructions loaded but blocks its scripts. A skill whose frontmatter doesn't parse is now reported instead of silently skipped.
- **Content hash.** `ContentHash`: SHA-256 over every regular file in the skill's directory (path, size, content, in order); symbolic links skipped; 64 MB cap; built-in skills included. Castor's declared `compiled_reference.sha256_hash` is shown but not enforced, since its algorithm isn't specified.
- **`skills.Evaluate`** applies `[skills.policy]` as designed:
  - tier and bypass consent;
  - network only for allowlisted skills that ask (`custom_hints.network`, since the proto has no network field);
  - environment passthrough by glob, with withheld names reported;
  - timeouts capped;
  - languages, `deny_tools` on tool or `tool:scope`, and `trusted_hashes`;
  - dependencies: plain PEP 508 requirements only, names normalized per PEP 503, allow/deny globs, and `==` pins when `require_hashes` is set.
- **Where it shows.** `SkillPolicy.Problems` catches invalid settings (in `doctor` and at startup). `/skills list` summarizes each skill's scripts, `/skills show` gives every decision and the hash, and `doctor` reports blocked skills.
- **Tests.** Parsing, tier names, validation, the path containment check, the hash (stable, symlinks ignored, sensitive to edits), reporting unparseable skills, each policy rule, and `doctor` and `/skills`. Mutation-checked: a bypass without the policy's consent, and the network allowlist.

---

## 23. A UI-independent core and a desktop app — 🚧 in progress (L)

**Goal.** A Wails desktop app with a tab per workspace, alongside the CLI, without a second copy of the program's logic.

**Decisions (2026-09-26).**
- **One engine service per user** (revised the same day; it replaces "a process per tab"). A single `code-puppy serve` daemon hosts every open workspace, because scheduled workers across workspaces are planned (item 24). Each workspace's state must therefore be per workspace, not per process (see phase 5b).
- **The CLI attaches when a service is available**, and otherwise runs the engine in-process as today. `tui` programs against a backend interface with two implementations: `*app.Workspace` (local) and a Connect client (remote). A workspace lock keeps a local CLI and the service from owning the same workspace at once.
- **API: protos in `./api`, Connect + buf.** `connectrpc.com/connect` over `net/http` serves gRPC, gRPC-Web and JSON from one handler; `buf` lints and checks breaking changes in CI; Go and TypeScript (protobuf-es, for the Wails frontend) are generated and committed, with a CI check that they're current. A turn is a server-streaming call; approvals and questions arrive as stream events with an ID and are answered with a separate unary call, so no bidirectional streaming is needed. Typed errors map to Connect codes with details; clients localise.
- **The service listens on a Unix socket** (`~/.code_puppy/run/`, mode 0600), no TCP by default: it can run shell commands.
- **Layout per golang-standards/project-layout:** `cmd/code-puppy` (pure Go, cross-compiled) and `cmd/code-puppy-desktop` (Wails, cgo, built on each OS); code in `internal/`; protos in `api/`; frontend in `web/desktop`; packaging in `build/`.
- **No Bazel.** Wails already orchestrates the frontend and Go builds, and it needs host cgo libraries (WebKit, webkit2gtk) that Bazel can't make hermetic. Pinned tools, lockfiles and the cross-host reproducibility check in CI cover the rest.
- **Typed operations, not a command dispatcher.** Slash-command syntax is a terminal concern and stays in `tui`; `internal/app` exposes methods that return data and never print, which RPC handlers call.

**Found.** The engine was already UI-independent (`runtime.EventHandler`, `tools.Approver`, `tools.UserPromptFunc`; the shell tool resolves its directory against the workspace root). The logic that wasn't lived in `cmd/code-puppy/setup.go` (wiring), `tui/repl.go` `runTurn` (the turn lifecycle: hooks, audit, checkpoint, recording, leftover steers) and `tui/commands*.go` (about 30 commands that act and print in the same place). The workspace is also the process's working directory (`-d` uses `os.Chdir`; `WorkspaceDir` is `"."`; `config.Load` and `./agents` read the working directory), which is fine for a process per tab.

**Phases.**
1. *(Superseded by 5b.)* `config.Load` takes the workspace directory instead of relying on the working directory.
2. ✅ **`app.Open`.** `buildEnv` is `app.Open` and `env` is `app.Workspace`, with `OpenSession` (selection plus audit context), `LoadAttachments`, model pins and settings, memory reload and locale. `app` knows nothing of exit codes: an unresumable session is a `*ResumeError`, which `cmd` maps to exit code 2. `Options.Model` injects a model; `cmd`'s tests now open real workspaces around a mock. `cmd` keeps flags, `--dir`, observability and output modes.
3. ✅ **`Workspace.Run` and `Steer`.** The turn lifecycle, once in both `tui/repl.go` `runTurn` and `cmd`'s `runOneShot`, is `app.Workspace.Run(ctx, sessionID, Turn, handler)`: `prompt_submit` hooks (a refusal is a `*BlockedError`), audit, the checkpoint, recording both sides, plan/read-only/aside turns, and the steer messages the agent never read (`TurnResult.Leftover`). `app` collects the model's output itself, so printers no longer keep a transcript. `Turn.OnAccepted` runs once hooks pass, which is where the REPL prints attachments and starts its spinner and steering watch; `Turn.OnFinished` runs when the agent stops and before unread steers are collected, which is where the REPL stops the watch, so a message still being typed is sent next rather than left queued. Methods take a session ID rather than returning a `Session` handle, because `/session` commands switch the active session under a running REPL. Small behavior changes: one-shot checkpoints are labelled like the REPL's (60 characters), one-shot transcript save failures are now reported, and an `@image` that fails to load stops the prompt before hooks and audit rather than after. `tui.App` gains a `Workspace` field alongside its individual fields; phase 4 removes those.
4. ✅ **Typed commands** in groups: agents and models; sessions; checkpoints and approvals; skills, envs and MCP; memory, locale and cost. `HandleCommand` becomes parsing plus rendering. Operations return plain structs and typed errors (never localised text), so each can become an RPC with a proto message later; `tui` words and localises the results. Names don't collide with the accessors `Open` already exposes (`ListAgents`, not `Agents`).
   - ✅ **Agents and models** (`internal/app/models.go`): `ListAgents`, `ActiveAgent`, `SetAgent`, `Model`, `SetModel`, `PinModel`, `Unpin`, `ModelSettings`, `AllModelSettings`, `UpdateModelSettings`, `Settings`, `Set`. Errors: `*UnknownAgentError`, `ErrBadModelRef`, `*InvalidSettingError`, `*UnknownSettingError`, `ErrInvalidAgency`. Saving to the config file is part of the operation and reported as `Saved{Path, Err}`: the change applies even when saving fails. `Options.NewModel` replaces `tui.App.NewModel`; `App.SaveAgentModel` and `SaveModelSettings` are gone, and tests read the saved file back instead of faking the save.
   - ✅ **Sessions** (`internal/app/sessions.go`): `ListSessions`, `ActiveSession`, `NewSession`, `LoadSession`, `SaveSnapshot`, `RenameSession`, `Dir`; `OpenSession` moved here. They return `SessionInfo` and `Message`, not the storage's `*session.SessionRecord`, so its fields (trace links, JSON tags) stay private. Errors: `ErrNoActiveSession`, `ErrSnapshotNameTaken`. The REPL's own session handling (creating the first session, the window title, the resume hint, `/btw` and `/search` targets) goes through them too.
   - ✅ **Checkpoints and approvals** (`internal/app/changes.go`): `ListCheckpoints`, `Undo` (which now audits the undo itself), `SessionDiff`, `GitDiff(ctx, color)`, `ListApprovals`, `RevokeApprovals`, `ClearApprovals`. An `Approval` is structured (`Kind`, `Subject`, `Dir`, `Always`, `Added`) instead of the internal key format (`cmd:<dir>\x00<command>`); the key stays as an opaque ID for revoking. `ErrUndoConflict` re-exports the tools error.
   - ✅ **Skills, environments, MCP and tools** (`internal/app/extensions.go`): `ListSkills`, `Skill`, `SearchSkills`, `ListEnvs`, `RemoveEnv`, `PruneEnvs`, `ListMCPServers`, `ActiveAgentTools`. A `SkillInfo` carries what the skill declares and the policy's verdict on it and each script (tier, network, environment variables passed or withheld, reasons a script is blocked), so no client has to evaluate the policy itself. `ErrScriptsDisabled` when skill scripts are off.
   - ✅ **Usage, context, compaction, memory, locale, images, search** (`internal/app/context.go`): `SessionUsage`, `Context`, `Compact`, `MemoryFiles`, `ReloadMemory`, `AddMemory`, `AvailableLocales`, `SetLocale(ctx, input)` (resolves the language itself), `SandboxSummary`, `LoadImage`, `AddImage`, `SearchProvider`, `SearchWeb`, `SearchSession`. `/search web`'s fetch grants are a `Turn.FetchGrants` field instead of a context value, so any client can run a search turn. The attachment queue stays client state in `tui`; `!cmd` stays client-side (it runs the user's shell on their terminal).
   - **`tui.App`** is now just the workspace plus terminal state (input, printer, attachment queue, window title, interrupts). Its `Cfg`, `Engine`, `Agents`, `Skills`, `Storage`, `Tools`, `Processes`, `SandboxSummary`, `ReloadMemory`, `Locales` and `SetLocale` fields are gone; tests reach internals through the workspace's accessors. Messages for cases that can no longer happen (`*.unavailable`, `session.disabled`, `common.not_available`) are removed from the catalogs.
   - **Left for phases 5–6:** `tui` still uses three accessors (the images-enabled flag for `/paste`, the audit log for `!cmd`, the process manager for the exit prompt), and `cmd` wires approvals, questions and the completer through accessors. A service needs these as operations or callbacks: approvals and questions become requests the server sends to the client.
5. ✅ **`app.Event`.** `Run` passes `func(app.Event)` events instead of ADK events: one per text part (`Partial`, `Thought`, and `Repeat` for final text that repeats streamed chunks), tool call (`Partial` when streamed) or tool result, each with its `Author`. Deduplicating streamed text moved from the printer into `app` (`relay`), which still sees ADK event boundaries, so every client shows text once by showing partial chunks and non-`Repeat` final text. The printer and both JSON output formats consume `app.Event`; their output is unchanged. Approval requests and questions join the event stream with the service (phase 7), where they stop being callbacks.
5b. ✅ **Per-workspace state.** Nothing depends on the process's working directory any more: `-d` names the workspace instead of calling `os.Chdir`; `app.Open` resolves `tools.workspace_dir` to an absolute path first; trusted `./agents` and `./skills` (`AgentSearchPaths(dir)`, `SkillSearchPaths(dir)`), relative `allowed_paths`, `read_only_paths` and `shell_writable_paths` resolve against the workspace; `ExecEnv.Dir` runs MCP servers and forged tools in the workspace instead of the inherited directory. The reply language is per workspace (`SetLocale` no longer touches the process's interface language, which belongs to the client; the REPL switches its own). `Open` owns the `Config` it's given, so each workspace needs its own copy. **Left process-wide on purpose:** the config directory (per user, never per workspace, for the security reason in `config.Load`; `config.Load` still sets `MODENV_PREFIX` because modenv reads only the environment, which is harmless with one config directory per process), telemetry and the diagnostic log, the gVisor `runsc` path. A test opens two workspaces in one process from an unrelated directory and checks their agents and reply languages stay separate.
6. ✅ **`api/codepuppy/v1` protos.** Four files: `turn.proto` (the `Turn` request; `TurnEvent`, a oneof of `accepted`, `text`, `tool_call`, `tool_result`, `approval_request`, `question`, `finished`; `Usage`; `ErrorInfo`), `session.proto` (`SessionService`: sessions, `RunTurn` as a server stream, `Steer`, `Approve`, `Answer`, usage, compaction, transcript search), `workspace.proto` (`WorkspaceService`: phase 4's other operations, plus `ListWorkspaces` and `CloseWorkspace`), `worker.proto` (`WorkerService`, item 24). Every request names its workspace by absolute directory. Images travel by ID (their SHA-256) after `LoadImage` or `AddImage`. Errors carry an `ErrorInfo` detail (`reason` such as `UNKNOWN_AGENT`, `NO_ACTIVE_SESSION`, `PROMPT_BLOCKED`, `RESUME_FAILED`, `SNAPSHOT_NAME_TAKEN`, `UNDO_CONFLICT`, `HASH_MISMATCH`; `metadata`; an English `message`), which clients word and localise. Tooling: `buf` lint (STANDARD) and breaking (FILE); `buf`, `protoc-gen-go` and `protoc-gen-connect-go` pinned in a separate `tools/go.mod` and run with `go tool`, so the main module gains only the `connectrpc.com/connect` runtime; generated Go committed in `internal/gen`; `make proto` and `make proto-check`; CI runs `proto-check` and `buf breaking` against the previous commit. TypeScript generation (protobuf-es) waits for `web/desktop`, where a pnpm lockfile can pin it.
7. **`internal/server` and `code-puppy serve`:** Connect handlers over `app.Workspace`, workspaces opened on demand by directory, the Unix socket, a workspace lock, approvals and questions over the turn stream.
8. **Attach:** a backend interface in `tui`, implemented by `*app.Workspace` and by a client adapter; the CLI attaches when the socket answers. Also moves the last accessor uses (the process list at exit, `!cmd`'s audit entry, the images flag, approval and question wiring in `cmd`) behind operations.
9. **Desktop scaffold:** a thin Wails shell that talks to the service, one tab per workspace. Needs notarization on macOS.
10. **Workers** (scheduled runs, ROADMAP item 24), run by the service. Their API is part of phase 6's protos.

---

## 24. Workers: scheduled runs defined in the workspace — 📋 planned (L)

**Goal.** A workspace defines workflows the service runs on a schedule, unattended: dependency reports, nightly checks, triage.

**Decisions (2026-09-26).**
- **Files:** `workers/<name>/WORKER.md` in the workspace, a directory per worker so it can carry supporting files (like `SKILL.md`); search paths configurable like `[skills] paths`. A workspace can have several. The service registers workers by scanning, and rescans on change (`fsnotify`).
- **Frontmatter:** `name`, `description`, `schedule`, optional `timezone` (default: the machine's), `agent`, `model`, `permissions`, `limits` (`max_turns`, `max_cost_usd`, `timeout`), `overlap` (default `skip`), `catch_up` (default none). The body is the workflow, sent as the prompt.
- **Schedules:** a cron expression, a descriptor (`@daily`, `@every 2h`), or plain text ("Every two hours", "Daily at 6 AM", "Weekdays at 9:30") parsed by a small deterministic parser into cron. Never interpreted by a model; unparseable text is an error. Listings show the resolved cron and the next run. Cron via `robfig/cron/v3` (time zones, `@every`).
- **Trust:** a worker runs only in a trusted workspace and only once enabled explicitly, pinned to the file's content hash (like `[skills.policy] trusted_hashes`). Any edit disables it until it is re-enabled; the service reports that. Cloning a repository never schedules anything.
- **Unattended actions:** a worker gets what its `permissions` ask for, capped by a host `[workers.policy]`. Anything else is **refused and recorded**, never left waiting: the agent is told why, and the run's record lists what was refused.
- **Runs:** each run is a new session named after the worker and its start time (visible, resumable, audited), with a run record: status, start, duration, cost, refusals, the session ID. Limits are required (or come from policy defaults), so a schedule can't spend without bound. A run still going when the next is due is skipped by default; missed runs (machine asleep, service down) are skipped unless `catch_up: once`; a global cap limits concurrent runs.
- **Service:** workers run only while the per-user service runs, so it gets a launchd/systemd user unit to start at login, and a registry of workspaces with enabled workers to watch even when no client has them open.
- **API** (in phase 6's protos): `WorkerService` with `ListWorkers` (schedule, next run, enabled, hash), `EnableWorker(hash)`, `DisableWorker`, `RunNow`, `ListRuns`, `GetRun`, and watching a run over the turn event stream.

---

## Antigravity CLI review (2026-09-25)

Google's Antigravity CLI (`agy`) was compared feature by feature with Code Puppy Go, from a feature summary of its docs (https://antigravity.google/docs/cli/features/). The summary read as AI-generated, so its descriptions were treated as approximate.

**Already covered.**

| Antigravity | Code Puppy Go |
|---|---|
| `-c`, `--conversation <id>`, `/resume` | `-C`, `-r`/`--resume`, `/resume`, `/session load` |
| `-p` (run a prompt, stay open) | `-p "…" -i` |
| `--agent`, `/agents`, `/skills`, `/mcp`, `/plan` | same commands |
| `/diff`, `/context` (overlays) | `/diff`, `/context` (text) |
| `/codesearch` | the `grep` tool |
| `/config`, `/permissions` | `/set`, `/approvals`, `.env.toml` |
| `/rewind` for files | `/undo`, `/checkpoints` |
| `/goal` (full autonomy) | `/set agency=extreme` |
| `/clear`/`/new`, `/quit` | `/session new`, `/exit` |
| terminal sandbox | Seatbelt / bubblewrap (`sandbox.shell`) |
| routing across Gemini/Claude/GPT | `fallback_models`, per-agent pins |

**Adopted.** `/btw` (item 20); `/rename`, the terminal title (`/title`) and the resume command printed on exit (item 21).

**Candidates, in order of value.**
1. `/copy`: the last reply to the clipboard (`pbcopy`, `wl-copy`, `xclip`, `clip.exe`, or OSC 52, which also works over SSH). S.
2. `--add-dir <path>`: an extra writable directory for one run, at startup only. Adding one mid-session has the same problem as `/cd`: the file roots and OS sandbox profile are built once. S.
3. `/grill-me <task>`: a prompt mode like `/plan` in which the agent asks clarifying questions (`ask_user_question`) before writing anything. S.
4. `/fork [n]`: a new session branched from an earlier turn, the non-destructive form of a conversation rewind; `/session save` only branches from the current point. M.

**Rejected.**
- Full-screen overlays (`/config`, `/keybindings`, `/statusline`, interactive `/diff` and `/context` graphs): the REPL is line-based, and these need a different UI framework.
- `/boost`, `/teamwork-preview`: vaguely described multi-agent modes; `invoke_agent` covers delegation.
- `--dangerously-skip-permissions`: `code_puppy.auto_approve` does this deliberately; a one-word flag makes it too easy to enable by accident.
- Automatic model routing by task complexity: hard to predict and to cost; ordered fallbacks and pins are explicit.
- `/open <path>`: `!$EDITOR <path>` does it.
- `/usage` (built-in manual): `/help` and the README.
- Not applicable: desktop app sync and export, `/credits`, `/logout`, Gemini CLI migration, SSH sign-in tunnelling.

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
- `/search web` pre-approves the five URLs it hands over for one turn; a page that redirects to another host is refused, and the agent must request the target (with approval).
- `/search web` judges readability from the URL; a document or script-rendered page without a telling extension fails at fetch time and is skipped by the agent.
- `/search session` is a literal, case-insensitive match over the current session's transcript, which holds prompts and replies but not tool calls or their output.
- Google search (Gemini grounding) returns the pages Gemini cited, not Google's ranked list; its per-query charges aren't in `/cost`; Search Suggestions aren't rendered; Vertex AI credentials aren't supported for search.
