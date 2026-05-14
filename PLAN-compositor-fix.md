# Fix Floating Window Overlay Artifacts

## Problem

The `overlayLines()` compositor in `compositor.go` produces visual artifacts when
rendering floating windows (agent windows, notifications) on top of tiled content.

Root cause: `visualByteOffset()` has a rune/byte index desync bug. After skipping
an ANSI escape sequence, it fails to advance the `runes` slice, causing the byte
offset to drift right with every escape code. Since tiled content is heavily styled
(borders, markdown, lipgloss colors), overlays shift significantly from their
intended position.

Secondary issues:
- Only SGR (`\x1b[...m`) escapes handled; other CSI sequences corrupt offsets
- No background fill on overlay lines — base content bleeds through
- Splitting ANSI strings by byte offset can truncate escape mid-sequence

## Bubble Tea Context

Bubble Tea v2 has no native overlay API. Its cell-based diffing renderer parses
all View() output into its own cell buffer. ANSI cursor positioning (`\x1b[row;colH`)
won't work — the renderer interprets it as cell data. The string-line compositor
pattern is the only viable approach; it just needs correct implementation.

## Changes

### 1. Rewrite `compositor.go`

Replace `visualByteOffset()` with a proper ANSI-aware implementation:
- Correctly advance through ALL CSI sequences (not just SGR `...m`)
- Handle OSC sequences (`\x1b]...BEL/\x1b\\`)
- Keep rune and byte indices in sync
- Add bounds checking

### 2. Background fill on overlay lines

Pad each overlay line to its full visual width with spaces so it completely
covers base content beneath. This prevents bleed-through of styled text from
the tiled layer showing through gaps in the overlay.

### 3. Revert ask mode to floating (larger window)

Switch back from tiled split to floating in `sub_agent.go`, but size the
window to 80% of screen width and 60% of height, centered. The compositor
fix makes floating viable without artifacts.

### 4. Keep existing improvements

The `agentWindow` viewport and `renderMarkdown()` changes from the previous
deploy are correct and remain.

## Files

| File | Change |
|---|---|
| `lain/compositor.go` | Rewrite `visualByteOffset()`, add background fill to `overlayLines()` |
| `lain/sub_agent.go` | Revert ask mode to floating, increase window size |
