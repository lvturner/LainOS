# lain — LLM Chat CLI with MCP Support

## Overview

A Go binary (`lain`) that provides an interactive (or one-shot) LLM chat interface with MCP tool support, profile-based configuration, and a Bubble Tea TUI. Runs inside the wh-gateway container, available to the `gateway` user.

## CLI Interface

```
lain                        # Interactive mode, default profile
lain qwen                   # Interactive mode, "qwen" profile
lain --prompt "..."         # One-shot, default profile
lain qwen --prompt "..."    # One-shot, "qwen" profile
```

Flag parsing: first non-flag positional arg = profile name. `--prompt` triggers non-interactive mode.

---

## Directory Structure

```
wh-gateway/
├── lain/                          # Go module (separate from gateway/)
│   ├── go.mod
│   ├── main.go                    # Entry point, flag parsing, profile resolution
│   ├── config.go                  # YAML config structs + loader
│   ├── profile.go                 # Profile directory resolution
│   ├── llm.go                     # OpenAI-compatible API client with streaming
│   ├── tools.go                   # Tool definitions + executor (run_command, MCP tools)
│   ├── mcp.go                     # MCP client manager (spawns servers, discovers tools)
│   ├── tui.go                     # Bubble Tea TUI model
│   ├── oneshot.go                 # Non-interactive --prompt mode
│   └── banner.go                  # ASCII art banner
├── config/
│   └── lain/                      # Bind-mounted into container at /home/gateway/.config/lain
│       └── profiles/
│           └── default/
│               ├── config.yaml    # LLM connection details
│               ├── servers.json   # MCP server config (mcpServers format)
│               └── agents.md      # System instructions
```

---

## Profile Structure

Each profile lives at `~/.config/lain/profiles/<name>/` and contains three files:

### config.yaml — LLM Connection

```yaml
api_url: "https://api.deepseek.com/v1"
api_key: "sk-..."
model: "deepseek-chat"
temperature: 0.7
max_tokens: 4096
```

Fields:
- `api_url` — Base URL for OpenAI-compatible API (e.g. DeepSeek, Ollama, vLLM, OpenAI)
- `api_key` — API key (can be empty for local models)
- `model` — Model identifier
- `temperature` — Sampling temperature (0.0–2.0)
- `max_tokens` — Maximum tokens per response

### servers.json — MCP Servers

Standard `mcpServers` format compatible with Claude Desktop, Cursor, etc:

```json
{
  "mcpServers": {
    "server-name": {
      "command": "executable",
      "args": ["arg1", "arg2"],
      "env": {
        "VAR": "value"
      }
    }
  }
}
```

Default is an empty `mcpServers` object.

### agents.md — System Instructions

Plain markdown file read on launch and injected as the system prompt. Default content provides basic guidance for the LLM as a coding/system assistant.

---

## Go Dependencies

| Dependency | Purpose |
|---|---|
| `github.com/charmbracelet/bubbletea` | TUI framework |
| `github.com/charmbracelet/lipgloss` | Terminal styling |
| `github.com/charmbracelet/bubbles` | Pre-built TUI components (textarea, viewport) |
| `github.com/mark3labs/mcp-go` | MCP client protocol (stdio JSON-RPC) |
| `github.com/sashabaranov/go-openai` | OpenAI-compatible API client with streaming |
| `gopkg.in/yaml.v3` | YAML config parsing |

---

## Module Details

### main.go — Entry Point

- Parse flags (`--prompt`)
- Determine profile name (first positional arg, or `"default"`)
- Load config from `~/.config/lain/profiles/<name>/`
- Read `agents.md` as system prompt
- Load MCP servers from `servers.json`, spawn MCP client subprocesses, discover tools
- If `--prompt` is set: run one-shot mode
- Otherwise: launch Bubble Tea TUI

### config.go — Configuration

```go
type LainConfig struct {
    APIURL      string  `yaml:"api_url"`
    APIKey      string  `yaml:"api_key"`
    Model       string  `yaml:"model"`
    Temperature float64 `yaml:"temperature"`
    MaxTokens   int     `yaml:"max_tokens"`
}
```

- `LoadConfig(path string) (*LainConfig, error)` — reads and parses config.yaml
- `LoadServers(path string) (*ServersConfig, error)` — reads servers.json
- `LoadAgents(path string) (string, error)` — reads agents.md as raw text

### profile.go — Profile Resolution

