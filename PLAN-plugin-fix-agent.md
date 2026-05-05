# Plugin Error Recovery & Sub-Agent System — Implementation Plan

## Overview

When a Lua plugin encounters an error (load failure, syntax error, runtime error), capture the error and offer to spawn an autonomous fix agent in a new tiled window. The fix agent reads the plugin source, identifies the issue, edits the file, and the hot-reload watcher picks up the fix. The main agent continues uninterrupted throughout.

This also introduces a **generic sub-agent system** — independent LLM clients running in their own tiled windows with isolated conversation history, tools, and system prompts. Reusable for background research, code review, or any future multi-agent workflows.

## Architecture

```
Plugin error occurs
  → PluginLoader.errCh ← PluginError
  → pluginErrorMsg arrives in TUI Update()
  → If sub_agent.auto_fix: true → spawn fix agent directly
  → If sub_agent.auto_fix: false (default):
     → Switch to normal mode, show in status bar:
       ⚠ Plugin "X": <error text> — F:fix  Esc:dismiss
     → Main agent continues streaming (normal mode doesn't block stream events)
     → User presses F → spawn fix agent, return to insert mode
     → User presses Esc/i → dismiss, return to insert mode
  → Fix agent spawned:
     → Load configurable profile (default: current profile)
     → Create own LLMClient + ToolRegistry (all tools)
     → Create agentWindow → add as tiled split
     → Show floating notification: "Fix agent spawned for plugin X"
     → System prompt: error details + plugin source path + instructions
     → Agent uses run_command to read/edit Lua file
     → Hot-reload picks up changes → plugin reloads
     → On successful reload → inject success message to fix agent
     → Agent stays open for review
  → Multiple errors → queue into single fix agent
```

## New Files

### `lain/sub_agent.go` — Sub-Agent Manager

```
SubAgentConfig struct:
  Profile  string  // yaml:"profile", default: "" (means current profile)
  AutoFix  bool    // yaml:"auto_fix", default: false

SubAgentOpts struct:
  ID, Title, ProfileName string
  SystemPrompt, InitialMsg string

SubAgent struct:
  id, profileName string
  client       *LLMClient
  window       *agentWindow
  registry     *ToolRegistry
  mcpMgr       *MCPManager
  cancelFn     context.CancelFunc
  streamCh     <-chan StreamEvent

SubAgentManager struct:
  agents     map[string]*SubAgent
  fixAgentID string                // single fix agent for all plugin errors
  model      *model                // back-reference

Methods:
  Spawn(opts) (*SubAgent, error)
    - LoadProfile(opts.ProfileName)
    - NewMCPManager, NewToolRegistry, add all tools
    - NewLLMClient with fix-oriented system prompt
    - NewAgentWindow, add to WM as tiled split (SplitVertical)
    - client.Chat(ctx, initialMsg) → start streaming
    - Return sub-agent with stream channel

  EnqueueMessage(agentID, message)
    - Inject message into running agent via client.InjectMessage()

  Stop(agentID)
    - Cancel context, remove window from WM, cleanup MCPManager

  StopAll()
    - Stop all active sub-agents

  HasActive(agentID) bool
```

### `lain/agent_window.go` — Agent Chat Window

```
agentWindow struct:
  id, title    string
  messages     []ChatMessage
  current      *ChatMessage
  width, height int
  streaming    bool
  done         bool
  spinner      spinner.Model

Implements Window interface:
  ID(), Title(), SetSize()

  Update(msg) → handles spinner ticks, no user input

  View(width, height, focused) → compact chat rendering:
    - Reuses formatMessage() for message blocks
    - Compact header: "▸ you: ..." and "▸ assistant: ..."
    - Spinner when streaming
    - "✓ done" indicator when complete
    - Border style: dimmer than main chat

Methods for sub-agent event routing:
  AppendEvent(event StreamEvent)
    - Same logic as handleStreamEvent() but for this window's messages
  IsDone() bool
  IsStreaming() bool
```

### `lain/notification.go` — Floating Notifications

