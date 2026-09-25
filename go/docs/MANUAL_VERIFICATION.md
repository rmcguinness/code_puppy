# Manual Verification Checklist

Everything here needs a person, a real terminal, real credentials, or GitHub. The automated suite (280 tests on macOS and Linux) covers the logic behind each item; this list checks the parts it can't. Each item has an **expected** result — if you see something else, note it next to the item.

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

## 13. Hooks

- [ ] A `pre_tool` hook for `run_shell_command` that exits 2 with a message. **Expected:** shell calls are blocked with that message; other tools work.
- [ ] A `post_tool` hook appending the JSON event to a file. **Expected:** one line per tool call, including tool name and args.
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

