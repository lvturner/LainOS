# Plan: Code Quality & Architecture

## Problem

The lain TUI has grown into a 1,619-line monolith. Several patterns deviate from Go idioms. The codebase would benefit from decomposition and consistency improvements.

## Issues

### 1. `tui.go` is a 1,619-line god file (HIGH)

**File:** `lain/tui.go`

This file handles:
- Model definition and initialization
- Insert/normal mode key routing
- 20+ message type handlers
- Slash command parsing and execution
- Stream event batching
- Plugin fix agent management
- Sub-agent event routing
- Notification display
- Question modal management
- Session picker integration
- Full view rendering (chat, status bar, mode indicator)
- Message formatting with box drawing

**Fix:** Decompose into focused files:

| New File | Contents | Est. Lines |
|---|---|---|
| `tui.go` | `model` struct, `Init()`, `newModel()`, mode constants | ~150 |
| `tui_update.go` | `Update()` method — message routing, mode switching | ~300 |
| `tui_handlers.go` | Individual handler funcs: `handleStreamMsg()`, `handlePluginError()`, `handleSubAgentEvent()`, etc. | ~400 |
| `tui_commands.go` | Slash command parsing, `/quit`, `/new`, `/save`, `/plugins`, `/compact`, `/sessions`, etc. | ~200 |
| `tui_keys.go` | Normal mode key handling, insert mode key handling | ~200 |
| `tui_view.go` | `View()` method, status bar, mode indicator, chat rendering | ~300 |
| `tui_styles.go` | All lipgloss style definitions | ~30 |

All files remain in `package main`. No interface changes needed — just file-level organization.

### 2. Lipgloss styles as package-level mutables (LOW)

**File:** `lain/tui.go:59-76`

```go
var (
    userStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true)
    assistantStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("213"))
    ...
)
```

These are fine functionally but are initialized at package load time. If theme customization is ever needed, they'd need to be restructured.

**Fix:** Move to `tui_styles.go` as part of the decomposition above. Optionally wrap in a struct if theme support is planned.

### 3. Inconsistent error handling patterns in lain (MEDIUM)

Several places use different patterns:

- `plugin.go:68-77`: Non-blocking send with `select/default` (silently drops errors)
- `session.go`: Returns errors but some callers log and continue
- `llm.go`: Mix of `log/slog` and returning errors

**Fix:** Adopt a consistent pattern:
- Library-style functions return errors
- Top-level Bubble Tea handlers (`Update`) log errors and return model+cmd
- Channel sends that can't block use the `select/default` pattern with a log on drop

### 4. Magic numbers scattered throughout lain (LOW)

**File:** Multiple lain files

Examples:
- `plugin_api.go`: render timeout `50ms`, callback timeout `5s`, load timeout `10s` as raw numbers
- `llm.go`: nudge multiplier `2`, max compaction threshold `90`
- `tui.go`: `todoSidebarW = 28`

**Fix:** Define constants at the top of each file or in `config.go` alongside the config structs. Some of these are already configurable via `config.yaml` (render_timeout, callback_timeout) but the defaults are still magic numbers in the code.

### 5. `llm.go` at 688 lines could be split (MEDIUM)

**File:** `lain/llm.go`

Contains: client struct, streaming loop, non-streaming loop, agentic loop, idle watchdog, context injection, history repair, token estimation, compaction info, error formatting.

**Fix:** Split into:
- `llm.go` — client struct, constructors, public API (`Stream`, `SendMessage`, etc.)
- `llm_stream.go` — streaming implementation, event handling
- `llm_compact.go` — already exists as `compaction.go`, but some compaction logic is still in `llm.go`

### 6. No tests exist (LOW)

Neither `gateway/` nor `lain/` have test files. The AGENTS.md says to run `go test ./...` if tests exist, but none do.

**Fix:** Start with gateway tests since it's smaller:

- `config_test.go`: Test YAML parsing, defaults, invalid YAML
- `handler_test.go`: Test method matching, env var injection, timeout, exit code mapping
- `watcher_test.go`: Test debouncing behavior

For lain, tests are harder due to TUI dependencies, but `session.go`, `todo.go`, `tile.go`, `loop_detector.go`, and `compaction.go` are testable in isolation.

### 7. `plugin_api.go` at 894 lines is large (LOW)

**File:** `lain/plugin_api.go`

Contains the entire Lua API surface. It's large but cohesive (all Lua bindings in one place). 

**Fix:** Optionally split by API module:
- `plugin_api_window.go` — `lain.window.*`
- `plugin_api_chat.go` — `lain.chat.*`
- `plugin_api_exec.go` — `lain.exec.*`, `lain.exec_async.*`
- `plugin_api.go` — core types, `lain.state`, `lain.log`, `lain.command`, `lain.keybind`

Lower priority since the file is cohesive and well-organized internally.

## Implementation Order

1. Decompose `tui.go` into 6-7 files (biggest win for maintainability)
2. Split `llm.go` streaming logic
3. Add gateway tests
4. Extract magic numbers to constants
5. (Optional) Split `plugin_api.go`

## Testing

After decomposition:

```bash
cd lain && go build ./...
cd gateway && go build ./...
```

No behavioral changes — pure refactoring. If it compiles, it works.