```
Notification struct:
  ID        string
  Text      string
  ExpiresAt time.Time

NotificationManager struct:
  notifications []Notification
  nextID       int

Methods:
  Add(text, duration) → returns notification ID
  Expire() → remove expired notifications
  Render(width) → floating box at top-right, uses ANSI positioning
    - Rounded border, compact (1-2 lines)
    - Dim yellow style, auto-dismisses
  HasActive() bool

Messages:
  notificationExpireMsg struct { ids []string }
  notificationTick() tea.Cmd → fires every second
```

## Modified Files

### `lain/config.go`

Add to `LainConfig`:

```go
SubAgent SubAgentConfig `yaml:"sub_agent"`
```

New struct:

```go
type SubAgentConfig struct {
    Profile string `yaml:"profile"`   // profile name for sub-agents, "" = current
    AutoFix bool   `yaml:"auto_fix"`  // skip confirmation prompt
}
```

No defaults needed — empty profile means "use current", auto_fix defaults to false (Go zero value).

### `lain/plugin.go`

Add to `PluginLoader`:

```go
errCh   chan PluginError
loaded  map[string]bool  // tracks successful loads
```

New struct and methods:

```go
type PluginError struct {
    PluginName string
    Error      string
    Source     string     // "load", "exec", "render", "callback", "command", "keybind", "watcher"
    Timestamp  time.Time
}

func (pl *PluginLoader) Errors() <-chan PluginError
func (pl *PluginLoader) IsLoaded(name string) bool
```

Modify `loadPluginLocked()`:
- Set `loaded[name] = false` on error, `loaded[name] = true` on success
- Send `PluginError` to `errCh` at each error site (file read, Lua exec)

Modify `NewPluginLoader()`:
- Initialize `errCh: make(chan PluginError, 16)`, `loaded: make(map[string]bool)`

### `lain/plugin_api.go`

Add field and setter:

```go
errCh chan<- PluginError

func (api *PluginAPI) SetErrorChannel(ch chan<- PluginError)
```

Send errors at all error sites (keeping existing `slog.Error()` calls):
- `pluginWindow.View()` render errors → send to `errCh` with Source "render"
- Command callback errors → send to `errCh` with Source "command"
- Keybind callback errors → send to `errCh` with Source "keybind"
- Message callback errors → send to `errCh` with Source "callback"
- Window registration missing ID → send to `errCh` with Source "register"

### `lain/tui.go`

Add to `model` struct:

```go
subAgentMgr    *SubAgentManager
notifications  *NotificationManager
pendingFixErr  *PluginError    // non-nil when awaiting user confirmation
fixAgentID     string          // "fix-plugins" or ""
```

New message types:

```go
type pluginErrorMsg struct { err PluginError }
type subAgentEventMsg struct { agentID string; event StreamEvent }
type notificationExpireMsg struct { ids []string }
```

#### In `model.Update()`:

Add case `pluginErrorMsg`:

```go
case pluginErrorMsg:
    if m.profile.Config.SubAgent.AutoFix {
        m.spawnFixAgent(msg.err)
    } else {
        m.pendingFixErr = &msg.err
        m.mode = normalMode
        m.textarea.Blur()
    }
    return m, waitForPluginError(m.pluginLoader.Errors())
```

Add case `subAgentEventMsg`:

```go
case subAgentEventMsg:
    agent := m.subAgentMgr.agents[msg.agentID]
    if agent != nil && agent.window != nil {
        agent.window.AppendEvent(msg.event)
        if msg.event.Type == "done" || msg.event.Type == "error" {
            agent.window.streaming = false
            agent.window.done = true
        }
    }
    if msg.event.Type == "done" || msg.event.Type == "error" || msg.event.Type == "cancelled" {
        if agent != nil && agent.streamCh != nil {
            return m, waitForSubAgentEvent(msg.agentID, agent.streamCh)
        }
        return m, nil
    }
    return m, waitForSubAgentEvent(msg.agentID, agent.streamCh)
```

Add notification handling:

```go
case notificationExpireMsg:
    m.notifications.Expire()
    if m.notifications.HasActive() {
        return m, notificationTick()
    }
    return m, nil
```

#### In `handleNormalKey()`:

Add before existing switch, when `pendingFixErr != nil`:

