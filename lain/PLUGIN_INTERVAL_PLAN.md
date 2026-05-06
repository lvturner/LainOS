# Plugin Interval Timer Plan

Add periodic re-render support to plugin windows so plugins like clocks can refresh their display on a timer.

## Current Architecture

The render pipeline:
1. `pluginRenderTick()` fires every **16ms** via `tea.Tick` (`tui.go:1437-1441`)
2. The `pluginRenderTickMsg` handler checks all plugin windows for `renderDirty` (`tui.go:291-297`)
3. If dirty, `triggerRender()` runs the Lua `render` function on the executor goroutine
4. The result flows back through `renderResultCh` → `waitForPluginRenderResult` → cached render

Currently, `renderDirty` is only set by: key presses, window resize, `lain.state.set()`, or `lain.window.invalidate()`. There is no way for a plugin to say "re-render me every second."

## Lua API

Add `interval` and `tick` options to `lain.window.register()`:

```lua
-- Simple clock (no tick callback needed)
lain.window.register({
  id = "clock",
  title = "Clock",
  interval = 1000,  -- ms
  render = function(w, h)
    return os.date("%H:%M:%S")
  end,
})

-- With tick callback (for async work before render)
lain.window.register({
  id = "weather",
  title = "Weather",
  interval = 30000,
  tick = function()
    -- called on executor goroutine before each re-render
    local result = lain.exec.exec("curl -s wttr.in?format=3")
    lain.state.set("weather", result.stdout)
  end,
  render = function(w, h)
    return lain.state.get("weather") or "loading..."
  end,
})
```

## Changes

### 1. `lain/plugin_api.go` — Core implementation

**Struct changes** — `pluginWindow`:
- Add `interval time.Duration`
- Add `tickFn *lua.LFunction`
- Add `stopTick chan struct{}`

**`newPluginWindow()`** — Initialize `stopTick: make(chan struct{})`

**`luaWindowRegister()`** — After parsing existing opts:
- Parse `interval` (LNumber, milliseconds) and `tick` (LFunction) from opts table
- Set fields on the `pluginWindow`
- If `interval > 0`, launch a goroutine that loops:
  1. Select between `time.After(interval)` and `stopTick` for clean shutdown
  2. If `tickFn != nil`, call it synchronously on the executor (`exec.call()`)
  3. Set `pw.renderDirty = true`
  4. Repeat

**`luaWindowClose()`** — Close `pw.stopTick` before removing from map

**`ClearPlugin()`** — Close `stopTick` for each window belonging to the plugin being cleared

### 2. `docs/lain.md` — User-facing docs

**a)** Update `lain.window.register()` opts in the API reference table (~line 284):

Add `interval` and `tick` to the opts description:
- `interval` (number, ms) — re-render every N milliseconds
- `tick` (function) — optional callback called before each interval re-render

**b)** Add a clock example after the log viewer example (~line 384):

```lua
-- Clock plugin: updates every second
lain.window.register({
  id = "clock",
  title = "Clock",
  interval = 1000,
  render = function(w, h)
    return os.date("%H:%M:%S")
  end,
})
```

And a more advanced example with `tick` for async data fetching.

**c)** Update the render timeout note (~line 340) to mention that tick callbacks use the `callback_timeout` (5s), not the render timeout.

### 3. `AGENTS.md` — Developer conventions

**a)** Update `plugin_api.go` row in the key modules table (~line 96):
- Change to mention interval timers in the description

**b)** Update the plugin system bullet (~line 140):
- Add "periodic timers" to the list of things plugins can register

### 4. `lain/tui.go` — Fix agent prompt

**`buildFixAgentPrompt()`** (~line 1516):
- Add `interval` and `tick` to the listed plugin API options in the prompt string

## Thread Safety

The tick goroutine calls `tickFn` via `exec.call()` (synchronous, runs on executor goroutine), then sets `renderDirty = true`. Since the executor processes calls sequentially, the tick callback completes before any subsequent render call. The existing 16ms `pluginRenderTickMsg` loop in the TUI picks up the dirty flag.

## Cleanup Paths

| Trigger | Action |
|---|---|
| `lain.window.close(id)` | Close `stopTick` |
| Plugin hot-reload (unload) | `ClearPlugin()` → close `stopTick` for all plugin windows |
| `/plugins stop <name>` | Same as above |
| Plugin load error (new plugin replaces old) | `unloadPluginLocked()` → `ClearPlugin()` |
