# Lain MDI Improvement Plan

## Problem Statement

The lain TUI has a binary-tree tiling layout (`tile.go`) with a floating window overlay (`float.go`), managed by `WindowManager` (`wm.go`). The current system has critical usability gaps:

1. **No visual window separation** -- tiled windows have no borders; content bleeds together
2. **No focus indication** -- the `focused` parameter passed to `Window.View()` is ignored by all window types (`chatWindow`, `todoWindow`, `agentWindow`, `pluginWindow`, `blankWindow`)
3. **No spatial navigation** -- `h`/`l` cycle linear `focusOrder` insertion order, not screen position
4. **Floating windows are broken** -- un-floating creates orphans, z-order is never sorted, floating windows aren't in `focusOrder`, no repositioning
5. **Split is destructive** -- `H`/`V` removes the focused window and creates a blank placeholder
6. **No window rearrangement** -- no move/swap, no maximize, no equalize

This plan addresses all five issues across 5 phases. Each phase is independently deployable.

---

## Phase 1: Borders & Focus Indication

**Goal**: Every tiled window is wrapped in a bordered box. Focused windows are visually distinct. Borders are on by default and togglable.

### 1.1 Add border state to WindowManager (`wm.go`)

Add fields to `WindowManager`:

```go
type WindowManager struct {
    // ... existing fields ...
    bordersEnabled bool
    zoomedID       string    // phase 4, but reserve the field
    prevRoot       *LayoutNode
}
```

- `NewWindowManager()` sets `bordersEnabled: true`
- Add method: `func (wm *WindowManager) ToggleBorders()`

### 1.2 Modify `renderNode()` in `wm.go` (lines 160-208)

Currently `renderNode()` passes raw `(w, h, focused)` to `win.View()`. Change to:

1. If `bordersEnabled`:
   - Compute content dimensions: `contentW = w - 2`, `contentH = h - 2` (border takes 1 char each side)
   - Call `win.View(contentW, contentH, focused)` to get content
   - Build a title bar line: window title, styled (bold if focused, faint if not)
   - Join title bar + content vertically
   - Wrap with lipgloss border:
     - **Focused**: `BorderForeground(lipgloss.Color("86"))` (bright cyan), rounded border
     - **Unfocused**: `BorderForeground(lipgloss.Color("243"))` (dim gray), rounded border
   - The `Width(w).Height(h)` on the outer style ensures exact sizing
2. If `!bordersEnabled`:
   - Pass through as today: `win.View(w, h, focused)`

### 1.3 Update `SetSize()` in `wm.go` (lines 69-84)

When computing layout rects, if borders are enabled, each leaf needs 2 extra characters in both dimensions. The layout tree operates in "outer" coordinates (including borders), and `renderNode()` handles the subtraction. So `SetSize()` calls `win.SetSize(rect.W-2, rect.H-2)` when borders are on, or `win.SetSize(rect.W, rect.H)` when off.

Alternatively (simpler approach): `SetSize()` always passes the raw rect dimensions. `renderNode()` subtracts the border overhead before calling `win.View()`, and the border style's `Width/Height` ensures the rendered output matches the allocated space. The `SetSize()` on the window should receive the *content* dimensions, so:

```go
func (wm *WindowManager) SetSize(width, height int) {
    wm.width = width
    wm.height = height
    if wm.root == nil {
        return
    }
    rects := wm.root.layout(width, height)
    for id, rect := range rects {
        if w, ok := wm.windows[id]; ok {
            if wm.bordersEnabled {
                w.SetSize(rect.W-2, rect.H-2)
            } else {
                w.SetSize(rect.W, rect.H)
            }
        }
    }
    // ... floating windows unchanged ...
}
```

### 1.4 Remove `todoWindow` self-drawn border (`window.go` lines 181-186)

The `todoWindow.View()` currently draws its own left border:

```go
return lipgloss.NewStyle().
    Border(lipgloss.Border{Left: "│"}, false, false, false, true).
    BorderForeground(lipgloss.Color("243")).
    Width(width).
    Height(height).
    Render(strings.TrimSuffix(b.String(), "\n"))
```

