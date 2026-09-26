# Code Puppy Go — Gap Implementation Plan

Status key: ✅ done · 🔜 next · 📋 planned · 🔍 needs investigation first

Items 10–20 were added after the first plan and appear before item 9, which stays last because it needs a person. Open work and resume notes: [NEXT_STEPS.md](NEXT_STEPS.md).

Each item lists the problem, the approach, where the change lands, how it is tested, and a rough size (S ≤ half a day, M ≈ 1–2 days, L ≈ 3+ days).

---

## 1. Anthropic provider — ✅ done (S/M)

**Problem.** `llm.provider = "anthropic"` was accepted by config but `runtime.NewModel` had no Anthropic branch, so startup failed. The ADK ships no Anthropic model.

**Approach.** A `model.LLM` adapter over the official `anthropic-sdk-go` (`pkg/runtime/anthropic.go`):
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
- **Breakers:** each model gets a circuit breaker (threshold 1, since SDK retries already ran). It's moved from `pkg/tools` to `pkg/breaker` so MCP and models share it, with `Abandon` added for cancelled trials. `Allow` is asked just before each attempt, because asking all breakers up front would reserve and strand a half-open trial on a model never tried. If every breaker is open, all models are tried rather than none.
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

**Both** run as read-only turns (`runtime.WithReadOnly("search")`: plan mode's tool list, with its own refusal message). `doctor` reports the provider, or why it can't be used, and `--online` runs a test search. Prompt builders live in `pkg/runtime` (`WebSearchPrompt`, `SessionSearchPrompt`) beside `PlanPrompt`.

**Not done.** Google search charges aren't in `/cost`. Search Suggestions (Google's HTML widget) aren't shown in the terminal. Vertex AI (project/location, no API key) isn't supported for search.

**Tests.** Google: request path, key header, `google_search` tool, answer without thoughts, redirect resolved, a denied target dropped after resolution, an unresolvable link kept, snippets from supports; key and model config. User search with no approver, and unconfigured. Fetch grants: the exact URL (fragment ignored) passes without asking, another page on the host asks, and no grant asks. Transcript search: terms and phrases, ranking, skipping earlier searches, excerpts on UTF-8. Viable links. REPL with a real engine and no auto-approval: usage; five links shown and sent (no PDF, no duplicate, not the sixth); the handed-over page is fetched with no approver while another is refused; `create_file` is refused as read-only; the command is recorded. Session search with and without matches. The grant test was confirmed to fail without the grant. Binary smoke test against a fake SearXNG and OpenAI server: both commands reach the model with the expected prompts.

---

## 20. Side questions (`/btw`) — ✅ done (S/M)

From a review of Google's Antigravity CLI (`docs/AGY.md`): ask something without adding it to the conversation.

**Approach.** `Engine.Aside` copies the session's events (through JSON, so nothing is shared) into a new in-memory session service and runs the question there with the same agent tree, through a separate runner. Compaction summaries in the copy are honoured, but the copy never compacts. The copy is dropped afterwards, so neither the persistent event log nor the next turn sees the question. The run state carries the real session ID, so usage is billed to it and hooks see it. It is read-only (`btw is read-only` refusals, via plan mode's tool list). In the REPL, `runTurn` with `aside` skips the transcript, checkpoints and steering, and leaves `/attach` images for the next real prompt. `prompt_submit` hooks and the audit log still see the question.

**Not done.** `/btw` can't be asked while a turn is running (steering covers that case), and answers can't be kept afterwards; ask again normally to keep one.

**Tests.** Engine, with a persistent session service: the side question's request has the history and the question; `create_file` is refused as read-only and writes nothing; the saved event log is byte-for-byte unchanged; usage counts the side calls; the next turn's request has the history but not the question or answer. That test was confirmed to fail when the side question runs in the real session. A side question on a session with no turns. REPL: usage, the notice, the request, the next prompt, and a transcript without the side question (confirmed to fail when it is recorded).

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
