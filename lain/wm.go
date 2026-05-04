package main

import (
	"github.com/charmbracelet/lipgloss"
)

type WindowManager struct {
	root       *LayoutNode
	windows    map[string]Window
	focusOrder []string
	focused    string
	floating   []*FloatingWindow
	width      int
	height     int
}

func NewWindowManager(width, height int) *WindowManager {
	return &WindowManager{
		windows: make(map[string]Window),
		width:   width,
		height:  height,
	}
}

func (wm *WindowManager) Add(w Window) {
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
	wm.windows[w.ID()] = w
	wm.focusOrder = append(wm.focusOrder, w.ID())
	if wm.root == nil {
		wm.root = newLeafNode(w.ID())
		return
	}
	wm.root.splitLeaf(existingID, dir, w.ID(), fixedRight)
}

func (wm *WindowManager) Remove(id string) {
	if id == "chat" {
		return
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
	wm.width = width
	wm.height = height
	if wm.root == nil {
		return
	}
	rects := wm.root.layout(width, height)
	for id, rect := range rects {
		if w, ok := wm.windows[id]; ok {
			w.SetSize(rect.W, rect.H)
		}
	}
	for _, fw := range wm.floating {
		fw.Window.SetSize(fw.W, fw.H)
	}
}

func (wm *WindowManager) Focused() Window {
	if wm.focused == "" {
		return nil
	}
	return wm.windows[wm.focused]
}

func (wm *WindowManager) FocusedID() string {
	return wm.focused
}

func (wm *WindowManager) SetFocused(id string) {
	if _, ok := wm.windows[id]; ok {
		wm.focused = id
	}
}

func (wm *WindowManager) FocusNext() {
	if len(wm.focusOrder) == 0 {
		return
	}
	for i, id := range wm.focusOrder {
		if id == wm.focused {
			wm.focused = wm.focusOrder[(i+1)%len(wm.focusOrder)]
			return
		}
	}
	wm.focused = wm.focusOrder[0]
}

func (wm *WindowManager) FocusPrev() {
	if len(wm.focusOrder) == 0 {
		return
	}
	for i, id := range wm.focusOrder {
		if id == wm.focused {
			wm.focused = wm.focusOrder[(i-1+len(wm.focusOrder))%len(wm.focusOrder)]
			return
		}
	}
	wm.focused = wm.focusOrder[0]
}

func (wm *WindowManager) FocusByIndex(idx int) {
	if idx < 0 || idx >= len(wm.focusOrder) {
		return
	}
	wm.focused = wm.focusOrder[idx]
}

func (wm *WindowManager) ResizeFocused(delta float64) {
	if wm.root == nil || wm.focused == "" {
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
		wm.SetSize(wm.width, wm.height)
	}
}

func (wm *WindowManager) View() string {
	if wm.root == nil {
		return ""
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
	if wm.root == nil {
		return nil
	}
	return wm.root.leaves()
}

func (wm *WindowManager) Get(id string) Window {
	return wm.windows[id]
}

func (wm *WindowManager) Has(id string) bool {
	_, ok := wm.windows[id]
	return ok
}
