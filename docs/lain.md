# lain — LLM Chat CLI

An interactive LLM chat interface with MCP tool support, MDI window management, a Lua plugin system for self-extension, and profile-based configuration. Runs inside the lainos container and can be launched with the `lain` command.

## CLI Usage

```
lain                        # Interactive mode, default profile
lain qwen                   # Interactive mode, "qwen" profile
lain --prompt "..."         # One-shot, default profile
lain qwen --prompt "..."    # One-shot, "qwen" profile
```

The first non-flag positional argument is the profile name. The `--prompt` flag triggers non-interactive mode.

### Interactive Mode

Full TUI with a multi-window MDI layout (tiled and floating panels), scrollable chat viewport, and multi-line input area:

- **Enter** — Send message
- **Alt+Enter** — Insert newline
- **Ctrl+W** — Enter window management mode (normal mode)
- **Ctrl+S** — Open session picker
- **Ctrl+C** — Cancel streaming response or exit
- **/quit** or **/exit** — Exit lain

The banner shows the active profile name, model, and session title.

The default layout is a tiled split: chat viewport (left) + todo sidebar (right, 28 chars wide). Plugin windows can be added as additional tiles or as floating overlays.

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

On the host, profiles are at `.data/home/.config/lain/profiles/<name>/` (bind-mounted into the container).

### config.yaml — LLM Connection

```yaml
api_url: "https://api.deepseek.com/v1"
api_key: "sk-..."
model: "deepseek-chat"
temperature: 0.7
max_tokens: 4096
context_length: 128000
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
| `context_length` | Estimated context length in tokens | `128000` |
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
2. When usage reaches the configured `compaction_threshold` percentage of `context_length`, triggers compaction
3. Sends the conversation to the LLM with a summarization prompt
4. Replaces old history with the summary, keeping the last user message (if `compaction_strategy` is `keep_last`)
5. Compaction events are shown in the TUI and in stderr for one-shot mode

Auto-compaction runs:
- Before each user message is processed
- After each tool call batch within the agentic loop (preventing runaway context growth during long sessions)

Compaction preserves factual information, decisions, actions taken, code snippets, and the current task state.

### Manual Compaction

Type `/compact` to manually trigger compaction at any time. This is useful when you want to reclaim context space before the automatic threshold is reached.

## Window Management

lain uses an MDI (Multiple Document Interface) with a tiling window manager. Press **Ctrl+W** to enter normal mode, where single-key commands control the layout (like tmux):

| Key | Action |
|---|---|
| `h`/`j`/`k`/`l` | Focus left/down/up/right |
| `x` | Close focused window (not chat) |
| `f` | Toggle float/tile for focused window |
| `+`/`-` | Resize split ratio |
| `1`–`9` | Focus window by index |
| `?` | Show help overlay |
| `Esc` or `i` | Return to insert mode |

The chat window is always present and cannot be closed. The todo sidebar is always shown on the right.

## Sub-Agent System

lain can spawn independent sub-agents that run in their own tiled windows alongside the main chat.
Each sub-agent has its own LLM client, conversation history, tools, and system prompt.

### Plugin Error Recovery

When a Lua plugin encounters an error (load failure, syntax error, runtime error), lain captures it
and offers to spawn a fix agent:

1. The error appears in the status bar: `⚠ plugin "name": error text — F:fix  Esc:dismiss`
2. lain switches to normal mode so you can respond without interrupting the main agent
3. Press **F** to spawn a fix agent, or **Esc**/**i** to dismiss
4. The fix agent opens in a new tiled window, reads the plugin source, and edits the file
5. Plugin auto-reloads on save — agent is notified of success or further errors
6. Multiple errors queue into a single fix agent window
7. The fix agent window stays open after completion for review

### Sub-Agent Configuration

Add to your profile's `config.yaml`:

```yaml
sub_agent:
  profile: "default"  # profile to use for sub-agents (default: current profile)
  auto_fix: false     # skip confirmation prompt, auto-spawn fix agent on errors
```

If `auto_fix: true`, plugin errors immediately spawn a fix agent without prompting.
Use with caution — each fix agent makes LLM API calls.

The `profile` field lets you use a cheaper/faster model for sub-agents. For example:

```yaml
sub_agent:
  profile: "fast"  # uses the "fast" profile for sub-agents
