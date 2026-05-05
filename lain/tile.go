package main

import "math"

type SplitDirection int

const (
	SplitHorizontal SplitDirection = iota
	SplitVertical
)

type LayoutNode struct {
	Split *SplitNode
	Leaf  *LeafNode
}

type SplitNode struct {
	Direction  SplitDirection
	Ratio      float64
	Left       *LayoutNode
	Right      *LayoutNode
	FixedRight int
}

type LeafNode struct {
	WindowID string
}

func newLeafNode(windowID string) *LayoutNode {
	return &LayoutNode{Leaf: &LeafNode{WindowID: windowID}}
}

func newSplitNode(dir SplitDirection, ratio float64, left, right *LayoutNode) *LayoutNode {
	return &LayoutNode{
		Split: &SplitNode{
			Direction: dir,
			Ratio:     ratio,
			Left:      left,
			Right:     right,
		},
	}
}

func newFixedSplitNode(dir SplitDirection, fixedRight int, left, right *LayoutNode) *LayoutNode {
	return &LayoutNode{
		Split: &SplitNode{
			Direction:  dir,
			Ratio:      0.8,
			Left:       left,
			Right:      right,
			FixedRight: fixedRight,
		},
	}
}

func (n *LayoutNode) findParent(windowID string) *LayoutNode {
	if n.Split == nil {
		return nil
	}
	if n.Split.Left.isLeaf(windowID) || n.Split.Right.isLeaf(windowID) {
		return n
	}
	if p := n.Split.Left.findParent(windowID); p != nil {
		return p
	}
	return n.Split.Right.findParent(windowID)
}

func (n *LayoutNode) isLeaf(windowID string) bool {
	return n.Leaf != nil && n.Leaf.WindowID == windowID
}

func (n *LayoutNode) find(windowID string) *LayoutNode {
	if n.Leaf != nil && n.Leaf.WindowID == windowID {
		return n
	}
	if n.Split == nil {
		return nil
	}
	if found := n.Split.Left.find(windowID); found != nil {
		return found
	}
	return n.Split.Right.find(windowID)
}

func (n *LayoutNode) remove(windowID string) bool {
	parent := n.findParent(windowID)
	if parent == nil || parent.Split == nil {
		return false
	}

	sibling := parent.Split.Right
	if parent.Split.Left.isLeaf(windowID) {
		sibling = parent.Split.Right
	} else {
		sibling = parent.Split.Left
	}

	*parent = *sibling
	return true
}

func (n *LayoutNode) splitLeaf(windowID string, dir SplitDirection, newWindowID string, fixedRight int) bool {
	leaf := n.find(windowID)
	if leaf == nil {
		return false
	}

	oldID := leaf.Leaf.WindowID
	if fixedRight > 0 {
		*leaf = *newFixedSplitNode(dir, fixedRight,
			newLeafNode(oldID),
			newLeafNode(newWindowID),
		)
	} else {
		*leaf = *newSplitNode(dir, 0.5,
			newLeafNode(oldID),
			newLeafNode(newWindowID),
		)
	}
	return true
}

func (n *LayoutNode) leaves() []string {
	if n.Leaf != nil {
		return []string{n.Leaf.WindowID}
	}
	if n.Split == nil {
		return nil
	}
	result := n.Split.Left.leaves()
	result = append(result, n.Split.Right.leaves()...)
	return result
}

func (n *LayoutNode) swapLeaves(id1, id2 string) bool {
	leaf1 := n.find(id1)
	leaf2 := n.find(id2)
	if leaf1 == nil || leaf2 == nil {
		return false
	}
	leaf1.Leaf.WindowID, leaf2.Leaf.WindowID = leaf2.Leaf.WindowID, leaf1.Leaf.WindowID
	return true
}

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

func (n *LayoutNode) findAdjacentLeaf(fromID string, dir FocusDir, totalW, totalH int) *LayoutNode {
	rects := n.layout(totalW, totalH)
	fromRect, ok := rects[fromID]
	if !ok {
		return nil
	}

	fromCX := float64(fromRect.X) + float64(fromRect.W)/2
	fromCY := float64(fromRect.Y) + float64(fromRect.H)/2

	var best *LayoutNode
	bestDist := math.MaxFloat64

	for id, rect := range rects {
		if id == fromID {
			continue
		}
		cx := float64(rect.X) + float64(rect.W)/2
		cy := float64(rect.Y) + float64(rect.H)/2

		switch dir {
		case FocusLeft:
			if cx >= fromCX {
				continue
			}
			overlapY := overlap(fromRect.Y, fromRect.Y+fromRect.H, rect.Y, rect.Y+rect.H)
			if overlapY <= 0 {
				continue
			}
		case FocusRight:
			if cx <= fromCX {
				continue
			}
			overlapY := overlap(fromRect.Y, fromRect.Y+fromRect.H, rect.Y, rect.Y+rect.H)
			if overlapY <= 0 {
				continue
			}
		case FocusUp:
			if cy >= fromCY {
				continue
			}
			overlapX := overlap(fromRect.X, fromRect.X+fromRect.W, rect.X, rect.X+rect.W)
			if overlapX <= 0 {
				continue
			}
		case FocusDown:
			if cy <= fromCY {
				continue
			}
			overlapX := overlap(fromRect.X, fromRect.X+fromRect.W, rect.X, rect.X+rect.W)
			if overlapX <= 0 {
				continue
			}
		}

		dist := math.Hypot(cx-fromCX, cy-fromCY)
		if dist < bestDist {
			bestDist = dist
			node := n.find(id)
			best = node
		}
	}

	return best
}

func overlap(a1, a2, b1, b2 int) float64 {
	left := max(a1, b1)
	right := min(a2, b2)
	if right <= left {
		return 0
	}
	return float64(right - left)
}

type LayoutRect struct {
	X, Y, W, H int
}

func (n *LayoutNode) layout(width, height int) map[string]LayoutRect {
	rects := make(map[string]LayoutRect)
	n.layoutRecursive(0, 0, width, height, rects)
	return rects
}

func (n *LayoutNode) layoutRecursive(x, y, w, h int, rects map[string]LayoutRect) {
	if n.Leaf != nil {
		rects[n.Leaf.WindowID] = LayoutRect{X: x, Y: y, W: w, H: h}
		return
	}
	if n.Split == nil {
		return
	}

	switch n.Split.Direction {
	case SplitHorizontal:
		var leftH int
		if n.Split.FixedRight > 0 {
			leftH = h - n.Split.FixedRight
		} else {
			leftH = int(float64(h) * n.Split.Ratio)
		}
		if leftH < 1 {
			leftH = 1
		}
		n.Split.Left.layoutRecursive(x, y, w, leftH, rects)
		n.Split.Right.layoutRecursive(x, y+leftH, w, h-leftH, rects)
	default:
		var leftW int
		if n.Split.FixedRight > 0 {
			leftW = w - n.Split.FixedRight
		} else {
			leftW = int(float64(w) * n.Split.Ratio)
		}
		if leftW < 1 {
			leftW = 1
		}
		n.Split.Left.layoutRecursive(x, y, leftW, h, rects)
		n.Split.Right.layoutRecursive(x+leftW, y, w-leftW, h, rects)
	}
}
