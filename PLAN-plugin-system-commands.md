# Plan: Plugin System Commands

## Problem

Plugins cannot run system commands. Two root causes:

1. **Agent instructions are wrong** — All documentation files tell the LLM that `io`, `os`, `debug`, and `package` are NOT available in plugins, but the code in `plugin.go:318-331` (`newSandboxedState`) already loads `io`, `os`, and `package`. The agent believes these libraries are blocked, so it either avoids them or expects failure.

2. **No dedicated command execution API** — Even with `io.popen`/`os.execute` available, the executor sets a context timeout on the Lua VM (50ms for renders, 5s for callbacks). While Go functions like `io.popen` aren't interrupted mid-call by context cancellation, the overall callback timeout may not be enough for real command execution work. A dedicated `lain.exec()` API running outside the Lua VM context would be more robust.

## Files to modify

| # | File | What |
|---|---|---|
| 1 | `lain/plugin.go` | Add `lua.OpenDebug(L)`, rename `newSandboxedState()` → `newPluginState()` |
| 2 | `lain/plugin_api.go` | Add `lain.exec()` (sync) and `lain.exec_async()` (async) |
| 3 | `config/agents.example.md` | Fix library list at line 266, add `lain.exec()` docs |
| 4 | `.data/home/.config/lain/profiles/default/agents.md` | Fix library list at line 210, add `lain.exec()` docs (live deployed version — preserve all agent additions) |
| 5 | `docs/lain.md` | Fix library list at line 335, add `lain.exec()` to API reference |
| 6 | `AGENTS.md` (project root) | Update plugin library list at line 138, update `plugin_api.go` description at line 96 |

## Step 1: Open `debug` library in `plugin.go`

**File:** `lain/plugin.go`

**Current** (`newSandboxedState`, lines 318-331):
```go
func newSandboxedState() *lua.LState {
	L := lua.NewState(lua.Options{
		SkipOpenLibs: true,
	})
	lua.OpenBase(L)
	lua.OpenString(L)
	lua.OpenTable(L)
	lua.OpenMath(L)
	lua.OpenCoroutine(L)
	lua.OpenIo(L)
	lua.OpenOs(L)
	lua.OpenPackage(L)
	return L
}
```

**New:**
- Rename function to `newPluginState()`
- Add `lua.OpenDebug(L)` after `lua.OpenCoroutine(L)`
- Update all callers (only `loadPluginLocked` at line 165)

The new function opens **all** Lua 5.1 standard libraries: `base`, `string`, `table`, `math`, `coroutine`, `debug`, `io`, `os`, `package`.

## Step 2: Add `lain.exec()` and `lain.exec_async()` in `plugin_api.go`

**File:** `lain/plugin_api.go`

### `lain.exec(cmd, opts)` — synchronous

Signature: `lain.exec(cmd, opts?)` → returns a result table

```lua
local result = lain.exec("ls -la /home/lainos")
-- result.stdout = "total 32\ndrwxr-xr-x ..."
-- result.stderr = ""
-- result.exit_code = 0
-- result.success = true
```

Options table (all optional):
```lua
lain.exec("make test", {
    timeout = 60,          -- seconds, default 30
    cwd = "/home/lainos/workspace",  -- working directory
    env = { KEY = "VALUE" }  -- extra environment variables
})
```

Implementation:
- Go function registered on the `lain` table under `exec`
- Uses `os/exec.CommandContext` with a context timeout derived from `opts.timeout` (default 30s)
- Runs **synchronously on the executor goroutine** (blocks the Lua callback until the command finishes)
- Captures stdout and stderr via pipes
- Returns a Lua table with `stdout`, `stderr`, `exit_code`, `success`
- If the command times out, returns `exit_code = -1`, `success = false`, `stderr = "command timed out"`

### `lain.exec_async(cmd, opts, callback)` — asynchronous

Signature: `lain.exec_async(cmd, opts, callback)` → returns nothing