Remove this border wrapper. Return the raw content from `View()`. The WM border system now handles all chrome. This means `todoWindow` must also account for the border overhead in its content layout -- but since `SetSize()` now receives `rect.W-2`, this is already handled.

Also remove the header underline and adjust padding to work within the new bordered area. The todo header (" Tasks") and separator line ("─") remain but are drawn within the content area.

### 1.5 Update `FloatingView()` border in `float.go` (lines 62-66)

Floating windows already get rounded borders (cyan "86"). Change the border color to match the new focus system:

- **Focused floating**: bright cyan "86" (same as focused tiled)
- **Unfocused floating**: dim gray "243"

Check `fw.ID() == wm.focused` inside the `FloatingView()` loop.

### 1.6 Keybinding: `b` to toggle borders (`tui.go` handleNormalKey)

Add to the normal mode switch:

```go
case "b":
    m.wm.ToggleBorders()
    m.resizeWM()
    return m, nil
```

### 1.7 Update help string (`tui.go` line 563)

Change:
```
"hjkl:focus H:hsplit V:vsplit x:close f:float +/-:resize 1-9:goto Esc:insert"
```
To:
```
"hjkl:focus x:close f:float +/-:resize b:borders z:zoom =:equalize 1-9:goto Esc:insert"
```

(Also removes `H`/`V` which are replaced in Phase 4.)

### 1.8 Minimum size guard

If borders are enabled and a leaf rect is smaller than 4x3 (2 for borders + 2 for content), skip the border and render content directly to avoid degenerate rendering.

### Files modified:
- `lain/wm.go` -- `WindowManager` struct, `renderNode()`, `SetSize()`, new `ToggleBorders()`
- `lain/window.go` -- `todoWindow.View()` remove self-drawn border
- `lain/float.go` -- `FloatingView()` focus-aware border colors
- `lain/tui.go` -- `handleNormalKey()` add `b` keybinding, update help string

### Visual result:
```
╭──────────────────────────────────────╮ ┌──────────────┐
│ Chat                                 │ │ Tasks        │
│ User: hello there                    │ │ ☐ 1. Fix bug │
│ Assistant: Hi! How can I help?       │ │ ☑ 2. Tests   │
│ > what is 2+2?                       │ │              │
│ 4                                    │ │              │
╰──────────────────────────────────────╯ └──────────────┘
 ^^^ bright cyan border (focused)          ^^^ dim gray (unfocused)
```

---

## Phase 2: Spatial Navigation

**Goal**: `h`/`j`/`k`/`l` navigate to the spatially adjacent window (left/down/up/right), not the linear insertion order.

### 2.1 Add `findAdjacentLeaf()` to `tile.go`

New method on `LayoutNode`:

```go
func (n *LayoutNode) findAdjacentLeaf(fromID string, direction SplitDirection) *LayoutNode
```

Algorithm:
1. Call `layout(0, 0, totalW, totalH)` to get `LayoutRect` for every leaf
2. Get the rect for `fromID`
3. Compute the "search edge" based on direction:
   - `SplitHorizontal` (want to go up/down): search for a leaf whose Y range overlaps `fromID`'s Y midpoint, and is directly above/below
   - `SplitVertical` (want to go left/right): search for a leaf whose X range overlaps `fromID`'s X midpoint, and is directly to the left/right
4. Among candidates, pick the one closest to the search edge
5. Return the found leaf node

Concretely for each direction:
- **Left** (`h`): Find leaf whose `rect.X + rect.W == fromRect.X` (shares the left edge), with Y overlap > 0. Closest by center-to-center distance.
- **Right** (`l`): Find leaf whose `rect.X == fromRect.X + fromRect.W` (shares the right edge), with Y overlap > 0.
- **Up** (`k`): Find leaf whose `rect.Y + rect.H == fromRect.Y` (shares the top edge), with X overlap > 0.
- **Down** (`j`): Find leaf whose `rect.Y == fromRect.Y + fromRect.H` (shares the bottom edge), with X overlap > 0.

