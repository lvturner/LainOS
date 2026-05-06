# Plugin Rendering & Data Sharing Plan

## Problem Statement

1. Plugins struggle to render — windows show blank content, state changes don't trigger re-renders, and failed executors never recover.
2. No bidirectional data sharing — plugins can't expose data to the agent, and the agent can't read plugin window content or send data to plugins.

## Part 1: Fix Plugin Rendering

### 1.1 Synchronous initial render on window creation

**File**: `plugin_api.go` — `setExecutor()` method

**Problem**: `cachedRender` starts as `""`. The first render only happens after an async Lua call completes via the tick pipeline (~16ms+), so newly created windows are blank.

**Fix**: In `setExecutor()`, after assigning the executor to all windows for a plugin, iterate over `api.pluginWindows` for that plugin and perform a synchronous `executor.call()` to `renderFn` with `(0, 0)` dimensions. Store the result in `cachedRender`. This populates the cache before the first `View()` call.

**Location**: `plugin_api.go:setExecutor()` — add a loop after the existing executor assignment loop.

### 1.2 Dirty-marking on `lain.state.set()`

**File**: `plugin_api.go` — `Inject()` method, inside `state.set` handler

**Problem**: When a plugin updates its state via `lain.state.set()`, the plugin window is never marked dirty, so the render function is never called with the updated state.

**Fix**: After `api.pluginState[pluginName][key] = luaToGo(val)`, iterate over `api.pluginWindows` and set `pw.renderDirty = true` for any window where `pw.pluginName == pluginName`.

**Location**: `plugin_api.go:365-370` — add a loop after the state assignment.

### 1.3 Add `lain.window.invalidate(id)` Lua API

**File**: `plugin_api.go` — `Inject()` method, inside the window table

**Problem**: Plugins have no way to trigger a re-render from their own logic (e.g., after a timer, file change, or external event).

**Fix**: Add a new function to the `windowTable`:

```lua
lain.window.invalidate("my-window-id")
```

Implementation: look up the plugin window by ID, set `renderDirty = true`.

**Location**: `plugin_api.go` — add after the `borders` function in the window table (around line 304).

### 1.4 Reset unhealthy flag on successful render

**File**: `plugin_api.go` — `triggerRender()` method

**Problem**: After 3 consecutive render failures, the executor is permanently marked `unhealthy = true` (line 146). There is no recovery path — even transient failures (timeout, temporary error) permanently kill the executor.

**Fix**: On successful render (in the goroutine, after the `call` returns without error), reset `w.executor.unhealthy = false` alongside the existing `w.executor.consecutiveFailures = 0`.

**Location**: `plugin_api.go:147-149` — add `w.executor.unhealthy = false` in the else branch.

### 1.5 Loading placeholder in `View()`

**File**: `plugin_api.go` — `pluginWindow.View()` method

**Problem**: When `cachedRender` is empty and a render hasn't completed yet, `View()` returns an empty string, making the window invisible.

**Fix**: After reading the cached render, if it's empty and `renderFn` is not nil, return a placeholder string:

```
[window-title: loading...]
```

**Location**: `plugin_api.go:96-112` — add a check after the `cached` variable is read.

## Part 2: Bidirectional Data Sharing

### 2.1 Agent tool: `plugin_query`

**File**: `tools.go` — new tool definition + handler

**Problem**: The LLM agent has no way to read plugin window content, state, or even know which plugins exist.

**Fix**: Add a new OpenAI tool `plugin_query` with parameters:

```json
{
  "action": "list" | "window" | "state",
  "plugin": "plugin-name",
  "window_id": "window-id"
}
```

Actions:
- `list` — returns all plugin names, their window IDs, and titles
- `window` — returns the cached rendered content of the specified window
- `state` — returns the current state dict for the named plugin

**Wiring**: Add a `pluginAPI *PluginAPI` field to `ToolRegistry`, set during TUI initialization in `tui.go:NewTUI()`. Add the tool to `builtinTools` after the plugin system is initialized.

**Location**: `tools.go` — add tool definition after `TodoTool`, add handler in `ExecuteTool()`. `tui.go:NewTUI()` — set `registry.pluginAPI = pluginAPI` and add tool to registry.

### 2.2 Agent tool: `plugin_send`

**File**: `tools.go` — new tool definition + handler

**Problem**: The agent has no way to send data or commands to plugins.

**Fix**: Add a new OpenAI tool `plugin_send` with parameters:

```json
{
  "plugin": "plugin-name",
  "data": "arbitrary string, typically JSON"
}
```

When called, it looks up `on_data` callbacks registered by the target plugin (see 2.3) and calls each one with the data string via the executor's `callAsync` method.

If no callbacks are registered, returns an error message: `"plugin X has no data handler registered"`.