```lua
lain.exec_async("curl -s https://example.com", { timeout = 10 }, function(result)
    lain.state.set("page", result.stdout)
    lain.window.invalidate("my-panel")
end)
```

Implementation:
- Go function registered on the `lain` table under `exec_async`
- Spawns the command in a **dedicated goroutine** (not the executor goroutine)
- On completion, uses `pluginExecutor.call()` to invoke the callback on the executor goroutine with the result table
- The callback runs with the normal `callback_timeout` (5s default) — enough to process results
- The command itself runs independently of any Lua VM timeout

### Implementation details

Both functions share a helper to parse opts and build the `exec.Cmd`:

```go
func parseExecOpts(L *lua.LState) (timeout time.Duration, cwd string, env map[string]string) {
    timeout = 30 * time.Second
    if L.GetTop() >= 2 {
        if opts, ok := L.Get(2).(*lua.LTable); ok {
            if v := L.RawGet(opts, lua.LString("timeout")); v != lua.LNil {
                timeout = time.Duration(L.CheckFieldInt(2, "timeout")) * time.Second
            }
            // parse cwd, env from opts table
        }
    }
    return
}
```

Both return the same result table structure via a shared helper:

```go
func pushExecResult(L *lua.LState, stdout, stderr string, exitCode int, err error) {
    t := L.NewTable()
    L.SetField(t, "stdout", lua.LString(stdout))
    L.SetField(t, "stderr", lua.LString(stderr))
    L.SetField(t, "exit_code", lua.LNumber(exitCode))
    L.SetField(t, "success", lua.LBool(exitCode == 0 && err == nil))
    L.Push(t)
}
```

For `exec_async`, capture the executor reference before spawning the goroutine:

```go
L.SetField(execTable, "exec_async", L.NewFunction(func(L *lua.LState) int {
    cmd := L.CheckString(1)
    opts := parseExecOpts(L)
    callback := L.CheckFunction(3)
    executor := api.executors[pluginName]  // capture before goroutine

    go func() {
        stdout, stderr, exitCode, err := runCommand(cmd, opts)
        // wrap callback + result into a pluginCall and send to executor
        executor.callAsync(callback,
            []lua.LValue{resultTable},
            1, api.callbackTimeout, api.errCh, pluginName, "exec_async")
    }()
    return 0
}))
```

The `callAsync` method needs a small modification: currently it doesn't wait for or deliver results. For `exec_async`, we need a variant that delivers the result Lua value as an argument to the callback. This can be done by creating a wrapper Lua function that pushes the result table and calls the callback, or by extending `callAsync` to support return value delivery.

**Approach:** Create the result table in the goroutine by acquiring a temporary Lua state operation, or more simply: have the goroutine send the raw Go values (stdout, stderr, exitCode) via a channel, and the executor constructs the Lua table when invoking the callback.

Simplest approach: use `exec.call()` (synchronous) from within a goroutine that wraps both the command execution and the callback invocation. But this won't work because `call()` sends to the executor's channel, and we're trying to call it from the executor goroutine.

**Better approach:** Send a custom message to the executor that includes the raw result data and the callback function. The executor processes it, constructs the Lua table, and calls the callback.

Implementation: add a new method `callAsyncWithResult` to `pluginExecutor`:

```go
type asyncResult struct {
    fn       *lua.LFunction
    args     []lua.LValue
    nRet     int
    timeout  time.Duration
    ret      chan<- pluginResult
}

func (e *pluginExecutor) callAsyncWithResult(fn *lua.LFunction, args []lua.LValue, nRet int, timeout time.Duration) {
    retCh := make(chan pluginResult, 1)
    c := pluginCall{
        fn:      fn,
        args:    args,
        nRet:    nRet,
        ret:     retCh,
        timeout: timeout,
    }
    select {
    case e.callCh <- c:
    default:
    }
}
```

Actually, this is just `call()` without waiting for the result. So `exec_async` can use `call()` in a fire-and-forget manner:

```go
go func() {
    stdout, stderr, exitCode, err := runCommand(cmd, opts)
    // We need to call the Lua callback with the result
    // But we can't construct Lua values from outside the executor goroutine
    // Solution: use the executor's call method, passing a wrapper approach
}()
```

**Final approach:** The simplest correct approach is:

1. In `exec_async`'s Go function, save the callback `*lua.LFunction` reference
2. Spawn a goroutine that runs the command
3. When done, construct the result as raw Go values
4. Use a new channel on the executor to deliver a "callback with Go args" message
5. The executor's run loop picks it up, constructs the Lua table, and calls the callback

Actually, even simpler: just use `exec.call()` directly. The goroutine calls `executor.call(callback, args, nRet, timeout)`. The `call()` method sends to the executor's channel and waits for the result. The executor processes it on the executor goroutine. This is thread-safe because `call()` is designed to be called from any goroutine.

```go
go func() {
    stdout, stderr, exitCode := runCommand(cmdStr, timeout, cwd, env)
    // Can't construct Lua table here (no LState access from this goroutine)
    // But call() takes lua.LValue args...
    // Problem: we need to construct a Lua table, but the LState is owned by the executor
}()
```

The issue is that we can't construct Lua tables from outside the executor goroutine (the LState isn't thread-safe). So we need a different approach.

**Working approach:** Add a new message type to the executor that carries raw Go data and a callback. The executor constructs the Lua table and calls the callback.

```go
type execResultMsg struct {
    callback *lua.LFunction
    stdout   string
    stderr   string
    exitCode int
    err      error
}

// In pluginExecutor.run():
case r := <-e.execResultCh:
    ctx, cancel := context.WithTimeout(context.Background(), callbackTimeout)
    e.L.SetContext(ctx)
    // Build result table
    t := e.L.NewTable()
    e.L.SetField(t, "stdout", lua.LString(r.stdout))
    e.L.SetField(t, "stderr", lua.LString(r.stderr))
    e.L.SetField(t, "exit_code", lua.LNumber(r.exitCode))
    e.L.SetField(t, "success", lua.LBool(r.exitCode == 0 && r.err == nil))
    // Call callback with the table
    e.L.CallByParam(lua.P{Fn: r.callback, NRet: 0, Protect: true}, t)
    cancel()
```

This adds a new channel to `pluginExecutor` and a new case in the `run()` select loop. Clean and correct.

## Step 3: Update `config/agents.example.md`

**File:** `config/agents.example.md`

**Line 266** — Change:
```
**Available Lua libraries:** `string`, `table`, `math`, `coroutine`. No `os`, `io`, `debug`, or `package`.
```
To:
```
**Available Lua libraries:** `string`, `table`, `math`, `coroutine`, `io`, `os`, `debug`, `package`. The full Lua 5.1 standard library is available — use `io.popen`, `os.execute`, etc. freely. For reliable command execution from render/callback functions, prefer `lain.exec()` (see below).
```

**After the logging section (around line 208), add new section:**

```markdown
**Command execution:**
```lua
-- Synchronous (blocks until command finishes, returns result table)
local r = lain.exec("ls -la")
-- r.stdout, r.stderr, r.exit_code, r.success

-- With options
local r = lain.exec("make test", {
  timeout = 60,     -- seconds (default: 30)
  cwd = "/path",    -- working directory
  env = { K = "V" } -- extra env vars
})

-- Asynchronous (returns immediately, calls back with result)
lain.exec_async("curl -s https://example.com", { timeout = 10 }, function(r)
  lain.state.set("page", r.stdout)
  lain.window.invalidate("my-panel")
end)
```
```

Also add a note after the library line about render function timeouts:
```
**Note:** Render functions have a 50ms timeout. Use `lain.exec_async()` for commands inside render functions. Callbacks have a 5s timeout — `lain.exec()` with fast commands is fine.
```

## Step 4: Update live deployed agents.md

**File:** `.data/home/.config/lain/profiles/default/agents.md`

