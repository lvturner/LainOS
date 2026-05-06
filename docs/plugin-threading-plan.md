# Plugin Threading Plan: Goroutine-per-Plugin with Cached Renders

## Problem

Lua plugin code runs synchronously on the Bubble Tea update goroutine (the main TUI
thread). These call sites all block the TUI:

1. **`pluginWindow.View()`** (`plugin_api.go:84`) — render functions, called every frame
2. **`FireMessageCallbacks()`** (`plugin_api.go:422-438`) — on every chat message
3. **Command/keybind callbacks** (`plugin_api.go:332,348`) — on user input
4. **Plugin loading** (`plugin.go:161`) — `DoString` on file change

A slow or infinite-looping Lua script freezes the entire TUI.

## Approach

Each plugin gets its own **Lua execution goroutine** (`pluginExecutor`). All Lua calls
are dispatched to this goroutine via channels. The TUI main thread never blocks on Lua —
it reads cached render results instead.

Context-based timeouts serve as a backstop to prevent goroutine leaks from infinite loops.

## New Types

### `pluginExecutor` (in `plugin.go`)

Each plugin gets an executor that owns the `LState` and runs a goroutine processing
calls sequentially:

```go
type pluginCall struct {
    fn      *lua.LFunction
    args    []lua.LValue
    ret     chan<- pluginResult
    timeout time.Duration
}

type pluginResult struct {
    values []lua.LValue
    err    error
}

type pluginExecutor struct {
    L        *lua.LState
    callCh   chan pluginCall
    stopCh   chan struct{}
    doneCh   chan struct{}
}
```

Methods:

- `newPluginExecutor(L *lua.LState) *pluginExecutor`
- `start()` — launches the goroutine loop
- `stop()` — closes `stopCh`, waits on `doneCh`, calls `L.Close()`
- `call(fn, args, timeout) (pluginResult, error)` — synchronous call with timeout
- `callAsync(fn, args, timeout)` — fire-and-forget, errors to errCh
- `run()` — the goroutine: reads from `callCh`, sets `L.SetContext` with timeout per call,
  executes, writes result to per-call `ret` channel

The goroutine loop:

```
for {
    select {
    case <-stopCh: return
    case c := <-callCh:
        ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
        L.SetContext(ctx)
        err := L.CallByParam(lua.P{Fn: c.fn, NRet: ..., Protect: true}, c.args...)
        cancel()
        // collect return values from stack
        c.ret <- pluginResult{values, err}
    }
}
```

### Render tick messages (in `tui.go`)

```go
type pluginRenderMsg struct {
    windowID string
    content  string
    err      error
}

type pluginRenderTickMsg time.Time
```

## Files to Modify

### `lain/plugin.go`

- Add `pluginExecutor` type and all methods listed above
- Add `map[string]*pluginExecutor` to `PluginLoader` (keyed by plugin name)
- Modify `newSandboxedState()` — no changes needed (context set per-call, not at creation)
- Modify `loadPluginLocked()`:
  - Create executor for the plugin
  - Run `DoString` via executor `call` with `PluginLoadTimeout`
  - Store executor in the map
- Modify `unloadPluginLocked()`:
  - Call `executor.stop()` to clean up goroutine
  - Remove from map

### `lain/plugin_api.go`

#### `pluginWindow` struct additions:

```go
type pluginWindow struct {
    // ... existing fields ...
    executor      *pluginExecutor
    cachedRender  string
    renderMu      sync.RWMutex
    lastWidth     int
    lastHeight    int
    renderDirty   bool
    renderTimeout time.Duration
}
```

#### `View()` rewrite:

```go
func (w *pluginWindow) View(width, height int, focused bool) string {
    if w.renderFn == nil {
        return fmt.Sprintf("[%s: no render function]", w.title)
    }

    w.renderMu.RLock()
    cached := w.cachedRender
    w.renderMu.RUnlock()

    if width != w.lastWidth || height != w.lastHeight {
        w.lastWidth = width
        w.lastHeight = height
        w.renderDirty = true
    }

    return cached
}
```

`View()` never calls Lua. It returns the cached string and marks dirty if dimensions
changed. The 16ms tick picks up dirty renders.

#### New `triggerRender()` method:

```go
func (w *pluginWindow) triggerRender() {
    if w.executor == nil || w.renderFn == nil {
        return
    }
    w.renderDirty = false
    go func() {
        result, err := w.executor.call(w.renderFn,
            []lua.LValue{lua.LNumber(w.lastWidth), lua.LNumber(w.lastHeight)},
            w.renderTimeout)
        var content string
        if err != nil {
            content = fmt.Sprintf("[render error: %s]", err.Error())
        } else if len(result.values) > 0 {
            if str, ok := result.values[0].(lua.LString); ok {
                content = string(str)
            } else {
                content = fmt.Sprintf("[%v]", result.values[0])
            }
        }
        w.renderMu.Lock()
        w.cachedRender = content
        w.renderMu.Unlock()
    }()
}
```