**Location**: `tools.go` — add tool definition + handler. `tui.go:NewTUI()` — add tool to registry.

### 2.3 Lua API: `lain.agent.on_data(fn)`

**File**: `plugin_api.go` — `Inject()` method

**Problem**: Plugins have no way to receive data from the agent.

**Fix**: Add a new `agent` sub-table to `lain` (or extend the existing one if it exists — currently `lain.session` exists but `lain.agent` does not). Add `on_data` function:

```lua
lain.agent.on_data(function(data)
    -- data is a string from the agent
    local parsed = json.decode(data) -- if json library available
    lain.state.set("from_agent", data)
    lain.window.invalidate("my-window")
end)
```

Store callbacks in a new field `dataCallbacks map[string][]luaCallback` on `PluginAPI`.

Add a new method `FireDataCallbacks(pluginName, data string)` that finds callbacks for the plugin and calls them via the executor.

Also add `ClearPlugin()` cleanup for `dataCallbacks`.

**Location**: `plugin_api.go` — add `dataCallbacks` field to `PluginAPI` struct, add `agentTable` in `Inject()`, add `FireDataCallbacks()` method, update `ClearPlugin()`.

### 2.4 Lua API: `lain.window.get_content(id)`

**File**: `plugin_api.go` — `Inject()` method, inside the window table

