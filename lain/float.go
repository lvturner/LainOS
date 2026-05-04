package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type FloatingWindow struct {
	Window
	X, Y   int
	W, H   int
	ZOrder int
}

func (wm *WindowManager) AddFloating(w Window, x, y, width, height int) {
	fw := &FloatingWindow{
		Window: w,
		X:      x,
		Y:      y,
		W:      width,
		H:      height,
		ZOrder: len(wm.floating),
	}
	wm.floating = append(wm.floating, fw)
	wm.focused = w.ID()
	w.SetSize(width, height)
}

func (wm *WindowManager) RemoveFloating(id string) {
	for i, fw := range wm.floating {
		if fw.ID() == id {
			wm.floating = append(wm.floating[:i], wm.floating[i+1:]...)
			if wm.focused == id {
				if len(wm.focusOrder) > 0 {
					wm.focused = wm.focusOrder[0]
				}
			}
			return
		}
	}
}

func (wm *WindowManager) HasFloating(id string) bool {
	for _, fw := range wm.floating {
		if fw.ID() == id {
			return true
		}
	}
	return false
}

func (wm *WindowManager) FloatingView(termWidth, termHeight int) string {
	var b strings.Builder
	for _, fw := range wm.floating {
		boxStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("86")).
			Width(fw.W).
			Height(fw.H)

		titleBar := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("86")).
			Width(fw.W - 2).
			Render(fw.Title())

		content := fw.View(fw.W, fw.H, true)

		boxContent := lipgloss.JoinVertical(lipgloss.Left, titleBar, content)
		rendered := boxStyle.Render(boxContent)

		rendered = ansiMoveTo(fw.Y, fw.X, rendered)
		b.WriteString(rendered)
	}
	return b.String()
}

func ansiMoveTo(row, col int, s string) string {
	return fmt.Sprintf("\x1b[%d;%dH%s", row, col, s)
}

func (fw *FloatingWindow) Update(msg tea.Msg) (Window, tea.Cmd) {
	return fw.Window.Update(msg)
}