Same changes as Step 3, but applied to the live file:
- **Line 210**: Replace the library restriction line
- **After line 208**: Add the command execution docs

All other content (Camoufox, email, account manager, skills, etc.) must be preserved untouched.

## Step 5: Update `docs/lain.md`

**File:** `docs/lain.md`

**Line 257** — Change:
```
Each plugin runs in an isolated Lua 5.1 sandbox. The standard `os`, `io`, `debug`, and `package` libraries are removed; only controlled APIs via the `lain.*` table are available.
```
To:
```
Each plugin runs in a Lua 5.1 state with the full standard library: `string`, `table`, `math`, `coroutine`, `io`, `os`, `debug`, `package`. The `lain.*` API provides TUI integration; standard library functions are available for filesystem and system access.
```

**Line 335** — Change:
```
Plugins can use: `string`, `table`, `math`, `coroutine`. The `os`, `io`, `debug`, and `package` libraries are **not** available.
```
To:
```
Plugins have access to the full Lua 5.1 standard library: `string`, `table`, `math`, `coroutine`, `io`, `os`, `debug`, `package`. Use `io.popen` or `os.execute` for system commands. For reliable command execution in render/callback contexts, use `lain.exec()` or `lain.exec_async()`.
```

**After the `lain.keybind` table (line 331), add:**

```markdown
#### lain.exec

| Function | Description |
|---|---|
| `exec(cmd, opts?)` | Run command synchronously. Returns `{ stdout, stderr, exit_code, success }`. Options: `{ timeout=30, cwd="", env={} }` |
| `exec_async(cmd, opts, callback)` | Run command asynchronously. `callback(result)` called on completion. |

Render functions have a 50ms timeout — use `exec_async` for commands in render. Callbacks have a 5s timeout — `exec` is fine for fast commands.
```

## Step 6: Update `AGENTS.md` (project root)

**File:** `AGENTS.md`

**Line 95** — Change description of `plugin.go`:
```
| `plugin.go` | `PluginLoader`: sandboxed Lua 5.1 VMs, fsnotify hot-reload (debounced 500ms) |
```
To:
```
| `plugin.go` | `PluginLoader`: Lua 5.1 VMs (full standard library), fsnotify hot-reload (debounced 500ms) |
```

**Line 96** — Update `plugin_api.go` description:
```
| `plugin_api.go` | `PluginAPI`: full `lain.*` Lua API surface (window, chat, session, state, log, command, keybind) |
```
To:
```
| `plugin_api.go` | `PluginAPI`: full `lain.*` Lua API surface (window, chat, session, state, log, command, keybind, exec) |
```

**Line 138** — Change library list:
```
- Each plugin runs in a Lua 5.1 state with full standard libraries (`base`, `string`, `table`, `math`, `coroutine`, `io`, `os`, `package`); only `debug` is omitted
```
To:
```
- Each plugin runs in a Lua 5.1 state with the full standard library (`base`, `string`, `table`, `math`, `coroutine`, `io`, `os`, `debug`, `package`)
```

**Line 279** — Update Lua plugin code style:
```
- Lua plugins: use `lain.*` API for TUI integration; `io`/`os` libraries are available for filesystem and system access; keep render functions fast (called every frame)
```
To:
```
- Lua plugins: use `lain.*` API for TUI integration; full standard library available including `io`/`os` for filesystem and system access; use `lain.exec()` for command execution in render/callback contexts; keep render functions fast (called every frame)
```

## Verification

After implementation:
1. `cd lain && go build ./...` — compiles without errors
2. Write a test plugin using `lain.exec()` and verify it works in the container
3. Verify `io.popen` and `os.execute` work from a plugin
4. Verify `lain.exec_async()` delivers results correctly to the callback

## Out of scope

- `PLAN-plugin-fix-agent.md` — stale planning doc, not worth updating
- Changing the `sync_config` behavior in `start.sh` — the template/live divergence is a separate concern
- Adding tests (no existing test files in `lain/`)
