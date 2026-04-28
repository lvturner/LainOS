# Todo Sidebar Plan

Add a persistent right-hand sidebar to lain's TUI that displays the current todo list and its state.

## Current Layout

```
┌──────────────────────────────────┐
│  LAIN banner + profile/model     │  (9 lines, full width)
├──────────────────────────────────┤
│                                  │
│  chat viewport                   │  (flexible height)
│                                  │
├──────────────────────────────────┤
│  > text input                    │  (3 lines, full width)
└──────────────────────────────────┘
```

`View()` renders a vertical stack via `lipgloss.JoinVertical`:
- banner (9 lines)
- viewport (flexible)
- separator
- input (3 lines)

## Target Layout

```
┌──────────────────────────────────┐
│  LAIN banner (full width, 9h)    │
├────────────────────┬─────────────┤
│                    │  Tasks       │
│  chat viewport     │  ☐ 1. Task  │
│  (width - 28)      │  ☑ 2. Done  │  (shared height)
│                    │              │
├────────────────────┴─────────────┤
│  > text input (full width, 3h)   │
└──────────────────────────────────┘
```

- Sidebar is **always visible** (shows "No tasks" when empty)
- Sidebar width is **28 chars** fixed
- Banner and input span **full width** (unchanged)
- Only the viewport row is split horizontally

## Files to Modify

### `lain/tui.go` — all changes

#### 1. New styles

```go
todoHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86"))
todoPendingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
todoDoneStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("60")).Faint(true)
todoEmptyStyle = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("243"))
```

#### 2. New constants

```go
const todoSidebarW = 28
```

#### 3. New method: `chatWidth() int`

Returns the width available for the chat viewport:

```go
func (m model) chatWidth() int {
    return m.width - todoSidebarW
}
```

Used everywhere that previously used `m.width` for viewport width and message formatting.

#### 4. New method: `renderTodoSidebar(height int) string`

Builds the sidebar panel:

- **Header line**: ` Tasks` rendered with `todoHeaderStyle`, followed by enough `─` to fill to 28 chars
- **Body**: For each item:
  - Pending: `☐ N. task text` in `todoPendingStyle`
  - Completed: `☑ N. task text` in `todoDoneStyle`
  - Task text truncated to fit (28 - chars for checkbox + ID + ". ")
- **Empty state**: `  No tasks` in `todoEmptyStyle`
- **Padding**: If body has fewer lines than `height`, pad with blank lines to fill the panel height exactly
- **Left border**: Use `lipgloss.NewStyle().Border(lipgloss.Left(), true)` with a dim border character, or manually prepend `│` to each line
- The entire panel is exactly `todoSidebarW` chars wide and `height` lines tall

Pseudocode:
```
func (m model) renderTodoSidebar(height int) string {
    store := m.registry.GetTodoStore()
    items := store.List()

    var b strings.Builder
    // header
    header := " Tasks"
    b.WriteString(todoHeaderStyle.Render(header))
    b.WriteString(strings.Repeat(" ", todoSidebarW - len(header)))
    b.WriteString("\n")
    b.WriteString(faint(strings.Repeat("─", todoSidebarW)))
    b.WriteString("\n")

    if len(items) == 0 {
        b.WriteString(todoEmptyStyle.Render("  No tasks"))
        b.WriteString("\n")
    } else {
        for _, item := range items {
            marker := "☐"
            style := todoPendingStyle
            if item.Completed {
                marker = "☑"
                style = todoDoneStyle
            }
            line := fmt.Sprintf(" %s %d. %s", marker, item.ID, item.Task)
            if len(line) > todoSidebarW {
                line = line[:todoSidebarW-1] + "…"
            }
            b.WriteString(style.Render(line))
            b.WriteString("\n")
        }
    }

    // pad to height
    linesWritten := strings.Count(b.String(), "\n")
    for i := linesWritten; i < height; i++ {
        b.WriteString(strings.Repeat(" ", todoSidebarW) + "\n")
    }

    // apply left border to entire block
    // join each line with "│" prefix
}
```

#### 5. Update `handleWindowSize()`

Current:
```go
m.viewport = viewport.New(msg.Width, m.viewportHeight())
m.textarea.SetWidth(msg.Width)
```

New:
```go
m.viewport = viewport.New(m.chatWidth(), m.viewportHeight())
m.textarea.SetWidth(msg.Width) // textarea still full width
```

Also invalidate rendered messages so they re-format at the new chat width.

#### 6. Update `resizeViewport()`

Current:
```go
func (m *model) resizeViewport(h int) {
    m.viewport = viewport.New(m.width, h)
    ...
}
```

New:
```go
func (m *model) resizeViewport(h int) {
    m.viewport = viewport.New(m.chatWidth(), h)
    ...
}
```

#### 7. Update `renderMessages()`

The `formatMessage` calls pass `m.width` — change to `m.chatWidth()`:

```go
func (m *model) renderMessages() string {
    ...
    msg.rendered = formatMessage(*msg, m.chatWidth())
    ...
}
```

Also update `formatMessage` callers: `formatMessage(*m.current, m.chatWidth())`, `formatQueuedMessage(qm, m.chatWidth())`.

#### 8. Restructure `View()` — main case

Current:
```go
return lipgloss.JoinVertical(lipgloss.Left,
    banner,
    m.viewport.View(),
    sep,
    inputArea,
)
```

New:
```go
sidebar := m.renderTodoSidebar(m.viewportHeight())
chatWithSidebar := lipgloss.JoinHorizontal(lipgloss.Top,
    m.viewport.View(),
    sidebar,
)
return lipgloss.JoinVertical(lipgloss.Left,
    banner,
    chatWithSidebar,
    sep,
    inputArea,
)
```

#### 9. Restructure `View()` — question modal case

Same pattern — place sidebar next to the viewport in the question layout:

```go
sidebar := m.renderTodoSidebar(m.viewportHeightQuestion())
chatWithSidebar := lipgloss.JoinHorizontal(lipgloss.Top,
    m.viewport.View(),
    sidebar,
)
return lipgloss.JoinVertical(lipgloss.Left,
    banner,
    chatWithSidebar,
    sep,
    questionView,
)
```

#### 10. Session picker — no sidebar

The session picker is a full-screen overlay. It already gets special treatment in `View()`. No sidebar needed here — keep it as-is.

#### 11. `refreshView()` — no changes needed

`refreshView()` calls `renderMessages()` which will use `m.chatWidth()`, and `View()` rebuilds the sidebar from scratch each frame. The Bubble Tea model re-renders on every event, so the sidebar stays current.

#### 12. `handleStreamEvent()` — no special changes

`refreshView()` is already called at the end of every stream event handler. Since `View()` reads the todo store fresh each time, the sidebar updates automatically when the agent adds/completes/removes tasks.

## Edge Cases

- **Terminal < 60 chars wide**: Sidebar still renders at 28 chars. Chat viewport gets very narrow. Acceptable — lain targets reasonable terminal sizes.
- **No todo store (nil)**: `renderTodoSidebar` should check for nil store and show "No tasks".
- **Very long task descriptions**: Truncated with `…` to fit within 28 chars.
- **Many tasks exceeding sidebar height**: Only render what fits, omit the rest. Could show a `… N more` indicator at the bottom.

## Testing

After implementation:
```bash
cd lain && go build ./...
cd lain && go vet ./...
```

Manual test: run `lain`, ask the agent to add tasks, verify sidebar appears and updates in real-time.
