package main

import (
	"sort"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

type FocusDir int

const (
	FocusLeft FocusDir = iota
	FocusRight
	FocusUp
	FocusDown
)

type WindowManager struct {
	mu              sync.RWMutex
	root            *LayoutNode
	windows         map[string]Window
	focusOrder      []string
	focused         string
	floating        []*FloatingWindow
	width           int
	height          int
	bordersEnabled  bool
	zoomedID        string
	prevRoot        *LayoutNode
}

func NewWindowManager(width, height int) *WindowManager {
	return &WindowManager{
		windows:        make(map[string]Window),
		width:          width,
		height:         height,
		bordersEnabled: true,
	}
}

func (wm *WindowManager) Add(w Window) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	wm.windows[w.ID()] = w
	wm.focusOrder = append(wm.focusOrder, w.ID())
	if wm.focused == "" {
		wm.focused = w.ID()
	}
	if wm.root == nil {
		wm.root = newLeafNode(w.ID())
	}
}

func (wm *WindowManager) AddWithSplit(existingID string, dir SplitDirection, w Window, fixedRight int) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	wm.windows[w.ID()] = w
	wm.focusOrder = append(wm.focusOrder, w.ID())
	if wm.root == nil {
		wm.root = newLeafNode(w.ID())
		return
	}
	wm.root.splitLeaf(existingID, dir, w.ID(), fixedRight)
}

func (wm *WindowManager) Remove(id string) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if id == "chat" {
		return
	}
	if wm.zoomedID == id {
		wm.toggleZoomLocked()
	}
	if wm.root != nil {
		wm.root.remove(id)
	}
	delete(wm.windows, id)
	for i, fid := range wm.focusOrder {
		if fid == id {
			wm.focusOrder = append(wm.focusOrder[:i], wm.focusOrder[i+1:]...)
			break
		}
	}
	if wm.focused == id {
		if len(wm.focusOrder) > 0 {
			wm.focused = wm.focusOrder[0]
		} else {
			wm.focused = ""
		}
	}
}

func (wm *WindowManager) SetSize(width, height int) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	wm.setsizeLocked(width, height)
}

func (wm *WindowManager) setsizeLocked(width, height int) {
	wm.width = width
	wm.height = height
	if wm.root == nil {
		return
	}
	rects := wm.root.layout(width, height)
	for id, rect := range rects {
		if w, ok := wm.windows[id]; ok {
			if wm.bordersEnabled && rect.W >= 4 && rect.H >= 3 {
				w.SetSize(rect.W-2, rect.H-2)
			} else {
				w.SetSize(rect.W, rect.H)
			}
		}
	}
	for _, fw := range wm.floating {
		fw.Window.SetSize(fw.W, fw.H)
	}
	for _, fw := range wm.floating {
		if fw.X+fw.W > wm.width {
			fw.X = wm.width - fw.W
		}
		if fw.Y+fw.H > wm.height {
			fw.Y = wm.height - fw.H
		}
		if fw.X < 0 {
			fw.X = 0
		}
		if fw.Y < 0 {
			fw.Y = 0
		}
	}
}

func (wm *WindowManager) ToggleBorders() {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	wm.bordersEnabled = !wm.bordersEnabled
	wm.setsizeLocked(wm.width, wm.height)
}

func (wm *WindowManager) Focused() Window {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	if wm.focused == "" {
		return nil
	}
	return wm.windows[wm.focused]
}

func (wm *WindowManager) FocusedID() string {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	return wm.focused
}

func (wm *WindowManager) SetFocused(id string) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if _, ok := wm.windows[id]; ok {
		wm.focused = id
		if wm.hasFloatingLocked(id) {
			wm.bringToFrontLocked(id)
		}
	}
}

