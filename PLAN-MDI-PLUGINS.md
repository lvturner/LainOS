# Lain MDI Window Manager + Lua Plugin System

## Overview

Transform lain's monolithic TUI into an MDI (Multiple Document Interface) with a tiling/floating window manager and a Lua plugin system that lets the agent dynamically extend its own UI.

## Current State

- Single `tui.go` (1019 lines) with fixed layout: banner, chat viewport, optional todo sidebar, status bar, textarea
- Bubble Tea (Elm architecture) + lipgloss for rendering
- No plugin system for UI extension
- MCP exists for tool plugins but not UI plugins

## Architecture

### Window Manager

All UI panels implement a common `Window` interface. A `WindowManager` owns a layout tree (tiling) and a floating layer (overlays). The Bubble Tea model delegates to the window manager for all rendering and key routing.

```
model
 └── WindowManager
      ├── LayoutTree (tiling — binary tree of Split/Leaf nodes)
      │    ├── Leaf(chatWindow)     ← permanent, cannot be closed
      │    └── Split(vertical, 0.8)
      │         ├── Leaf(todoWindow)
      │         └── Leaf(pluginWindow)
      └── FloatingWindows[]         ← z-ordered overlay layer
           └── FloatingWindow(sessionPicker)
```

### Mode System

| Mode | Trigger | Keys go to |
|---|---|---|
| Insert | Default, `i` from normal | textarea (chat input) |
| Normal | `Ctrl+W` prefix | window manager commands |
| Picker | `Ctrl+S` | session picker window |

Normal mode bindings (after `Ctrl+W`, single-key commands like tmux):

| Key | Action |
|---|---|
| `h/j/k/l` | Focus left/down/up/right |
| `H` | Split horizontal |
| `V` | Split vertical |
| `x` | Close window (not chat) |
| `f` | Toggle float/tile |
| `+`/`-` | Resize split ratio |
| `1-9` | Focus window by index |
| `?` | Help overlay |
| `Esc` / `i` | Return to insert mode |

### Plugin Language: Lua (gopher-lua)

- `github.com/yuin/gopher-lua` — pure Go Lua 5.1 VM, no CGO, ~2MB binary impact
- Each plugin gets its own `*lua.LState` for isolation
- Sandboxed: `os`, `io`, `debug`, `package` standard libs removed; controlled APIs injected
- Plugins loaded from `~/.config/lain/plugins/*.lua`
- fsnotify watches for changes — hot-reload without restart
- Lua 5.1 limitation is acceptable for small UI plugins

### Plugin API

```lua
lain = {
  window = {
    register(opts),   -- opts: { id, title, render=fn, update=fn, float=bool }
    close(id),
    float(id, bool),
    focus(id),
    list(),
  },

  chat = {
    send(text),
    on_message(cb),       -- cb(role, text)
    on_stream_start(cb),
    on_stream_end(cb),
    get_messages(),
    append(text),
  },

  session = {
    get_id(),
    get_title(),
    get_profile(),
    get_model(),
    get_context_percent(),
    get_messages(),
  },

  command = {
    register(name, cb),   -- register /name slash command
  },

  keybind = {
    register(key, cb),    -- register normal-mode keybinding
  },

  state = {
    set(key, value),
    get(key),
  },

  log = {
    info(msg),
    warn(msg),
    error(msg),
  },
}
```

### Window Registration Example

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
    table.insert(lines, os.date("%H:%M:%S") .. " " .. text)
    lain.state.set("lines", lines)
  end