```go
func ProfileDir(name string) (string, error)
func LoadProfile(name string) (*Profile, error)

type Profile struct {
    Config   *LainConfig
    Servers  *ServersConfig
    Agents   string
    BasePath string
}
```

- Resolves `~/.config/lain/profiles/<name>/`
- Loads all three files from the profile directory
- Returns error if `config.yaml` is missing; `servers.json` and `agents.md` are optional (defaults used)

### llm.go — LLM Client

- Uses `go-openai` with streaming support
- Manages conversation history (`[]openai.ChatCompletionMessage`)
- System prompt = contents of `agents.md`
- Presents all tools (built-in + MCP) in OpenAI function calling format
- **Agentic loop**:
  1. Send messages + system prompt + tools to LLM (streaming)
  2. Receive streamed response
  3. If response contains `tool_calls`:
     a. Execute each tool (dispatch to built-in or MCP handler)
     b. Append assistant message with tool calls + tool result messages to history
     c. Go to step 1 (re-send with tool results)
  4. If response is plain text:
     a. Return final text to caller (TUI or oneshot)
- Streaming via channel: `type StreamEvent struct { Type string; Content string }` where Type is `"token"`, `"tool_start"`, `"tool_output"`, `"done"`, `"error"`

### tools.go — Tool System

Built-in tool `run_command`:

```go
var RunCommandTool = openai.Tool{
    Type: "function",
    Function: &openai.FunctionDefinition{
        Name:        "run_command",
        Description: "Execute a system command and return its output",
        Parameters: map[string]interface{}{
            "type": "object",
            "properties": map[string]interface{}{
                "command": map[string]interface{}{
                    "type":        "string",
                    "description": "The shell command to execute",
                },
            },
            "required": []string{"command"},
        },
    },
}
```

- Executes via `exec.Command("sh", "-c", command)`
- Captures stdout + stderr
- Returns combined output as tool result
- No confirmation prompt — LLM decides, command runs immediately
- 30-second timeout per command

Tool registry:
```go
type ToolRegistry struct {
    builtinTools []openai.Tool
    mcpTools     []openai.Tool
    mcpClients   map[string]*MCPClient
}

func (r *ToolRegistry) AllTools() []openai.Tool
func (r *ToolRegistry) ExecuteTool(name string, args json.RawMessage) (string, error)
```

Dispatch logic:
- If tool name is `run_command` → execute locally
- Otherwise → find the MCP client that owns this tool → call `tools/call` via MCP protocol

### mcp.go — MCP Client Manager

```go
type MCPManager struct {
    clients map[string]*mcp.Client
    tools   map[string]MCPServerTool
}

type MCPServerTool struct {
    ServerName string
    ToolName   string
    Tool       mcp.Tool
}
```

- Reads `servers.json`
- For each server in `mcpServers`:
  1. Spawn subprocess with configured `command`, `args`, `env`
  2. Connect via stdio JSON-RPC using `mcp-go` client
  3. Call `tools/list` to discover available tools
  4. Convert MCP tool definitions to OpenAI function calling format
- Provides `CallTool(serverName, toolName, args)` method
- Handles subprocess lifecycle (start, stop, cleanup)
- All MCP servers are launched at startup, shut down on exit

MCP tool name convention: `<server-name>__<tool-name>` (double underscore) to avoid collisions between servers.

### tui.go — Bubble Tea TUI

**Layout** (top to bottom):
```
┌─────────────────────────────────────┐
│          LAIN ASCII BANNER          │
│        profile: default             │
│        model: deepseek-chat         │
├─────────────────────────────────────┤
│                                     │
│         Scrollable Chat             │
│          Viewport                   │
│                                     │
│  ┌─ user ────────────────────────┐  │
│  │ How do I check disk usage?    │  │
│  └──────────────────────────────┘  │
│  ┌─ assistant ──────────────────┐  │
│  │ I'll check that for you.     │  │
│  │ ▸ run_command: df -h         │  │
│  │   (output truncated, 3 lines)│  │
│  │ Here's your disk usage...    │  │
│  └──────────────────────────────┘  │
│                                     │
├─────────────────────────────────────┤
│  > type your message here...        │
└─────────────────────────────────────┘
```

Components:
- **Banner**: ASCII art from `banner.go`, styled with lipgloss, shows profile name + model
- **Chat viewport**: `bubbles/viewport` with styled message bubbles
  - User messages: colored border + background
  - Assistant messages: different color
  - Tool calls: dim, collapsed blocks showing tool name + truncated output
  - Streaming tokens append to the current assistant bubble in real-time