Wait — this needs to be delivered to the TUI goroutine, not just updated in background.
Revised: the render result goes through a channel that a `tea.Cmd` reads:

```go
func (w *pluginWindow) triggerRender(resultCh chan<- pluginRenderMsg) {
    if w.executor == nil || w.renderFn == nil {
        return
    }
    w.renderDirty = false
    width, height := w.lastWidth, w.lastHeight
    windowID := w.id
    pluginName := w.pluginName

    go func() {
        result, err := w.executor.call(w.renderFn,
            []lua.LValue{lua.LNumber(width), lua.LNumber(height)},
            w.renderTimeout)
        msg := pluginRenderMsg{windowID: windowID}
        if err != nil {
            msg.err = err
            msg.content = fmt.Sprintf("[render error: %s]", err.Error())
        } else if len(result.values) > 0 {
            if str, ok := result.values[0].(lua.LString); ok {
                msg.content = string(str)
            }
        }
        resultCh <- msg
    }()
}
```

A `tea.Cmd` reads from `resultCh` and delivers `pluginRenderMsg` to `Update()`.

#### Modify `FireMessageCallbacks()`:

```go
func (api *PluginAPI) FireMessageCallbacks(role, text string) {
    for plugin, callbacks := range api.messageCallbacks {
        executor := api.getExecutor(plugin)
        if executor == nil {
            continue
        }
        for _, cb := range callbacks {
            fn := cb.Fn
            executor.callAsync(fn,
                []lua.LValue{lua.LString(role), lua.LString(text)},
                api.callbackTimeout)
        }
    }
}
```

Fire-and-forget. Errors from async calls are sent to `errCh` by the executor.

#### Modify command/keybind closures:

The closures registered in `Inject()` should dispatch to the executor:

```go
api.commandCallbacks[name] = func(arg string) {
    executor := api.getExecutor(pluginName)
    if executor == nil {
        return
    }
    executor.callAsync(fn,
        []lua.LValue{lua.LString(arg)},
        api.callbackTimeout)
}
```

#### Modify `Inject()`:

Store executor reference in `pluginWindow` so it can dispatch renders.

#### `pluginWindow.Update()`:

Currently a no-op. Could dispatch update events to the executor for plugins that handle
input in their windows. For now stays a no-op — future work.

### `lain/wm.go`

#### Make WindowManager thread-safe:

- Change any existing `mu sync.Mutex` to `mu sync.RWMutex`
- If no mutex exists, add one
- Public mutation methods acquire **write lock**:
  - `Add()`, `Remove()`, `AddWithSplit()`, `AddFloating()`, `RemoveFloating()`
  - `SetFocused()`, `SetSize()`, `ToggleZoom()`, `ToggleBorders()`
  - `Equalize()`, `ResizeFocused()`, `MoveFocused()`, `MoveFloating()`
- Render methods acquire **read lock**:
  - `View()`, `renderNode()`, `FloatingView()`
- `Get()`, `ListWindows()`, `FocusedID()`, `HasFloating()` acquire **read lock**

This allows executor goroutines calling `lain.window.register` → `wm.AddFloating()` etc.
to do so safely.

### `lain/tui.go`

#### Handle `pluginRenderMsg` in `Update()`:

```go
case pluginRenderMsg:
    pw := m.pluginAPI.pluginWindows[msg.windowID]
    if pw != nil {
        pw.renderMu.Lock()
        pw.cachedRender = msg.content
        pw.renderMu.Unlock()
    }
    if msg.err != nil {
        pw.api.sendPluginError(pw.pluginName, msg.err.Error(), "render")
    }
    return m, nil
```

#### Handle `pluginRenderTickMsg` in `Update()`:

```go
case pluginRenderTickMsg:
    for _, pw := range m.pluginAPI.pluginWindows {
        if pw.renderDirty && pw.executor != nil {
            pw.triggerRender(m.pluginAPI.renderResultCh)
        }
    }
    return m, tea.Batch(
        waitForPluginRenderResult(m.pluginAPI.renderResultCh),
        pluginRenderTick(),
    )
```

Where:
- `pluginRenderTick()` returns a `tea.Tick(16ms, ...)` that produces `pluginRenderTickMsg`
- `waitForPluginRenderResult(ch)` is a `tea.Cmd` that blocks on the render result channel

#### Add to `Init()`:

```go
cmds = append(cmds, pluginRenderTick())
cmds = append(cmds, waitForPluginRenderResult(m.pluginAPI.renderResultCh))
```

### `lain/config.go`

Add to the config struct:

```go
PluginRenderTimeout    time.Duration `yaml:"plugin_render_timeout"`
PluginCallbackTimeout  time.Duration `yaml:"plugin_callback_timeout"`
PluginLoadTimeout      time.Duration `yaml:"plugin_load_timeout"`
```

Defaults (set in config loading):