end)
```

### Agent Self-Extension

The agent (LLM) can write `.lua` files to `~/.config/lain/plugins/` using `run_command`. The fsnotify watcher picks up changes and loads them automatically. This creates a self-modification loop: agent identifies a need for a panel, writes the plugin, window appears immediately.

---

## New File Structure

```
lain/
  wm.go           -- Window manager: layout tree, focus cycling, composite rendering
  window.go       -- Window interface + built-in implementations (chatWindow, todoWindow)
  tile.go         -- Tiling engine: split/unsplit, resize, tree manipulation
  float.go        -- Floating layer: position, z-order, drag/resize
  plugin.go       -- Lua plugin loader, sandboxing, hot-reload via fsnotify
  plugin_api.go   -- Lua API surface (lain.* tables)
  tui.go          -- Refactor: model uses WindowManager, mode system, key routing
  llm.go          -- Unchanged
  tools.go        -- Unchanged
  mcp.go          -- Unchanged
  compaction.go   -- Unchanged
  config.go       -- Unchanged
  profile.go      -- Unchanged
  session.go      -- Unchanged
  session_picker.go -- Refactor: becomes a floating window
  question.go     -- Refactor: becomes a floating window
  oneshot.go      -- Unchanged
  todo.go         -- Unchanged
  markdown.go     -- Unchanged
  banner.go       -- Unchanged
```

---

## Implementation Phases

### Phase 1: Window Interface + Extract Windows

**Files**: `window.go`, `tui.go`

1. Define `Window` interface:

```go
type Window interface {
    ID() string
    Title() string
    Update(tea.Msg) (Window, tea.Cmd)
    View(width, height int, focused bool) string
    SetSize(width, height int)
}
```

2. Extract `chatWindow` from `tui.go`:
   - Moves: viewport, messages, current message, streaming state, rendering logic
   - The input bar (textarea + status) is part of the chat window, rendered below the viewport
   - Chat window is always present, cannot be closed

3. Extract `todoWindow` from `tui.go`:
   - Moves: `renderTodoSidebar()` logic, todo state rendering
   - Becomes a standalone Window that can be tiled/floated

4. Refactor `model` struct to hold `windows map[string]Window` instead of direct viewport/textarea fields

**Verification**: `cd lain && go build ./...`

### Phase 2: Window Manager + Layout Tree

**Files**: `wm.go`, `tile.go`

1. Define layout tree types:

```go
type LayoutNode struct {
    Split     *SplitNode
    Leaf      *LeafNode
}

type SplitNode struct {
    Direction SplitDirection  // Horizontal | Vertical
    Ratio     float64         // 0.0–1.0, size of Left child
    Left      *LayoutNode
    Right     *LayoutNode
}