- **Input area**: `bubbles/textarea` for multi-line input
  - Enter sends message
  - Alt+Enter for newline
  - `/quit` or Ctrl+C to exit

Bubble Tea model:
```go
type model struct {
    profile     *Profile
    registry    *ToolRegistry
    client      *LLMClient
    viewport    viewport.Model
    textarea    textarea.Model
    messages    []ChatMessage
    streaming   bool
    err         error
}
```

### oneshot.go — Non-Interactive Mode

```go
func RunOneShot(profile *Profile, registry *ToolRegistry, prompt string) error
```

- Sends prompt to LLM with streaming
- Streams response tokens to stdout
- Prints tool executions to stderr (prefixed with `▸`)
- Exits with code 0 on success, 1 on error
- Agentic loop still runs (tool calls are executed, but only final text goes to stdout)

### banner.go — ASCII Art

```
  ██╗     ██╗██╗  ██╗███████╗
  ██║     ██║╚██╗██╔╝██╔════╝
  ██║     ██║ ╚███╔╝ █████╗
  ██║     ██║ ██╔██╗ ██╔══╝
  ███████╗██║██╔╝ ██╗███████╗
  ╚══════╝╚═╝╚═╝  ╚═╝╚══════╝
```

Styled with lipgloss in cyan. Below it: profile name and model in dim text.

---

## Default agents.md

```markdown
# Lain Assistant

You are a helpful coding and system administration assistant running inside a Linux container.

You have access to system commands via the `run_command` tool. Use it freely to:
- Explore the filesystem
- Run build and test commands
- Inspect running processes and services
- Execute any shell commands needed to help the user

Always explain what you're doing before running commands. When writing code or config files,
show the content first, then write it.

Be concise. Prefer action over explanation.
```

---

## Container Integration

### Containerfile Changes

Add a second Go build stage alongside the existing gateway builder:

```dockerfile
FROM golang:1.23 AS lain-builder
WORKDIR /build
COPY lain/ .
RUN CGO_ENABLED=0 go build -o lain .

# In runtime stage:
COPY --from=lain-builder /build/lain /usr/local/bin/lain
```

### compose.yaml Changes

Add volume mount for lain profiles:

```yaml
volumes:
  - ./config:/config:Z
  - ./workspace:/workspace:Z
  - ./config/lain:/home/gateway/.config/lain:Z
```

This makes `./config/lain/profiles/` editable from the host, visible as `~/.config/lain/profiles/` inside the container for the `gateway` user.

### start.sh Changes

Add a new section after the existing config checks that:

1. Creates `config/lain/profiles/default/` if it doesn't exist
2. If `config.yaml` doesn't exist in the default profile, prompts interactively:
   - API URL (default: `https://api.deepseek.com/v1`)
   - API Key (no default, required)
   - Model name (default: `deepseek-chat`)
   - Temperature (default: `0.7`)
3. Writes `config.yaml` with collected values
4. Writes default `agents.md` if not present
5. Writes empty `servers.json` if not present:
   ```json
   {
     "mcpServers": {}
   }
   ```

---

## Implementation Order

1. `lain/go.mod` — module init + dependencies
2. `config.go` + `profile.go` — config loading, profile resolution
3. `llm.go` — basic LLM client with streaming (no tools yet)
4. `oneshot.go` — verify one-shot mode works end-to-end
5. `tools.go` — `run_command` built-in tool
6. Agentic loop in `llm.go` — tool call handling with re-prompting
7. `mcp.go` — MCP client integration (spawn servers, discover tools, execute)
8. `tui.go` + `banner.go` — Bubble Tea interactive TUI
9. `main.go` — wire everything together
10. Containerfile + compose.yaml — build integration
11. `start.sh` — profile setup wizard
12. Default config files in `config/lain/profiles/default/`

---

## Open Items

- **Streaming parser**: The `go-openai` library handles SSE streaming. Need to verify DeepSeek's streaming format is fully compatible (it should be — they claim OpenAI compat).
- **MCP tool name collisions**: Using `<server>__<tool>` naming. If two servers expose a tool with the same name, both are available with different prefixes.
- **Context window management**: No explicit token counting in v1. If conversation gets too long, older messages get truncated from the start. Can add smarter summarization later.
- **Multiple profiles**: Profile switching mid-session is not supported in v1. Exit and relaunch with different profile name.
