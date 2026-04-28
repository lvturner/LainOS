# lain — LLM Chat CLI

An interactive LLM chat interface with MCP tool support, profile-based configuration, and a terminal UI. Runs inside the lainos container and can be launched with the `lain` command.

## CLI Usage

```
lain                        # Interactive mode, default profile
lain qwen                   # Interactive mode, "qwen" profile
lain --prompt "..."         # One-shot, default profile
lain qwen --prompt "..."    # One-shot, "qwen" profile
```

The first non-flag positional argument is the profile name. The `--prompt` flag triggers non-interactive mode.

### Interactive Mode

Full TUI with a scrollable chat viewport and multi-line input area:

- **Enter** — Send message
- **Alt+Enter** — Insert newline
- **/quit** or **Ctrl+C** — Exit (Ctrl+C also cancels a streaming response)

The banner shows the active profile name and model.

### One-Shot Mode

With `--prompt`, lain sends the prompt, runs the agentic loop (including tool calls), and streams the final response to stdout. Tool executions are printed to stderr prefixed with `▸`.

```bash
lain --prompt "What processes are running?"
lain --prompt "Check disk usage and tell me if I need to worry"
```

## Profiles

Each profile lives at `~/.config/lain/profiles/<name>/` and contains three files:

```
~/.config/lain/profiles/
└── default/
    ├── config.yaml      # LLM connection details
    ├── servers.json     # MCP server config
    └── agents.md        # System instructions
```

On the host, profiles are at `.data/lain/profiles/<name>/` (bind-mounted into the container).

### config.yaml — LLM Connection

```yaml
api_url: "https://api.deepseek.com/v1"
api_key: "sk-..."
model: "deepseek-chat"
temperature: 0.7
max_tokens: 4096
context_window: 128000
compaction_threshold: 60
compaction_strategy: "keep_last"
idle_timeout: 120s
max_nudges: 3
nudge_message: "You haven't produced output in a while. If you're about to run something slow, use extend_timeout. Otherwise, continue your task."
```

| Field | Description | Default |
|---|---|---|
| `api_url` | Base URL for OpenAI-compatible API (DeepSeek, Ollama, vLLM, OpenAI, etc.) | required |
| `api_key` | API key (can be empty for local models like Ollama) | `""` |
| `model` | Model identifier | required |
| `temperature` | Sampling temperature (0.0–2.0) | `0.7` |
| `max_tokens` | Maximum tokens per response | `4096` |
| `context_window` | Estimated context window in tokens | `128000` |
| `compaction_threshold` | Percentage of context window before compaction triggers | `60` |
| `compaction_strategy` | Compaction strategy: `keep_last` keeps the last user message | `keep_last` |
| `idle_timeout` | Seconds before nudging a stalled agent | `120s` |
| `max_nudges` | Maximum automatic nudges before giving up | `3` |
| `nudge_message` | Message injected when nudging | *(see defaults)* |

`config.yaml` is the only required file in a profile. If `servers.json` or `agents.md` are missing, defaults are used.

### servers.json — MCP Servers

Standard `mcpServers` format compatible with Claude Desktop, Cursor, etc:

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/home/lainos"],
      "env": {
        "NODE_OPTIONS": "--max-old-space-size=4096"
      }
    },
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": {
        "GITHUB_PERSONAL_ACCESS_TOKEN": "ghp_..."
      }
    }
  }
}
```

Each MCP server is spawned as a subprocess at startup, connected via stdio JSON-RPC. Tools are discovered automatically via `tools/list`.

MCP tools are named using the convention `<server-name>__<tool-name>` (double underscore) to avoid collisions between servers. For example, the `read_file` tool from the `filesystem` server becomes `filesystem__read_file`.

### agents.md — System Instructions

Plain markdown file injected as the system prompt on every conversation turn. This is where you customize the LLM's behavior:

```markdown
# My Custom Assistant

You are a deployment assistant. When code is pushed to main:
1. Run tests
2. Build the project
3. Deploy to staging

Always verify the GitHub signature before acting on webhooks.
```

## Built-in Tool: run_command

Every profile has access to `run_command` — it executes shell commands and returns the output:

```json
{
  "name": "run_command",
  "parameters": {
    "command": "the shell command to execute"
  }
}
```

Behavior:
- Runs via `sh -c <command>`
- 30-second timeout per command
- Returns combined stdout + stderr
- `sudo` is blocked — use `nix profile install` for software instead

## Built-in Tool: extend_timeout

Asks the system to wait longer before nudging:

```json
{
  "name": "extend_timeout",
  "parameters": {
    "duration_seconds": 300,
    "reason": "Running a long build"
  }
}
```

Behavior:
- Extends idle timeout by the requested seconds (max 3600)
- Should be called before long operations
- Returns confirmation message

## Idle Watchdog

lain monitors agent activity and detects stalls at both the API connection and stream-reading phases:

1. **Connection watchdog** — If the API doesn't respond within `idle_timeout`, a nudge is injected
2. **Stream watchdog** — If no tokens arrive within `idle_timeout` during streaming, the stream is killed and a nudge is injected
3. **Nudge injection** — A user message is appended to history prompting the agent to continue, then the request is retried
4. **Max nudges** — After `max_nudges` consecutive stalls, the agent gives up with an error

The `extend_timeout` tool lets the agent proactively request more time before long operations. The timeout extension applies to the next iteration only.

## Context Compaction

When the conversation history approaches the context window limit, lain automatically compacts it:

1. Estimates current token usage from message history
2. When usage reaches the configured `compaction_threshold` percentage of `context_window`, triggers compaction
3. Sends the conversation to the LLM with a summarization prompt
4. Replaces old history with the summary, keeping the last user message (if `compaction_strategy` is `keep_last`)
5. Compaction events are shown in the TUI and in stderr for one-shot mode

Compaction preserves factual information, decisions, actions taken, code snippets, and the current task state.

## Nix Package Management

The container has nix installed for the `lainos` user. Use it to install tools that your LLM or handler scripts need:

```bash
# Install packages
nix profile install nixpkgs#jq
nix profile install nixpkgs#curl
nix profile install nixpkgs#python3

# List installed
nix profile list

# Remove by index
nix profile remove 0

# Free disk space from old packages
nix-collect-garbage
```

Installed binaries are immediately available in PATH.

## Creating Additional Profiles

To create a new profile (e.g. for a different model):

```bash
mkdir -p .data/lain/profiles/codellama
cat > .data/lain/profiles/codellama/config.yaml << 'EOF'
api_url: "http://localhost:11434/v1"
api_key: ""
model: "codellama:34b"
temperature: 0.2
max_tokens: 8192
EOF

cat > .data/lain/profiles/codellama/servers.json << 'EOF'
{ "mcpServers": {} }
EOF

cat > .data/lain/profiles/codellama/agents.md << 'EOF'
You are a coding assistant focused on code review and refactoring.
EOF
```

Then launch: `lain codellama`