func (wm *WindowManager) FocusNext() {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if len(wm.focusOrder) == 0 {
		return
	}
	for i, id := range wm.focusOrder {
		if id == wm.focused {
			wm.focused = wm.focusOrder[(i+1)%len(wm.focusOrder)]
			if wm.hasFloatingLocked(wm.focused) {
				wm.bringToFrontLocked(wm.focused)
			}
			return
		}
	}
	wm.focused = wm.focusOrder[0]
}

func (wm *WindowManager) FocusPrev() {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if len(wm.focusOrder) == 0 {
		return
	}
	for i, id := range wm.focusOrder {
		if id == wm.focused {
			wm.focused = wm.focusOrder[(i-1+len(wm.focusOrder))%len(wm.focusOrder)]
			if wm.hasFloatingLocked(wm.focused) {
				wm.bringToFrontLocked(wm.focused)
			}
			return
		}
	}
	wm.focused = wm.focusOrder[0]
}

func (wm *WindowManager) FocusByIndex(idx int) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if idx < 0 || idx >= len(wm.focusOrder) {
		return
	}
	wm.focused = wm.focusOrder[idx]
	if wm.hasFloatingLocked(wm.focused) {
		wm.bringToFrontLocked(wm.focused)
	}
}

func (wm *WindowManager) FocusSpatial(dir FocusDir) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if wm.root == nil || wm.focused == "" {
		return
	}
	if wm.hasFloatingLocked(wm.focused) {
		return
	}
	target := wm.root.findAdjacentLeaf(wm.focused, dir, wm.width, wm.height)
	if target != nil && target.Leaf != nil {
		wm.focused = target.Leaf.WindowID
		if wm.hasFloatingLocked(wm.focused) {
			wm.bringToFrontLocked(wm.focused)
		}
	}
}

func (wm *WindowManager) ResizeFocused(delta float64) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if wm.root == nil || wm.focused == "" {
		return
	}
	if wm.hasFloatingLocked(wm.focused) {
		dw := int(delta * 40)
		dh := int(delta * 20)
		wm.resizeFloatingLocked(wm.focused, dw, dh)
		return
	}
	parent := wm.root.findParent(wm.focused)
	if parent != nil && parent.Split != nil && parent.Split.FixedRight == 0 {
		parent.Split.Ratio += delta
		if parent.Split.Ratio < 0.1 {
			parent.Split.Ratio = 0.1
		}
		if parent.Split.Ratio > 0.9 {
			parent.Split.Ratio = 0.9
		}
		wm.setsizeLocked(wm.width, wm.height)
	}
}

func (wm *WindowManager) View() string {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	if wm.root == nil {
		return ""
	}
	if wm.zoomedID != "" {
		if _, ok := wm.windows[wm.zoomedID]; !ok {
			wm.toggleZoomLocked()
		}
		return wm.renderNode(wm.root, wm.width, wm.height)
	}
	return wm.renderNode(wm.root, wm.width, wm.height)
}