| Field | Default | Purpose |
|---|---|---|
| `plugin_render_timeout` | 50ms | Max time for a single render call |
| `plugin_callback_timeout` | 5s | Max time for message/command/keybind callbacks |
| `plugin_load_timeout` | 10s | Max time for initial plugin `DoString` |

### `PluginAPI` additions

```go
type PluginAPI struct {
    // ... existing fields ...
    executors       map[string]*pluginExecutor
    renderResultCh  chan pluginRenderMsg
    renderTimeout   time.Duration
    callbackTimeout time.Duration
}
```

New method `getExecutor(pluginName string) *pluginExecutor`.

New method `SetTimeouts(render, callback time.Duration)`.

The `renderResultCh` is a buffered channel (capacity 32 or so) shared across all plugin
windows for delivering render results back to the TUI goroutine.

## Data Flow Diagrams

### Render (hot path, 60fps)

```
Bubble Tea Update()
  │
  ├─ pluginRenderTickMsg arrives (every 16ms)
  │   └─ for each pluginWindow:
  │       if renderDirty → triggerRender(renderResultCh)
  │                          │
  │                          └─ goroutine: executor.call(renderFn, args, timeout)
  │                              │
  │                              └─ executor goroutine: L.SetContext(ctx) + CallByParam
  │                                  │
  │                                  └─ writes pluginRenderMsg to renderResultCh
  │
  ├─ pluginRenderMsg arrives
  │   └─ update pluginWindow.cachedRender
  │
  └─ View() called by Bubble Tea
      └─ return cachedRender (RLock, read, RUnlock — never blocks)
```

### Message callback

```
FireMessageCallbacks("user", text)
  └─ for each plugin:
      executor.callAsync(callbackFn, [role, text], callbackTimeout)
        └─ executor goroutine: L.SetContext(ctx) + CallByParam
            └─ on error → errCh → pluginErrorMsg → fix agent
```

### Plugin load

```
loadPluginLocked(filename)
  └─ create executor, start goroutine
  └─ executor.call(loadFn, nil, loadTimeout)  [actually DoString, same mechanism]
      └─ on error → errCh, close executor
      └─ on success → store executor
```

### lain.window.register from Lua

```
executor goroutine executing Lua
  └─ calls lain.window.register (Go function)
      └─ acquires wm.mu (write lock)
      └─ wm.AddFloating(pw, x, y, w, h)
      └─ releases wm.mu
```

## Unhealthy Plugin Handling

If a plugin times out 3 times in a row on render:

1. Mark executor as `unhealthy`
2. Skip render dispatches (use last cached render permanently)
3. Send error to `errCh` → triggers fix agent
4. Fix agent edits plugin → hot-reload → new executor → healthy again

Add to `pluginExecutor`:

```go
type pluginExecutor struct {
    // ...
    consecutiveFailures int
    unhealthy           bool
}
```

On success, reset `consecutiveFailures` to 0. On timeout/error, increment. If >= 3, set
`unhealthy = true`.

## Concerns & Mitigations

| Concern | Mitigation |
|---|---|
| Stale renders | 16ms tick = ~60fps refresh, imperceptible |
| Race conditions | Only `cachedRender` shared between goroutines (RWMutex). All Lua state access single-threaded per executor. |
| Goroutine leaks | `pluginExecutor.stopCh` closes on unload. Context timeout kills infinite loops. `doneCh` confirms goroutine exited. |
| WindowManager thread safety | RWMutex on all WindowManager methods. Executors acquire lock for mutations. |
| `lain.*` API called from executor goroutine | These are fast Go functions. They acquire `wm.mu` write lock for mutations. Read-only accesses (like listing windows) use read lock. |
| Render channel backpressure | Buffer size 32. If full, drop oldest pending render (TUI will refresh on next tick anyway). |
| Multiple plugin windows rendering simultaneously | Each dispatches its own goroutine to its executor. Executors are independent. Results all flow through shared `renderResultCh`. |

## Alternatives Considered

1. **Context-only timeouts (no goroutines):** Simpler but doesn't fully solve the problem — Go functions called from Lua still block. Requires all Lua to run on TUI goroutine.
2. **Instruction counting (custom VM hook):** Would require forking gopher-lua. Not upstreamable.
3. **Separate process per plugin:** Massive IPC overhead, defeats the purpose of embedded Lua.
4. **Single shared goroutine for all Lua:** Serializes all plugins, one slow plugin delays renders for all others.

## Implementation Order

1. Add timeouts to config
2. Add `pluginExecutor` to `plugin.go`
3. Add RWMutex to `WindowManager` in `wm.go`
4. Rewrite `pluginWindow` cached render in `plugin_api.go`
5. Add render tick and `pluginRenderMsg` to `tui.go`
6. Modify `FireMessageCallbacks` and command/keybind dispatch
7. Modify `loadPluginLocked` to use executor
8. Add unhealthy plugin detection
9. Test with infinite-loop plugin
