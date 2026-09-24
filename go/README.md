# 🐶 Code Puppy Go (Google ADK Edition)

> High-performance, memory-efficient, universally distributable AI code agent rewritten in Go, built on the **Google Agent Development Kit (`google.golang.org/adk/v2`)** and configured via **`retail-cortex/modenv`**.

---

## 🌟 Highlights

- **⚡ Blazing Fast**: Startup in `<10ms` (compared to 2–5s in Python) and ultra-low memory footprint.
- **📦 Zero-Dependency Static Binaries**: Self-contained single binary with embedded agent personas, skills, and tools. Universally distributable across macOS, Linux, and Windows.
- **🤖 Powered by Google ADK**: Built directly on `google.golang.org/adk/v2` (`llmagent`, `functiontool`, `runner`, `session`).
- **🔐 Cascading Secrets via `modenv`**: Automatic resolution of `.env.toml`, `.env.<runtime>.toml`, and `.env.local.toml`, with built-in decryption for `cloud://`, `pks://`, and `simple://` secret URIs.
- **📝 Markdown Agent Personas**: All agent personalities are structured as Markdown files with YAML frontmatter embedded via `embed.FS` at build time or discovered from `./agents/`.
- **🎯 Full Tooling Suite**:
  - File Operations: `read_file`, `list_files`, `create_file`, `delete_file`
  - Code Editing: `replace_in_file`, `edit`, `delete_snippet`, `apply_patch`
  - High-Speed Grep: Multi-pattern regex and text search
  - Subprocess Execution: `run_shell_command` with timeout, streaming, and background process management
  - Human-in-the-Loop: `ask_user_question` for interactive clarifications
  - Agent Skills: `list_or_search_skills`, `activate_skill`
  - Multi-Agent Delegation: `list_agents`, `invoke_agent`
  - Universal Constructor: `universal_constructor` (Helios agent dynamic tool creation)

---

## 🚀 Quick Start

### 1. Build Local Binary
```bash
./scripts/build.sh
```
This produces `./bin/code-puppy`.

### 2. Configure API Key
Create `.env.toml` (or export environment variables):
```toml
[code_puppy]
default_agent = "code-puppy"
default_model = "gemini-2.5-flash"
agency_level = "high" # "low", "medium", "high", "extreme"

[llm.gemini]
api_key = "cloud://GEMINI_API_KEY" # or raw API key
```

Or simply:
```bash
export GEMINI_API_KEY="your-api-key"
```

### 3. Run Interactively (REPL)
```bash
./bin/code-puppy
```

### 4. Run One-Shot (Non-Interactive CLI)
```bash
./bin/code-puppy -p "Refactor the error handling in main.go to use fmt.Errorf with %w"
```

---

## 🤖 Built-In Agent Personas

| Agent | Display Name | Role & Specialties |
|---|---|---|
| `code-puppy` | Code-Puppy 🐶 | Primary autonomous code agent; pedantic about DRY/SOLID; loyal and sassy |
| `helios` | Helios ☀️ | Universal Constructor; builds and runs custom tools and scripts on the fly |
| `qa-kitten` | Quality Assurance Kitten 🐱 | TDD test loops, boundary checks, edge-case discovery, regression suites |
| `web-retriever` | Web Retriever 🌐 | Documentation crawling, web research, structured data extraction |
| `planning-agent` | Planning Agent 📋 | Requirement decomposition, architectural roadmaps, milestone verification |
| `agent-creator` | Agent Creator 🏗️ | Creates and validates custom agent markdown specs and skills |
| `model-judge` | Model Judge ⚖️ | Model benchmarking, latency evaluation, and side-by-side comparisons |

---

## 🛠️ Slash Commands

In interactive mode, type slash commands to steer Code Puppy:

- `/help` - Show command catalog
- `/agents` - List all available agent personas
- `/agent [name]` - Switch active agent persona
- `/model [name]` - View or switch the active LLM model
- `/skills list` - List discovered and embedded Agent Skills
- `/skills search [query]` - Search skills by keyword
- `/session list` - List saved conversation sessions
- `/session new` - Start a fresh conversation session
- `/set [key=value]` - Inspect or adjust runtime parameters
- `/clear` - Clear terminal screen
- `/exit` or `/quit` - Exit

---

## 📦 Universal Distribution (Cross-Compilation)

Cross-compile zero-dependency static binaries for all major platforms:

```bash
./scripts/build.sh --cross-compile
```

Outputs generated in `bin/`:
- `code-puppy-darwin-arm64` (Apple Silicon macOS)
- `code-puppy-darwin-amd64` (Intel macOS)
- `code-puppy-linux-amd64` (x86_64 Linux)
- `code-puppy-linux-arm64` (ARM64 Linux)
- `code-puppy-windows-amd64.exe` (Windows x64)

---

## 🧪 Testing

Run the unit test suite across all packages:
```bash
CGO_ENABLED=0 go test -v ./...
```