```go
if m.pendingFixErr != nil {
    switch key {
    case "f", "F":
        err := *m.pendingFixErr
        m.pendingFixErr = nil
        m.spawnFixAgent(err)
        m.mode = insertMode
        m.textarea.Focus()
        return m, nil
    case "esc", "i":
        m.pendingFixErr = nil
        m.mode = insertMode
        m.textarea.Focus()
        m.statusMsg = "Plugin error dismissed"
        m.refreshView()
        return m, m.clearStatus()
    }
    return m, nil  // block all other normal-mode keys while prompt is active
}
```

#### In `model.View()` — status bar modification:

When `pendingFixErr != nil`:

```go
if m.pendingFixErr != nil {
    truncErr := m.pendingFixErr.Error
    if len(truncErr) > 60 { truncErr = truncErr[:60] + "..." }
    statusBar = errorStyle.Render(fmt.Sprintf(
        "⚠ plugin %q: %s — F:fix  Esc:dismiss",
        m.pendingFixErr.PluginName, truncErr))
}
```

After the main layout, render notifications:

```go
result := lipgloss.JoinVertical(lipgloss.Left, parts...)
if m.notifications.HasActive() {
    result = m.notifications.Render(m.width) + result
}
return result
```

#### In `pluginEventMsg` handler — detect successful reload for fix agent:

```go
case pluginEventMsg:
    switch msg.action {
    case "reload":
        m.pluginLoader.Load(msg.filename)
        name := strings.TrimSuffix(msg.filename, filepath.Ext(msg.filename))
        if m.pluginLoader.IsLoaded(name) && m.subAgentMgr.HasActive(m.fixAgentID) {
            m.subAgentMgr.EnqueueMessage(m.fixAgentID,
                fmt.Sprintf("Plugin %q reloaded successfully after your fix.", name))
            m.notifications.Add(fmt.Sprintf("✓ Plugin %q fixed and reloaded", name), 5*time.Second)
        }
    case "remove":
        m.pluginLoader.Unload(msg.filename)
    }
    return m, waitForPluginEvent(m.pluginLoader.Events())
```

#### New helper functions:

```go
func (m *model) spawnFixAgent(pluginErr PluginError) {
    if m.subAgentMgr.HasActive(m.fixAgentID) {
        m.subAgentMgr.EnqueueMessage(m.fixAgentID,
            fmt.Sprintf("New plugin error:\nPlugin: %s\nError: %s\nSource: %s",
                pluginErr.PluginName, pluginErr.Error, pluginErr.Source))
    } else {
        profileName := m.profile.Config.SubAgent.Profile
        if profileName == "" {
            profileName = m.profileName
        }
        agent, err := m.subAgentMgr.Spawn(SubAgentOpts{
            ID:           "fix-plugins",
            Title:        "Plugin Fix Agent",
            ProfileName:  profileName,
            SystemPrompt: buildFixAgentPrompt(pluginErr),
            InitialMsg:   fmt.Sprintf("Plugin %q failed with error: %s\nRead the file and fix it.",
                            pluginErr.PluginName, pluginErr.Error),
        })
        if err != nil {
            m.statusMsg = "Failed to spawn fix agent: " + err.Error()
            m.refreshView()
            return
        }
        m.fixAgentID = agent.id
        m.wm.SetFocused("chat")
    }
    m.notifications.Add(
        fmt.Sprintf("Fix agent %s for plugin %q",
            actionLabel, pluginErr.PluginName),
        5*time.Second)
}

func buildFixAgentPrompt(err PluginError) string {
    return `You are a plugin debugger for the lain TUI application.

You fix Lua plugin errors. When you receive a plugin error:
1. Read the plugin source file from ~/.config/lain/plugins/
2. Identify the issue
3. Edit the file to fix it
4. The plugin will auto-reload after you save — you will be notified of the result

If you receive multiple errors, fix them one at a time.

Available tools: run_command (use to cat, sed, or rewrite files).
Plugin directory: ~/.config/lain/plugins/
Plugin extension: .lua
Available Lua libraries: string, table, math, coroutine (no os, io, debug, package)
Plugin API: lain.window, lain.chat, lain.session, lain.state, lain.log, lain.command, lain.keybind`
}

func waitForPluginError(ch <-chan PluginError) tea.Cmd
func waitForSubAgentEvent(agentID string, ch <-chan StreamEvent) tea.Cmd
```