func (wm *WindowManager) renderNode(node *LayoutNode, w, h int) string {
	if node.Leaf != nil {
		win, ok := wm.windows[node.Leaf.WindowID]
		if !ok {
			return lipgloss.NewStyle().Width(w).Height(h).Render("")
		}
		focused := node.Leaf.WindowID == wm.focused

		if wm.bordersEnabled && w >= 4 && h >= 3 {
			contentW := w - 2
			contentH := h - 2
			content := win.View(contentW, contentH, focused)

			borderColor := lipgloss.Color("243")
			if focused {
				borderColor = lipgloss.Color("86")
			}

			titleStyle := lipgloss.NewStyle().
				Foreground(borderColor)
			if focused {
				titleStyle = titleStyle.Bold(true)
			} else {
				titleStyle = titleStyle.Faint(true)
			}
			titleText := " " + win.Title() + " "
			titleBar := titleStyle.Width(contentW).Render(titleText)

			inner := lipgloss.JoinVertical(lipgloss.Left, titleBar, content)

			boxStyle := lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(borderColor).
				Width(w).
				Height(h)

			return boxStyle.Render(inner)
		}

		return win.View(w, h, focused)
	}
	if node.Split == nil {
		return ""
	}

	switch node.Split.Direction {
	case SplitVertical:
		var leftW int
		if node.Split.FixedRight > 0 {
			leftW = w - node.Split.FixedRight
		} else {
			leftW = int(float64(w) * node.Split.Ratio)
		}
		if leftW < 1 {
			leftW = 1
		}
		rightW := w - leftW
		if rightW < 1 {
			rightW = 1
		}
		left := wm.renderNode(node.Split.Left, leftW, h)
		right := wm.renderNode(node.Split.Right, rightW, h)
		return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	default:
		var leftH int
		if node.Split.FixedRight > 0 {
			leftH = h - node.Split.FixedRight
		} else {
			leftH = int(float64(h) * node.Split.Ratio)
		}
		if leftH < 1 {
			leftH = 1
		}
		rightH := h - leftH
		if rightH < 1 {
			rightH = 1
		}
		top := wm.renderNode(node.Split.Left, w, leftH)
		bottom := wm.renderNode(node.Split.Right, w, rightH)
		return lipgloss.JoinVertical(lipgloss.Left, top, bottom)
	}
}

func (wm *WindowManager) ListWindows() []string {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	if wm.root == nil {
		return nil
	}
	return wm.root.leaves()
}

func (wm *WindowManager) Get(id string) Window {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	return wm.windows[id]
}

func (wm *WindowManager) Has(id string) bool {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	_, ok := wm.windows[id]
	return ok
}

func (wm *WindowManager) SwapWindows(id1, id2 string) bool {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if wm.root == nil {
		return false
	}
	return wm.root.swapLeaves(id1, id2)
}

func (wm *WindowManager) MoveFocused(dir FocusDir) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if wm.root == nil || wm.focused == "" {
		return
	}
	target := wm.root.findAdjacentLeaf(wm.focused, dir, wm.width, wm.height)
	if target != nil && target.Leaf != nil {
		wm.swapWindowsLocked(wm.focused, target.Leaf.WindowID)
	}
}

func (wm *WindowManager) ToggleZoom() {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if wm.zoomedID != "" {
		wm.root = wm.prevRoot
		wm.zoomedID = ""
	} else {
		if wm.focused == "" {
			return
		}
		wm.prevRoot = wm.root
		wm.zoomedID = wm.focused
		wm.root = newLeafNode(wm.focused)
	}
	wm.setsizeLocked(wm.width, wm.height)
}

func (wm *WindowManager) IsZoomed() bool {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	return wm.zoomedID != ""
}

func (wm *WindowManager) Equalize() {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if wm.root == nil {
		return
	}
	wm.root.equalize()
	wm.setsizeLocked(wm.width, wm.height)
}

func (wm *WindowManager) MoveFloating(id string, dx, dy int) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	for _, fw := range wm.floating {
		if fw.ID() == id {
			fw.X += dx
			fw.Y += dy
			if fw.X < 0 {
				fw.X = 0
			}
			if fw.Y < 0 {
				fw.Y = 0
			}
			if fw.X+fw.W > wm.width {
				fw.X = wm.width - fw.W
			}
			if fw.Y+fw.H > wm.height {
				fw.Y = wm.height - fw.H
			}
			return
		}
	}
}

func (wm *WindowManager) ResizeFloating(id string, dw, dh int) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	for _, fw := range wm.floating {
		if fw.ID() == id {
			fw.W += dw
			fw.H += dh
			if fw.W < 10 {
				fw.W = 10
			}
			if fw.H < 5 {
				fw.H = 5
			}
			fw.Window.SetSize(fw.W, fw.H)
			return
		}
	}
}

func (wm *WindowManager) BringToFront(id string) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
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