**Problem**: Plugins can read chat messages via `lain.chat.get_messages()` but have no way to read the rendered content of other windows (including the chat window's formatted output).

**Fix**: Add a new function to the `windowTable`:

```lua
local content = lain.window.get_content("chat")
local content = lain.window.get_content("my-other-plugin")
```

Implementation:
- For the chat window (`id == "chat"`): return `chat.renderMessages()` (the formatted string). Must acquire `chatMu` read lock (see 2.5).
- For plugin windows: return `pw.cachedRender` (acquire `renderMu` read lock).
- For other windows: call `win.View(width, height, false)` with current window manager dimensions.
- For unknown windows: return `nil`.

**Location**: `plugin_api.go` — add after `lain.window.list()` in the window table.

### 2.5 Fix `lain.chat.get_messages()` thread safety

**File**: `window.go`, `plugin_api.go`

**Problem**: `lain.chat.get_messages()` is called from the Lua executor goroutine but reads `api.chat.messages` which is modified on the Bubble Tea goroutine. This is a data race.

**Fix**: Add `chatMu sync.RWMutex` to `chatWindow`. Wrap all accesses to `messages`, `current`, and `messageQueue` with the mutex:

**Writes** (Bubble Tea goroutine, in `tui.go`):
- `m.chat.messages = append(...)` — write lock
- `m.chat.current = &ChatMessage{...}` — write lock
- `m.chat.current.Blocks = append(...)` — write lock
- `m.chat.messageQueue = ...` — write lock

**Reads** (Lua executor goroutine, in `plugin_api.go`):
- `api.chat.messages` in `get_messages()` — read lock
- `api.chat.messages` in `get_content("chat")` — read lock

Also update `renderMessages()` to acquire the read lock, since it iterates messages.

**Location**: `window.go` — add `chatMu` field to `chatWindow`. Add `Lock()/RLock()/Unlock()/RUnlock()` calls at all message access points. `plugin_api.go` — add read lock in `get_messages()` and `get_content("chat")`.

## Part 3: Plugin Key Handling

### 3.1 Add `pendingKeys` to `pluginWindow`

**File**: `plugin_api.go` — `pluginWindow` struct

**Problem**: The `Update()` method is currently a no-op. Plugin windows never receive key events.

**Fix**: Add two new fields to `pluginWindow`:

```go
pendingKeys []string
keyMu       sync.Mutex
```

Update `Update()` to buffer key events:

```go
func (w *pluginWindow) Update(msg tea.Msg) (Window, tea.Cmd) {
    if w.updateFn == nil {
        return w, nil
    }
    if keyMsg, ok := msg.(tea.KeyMsg); ok {
        w.keyMu.Lock()
        w.pendingKeys = append(w.pendingKeys, keyMsg.String())
        w.keyMu.Unlock()
        w.renderDirty = true
    }
    return w, nil
}
```

**Location**: `plugin_api.go` — update `pluginWindow` struct and `Update()` method.

### 3.2 Route normal-mode key events to focused plugin window

**File**: `tui.go` — `handleNormalKey()`

**Problem**: In normal mode, key events are only handled by the hardcoded window management bindings. Plugin windows never receive keys.

**Fix**: At the top of `handleNormalKey()`, before the plugin keybind check and the switch statement, check if the focused window is a plugin window:

```go
if focusedID := m.wm.FocusedID(); focusedID != "" {
    if win := m.wm.Get(focusedID); win != nil {
        if pw, ok := win.(*pluginWindow); ok {
            if pw.updateFn != nil {
                _, cmd := pw.Update(msg)
                return m, cmd
            }
        }
    }
}
```

This routes ALL key events to the focused plugin window first. If the plugin has an `updateFn`, keys go to it. If not, they fall through to the existing normal mode bindings.

**Important**: Plugin keybinds registered via `lain.keybind.register()` should still work. Move the plugin keybind check AFTER the plugin window routing, so window-focused key handling takes priority. If the plugin window's update function doesn't handle the key, it still reaches the keybind and switch statement.

Actually, to avoid breaking existing keybinds, the plugin window should only consume keys that the plugin explicitly handles. Since `Update()` just buffers keys asynchronously, we can't know if the plugin "handled" the key. The simplest approach: always route keys to the plugin window AND let them fall through to keybinds/normal mode. The plugin gets all keys, and the normal mode bindings still work.

**Revised approach**: Route key events to the focused plugin window as a side effect (buffering), then continue with normal key handling:

```go
// In handleNormalKey, after fix error handling, before keybind check:
if focusedID := m.wm.FocusedID(); focusedID != "" && focusedID != "chat" {
    if win := m.wm.Get(focusedID); win != nil {
        if pw, ok := win.(*pluginWindow); ok && pw.updateFn != nil {
            pw.Update(msg) // buffers the key, ignores return
        }
    }
}
```

Then continue with the existing keybind and switch logic. This means both the plugin and normal mode bindings see the key.

**Location**: `tui.go:478-506` — add plugin window routing after the fix error handling block, before the keybind check.

### 3.3 Process pending keys before render

**File**: `plugin_api.go` — `triggerRender()` method

**Problem**: Key events are buffered but never passed to the Lua `updateFn`.

**Fix**: In the `triggerRender()` goroutine, before calling `renderFn`, drain `pendingKeys` and call `updateFn` for each key:

```go
go func() {
    // Drain pending keys
    w.keyMu.Lock()
    keys := w.pendingKeys
    w.pendingKeys = nil
    w.keyMu.Unlock()

    if len(keys) > 0 && w.updateFn != nil {
        for _, key := range keys {
            w.executor.call(w.updateFn,
                []lua.LValue{lua.LString(key)},
                0, w.api.callbackTimeout)
        }
    }

    // Then render
    result, err := w.executor.call(w.renderFn,
        []lua.LValue{lua.LNumber(width), lua.LNumber(height)},
        1, renderTimeout)
    // ... existing result handling ...
}()
```

This ensures all pending keys are processed on the executor goroutine before the render function runs, giving the render function access to the latest state.

**Location**: `plugin_api.go:114-152` — add key draining at the start of the goroutine in `triggerRender()`.

## File Change Summary

| File | Changes |
|---|---|
| `plugin_api.go` | Initial render in `setExecutor()`, dirty-marking in `state.set`, `invalidate()` API, `get_content()` API, `on_data()` API + `dataCallbacks`, `View()` placeholder, `Update()` key buffering + `pendingKeys`/`keyMu` fields, `triggerRender()` key processing + unhealthy reset, `FireDataCallbacks()`, `ClearPlugin()` cleanup |
| `tools.go` | `plugin_query` tool definition + handler, `plugin_send` tool definition + handler, `pluginAPI` field on `ToolRegistry` |
| `tui.go` | Route normal-mode keys to plugin windows, register new tools with registry, set `pluginAPI` on `ToolRegistry` |
| `window.go` | Add `chatMu sync.RWMutex` to `chatWindow`, lock in all message access points |
| `plugin.go` | No changes (executor infrastructure is sufficient) |
| `config.go` | No changes needed |

## Implementation Order

1. **Part 1.5** — Loading placeholder (trivial, immediate visual improvement)
2. **Part 1.4** — Unhealthy flag reset (trivial, fixes permanent death)
3. **Part 1.2** — Dirty-marking on state set (simple, fixes most render issues)
4. **Part 1.3** — `invalidate()` API (simple, new API surface)
5. **Part 1.1** — Synchronous initial render (moderate, eliminates blank-on-create)
6. **Part 2.5** — Thread safety for `get_messages()` (moderate, correctness fix)
7. **Part 2.1** — `plugin_query` agent tool (moderate, agent→plugin reading)
8. **Part 2.3** — `lain.agent.on_data()` Lua API (moderate, plugin callback registration)
9. **Part 2.2** — `plugin_send` agent tool (moderate, agent→plugin writing)
10. **Part 2.4** — `lain.window.get_content()` Lua API (moderate, plugin→plugin/agent reading)
11. **Part 3.1** — `pendingKeys` field + `Update()` method (simple, key buffering)
12. **Part 3.3** — Process keys before render (moderate, executor integration)
13. **Part 3.2** — Route normal-mode keys to plugin windows (simple, TUI routing)
