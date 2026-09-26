# Manual Verification Checklist

Everything here needs a person, a real terminal, real credentials, or GitHub. The automated suite (379 tests on macOS and Linux) covers the logic behind each item; this list checks the parts it can't. Each item has an **expected** result — if you see something else, note it next to the item.

Setup for most items: `make build`, then use `./bin/code-puppy` (or put `bin/` on your `PATH`). Use a scratch Git repository as the workspace so edits are safe.

**Cost note:** items marked 💲 call a paid API. A short session costs cents; long sessions and `/compact` cost more.

---

## 0. Setup

- [ ] `code-puppy config init`, then add your API key(s) to `~/.code_puppy/.env.toml`.
  **Expected:** file created with mode 600; re-running without `--force` refuses.
- [ ] `code-puppy doctor` 💲 then `code-puppy doctor --online`
  **Expected:** credentials ✓, model ✓, `model request` ✓ with `--online`, shell sandbox **on**, pricing ✓ for the default model.

## 1. Gemini end to end 💲

- [ ] Ask it to read a file and summarize it.
  **Expected:** answer streams in as it's generated; Markdown renders; a dim usage line (`↳ … in · … out · context … · $…`) follows the turn.
- [ ] Ask it to fix a small bug that needs `grep` + an edit + `go test` (or your language's tests).
  **Expected:** tool badges appear; the edit asks for approval with a colored diff; the test command asks for approval; the turn finishes.
- [ ] `/cost` and `/context`.
  **Expected:** non-zero tokens; cost shown; context size roughly matches the usage line.
- [ ] Set `[context] token_threshold = 4000` temporarily, then have a few long turns.
  **Expected:** the conversation keeps working past the threshold; `/context` stays near or below it.

## 2. Anthropic end to end 💲

Set `[llm] provider = "anthropic"` (key via `api_key`, `ANTHROPIC_API_KEY`, or `ant auth login`).

- [ ] Banner shows model `claude-opus-5`; `doctor --online` passes.
- [ ] A multi-step task with several tool calls (read → edit → run tests).
  **Expected:** completes without "invalid request" errors between tool calls (thinking blocks are carried across correctly).
- [ ] Run two turns in a row, then `/cost`.
  **Expected:** "read from cache" is non-zero from the second turn on.
- [ ] Quit, then `code-puppy --continue "what did we just do?"`.
  **Expected:** it remembers the previous turn's details.
- [ ] Optional: `fallbacks = "off"` in config, repeat one turn. **Expected:** works the same.

## 3. OpenAI-compatible and Ollama 💲

- [ ] `provider = "openai"` with a real key: a turn with at least one tool call.
  **Expected:** tool runs; no JSON printed as text.
- [ ] `provider = "ollama"` with a local model that emits tool calls as JSON text (e.g. `qwen2.5-coder`).
  **Expected:** the JSON becomes a real tool call (badge shown), not echoed text.

## 4. Terminal experience

- [ ] Up/Down recall history; history survives restart (`~/.code_puppy/history`, mode 600).
- [ ] Ctrl+R searches history.
- [ ] Tab completes `/com` → `/compact`, `/agent he` → `helios`, `/resume ` → this directory's session IDs, `look at @src/` → file names.
- [ ] Multi-line: end a line with `\` and continue; also a block between two `"""` lines.
  **Expected:** sent as one prompt with the line breaks kept.
- [ ] Resize the terminal mid-answer. **Expected:** no garbled output afterwards.
- [ ] Light terminal theme: `COLORFGBG="0;15" code-puppy`. **Expected:** Markdown readable on a light background.
- [ ] Spinner shows while waiting and disappears before output and approval prompts.

## 5. Ctrl+C behaviour

- [ ] At an idle prompt with no background processes. **Expected:** exits immediately with "Goodbye".
- [ ] During a long model answer. **Expected:** "⏹ Interrupted", back at the prompt, session still usable.
- [ ] At an approval prompt. **Expected:** the whole turn is cancelled (not just that one action).
- [ ] During a long shell command (`sleep 60` approved). **Expected:** the command is killed promptly.

## 6. Approvals

- [ ] Long edit: diff is truncated; `d` shows the full diff and asks again.
- [ ] `s` on a command, then the identical command again. **Expected:** no second prompt this session.
- [ ] `a` on a command; restart; same command in the **same** directory → no prompt; in **another** directory → prompts.
- [ ] `/approvals` lists both; `/approvals revoke 1` removes one.
- [ ] Add `deny = ["rm -rf *"]` under `[sandbox.commands]`, approve everything with `a`, then ask for `rm -rf build`. **Expected:** blocked by policy without a prompt.

## 7. Undo, diff, checkpoints

- [ ] After a turn that edits files: `/diff` shows the change; `/checkpoints` lists the turn; `/undo` restores the files.
- [ ] Make an edit via the agent, then change the same file yourself, then `/undo`. **Expected:** refuses with a conflict; `/undo --force` restores.
- [ ] `/diff git` shows the working tree diff.

## 8. Compaction 💲

- [ ] In a session with 5+ turns: `/compact keep the file names we touched`.
  **Expected:** "Replaced N earlier events…"; the next answer still knows the key facts and file names.
- [ ] Quit and `--continue`. **Expected:** the summary still applies (the model knows earlier context but `/context` is small).
- [ ] `/compact` twice with a turn in between. **Expected:** facts from before the first compaction are still known.

## 9. Sessions

- [ ] Run a prompt in directory A, then in directory B. In A: `code-puppy --continue "…"`. **Expected:** continues A's session, not B's.
- [ ] `/session list` in A shows only A's; `/session list --all` shows both with their directories.
- [ ] `code-puppy --resume <B's id>` from A. **Expected:** works, with a warning that it started elsewhere.

## 10. Sandbox on your real workflow

- [ ] Ask the agent to run your project's test command. **Expected:** works (build caches are writable).
- [ ] Ask it to `cat ~/.ssh/id_ed25519` (or any blocked file). **Expected:** denied / empty.
- [ ] Ask it to write outside the workspace (e.g. `echo x > ~/Desktop/x`). **Expected:** "Operation not permitted".
- [ ] `npm install` (or another tool writing to its own cache). **Expected:** works if the cache is in `shell_writable_paths`; otherwise fails until you add it.
- [ ] `allow_network = false`, then ask for `curl https://example.com`. **Expected:** fails.

## 11. Background processes

- [ ] Ask the agent to start a dev server in the background, then `/exit`. **Expected:** warning listing it; `k` kills it; `w` waits; `c` cancels the exit.
- [ ] Start one, then from another terminal `kill -9 <code-puppy pid>`. **Expected:** the server is gone within a second or two (`lsof -i :<port>` shows nothing).

## 12. MCP (real server)

Add to config:
```toml
[[mcp.servers]]
name = "fs"
command = "npx"
args = ["-y", "@modelcontextprotocol/server-filesystem", "."]
prefix = "fs"
```
- [ ] `doctor --online` shows `mcp fs  N tools`. `/mcp` lists it.
- [ ] Ask the agent to list files using the fs server. **Expected:** approval prompt naming `fs__…` and the server; the call works inside the sandbox.
- [ ] Add `agents = ["qa-kitten"]`. **Expected:** the main agent no longer sees `fs__` tools; `invoke_agent` → qa-kitten can use them.
- [ ] `kill -9` the CLI. **Expected:** no leftover `npx` / server processes.
- [ ] During a session, `pkill -f server-filesystem` (kill the MCP server), then ask for another fs tool call. **Expected:** it works; `ps` shows a new server process.
- [ ] Point an MCP server at a command that exits immediately (e.g. `command = "false"`). **Expected:** one "unavailable" warning, then one "paused for 15s" warning; later turns aren't slowed and don't repeat the warning.

## 13. Hooks

- [ ] A `pre_tool` hook for `run_shell_command` that exits 2 with a message. **Expected:** shell calls are blocked with that message; other tools work.
- [ ] A `post_tool` hook appending the JSON event to a file. **Expected:** one line per tool call, including tool name and args.
- [ ] A `post_tool` hook `sleep 3; cat >> /tmp/post.jsonl`, then a turn with several tool calls. **Expected:** the turn is not slowed down; the events arrive in `/tmp/post.jsonl` a few seconds later, in tool-call order. Quit right after a turn: the pending events are still written (within about 5 s).
- [ ] A `prompt_submit` hook that blocks prompts containing `password`. **Expected:** "Prompt blocked by hook".

## 14. Web 💲 (search key)

- [ ] `web_fetch` of a docs page. **Expected:** approval per host; readable text returned.
- [ ] Ask it to fetch `http://localhost:…` or `http://169.254.169.254/`. **Expected:** refused ("not a public address").
- [ ] Configure `web.search_provider = "brave"` (or `tavily`) with a key; ask a question needing search. **Expected:** approval once per provider with `s`; results with titles/URLs; follow-up `web_fetch` works.

## 15. Forged tools (helios)

- [ ] `/agent helios`, ask it to forge a small bash tool and run it. **Expected:** approval shows the code as a diff; run needs separate approval.
- [ ] Restart, ask helios to list and run it. **Expected:** still there. Ask it to delete it. **Expected:** gone from `~/.code_puppy/uc_tools`.

## 16. Scripting

- [ ] `git diff | code-puppy review this --output-format json | jq .result` **Expected:** a single JSON object; `jq` works.
- [ ] `code-puppy --output-format stream-json "…" | jq -c .type` **Expected:** `session`, then events, ending with `result`.
- [ ] `code-puppy --max-turns 1 "do a multi-step task"; echo $?` **Expected:** exit code 3.
- [ ] Without credentials: `code-puppy "hi"; echo $?` **Expected:** clear error, exit code 1.

## 17. Other platforms

- [ ] Ubuntu 24.04 desktop: `sudo apt install bubblewrap`, `code-puppy doctor`. **Expected:** sandbox on, or a clear AppArmor reason (fix: an AppArmor profile for bwrap, or `sysctl kernel.apparmor_restrict_unprivileged_userns=0`).
- [ ] Windows: run the `.exe` with Git Bash on `PATH`; a simple prompt and a shell command. **Expected:** works without the OS sandbox; `doctor` shows sandbox off with a reason.

## 18. Repository and release

- [ ] `git rm -r --cached go/bin` and commit (bin is now ignored).
- [ ] Pin the actions in `.github/workflows/go-*.yml` to commit SHAs.
- [ ] Push; both `go-ci` jobs pass; the Linux job's "Sandbox enforcement must not be skipped" step passes.
- [ ] Tag `v0.1.0` and push the tag. **Expected:** `go-release` creates a **draft** release with 5 archives, 5 SBOMs, `checksums.txt`, `checksums.txt.sigstore.json`.
- [ ] Download the assets and verify:
  ```bash
  cosign verify-blob --bundle checksums.txt.sigstore.json \
    --certificate-identity-regexp 'https://github.com/.*' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
  shasum -a 256 --ignore-missing -c checksums.txt
  ```
  **Expected:** `Verified OK` and every file `OK`. Then publish the draft.

## 19. Cost sanity 💲

- [ ] After a day of use, compare `/cost` totals with your provider's billing dashboard. **Expected:** same order of magnitude; if not, adjust `[pricing]`.

## 20. Language

- [ ] `/locale` **Expected:** "Interface language: English (US) (en-US)" and the list `en-US, es, fr-CA`.
- [ ] `/locale ES-sp` **Expected:** confirmation in Spanish; `~/.code_puppy/.env.toml` now has `locale = "es"` under `[ui]`, with your other settings and comments unchanged.
- [ ] `/help`, `/cost`, an approval prompt, and `/exit` with a background process running. **Expected:** all in Spanish; answer letters still `y/s/a/n` and `k/w/c`.
- [ ] 💲 Ask a question. **Expected:** the answer is in Spanish; code, paths, and command output are unchanged.
- [ ] Restart. **Expected:** still Spanish (banner, spinner "pensando").
- [ ] `/locale ja` 💲 **Expected:** a note that menus stay in English; replies come in Japanese.
- [ ] Put a `de.json` with a few keys in `~/.code_puppy/locales/` (see `docs/TRANSLATING.md`), then `/locale de`. **Expected:** those keys in German, the rest in English.
- [ ] `/locale en-XA` **Expected:** accented ⟦…⟧ text everywhere; note any plain English you see.
- [ ] **Native-speaker review:** someone fluent reads `pkg/i18n/locales/es.json` and `fr-CA.json` (tone, terminology, Québec typography for fr-CA) and runs a short session in each.
- [ ] `/locale en-US` to switch back.

## 21. Images 💲

Use a real screenshot (e.g. a UI with visible text) saved in the workspace as `shot.png`.

- [ ] Gemini: `what does @shot.png show?` **Expected:** a dim `📎 shot.png W×H, … KB` line, then an answer that quotes text from the picture.
- [ ] Anthropic: same. Then ask it to "look at shot.png with view_image". **Expected:** a `view_image` badge and an answer about the picture (image delivered inside the tool result).
- [ ] OpenAI (`gpt-5` or another vision model): same two checks. **Expected:** no "unsupported content part" error.
- [ ] Gemini: the `view_image` check. **Expected:** works; if Gemini rejects an image next to a tool result, note the error text here.
- [ ] Ollama with a vision model (e.g. `qwen2.5vl`) and with a text-only model. **Expected:** vision works; text-only gives a clear API error, not a crash.
- [ ] Take a screenshot to the clipboard (macOS: Cmd+Ctrl+Shift+4), `/paste`, then ask about it. **Expected:** `📎 clipboard-HHMMSS.png … will be sent with your next message`. With text on the clipboard: "The clipboard has no image".
- [ ] Linux desktop: `/paste` with `wl-paste` (Wayland) or `xclip` (X11) installed.
- [ ] A 4000×3000 photo: **Expected:** attaches as 1568×1176; the model still describes it.
- [ ] `@~/Desktop/x.png` (outside the workspace) and `@id_rsa_screenshot.png`. **Expected:** refused; "Nothing was sent".
- [ ] Quit and `--continue "what was in that image?"`. **Expected:** the model can still see it. Check `ls -la ~/.code_puppy/images` (files mode 600) and that the session file under `~/.code_puppy/sessions` is small (no base64).
- [ ] `code-puppy --image shot.png "describe" --output-format json | jq .result` **Expected:** a description.

## 22. Diagnostic log and telemetry

- [ ] After any session: `ls -la ~/.code_puppy/logs`. **Expected:** directory mode 700, `code-puppy-YYYY-MM-DD.jsonl` mode 600 with a `start` line; a failed turn adds an `ERROR` line.
- [ ] `CODE_PUPPY_LOG_LEVEL=off code-puppy "hi"`. **Expected:** nothing new in the log.
- [ ] Run a local collector, e.g. Jaeger: `docker run --rm -p 16686:16686 -p 4318:4318 jaegertracing/jaeger:latest`. Then `CODE_PUPPY_TELEMETRY=1 code-puppy` 💲 and do a turn that reads and edits a file.
  **Expected:** in Jaeger (http://localhost:16686, service `code-puppy`), one trace per prompt: `turn` → `invoke_agent` → `generate_content …` and `execute_tool …`, with `approval` under the edit's tool span. No file content, prompt text or tool arguments in any attribute.
- [ ] Two prompts, quit, then `--continue` with a third. In Jaeger search by tag `gen_ai.conversation.id=<session id>`. **Expected:** three `turn` traces with `turn.index` 1–3; turns 2 and 3 each show a link ("References") to the previous turn, including across the restart.
- [ ] Same with `capture_content = true`. **Expected:** prompts and tool arguments appear; your API key does not.
- [ ] Stop the collector, then run and quit a session. **Expected:** exit is not delayed by more than about 3 s; no telemetry errors in the terminal (they go to the log at debug level).
- [ ] `code-puppy doctor`. **Expected:** `log` and `telemetry` lines matching your settings.

## 23. Resilience 💲

- [ ] Turn Wi-Fi off, send a prompt, turn it back on within about 5 s. **Expected:** the turn completes (retried); the log shows the failed attempts.
- [ ] `max_retries = 0`, Wi-Fi off, send a prompt. **Expected:** a clear error; the session keeps working afterwards.
- [ ] `stall_timeout_seconds = 5`, then ask for a long answer with `provider = "openai"` (not streamed). **Expected:** it fails after about 5 s with "model API stopped responding". Restore the default afterwards.
- [ ] Ask for a task that makes many tool calls at once (e.g. "read these 12 files"), with `max_parallel = 2`. **Expected:** it completes; with telemetry on, no more than two `execute_tool` spans overlap.

## 24. Steering 💲

- [ ] Start the REPL. **Expected:** the hint line mentions typing (or Ctrl+T) to message the agent.
- [ ] Ask for a multi-step task (read several files, then edit). While it works, type `use tabs for indentation`. **Expected:** output pauses and a `↪ message for the agent` prompt appears with your text. Enter shows "Sent", output resumes, and the agent's next step reflects the message.
- [ ] Press Ctrl+T during a turn. **Expected:** the same prompt, empty. Enter on an empty line shows "Nothing sent".
- [ ] Type a message while an approval prompt is showing. **Expected:** your keys go to the approval prompt, not a steer prompt.
- [ ] Send a message just as the agent writes its final answer (no more tool calls). **Expected:** "The agent finished before reading your message; sending it now", then a new turn with it.
- [ ] During a steer prompt press Ctrl+C. **Expected:** the turn is cancelled ("Interrupted"), as Ctrl+C always does.
- [ ] After a few steered turns, quit and `--continue "what did I ask you mid-way?"`. **Expected:** it knows.
- [ ] After quitting, type in the shell. **Expected:** echo and line editing work normally (the terminal mode was restored).
- [ ] Linux desktop: the same basic check.

## 25. `!`, `/plan`, `/tools`

- [ ] `!git status` and `!ls`. **Expected:** output as in your terminal, then `✅ Done (…)`; the agent's next answer doesn't know about it.
- [ ] `!vim README.md` (or `!less README.md`), then quit it. **Expected:** the program works normally; the REPL prompt comes back intact.
- [ ] `!sleep 30`, then Ctrl+C. **Expected:** `⚡ Interrupted`; the session continues (no exit prompt).
- [ ] `!exit 3`. **Expected:** `❌ Exit code 3`. The audit log has a `user_shell` entry.
- [ ] 💲 `/plan add input validation to the signup handler`. **Expected:** a "Plan mode" note; the agent reads files; any attempt to edit or run a command comes back as "plan mode: … disabled"; the answer is a numbered plan and no files change (`git status` clean).
- [ ] 💲 `code-puppy --plan "…" --output-format json | jq .result`. **Expected:** a plan; no changes.
- [ ] `/tools`. **Expected:** the active agent's tools with ● on read-only ones; configured MCP servers listed for the primary agent only (unless `agents` says otherwise).

## 26. Model fallback 💲

Set `fallback_models = ["anthropic/claude-sonnet-5"]` with a working Anthropic key, and the primary on Gemini.
- [ ] `code-puppy doctor --online`. **Expected:** `model` and `fallback 1` each initialised and responding.
- [ ] Break the primary: an invalid `GEMINI_API_KEY`. Ask something. **Expected:** one notice "gemini-… is unavailable; answering with fallback claude-sonnet-5", then the answer. Ask again: no second notice and no delay from the primary.
- [ ] `/cost`. **Expected:** priced at Claude's rates.
- [ ] Fix the key, wait 15 s or more, and ask. **Expected:** "gemini-… is answering again".
- [ ] `fallback_models = ["anthropic/no-such-model"]` with a broken primary. **Expected:** a clear "every model failed" error listing both reasons.
- [ ] `provider = "ollama"` with no `base_url` and Ollama running locally. **Expected:** it works (it used to call api.openai.com).

## 27. Per-agent models 💲

- [ ] `/pin_model qa-kitten anthropic/claude-haiku-4-5`. **Expected:** "qa-kitten now runs on claude-haiku-4-5", "Saved in …/.env.toml"; the file has `[agent_models]` with that line and your comments intact.
- [ ] `/agents`. **Expected:** 📌 claude-haiku-4-5 next to qa-kitten.
- [ ] Ask the main agent to have qa-kitten review a file. **Expected:** it works; `/cost` includes Haiku-priced tokens. With telemetry on, qa-kitten's `generate_content` span names claude-haiku-4-5.
- [ ] Restart. **Expected:** the pin is still there (`/pin_model` lists it). `code-puppy doctor` shows `pin qa-kitten`.
- [ ] `/model anthropic/claude-sonnet-5`. **Expected:** the main agent switches provider.
- [ ] `/unpin qa-kitten`. **Expected:** it runs on the configured model again; the line is gone from the config file.

## 28. Per-model settings 💲

- [ ] `/model_settings`. **Expected:** "No model has settings of its own…".
- [ ] `/model_settings gemini-3.8-flash temperature=0.1 seed=7`. **Expected:** "gemini-3.8-flash now uses temperature=0.1 seed=7", "Saved in …/.env.toml"; the file has `[model_settings."gemini-3.8-flash"]` with both lines and your comments intact.
- [ ] Ask the same short question twice. **Expected:** it works; the answers are close to identical. With `CODE_PUPPY_LOG_LEVEL=debug` nothing is logged about dropped settings.
- [ ] `/model_settings gemini-3.8-flash`. **Expected:** temperature 0.1, seed 7, max_tokens "(global: 8192)", top_p "(provider default)".
- [ ] `/model_settings anthropic/claude-sonnet-5 temperature=0.5`. **Expected:** a warning that anthropic doesn't accept temperature for claude-sonnet-5. `/model anthropic/claude-sonnet-5` and ask something: it answers (no 400 error).
- [ ] With an OpenAI key: `/model_settings openai/gpt-5 seed=1`, `/model openai/gpt-5`, ask something. **Expected:** a warning when setting; the answer works (the seed isn't sent).
- [ ] `/model_settings gemini-3.8-flash temperature=5`. **Expected:** "Nothing changed: temperature must be a number in [0, 2]".
- [ ] Restart. **Expected:** `/model_settings` still lists the settings. `/model_settings gemini-3.8-flash reset` removes them and the table from the file.

## 29. Session snapshots 💲

- [ ] In a session, ask the agent to remember a word ("pineapple"). `/session save fruit`. **Expected:** "Saved snapshot fruit (2 messages)…"; `/session list` shows 📸 fruit.
- [ ] Tell it "actually, remember mango". `/session load fruit`, then ask "which word?". **Expected:** "Started session … from snapshot fruit"; it answers pineapple and doesn't know mango.
- [ ] `/resume <the original session's id>` and ask again. **Expected:** mango.
- [ ] `/session save fruit`. **Expected:** refused with a hint about `--force`; with `--force` it's replaced and `/session list` shows one 📸 fruit.
- [ ] Exit. `code-puppy --continue "which word?"`. **Expected:** continues your last ordinary session, not the snapshot. `code-puppy --resume=fruit "which word?"`: pineapple, in a new session.
- [ ] In a session that used tools (e.g. edited a file), save, load, and ask what it just did. **Expected:** it knows about the tool calls, not only the chat text.

## 30. `/search` and Google search 💲

- [ ] `[web] search_provider = "google"` with a Gemini key. `code-puppy doctor --online`. **Expected:** `web search  google: N results`.
- [ ] `/search web golang errors.Is vs errors.As`. **Expected:** up to five numbered links with real site URLs (no `vertexaisearch.cloud.google.com` links), "Handing these to the agent to read", then an answer that cites some of them. No approval prompt for those pages.
- [ ] In the same answer, if the agent tries a page that wasn't listed. **Expected:** an approval prompt.
- [ ] Ask it to save its findings to a file in that turn (e.g. `/search web … and write notes.md`). **Expected:** `create_file` is refused as read-only; no file.
- [ ] `/search session <something said earlier>` after a `/compact`. **Expected:** "Found N messages…" and an answer that recalls the compacted detail.
- [ ] `/search session nonsense-word`. **Expected:** "Nothing in this session's transcript mentions it…", and the agent says it never came up.
- [ ] SearXNG instead: run `docker run -p 8888:8080 searxng/searxng` with `json` added to `search.formats`, set `search_provider = "searxng"` and `search_url = "http://localhost:8888"`, then `/search web …`. **Expected:** it works, with no API key.
- [ ] Remove `search_provider`. `/search web x`. **Expected:** "Web search isn't set up…".
