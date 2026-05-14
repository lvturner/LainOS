# Ask Mode — Read-Only Research Agent

## Summary

Add a secondary "ask mode" to `lain` where the agent can read files, search the web,
and execute read-only commands, but **cannot modify the filesystem**. Kernel-level
enforcement via bubblewrap (Linux namespaces). Invocable via `--ask` CLI flag and
`/ask` TUI slash command.

## Threat Model

The sandbox prevents filesystem writes at the kernel level. It is a **security boundary**:

- Read-only bind mount of `/` → `EROFS` on any write syscall
- Minimal `/dev` → no raw device access
- New PID/IPC/cgroup namespaces → process isolation
- Writable tmpfs at `/tmp` (size-limited) → only writable location, in-memory only
- Shared network namespace → required for curl/camofox

### Not in scope

- Network isolation (required for curl/camofox)
- CPU/memory resource limits (can add cgroup limits later)

## Configuration

New `ask_mode` section in profile `config.yaml`:

```yaml
ask_mode:
  system_prompt: "You are a research assistant..."  # optional
  sandbox:
    tmp_size: "100m"                                  # tmpfs size cap
  tools:
    builtin: ["restricted_command", "extend_timeout"] # built-in tools to allow
    mcp_servers: ["brave-search", "fetch"]            # MCP server allowlist
```

Defaults if absent:

- `system_prompt`: sensible read-only assistant prompt
- `sandbox.tmp_size`: `"100m"`
- `tools.builtin`: `["restricted_command", "extend_timeout"]`
- `tools.mcp_servers`: `[]`

## Sandboxed Command Execution (bubblewrap)

`restricted_command` wraps all commands in `bwrap`:

```
bwrap \
  --ro-bind / / \
  --dev /dev \
  --proc /proc \
  --tmpfs /tmp:size=100m \
  --unshare-pid \
  --unshare-ipc \
  --unshare-cgroup \
  --die-with-parent \
  --new-session \
  -- sh -c "<command>"
```

## Tool Rejection (Agent Steering)

`ToolRegistry` gains an `allowedTools` field. When set, `ExecuteTool` rejects
disallowed tools with a steering message:

- Known built-in but not allowed: `"Tool 'X' is not available in ask mode.
  You are in a read-only research mode. Available tools: ..."`
- MCP tool from non-allowlisted server: same pattern
- Allowed tool: executes normally

This prevents wasted agentic iterations when the model hallucinates tool names.

## Invocation

### CLI: `lain --ask "question"`

- Loads profile, reads `ask_mode` config
- Creates filtered `ToolRegistry` (only `restricted_command`, `extend_timeout`)
- Creates filtered `MCPManager` (only allowlisted servers)
- Runs through oneshot path with `ask_mode.system_prompt`

### TUI: `/ask question`

- Spawns a sub-agent with filtered tools
- Opens in a tiled `agentWindow`
- Uses `ask_mode.system_prompt` as the system prompt

## Files Changed

| File | Changes |
|---|---|
| `Containerfile` | Add `nix profile install nixpkgs#bubblewrap` |
| `lain/config.go` | `AskModeConfig`, `SandboxConfig`, `AskModeTools` structs, defaults |
| `lain/tools.go` | `RestrictedCommandTool`, `SandboxedExecutor`, `FilteredTools()`, `SetAllowedTools()`, execution-time rejection |
| `lain/main.go` | `-ask` flag, filtered oneshot path |
| `lain/tui.go` | `/ask` slash command |
| `lain/sub_agent.go` | `SubAgentOpts.Tools` for filtered tool sets |