If no exact edge match, relax: find the leaf in the correct half-plane (left/right/up/down of from) with minimum center-to-center distance.

The method needs total `(width, height)` from the WindowManager to compute the layout. Pass these in or cache them.

### 2.2 Add `FocusDirection()` to `wm.go`

```go
func (wm *WindowManager) FocusDirection(direction SplitDirection) {
    if wm.root == nil || wm.focused == "" {
        return
    }
    target := wm.root.findAdjacentLeaf(wm.focused, direction, wm.width, wm.height)
    if target != nil && target.Leaf != nil {
        wm.focused = target.Leaf.WindowID
    }
}
```

### 2.3 Update keybindings in `tui.go` handleNormalKey (lines 505-510)

Change:
```go
case "h", "left":
    m.wm.FocusPrev()    // was linear
case "l", "right":
    m.wm.FocusNext()    // was linear
```
To:
```go
case "h", "left":
    m.wm.FocusDirection(SplitVertical)    // spatial: go left
case "l", "right":
    m.wm.FocusDirection(SplitVertical)    // wait, need direction concept...
```

Actually we need a direction enum that's distinct from `SplitDirection` since `SplitDirection` represents the split orientation, not the movement direction. Define:

```go
type FocusDirection int
const (
    FocusLeft FocusDirection = iota
    FocusRight
    FocusUp
    FocusDown
)
```

Then:
```go
case "h", "left":
    m.wm.FocusSpatial(FocusLeft)
case "l", "right":
    m.wm.FocusSpatial(FocusRight)
case "j", "down":
    m.wm.FocusSpatial(FocusDown)
case "k", "up":
    m.wm.FocusSpatial(FocusUp)
```

Keep `FocusNext()`/`FocusPrev()` wired to `Tab`/`Shift+Tab` if we add those later, or keep them accessible via some other mechanism. For now `1`-`9` still works as direct index access.

### 2.4 Handle floating window focus

When `wm.focused` is a floating window ID, spatial navigation should cycle to the next/prev floating window or fall back to the first tiled window. Add a check in `FocusSpatial()`:

```go
if wm.HasFloating(wm.focused) {
    // For floating windows, fall through to tiled navigation
    // or cycle floating windows if multiple exist
}
```

### Files modified:
- `lain/tile.go` -- new `findAdjacentLeaf()` method
- `lain/wm.go` -- new `FocusDirection` type, `FocusSpatial()` method
- `lain/tui.go` -- update `h`/`j`/`k`/`l` keybindings

---

## Phase 3: Fix Floating Windows

**Goal**: Floating windows work correctly -- un-floating restores to tiling, z-order is respected, floating windows participate in focus, and can be repositioned/resized.

### 3.1 Fix the orphan window bug (`tui.go` line 692)

**Current** (broken): `/float` command un-floats by calling `wm.Add(win)` which adds to the map but not the tree:

```go
m.wm.RemoveFloating(id)
m.wm.Add(win)  // BUG: doesn't insert into tree
```

**Fix**: Use `AddWithSplit()` to insert next to the currently focused window (or chat as fallback):

```go
m.wm.RemoveFloating(id)
targetID := m.wm.FocusedID()
if targetID == "" || targetID == id {
    targetID = "chat"
}
m.wm.AddWithSplit(targetID, SplitVertical, win, 0)
```

Same fix needed in `handleNormalKey` `f` case (line 538).

### 3.2 Add floating windows to focusOrder (`float.go`)

**Current**: `AddFloating()` sets `wm.focused` directly but never adds to `focusOrder`. `RemoveFloating()` falls back to `focusOrder[0]`.

**Fix**: In `AddFloating()`, append the window ID to `focusOrder`. In `RemoveFloating()`, remove from `focusOrder`:

```go
func (wm *WindowManager) AddFloating(w Window, x, y, width, height int) {
    // ... existing code ...
    wm.focusOrder = append(wm.focusOrder, w.ID())
    // ...
}

func (wm *WindowManager) RemoveFloating(id string) {
    // ... existing removal ...
    for i, fid := range wm.focusOrder {
        if fid == id {
            wm.focusOrder = append(wm.focusOrder[:i], wm.focusOrder[i+1:]...)
            break
        }
    }
    // ...
}
```

### 3.3 Sort floating windows by ZOrder during render (`float.go` line 61)

**Current**: `FloatingView()` iterates `wm.floating` in insertion order, ignoring `ZOrder`.

**Fix**: Sort the floating slice by `ZOrder` before rendering:

```go
sort.Slice(wm.floating, func(i, j int) bool {
    return wm.floating[i].ZOrder < wm.floating[j].ZOrder
})
```

### 3.4 Add `MoveFloating()` and `ResizeFloating()` to `wm.go`

```go
func (wm *WindowManager) MoveFloating(id string, dx, dy int) {
    for _, fw := range wm.floating {
        if fw.ID() == id {
            fw.X += dx
            fw.Y += dy
            if fw.X < 0 { fw.X = 0 }
            if fw.Y < 0 { fw.Y = 0 }
            if fw.X + fw.W > wm.width { fw.X = wm.width - fw.W }
            if fw.Y + fw.H > wm.height { fw.Y = wm.height - fw.H }
            return
        }
    }
}

func (wm *WindowManager) ResizeFloating(id string, dw, dh int) {
    for _, fw := range wm.floating {
        if fw.ID() == id {
            fw.W += dw
            fw.H += dh
            if fw.W < 10 { fw.W = 10 }
            if fw.H < 5 { fw.H = 5 }
            fw.Window.SetSize(fw.W, fw.H)
            return
        }
    }
}
```

### 3.5 Add floating reposition/resize keybindings (`tui.go` handleNormalKey)

When the focused window is floating, use Ctrl+arrows to move and Ctrl+Shift+arrows to resize:

```go
case "ctrl+left":
    if m.wm.HasFloating(m.wm.FocusedID()) {
        m.wm.MoveFloating(m.wm.FocusedID(), -2, 0)
    }
case "ctrl+right":
    if m.wm.HasFloating(m.wm.FocusedID()) {
        m.wm.MoveFloating(m.wm.FocusedID(), 2, 0)
    }
case "ctrl+up":
    if m.wm.HasFloating(m.wm.FocusedID()) {
        m.wm.MoveFloating(m.wm.FocusedID(), 0, -1)
    }
case "ctrl+down":
    if m.wm.HasFloating(m.wm.FocusedID()) {
        m.wm.MoveFloating(m.wm.FocusedID(), 0, 1)
    }
```

Note: bubbletea key names for Ctrl+arrows are `"ctrl+left"`, `"ctrl+right"`, `"ctrl+up"`, `"ctrl+down"`.

For floating resize, use `+`/`-` keys (which already exist for tiled resize). In `handleNormalKey`:

```go
case "+":
    if m.wm.HasFloating(m.wm.FocusedID()) {
        m.wm.ResizeFloating(m.wm.FocusedID(), 2, 1)
    } else {
        m.wm.ResizeFocused(0.05)
    }
case "-":
    if m.wm.HasFloating(m.wm.FocusedID()) {
        m.wm.ResizeFloating(m.wm.FocusedID(), -2, -1)
    } else {
        m.wm.ResizeFocused(-0.05)
    }
```

### 3.6 Re-clamp floating positions on terminal resize (`wm.go` SetSize)

After updating `wm.width`/`wm.height`, clamp each floating window's position:

```go
for _, fw := range wm.floating {
    if fw.X + fw.W > wm.width { fw.X = wm.width - fw.W }
    if fw.Y + fw.H > wm.height { fw.Y = wm.height - fw.H }
    if fw.X < 0 { fw.X = 0 }
    if fw.Y < 0 { fw.Y = 0 }
}
```

### 3.7 Add `BringToFront()` for z-order management