### `lain/wm.go`

Add helper (if not already present):

```go
func (wm *WindowManager) WindowExists(id string) bool
```

## Documentation Updates

### `AGENTS.md` Changes

Update the lain "Key modules" table to add:

```
| `sub_agent.go` | Sub-agent spawner: manages independent LLMClient + ToolRegistry + context lifecycle |
| `agent_window.go` | `agentWindow` type: mini-chat Window implementation for sub-agents |
| `notification.go` | Floating notification overlay: auto-dismissing, non-modal |
```

Add new section after the Plugin system section:

```
### Sub-Agent System

Sub-agents are independent LLM clients running in their own tiled windows. They share the same
WindowManager but have isolated conversation history, tools, and cancellation.

- Spawned via `SubAgentManager.Spawn()` with configurable profile and system prompt
- Each gets its own `LLMClient`, `ToolRegistry`, and `MCPManager`
- Events routed via `subAgentEventMsg` to the correct `agentWindow`
- Window stays open after completion for user review

### Plugin Error Recovery

When a plugin fails to load or execute:
1. Error is captured via `PluginLoader.ErrCh` (not just logged)
2. User is prompted (normal mode: F to fix, Esc to dismiss) unless `auto_fix` is enabled
3. Fix agent spawned in a tiled window with the error context and plugin source
4. Agent reads/edits the Lua file, hot-reload picks up changes
5. On successful reload, agent is notified and a success notification is shown

Configuration in profile's `config.yaml`:
```yaml
sub_agent:
  profile: "default"  # profile for sub-agents, empty = current profile
  auto_fix: false     # if true, skip user confirmation before spawning fix agent
```
```

### `docs/lain.md` Changes

Add new sections after "Window Management":

```
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
```

## Implementation Order

1. `lain/config.go` — Add `SubAgentConfig` struct
2. `lain/plugin.go` — Add `PluginError`, `errCh`, `loaded` tracking
3. `lain/plugin_api.go` — Add `errCh`, send errors at all sites
4. `lain/notification.go` — Notification system (independent, testable)
5. `lain/agent_window.go` — Agent window (depends on existing chat rendering)
6. `lain/sub_agent.go` — Sub-agent manager (depends on agent_window, config)
7. `lain/tui.go` — Wire everything together (depends on all above)
8. `lain/wm.go` — Add `WindowExists()` helper
9. `AGENTS.md` — Update architecture docs
10. `docs/lain.md` — Update user docs
11. Running container AGENTS.md — Copy updated AGENTS.md into container

## Key Design Decisions

- **Normal mode prompt** (F/Esc) for confirmation — non-interrupting, uses existing mode system
- **Single fix agent** for all plugin errors — queues errors sequentially, cleaner UI
- **Separate MCPManager per sub-agent** — avoids shared state issues with MCP server processes
- **Tiled window** — gives fix agent room to work, user can see progress
- **All tools available** — fix agent can use run_command, todo, MCP tools
- **Configurable profile** — can use cheaper/faster model for sub-agents, defaults to current profile
- **Success detection via IsLoaded()** — after hot-reload, check if plugin loaded without error to notify fix agent
- **Window stays open** — user can review what the agent did, close manually with `x` in normal mode

## Things to Watch For

- **Stream event routing**: Must correctly multiplex events from multiple `LLMClient.Chat()` channels
- **Window manager race conditions**: All window mutations happen on the Bubble Tea update goroutine (already the case)
- **MCP server lifecycle**: Each sub-agent gets its own MCPManager, which spawns its own server processes. Need to ensure cleanup on Stop()
- **Error channel buffering**: `errCh` is buffered at 16 — if errors come faster than processing, some may be dropped. Should be fine for plugin errors
- **Keybind cleanup bug**: In `ClearPlugin()` (plugin_api.go:388-390), ALL keybinds are deleted, not just the plugin's. Should fix this while in the area
- **Sub-agent context cancellation**: When the main TUI exits, all sub-agents must be stopped cleanly via `StopAll()`
