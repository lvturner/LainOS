package main

import (
	tea "github.com/charmbracelet/bubbletea"
)

type FloatingWindow struct {
	Window
	X, Y   int
	W, H   int
	ZOrder int
}

func (fw *FloatingWindow) Update(msg tea.Msg) (Window, tea.Cmd) {
	return fw.Window.Update(msg)
}