```go
func (wm *WindowManager) BringToFront(id string) {
    maxZ := 0
    for _, fw := range wm.floating {
        if fw.ZOrder > maxZ {
            maxZ = fw.ZOrder
        }
    }
    for _, fw := range wm.floating {
        if fw.ID() == id {
            fw.ZOrder = maxZ + 1
            return
        }
    }
}
```

Call this when focusing a floating window (in `SetFocused()` or after `FocusSpatial()` lands on a floating window).

### Files modified:
- `lain/float.go` -- `AddFloating()`, `RemoveFloating()` update focusOrder; `FloatingView()` sort by ZOrder
- `lain/wm.go` -- new `MoveFloating()`, `ResizeFloating()`, `BringToFront()`; `SetSize()` clamp positions
- `lain/tui.go` -- fix `/float` orphan bug, fix `f` key orphan bug, add Ctrl+arrow keybindings, extend `+`/`-` for floating

---

## Phase 4: Window Manipulation

**Goal**: Move/swap windows, maximize (tmux-style zoom), equalize splits.

### 4.1 Swap windows (`tile.go` + `wm.go`)

Add a method to swap two leaf nodes in the tree:

```go
func (n *LayoutNode) swapLeaves(id1, id2 string) bool
```

Algorithm:
1. Find both leaf nodes via `find()`
2. Swap their `WindowID` values
3. Return true if both found

Add to WindowManager:
```go
func (wm *WindowManager) SwapWindows(id1, id2 string) bool {
    if wm.root == nil {
        return false
    }
    return wm.root.swapLeaves(id1, id2)
}
```

### 4.2 Find adjacent window for swap

Combine `findAdjacentLeaf()` from Phase 2 with `SwapWindows()`:

```go
func (wm *WindowManager) MoveFocused(direction FocusDirection) {
    if wm.root == nil || wm.focused == "" {
        return
    }
    target := wm.root.findAdjacentLeaf(wm.focused, direction, wm.width, wm.height)
    if target != nil && target.Leaf != nil {
        wm.SwapWindows(wm.focused, target.Leaf.WindowID)
        // focus stays on the same window (now in new position)
    }
}
```

### 4.3 Ctrl+arrow keybindings for swap/move (`tui.go`)

When the focused window is **tiled**, Ctrl+arrows swap with the spatial neighbor:

```go
case "ctrl+left":
    if m.wm.HasFloating(m.wm.FocusedID()) {
        m.wm.MoveFloating(m.wm.FocusedID(), -2, 0)
    } else {
        m.wm.MoveFocused(FocusLeft)
        m.resizeWM()
    }
case "ctrl+right":
    if m.wm.HasFloating(m.wm.FocusedID()) {
        m.wm.MoveFloating(m.wm.FocusedID(), 2, 0)
    } else {
        m.wm.MoveFocused(FocusRight)
        m.resizeWM()
    }
// ... same for up/down ...
```

### 4.4 Maximize/Zoom (tmux-style) (`wm.go`)

Add zoom support to WindowManager:

```go
func (wm *WindowManager) ToggleZoom() {
    if wm.zoomedID != "" {
        // Unzoom: restore previous layout
        wm.root = wm.prevRoot
        wm.zoomedID = ""
    } else {
        // Zoom: save current layout, create single-leaf root
        if wm.focused == "" {
            return
        }
        wm.prevRoot = wm.root
        wm.zoomedID = wm.focused
        wm.root = newLeafNode(wm.focused)
    }
    wm.SetSize(wm.width, wm.height)
}

func (wm *WindowManager) IsZoomed() bool {
    return wm.zoomedID != ""
}
```

When zoomed, `View()` renders only the zoomed window at full size. When unzoomed, the previous tree is restored.

**Edge case**: If the zoomed window is removed while zoomed, unzoom automatically (the tree collapse may have already happened, but `prevRoot` still has the old layout without that window).

### 4.5 Keybinding: `z` to toggle zoom (`tui.go` handleNormalKey)

```go
case "z":
    m.wm.ToggleZoom()
    return m, nil
```

No `resizeWM()` needed -- `ToggleZoom()` calls `SetSize()` internally.

