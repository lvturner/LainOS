# Floating Window Compositor Fix — Implementation Plan

## Root Cause

`ansiMoveTo()` in `float.go` and `notification.go` emits raw `\x1b[row;colH` escape sequences. Bubble Tea's renderer splits `View()` output on `\n` and diffs line-by-line — it does **not** interpret raw cursor positioning. This causes garbled diffs and corrupted output.

## Solution: Line-level overlay compositor

Replace raw ANSI positioning with a line-by-line overlay that stamps floating window content onto the correct lines of the base output. Uses `ansi.StringWidth()` (already in our dependency tree via `github.com/charmbracelet/x/ansi`) to handle ANSI escape sequences correctly when measuring visual column positions.

## New file: `lain/compositor.go` (~60 lines)

A single utility function:

```go
// overlayLines writes overlayLines on top of baseLines starting at visual (x, y).
// Uses ansi.StringWidth to correctly handle ANSI sequences in the base content.
func overlayLines(baseLines []string, overlayLines []string, x int, y int, termWidth int) []string
```

Logic:
1. For each overlay line at index `i`, target is `baseLines[y+i]`
2. Measure base line's visual width with `ansi.StringWidth()`
3. Pad base line to `termWidth` if needed
4. Find the byte offset in the base line that corresponds to visual column `x` (walk runes, skip ANSI sequences, count visual width)
5. Truncate base line at that byte offset
6. Append the overlay line
7. If overlay is shorter than remaining width, append the rest of the base line from after the overlay region

Also add a helper:

```go
// visualByteOffset returns the byte index in s that corresponds to visual column targetCol.
// Returns len(s) if targetCol exceeds the visual width.
func visualByteOffset(s string, targetCol int) int
```

Walks the string rune-by-rune, skipping ANSI escape sequences, counting visual width until reaching `targetCol`. Returns the byte offset at that point.

## Changes to `lain/float.go`

- `FloatingView()` signature changes to `FloatingView(baseContent string, termWidth int) string`
- Split `baseContent` into lines
- For each floating window:
  - Render bordered box (rounded border, title bar, content) into a string
  - Split into lines
  - Call `overlayLines(baseLines, fwLines, fw.X, fw.Y, termWidth)`
- Rejoin and return
- Delete `ansiMoveTo()` function entirely

## Changes to `lain/notification.go`

- `Render()` signature changes to `Render(baseContent string, termWidth int) string`
- Compute position: top-right area (y=1, x = termWidth - notifWidth - 2)
- For each notification:
  - Render styled notification box
  - Call `overlayLines()`
- No `ansiMoveTo()` calls

## Changes to `lain/tui.go` — `View()`

Current:
```go
result := lipgloss.JoinVertical(lipgloss.Left, parts...)
result += m.wm.FloatingView(m.width, m.wmHeight())
if m.notifications.HasActive() {
    result = m.notifications.Render(m.width) + result
}
return result
```

New:
```go
result := lipgloss.JoinVertical(lipgloss.Left, parts...)
result = m.wm.FloatingView(result, m.width)
if m.notifications.HasActive() {
    result = m.notifications.Render(result, m.width)
}
return result
```

Each layer gets the previous result as input, overlays on top, passes forward. Single string in, single string out.

## Changes to `lain/agent_window.go`

- Remove the left-border styling from `View()` — the floating window border is handled by `FloatingView()`'s box rendering
- Just render plain text content (no border, no width/height enforcement)
- The float system adds the rounded border and title bar around it

## Delete

- `ansiMoveTo()` function from `float.go`

## Dependency

- `github.com/charmbracelet/x/ansi` — already an indirect dependency via lipgloss. Import `ansi "github.com/charmbracelet/x/ansi"` and use `ansi.StringWidth()`. No new dependency needed.

## Implementation Order

1. Create `lain/compositor.go` with `overlayLines()` and `visualByteOffset()`
2. Update `lain/float.go` — new `FloatingView()` signature, remove `ansiMoveTo()`
3. Update `lain/notification.go` — new `Render()` signature, remove `ansiMoveTo()`
4. Update `lain/agent_window.go` — remove border rendering, return plain content
5. Update `lain/tui.go` — chain the overlay calls in `View()`
6. `cd lain && go build ./... && go vet ./...`
7. Rebuild container

## Testing

After rebuild, create a broken plugin to trigger the fix agent:
```bash
podman exec lainos bash -c 'echo "invalid lua !!!" > ~/.config/lain/plugins/test-broken.lua'
```

Then start `lain` and verify:
- The plugin error appears in the status bar
- Pressing F spawns a floating overlay (centered, rounded border, readable)
- The main chat/todo layout is not corrupted underneath
- The floating window content is properly positioned within the terminal