```

## Plugin System

Plugins are Lua scripts that extend lain's UI at runtime. They live at `~/.config/lain/plugins/*.lua` and are **hot-reloaded** on file change — no restart needed.

Each plugin runs in a Lua 5.1 state with the full standard library: `string`, `table`, `math`, `coroutine`, `io`, `os`, `debug`, `package`. The `lain.*` API provides TUI integration; standard library functions are available for filesystem and system access.

### Quick example

Create `~/.config/lain/plugins/hello.lua`:

```lua
lain.window.register({
  id = "hello",
  title = "Hello",
  float = false,
  render = function(width, height)
    return "Hello from Lua! Width: " .. width .. ", Height: " .. height
  end,
  update = function(event)
  end,
})
```

Save the file and the window appears immediately in the tiled layout.

### Plugin API Reference

#### lain.window

| Function | Description |
|---|---|
| `register(opts)` | Register a window. `opts`: `{ id, title, float, render=fn, update=fn }` |
| `close(id)` | Close and unregister a window |
| `focus(id)` | Focus a window by ID |
| `list()` | List all open window IDs |

The `render` function receives `(width, height)` and must return a string. The `update` function receives an event table.

#### lain.chat

| Function | Description |
|---|---|
| `on_message(cb)` | Register callback: `cb(role, text)` called on every message |
| `get_messages()` | Returns array of `{ role, content }` tables |

#### lain.session

| Function | Description |
|---|---|
| `get_id()` | Current session ID |
| `get_title()` | Current session title |
| `get_profile()` | Current profile name |

#### lain.state

| Function | Description |
|---|---|
| `set(key, value)` | Set per-plugin state (persists in memory) |
| `get(key)` | Get per-plugin state |

#### lain.log

| Function | Description |
|---|---|
| `info(msg)` | Log info |
| `warn(msg)` | Log warning |
| `error(msg)` | Log error |

#### lain.command

| Function | Description |
|---|---|
| `register(name, cb)` | Register a `/name` slash command. `cb(arg)` receives the text after the command name. |

#### lain.keybind

| Function | Description |
|---|---|
| `register(key, cb)` | Register a normal-mode keybinding. `cb()` is called when the key is pressed in normal mode. |

#### lain.exec

| Function | Description |
|---|---|
| `exec(cmd, opts?)` | Run command synchronously. Returns `{ stdout, stderr, exit_code, success }`. Options: `{ timeout=30, cwd="", env={} }` |
| `exec_async(cmd, opts, callback)` | Run command asynchronously. `callback(result)` called on completion. |

Render functions have a 50ms timeout — use `exec_async` for commands in render. Callbacks have a 5s timeout — `exec` is fine for fast commands.

### Available Lua libraries

Plugins have access to the full Lua 5.1 standard library: `string`, `table`, `math`, `coroutine`, `io`, `os`, `debug`, `package`. Use `io.popen` or `os.execute` for system commands. For reliable command execution in render/callback contexts, use `lain.exec()` or `lain.exec_async()`.

### Plugin directory

```
~/.config/lain/plugins/       → *.lua plugin files (hot-reloaded)
~/.config/lain/plugins/state/ → per-plugin JSON state files
```

### Log viewer example

```lua
lain.window.register({
  id = "log-viewer",
  title = "Logs",
  float = false,
  render = function(width, height)
    local lines = lain.state.get("lines") or {}
    local out = {}
    for i, line in ipairs(lines) do
      if i > height then break end
      out[i] = line
    end
    return table.concat(out, "\n")
  end,
  update = function(event)
    if event.type == "key" and event.key == "r" then
      lain.state.set("lines", {})
      lain.log.info("logs cleared")
    end
  end,
})

lain.chat.on_message(function(role, text)
  if role == "tool" then
    local lines = lain.state.get("lines") or {}
    table.insert(lines, text)
    lain.state.set("lines", lines)
  end
end)
```

## Slash Commands

| Command | Description |
|---|---|
| `/new` | Start a new session |
| `/save` | Save current session |
| `/rename <title>` | Rename current session |
| `/sessions` | Open session picker |
| `/compact` | Manually trigger context compaction |
| `/plugins` | List loaded plugins and their windows |
| `/plugins reload` | Force-reload all plugins from disk |
| `/windows` | List open windows and their IDs |
| `/float <id>` | Toggle floating for a window |
| `/quit` or `/exit` | Exit lain |

## Nix Package Management

The container has nix installed for the `lainos` user. Use it to install tools that your LLM or handler scripts need:

```bash
# Install packages
nix profile install nixpkgs#jq
nix profile install nixpkgs#curl
nix profile install nixpkgs#python3

# List installed
nix profile list

# Remove by name
nix profile remove jq

# Free disk space from old packages
nix-collect-garbage
```

Installed binaries are immediately available in PATH.

## Creating Additional Profiles

To create a new profile (e.g. for a different model):

```bash
mkdir -p .data/home/.config/lain/profiles/codellama
cat > .data/home/.config/lain/profiles/codellama/config.yaml << 'EOF'
api_url: "http://localhost:11434/v1"
api_key: ""
model: "codellama:34b"
temperature: 0.2
max_tokens: 8192
EOF

cat > .data/home/.config/lain/profiles/codellama/servers.json << 'EOF'
{ "mcpServers": {} }
EOF

cat > .data/home/.config/lain/profiles/codellama/agents.md << 'EOF'
You are a coding assistant focused on code review and refactoring.
EOF
```

Then launch: `lain codellama`