### 4.6 Equalize splits (`tile.go` + `wm.go`)

Add a recursive method to reset all split ratios to 0.5:

```go
func (n *LayoutNode) equalize() {
    if n.Split == nil {
        return
    }
    if n.Split.FixedRight == 0 {
        n.Split.Ratio = 0.5
    }
    n.Split.Left.equalize()
    n.Split.Right.equalize()
}
```

```go
func (wm *WindowManager) Equalize() {
    if wm.root == nil {
        return
    }
    wm.root.equalize()
    wm.SetSize(wm.width, wm.height)
}
```

### 4.7 Keybinding: `=` to equalize (`tui.go` handleNormalKey)

```go
case "=":
    m.wm.Equalize()
    return m, nil
```

### 4.8 Replace `H`/`V` split with clean split behavior

Remove the current `H`/`V` keys (which create blank windows). Instead, add `s` for a clean horizontal split and `v` for vertical split that takes the focused window and splits it, moving it into one pane and creating a new empty pane:

Actually, creating blank windows isn't very useful. A better approach: `s`/`v` split the focused window, putting the focused window on the left/top and creating a blank "Scratch" pane on the right/bottom. The user can then move other windows into the scratch pane using Ctrl+arrows.

```go
case "s":
    focused := m.wm.FocusedID()
    if focused != "" {
        scratch := &blankWindow{id: "scratch-" + focused, title: "Scratch"}
        m.wm.AddWithSplit(focused, SplitHorizontal, scratch, 0)
        m.resizeWM()
    }
case "v":
    focused := m.wm.FocusedID()
    if focused != "" {
        scratch := &blankWindow{id: "scratch-" + focused, title: "Scratch"}
        m.wm.AddWithSplit(focused, SplitVertical, scratch, 0)
        m.resizeWM()
    }
```

This is much cleaner than the old `H`/`V` which removed and re-added the focused window.

### Files modified:
- `lain/tile.go` -- new `swapLeaves()`, `equalize()` methods
- `lain/wm.go` -- new `SwapWindows()`, `MoveFocused()`, `ToggleZoom()`, `Equalize()`; new `zoomedID`/`prevRoot` fields
- `lain/tui.go` -- replace `H`/`V` with `s`/`v`; add `z`, `=` keybindings; add Ctrl+arrow for tiled swap

---

## Phase 5: Close/Dismiss Improvements

**Goal**: Better close UX -- close all except chat, dismiss completed agents, visual close affordance.

### 5.1 Keybinding: `X` (shift-x) to close all except chat (`tui.go` handleNormalKey)

```go
case "X":
    windows := m.wm.ListWindows()
    for _, id := range windows {
        if id != "chat" {
            // Stop any sub-agent associated with this window
            m.subAgentMgr.Stop(id)
            m.wm.Remove(id)
        }
    }
    m.resizeWM()
    return m, nil
```

### 5.2 Completed agent window hint (`agent_window.go`)

When an agent window is done (`w.done == true`), show a dismiss hint in the `View()`:

```go
if w.done {
    b.WriteString(agentDoneStyle.Render("✓ done"))
    b.WriteString(lipgloss.NewStyle().Faint(true).Render(" (x to close)"))
    b.WriteString("\n")
}
```

### 5.3 Floating window close affordance in title bar (`float.go`)

Add a visual close indicator to the floating window title bar:

```go
titleText := " " + fw.Title() + " "
closeHint := lipgloss.NewStyle().Faint(true).Render("×")
titleBar := lipgloss.NewStyle().
    Bold(true).
    Foreground(borderColor).
    Width(fw.W - 4).  // account for border + close hint
    Render(titleText) + closeHint
```

This shows something like `╭ Agent (fix)                        ×╮` in the title bar.

### 5.4 Remove redundant `WindowExists()` method (`wm.go` line 227)

`Has()` and `WindowExists()` are identical. Remove `WindowExists()` and update any callers.

### 5.5 Remove dead `leafIndex()` method (`tile.go` line 134)

`leafIndex()` is never called. Remove it.