type LeafNode struct {
    WindowID string
}
```

2. Implement `WindowManager`:

```go
type WindowManager struct {
    root         *LayoutNode
    windows      map[string]Window
    focusOrder   []string        // ordered list of window IDs
    focused      string          // currently focused window ID
    floating     []*FloatingWindow
    width, height int
}
```

3. Implement tree operations in `tile.go`:
   - `Split(windowID, direction)` — replace leaf with split containing old window + new slot
   - `Unsplit(windowID)` — remove leaf, replace parent split with sibling
   - `Resize(windowID, delta)` — adjust parent split ratio
   - `Swap(id1, id2)` — swap windows in the tree
   - `FocusNext()` / `FocusPrev()` — cycle focus
   - `FocusDirection(dir)` — focus adjacent window (h/j/k/l)

4. Implement composite `View()` that walks the tree, computes `(w, h)` for each leaf, calls `Window.View(w, h, focused)`, and joins with lipgloss

**Verification**: `cd lain && go build ./...`

### Phase 3: Refactor tui.go to Use WindowManager

**Files**: `tui.go`, `wm.go`

1. Replace monolithic `View()` with delegation to `WindowManager.View()`
2. Route `Update()` messages to the focused window
3. Banner renders above the window manager area
4. Initial layout: single chatWindow filling the space
5. Todo window opens as a vertical split when todo items exist

**Verification**: `cd lain && go build ./...` then manual TUI testing

### Phase 4: Floating Windows

**Files**: `float.go`, `wm.go`

1. Define:

```go
type FloatingWindow struct {
    Window
    X, Y, W, H int
    ZOrder     int
}
```

2. Floating windows render on top of the tiled output using ANSI cursor positioning
3. Z-order sorting determines draw order
4. Operations: `Float(id)` toggles a tiled window to floating, `Unfloat(id)` returns it
5. Move: `Alt+Arrows` when floating window focused
6. Resize: `Alt+Shift+Arrows` when floating window focused

4. Refactor `sessionPicker` and `question` modals into floating windows:
   - Session picker: centered floating overlay, dismissed on select/cancel
   - Question modal: floating overlay over chat window

**Verification**: `cd lain && go build ./...`

**Risk**: ANSI escape collision in floating windows. If lipgloss positioning is insufficient, may need a virtual screen buffer compositor. Prototype this early.

### Phase 5: Mode System + Keybindings

**Files**: `tui.go`

1. Add mode state to model: `mode insertMode | normalMode`
2. In `Update()`, route keys based on mode:
   - Insert mode: all keys go to textarea (current behavior)
   - Normal mode: keys go to window manager commands
   - `Ctrl+W` switches from insert to normal
   - `Esc`/`i` switches from normal to insert
3. Plugin keybindings are checked during normal mode key routing

**Verification**: `cd lain && go build ./...`

### Phase 6: Lua Plugin Infrastructure

**Files**: `plugin.go`

1. Add `github.com/yuin/gopher-lua` dependency
2. Implement `PluginLoader`:

```go
type PluginLoader struct {
    states   map[string]*lua.LState   // plugin name → Lua state
    api      *PluginAPI
    watcher  *fsnotify.Watcher
    dir      string
}
```

3. Sandboxing per state:
   - Remove: `os`, `io`, `debug`, `package`, `require`
   - Inject: `lain` table with controlled API
   - Allow: `string`, `table`, `math`, `coroutine`

4. Loading:
   - Scan `~/.config/lain/plugins/*.lua` on startup
   - Each file gets its own `*lua.LState`
   - Run file as init script (registers windows, callbacks, etc.)

5. Hot-reload:
   - fsnotify watches the plugins directory
   - On write/create: destroy old LState, create new one, re-run init
   - On remove: destroy LState, unregister windows/callbacks
   - Debounce 500ms (same pattern as gateway config watcher)

6. Thread safety: all Lua execution on the Bubble Tea update goroutine. Background callbacks dispatched as `tea.Msg`.

**Verification**: `cd lain && go build ./...`

### Phase 7: Plugin API Implementation

**Files**: `plugin_api.go`

1. `PluginAPI` struct holds references to WindowManager, LLMClient, session state, etc.

2. Implement each `lain.*` table as Lua tables with Go closures:

   - `lain.window.*` — calls WindowManager methods
   - `lain.chat.*` — interacts with chatWindow, registers message callbacks
   - `lain.session.*` — reads from current session/profile
   - `lain.command.*` — registers slash commands in the command table
   - `lain.keybind.*` — registers normal-mode keybindings
   - `lain.state.*` — per-plugin key-value store (backed by JSON file in `~/.config/lain/plugins/state/<name>.json`)
   - `lain.log.*` — writes to slog

3. Lua→Go bridge for window `render` and `update` callbacks:
   - `render(width, height) → string` called during `View()`
   - `update(event) → nil` called during `Update()` for focused window

**Verification**: `cd lain && go build ./...`

### Phase 8: Slash Command Updates

**Files**: `tui.go`

Add new slash commands:

| Command | Description |
|---|---|
| `/plugins` | List loaded plugins |
| `/plugins reload [name]` | Reload a plugin or all |
| `/windows` | List open windows |
| `/float <id>` | Toggle float/tile for a window |

**Verification**: `cd lain && go build ./...`

---

## Dependency Additions

```
github.com/yuin/gopher-lua    -- Lua 5.1 VM, pure Go, no CGO
```

No other new dependencies. fsnotify is already a transitive dependency.

---

## Risks and Mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| ANSI escape collision in floating windows | Floating windows may break rendering | Prototype early; may need virtual screen buffer compositor |
| gopher-lua Lua 5.1 only | Some Lua 5.3+ features unavailable | Acceptable for small plugins; document clearly |
| Thread safety | Lua states not thread-safe | All execution on Bubble Tea update goroutine |
| Plugin crashes | Bad Lua could panic | pcall wrapping for all plugin callbacks; recover and log |
| Binary size | gopher-lua adds ~2MB | Acceptable for a CLI tool |
| Agent writes bad plugins | Agent may produce broken Lua | Log errors clearly; don't crash TUI on plugin failure |

---

## Documentation Updates

### 1. `AGENTS.md` (project root)

**Tech Stack** — Add:
- **Plugin language**: Lua 5.1 via gopher-lua
- **TUI framework**: Bubble Tea + lipgloss (lain)

**New section: Lain Application** — Add after the gateway section:

- Source lives in `lain/`, flat `main` package
- Updated module table with new files (wm.go, window.go, tile.go, float.go, plugin.go, plugin_api.go)
- Dependency: `github.com/yuin/gopher-lua`

**Testing** — Add alongside gateway testing:
```bash
cd lain && go build ./...
```

**Code Style** — Add Lua conventions:
- No comments unless asked
- Use `lain.*` API only (no os/io access)
- Plugins run in isolated Lua states
- Keep render functions fast (called every frame)

**File conventions** — Add:
- Plugin files are `.lua` in `~/.config/lain/plugins/`
- Plugin state files are `.json` in `~/.config/lain/plugins/state/`

### 2. `config/agents.example.md` (LLM system prompt)

**New section: `## Lain TUI`**
- MDI layout: tiled and floating windows
- Chat window is permanent
- Ctrl+W for window management mode
- The agent can open/close/float windows via plugins

**New section: `## Plugin System`**
- Plugins live at `~/.config/lain/plugins/*.lua`
- Hot-reloaded on file change (no restart)
- Each plugin is an isolated Lua 5.1 sandbox
- Plugin template with `lain.window.register()`

**New section: `## Plugin API Reference`**
- Full reference for all `lain.*` tables
- Examples for common patterns (log viewer, file browser, process monitor)

**New section: `## Self-Extension`**
- Agent can write `.lua` files to create new windows
- Use `run_command` to write the file
- Window appears immediately
- Examples: "Create a diff viewer panel", "Add a file tree browser"

### 3. `docs/lain.md` (user-facing documentation)

**Interactive Mode** — Rewrite for MDI:
- Describe tiled windows, floating windows, focus cycling
- Mode system explanation (insert vs normal)

**New section: `## Window Management`**
- Keybindings table (insert all Ctrl+W commands)
- Explain that Ctrl+W is a tmux-style prefix
- Window list ordering
- Float/tile toggle behavior

**New section: `## Plugin System`**
- Plugin location: `~/.config/lain/plugins/*.lua`
- Hot-reload behavior
- Sandboxed Lua 5.1 environment
- Chat window is always present
- Plugin API reference (user-facing tone)
- Example plugins with full code:
  - Log viewer
  - File browser
  - Process monitor
  - Markdown previewer

**Slash Commands** — Add new commands:
- `/plugins` — list loaded plugins
- `/plugins reload [name]` — reload plugin(s)
- `/windows` — list open windows
- `/float <id>` — toggle float/tile

### 4. `README.md` (project root)

**What's included** — Update lain description:
> **[lain](docs/lain.md)** — Interactive LLM CLI with MCP tool support, MDI window management, and a Lua plugin system for self-extension

**Lain CLI** — Add mention of MDI and plugins:
> The `lain` command starts an interactive LLM TUI with a multi-window MDI layout (tiled and floating panels) and a Lua plugin system. The agent can write its own plugins to add specialized panels on the fly.

**Project Structure** — Add:
```
plugins/          → Example Lua plugins (bind-mounted into container)
```

### 5. `docs/README.md`

**Contents** — Update lain.md description:
> **[lain.md](lain.md)** — The lain LLM chat CLI: profiles, config, MCP servers, tools, window management, Lua plugins, context compaction, nix packages

**Project Structure** — Add `plugins/` entry

---

## Documentation Implementation Order

| Step | File | When |
|---|---|---|
| 1 | `AGENTS.md` | After Phase 1 (window interface defined, file structure finalized) |
| 2 | `config/agents.example.md` | After Phase 7 (plugin API implemented and tested) |
| 3 | `docs/lain.md` | After Phase 8 (all features implemented, keybindings finalized) |
| 4 | `README.md` + `docs/README.md` | Last, after everything is stable |