func (wm *WindowManager) AddFloating(w Window, x, y, width, height int) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	fw := &FloatingWindow{
		Window: w,
		X:      x,
		Y:      y,
		W:      width,
		H:      height,
		ZOrder: len(wm.floating),
	}
	wm.floating = append(wm.floating, fw)
	wm.focusOrder = append(wm.focusOrder, w.ID())
	wm.focused = w.ID()
	wm.windows[w.ID()] = w
	w.SetSize(width, height)
}

func (wm *WindowManager) RemoveFloating(id string) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	for i, fw := range wm.floating {
		if fw.ID() == id {
			wm.floating = append(wm.floating[:i], wm.floating[i+1:]...)
			break
		}
	}
	for i, fid := range wm.focusOrder {
		if fid == id {
			wm.focusOrder = append(wm.focusOrder[:i], wm.focusOrder[i+1:]...)
			break
		}
	}
	delete(wm.windows, id)
	if wm.focused == id {
		if len(wm.focusOrder) > 0 {
			wm.focused = wm.focusOrder[0]
		} else {
			wm.focused = ""
		}
	}
}

func (wm *WindowManager) HasFloating(id string) bool {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	return wm.hasFloatingLocked(id)
}

func (wm *WindowManager) hasFloatingLocked(id string) bool {
	for _, fw := range wm.floating {
		if fw.ID() == id {
			return true
		}
	}
	return false
}

func (wm *WindowManager) bringToFrontLocked(id string) {
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

func (wm *WindowManager) resizeFloatingLocked(id string, dw, dh int) {
	for _, fw := range wm.floating {
		if fw.ID() == id {
			fw.W += dw
			fw.H += dh
			if fw.W < 10 {
				fw.W = 10
			}
			if fw.H < 5 {
				fw.H = 5
			}
			fw.Window.SetSize(fw.W, fw.H)
			return
		}
	}
}

func (wm *WindowManager) swapWindowsLocked(id1, id2 string) bool {
	if wm.root == nil {
		return false
	}
	return wm.root.swapLeaves(id1, id2)
}

func (wm *WindowManager) toggleZoomLocked() {
	if wm.zoomedID != "" {
		wm.root = wm.prevRoot
		wm.zoomedID = ""
	} else {
		if wm.focused == "" {
			return
		}
		wm.prevRoot = wm.root
		wm.zoomedID = wm.focused
		wm.root = newLeafNode(wm.focused)
	}
	wm.setsizeLocked(wm.width, wm.height)
}

func (wm *WindowManager) FloatingView(baseContent string, termWidth int) string {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	if len(wm.floating) == 0 {
		return baseContent
	}

	baseLines := strings.Split(baseContent, "\n")

	sort.Slice(wm.floating, func(i, j int) bool {
		return wm.floating[i].ZOrder < wm.floating[j].ZOrder
	})

	for _, fw := range wm.floating {
		borderColor := lipgloss.Color("243")
		if fw.ID() == wm.focused {
			borderColor = lipgloss.Color("86")
		}

		boxStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(borderColor).
			Width(fw.W).
			Height(fw.H)

		titleStyle := lipgloss.NewStyle().
			Foreground(borderColor)
		if fw.ID() == wm.focused {
			titleStyle = titleStyle.Bold(true)
		} else {
			titleStyle = titleStyle.Faint(true)
		}

		titleText := " " + fw.Title() + " "
		closeHint := lipgloss.NewStyle().Faint(true).Render("×")
		titleBar := titleStyle.Width(fw.W - 4).Render(titleText) + closeHint

		content := fw.View(fw.W-2, fw.H-3, fw.ID() == wm.focused)

		boxContent := lipgloss.JoinVertical(lipgloss.Left, titleBar, content)
		rendered := boxStyle.Render(boxContent)

		fwLines := strings.Split(rendered, "\n")
		baseLines = overlayLines(baseLines, fwLines, fw.X, fw.Y, termWidth)
	}

	return strings.Join(baseLines, "\n")
}