### 5.6 Plugin API: expose new operations (`plugin_api.go`)

Add to the `lain.window` Lua API:

```lua
lain.window.move(id, direction)   -- FocusDirection-based move/swap
lain.window.zoom()                -- toggle zoom
lain.window.equalize()            -- equalize splits
lain.window.borders(enabled)      -- toggle borders (nil = toggle)
```

### Files modified:
- `lain/tui.go` -- add `X` keybinding
- `lain/agent_window.go` -- add dismiss hint in done state
- `lain/float.go` -- close hint in title bar
- `lain/wm.go` -- remove `WindowExists()` dead code
- `lain/tile.go` -- remove `leafIndex()` dead code
- `lain/plugin_api.go` -- new window management APIs

---

## Implementation Order

The phases should be implemented in order since each builds on the previous:

1. **Phase 1** (borders/focus) -- standalone, highest visual impact
2. **Phase 2** (spatial nav) -- depends on Phase 1 for the layout rect calculations
3. **Phase 3** (fix floating) -- depends on Phase 2 for spatial focus with floating awareness
4. **Phase 4** (manipulation) -- depends on Phase 2 for `findAdjacentLeaf()` used by swap
5. **Phase 5** (close/dismiss) -- depends on Phase 4 for zoom awareness

## Complete Keybinding Map (Normal Mode)

After all phases are implemented:

| Key | Action |
|---|---|
| `esc`, `i` | Return to insert mode |
| `h`, `left` | Focus left (spatial) |
| `j`, `down` | Focus down (spatial) |
| `k`, `up` | Focus up (spatial) |
| `l`, `right` | Focus right (spatial) |
| `ctrl+left` | Move/swap window left, or move floating left |
| `ctrl+right` | Move/swap window right, or move floating right |
| `ctrl+up` | Move/swap window up, or move floating up |
| `ctrl+down` | Move/swap window down, or move floating down |
| `s` | Split horizontal (new scratch pane below) |
| `v` | Split vertical (new scratch pane to right) |
| `x` | Close focused window |
| `X` | Close all windows except chat |
| `f` | Toggle float (tile ↔ floating) |
| `+` | Grow split ratio / grow floating window |
| `-` | Shrink split ratio / shrink floating window |
| `b` | Toggle borders on/off |
| `z` | Toggle zoom (maximize focused) |
| `=` | Equalize all split ratios |
| `1`-`9` | Jump to window by index |
| `?` | Show help |

## Architecture Notes

### Border rendering strategy

Borders are rendered by `renderNode()` in `wm.go`, not by individual windows. This is important because:

1. **Consistency**: All windows get identical chrome treatment
2. **Single point of control**: Toggle borders once, affects everything
3. **Window simplicity**: Windows only worry about their content, not their frame

The `Window.View()` method receives content-area dimensions (after border subtraction). Windows should not draw their own borders.

### Why `findAdjacentLeaf()` instead of tree-walking

Tree-walking (e.g., "go to parent's sibling's leftmost leaf") is the "correct" algorithmic approach but fails in practice because:

1. The binary tree structure doesn't always map cleanly to screen position (a vertical split's left child might contain a horizontal split)
2. Fixed-size splits complicate direction inference
3. The layout rect computation is already available and gives exact pixel positions

Using layout rects is simpler, more robust, and handles all edge cases naturally.

### Zoom design choice

The tmux-style zoom (save/restore root pointer) is simpler than the "layout replace" approach because:

1. No need to reconstruct the tree from scratch
2. The saved tree maintains exact split ratios
3. Unzoom is O(1) -- just swap the pointer back
4. The zoomed window still gets borders, so it's visually consistent

### Floating window architecture

Floating windows should be treated as overlay citizens, not tree members. They:
- Are tracked in `wm.floating[]` (not in the layout tree)
- Are composited on top of tiled content via ANSI positioning
- Participate in `focusOrder` for cycling but not in spatial navigation of the tree
- Get their own Ctrl+arrow behavior (move instead of swap)
- Have their own z-order management
