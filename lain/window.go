package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type Window interface {
	ID() string
	Title() string
	Update(tea.Msg) (Window, tea.Cmd)
	View(width, height int, focused bool) string
	SetSize(width, height int)
}

type chatWindow struct {
	vp           viewport.Model
	messages     []ChatMessage
	current      *ChatMessage
	messageQueue []ChatMessage
	atBottom     bool
	newBelow     bool
	chatWidth    int
	height       int
}

func newChatWindow() *chatWindow {
	return &chatWindow{
		vp:       viewport.New(80, 20),
		messages: []ChatMessage{},
	}
}

func (w *chatWindow) ID() string   { return "chat" }
func (w *chatWindow) Title() string { return "Chat" }

func (w *chatWindow) SetSize(width, height int) {
	w.chatWidth = width
	w.height = height
	if w.chatWidth <= 0 {
		w.chatWidth = 80
	}
	w.resizeViewport(height)
}

func (w *chatWindow) Update(msg tea.Msg) (Window, tea.Cmd) {
	var cmd tea.Cmd
	w.vp, cmd = w.vp.Update(msg)
	w.atBottom = w.vp.AtBottom()
	if w.atBottom {
		w.newBelow = false
	}
	return w, cmd
}

func (w *chatWindow) View(width, height int, focused bool) string {
	return w.vp.View()
}

func (w *chatWindow) refreshView() {
	wasAtBottom := w.vp.AtBottom()
	w.vp.SetContent(w.renderMessages())
	if wasAtBottom {
		w.vp.GotoBottom()
		w.newBelow = false
	} else {
		w.newBelow = true
	}
	w.atBottom = w.vp.AtBottom()
}

func (w *chatWindow) resizeViewport(h int) {
	if h < 5 {
		h = 5
	}
	wasAtBottom := w.vp.AtBottom()
	w.vp = viewport.New(w.chatWidth, h)
	w.vp.SetContent(w.renderMessages())
	if wasAtBottom {
		w.vp.GotoBottom()
	}
}

func (w *chatWindow) fullRedraw() {
	for i := range w.messages {
		w.messages[i].rendered = ""
	}
	w.resizeViewport(w.height)
}

func (w *chatWindow) renderMessages() string {
	var b strings.Builder
	for i := range w.messages {
		msg := &w.messages[i]
		if msg.rendered == "" {
			msg.rendered = formatMessage(*msg, w.chatWidth)
		}
		b.WriteString(msg.rendered)
		b.WriteString("\n\n")
	}
	if w.current != nil {
		b.WriteString(formatMessage(*w.current, w.chatWidth))
		b.WriteString("\n\n")
	}
	for _, qm := range w.messageQueue {
		b.WriteString(formatQueuedMessage(qm, w.chatWidth))
		b.WriteString("\n\n")
	}
	return b.String()
}

type todoWindow struct {
	registry *ToolRegistry
}

func newTodoWindow(registry *ToolRegistry) *todoWindow {
	return &todoWindow{registry: registry}
}

func (w *todoWindow) ID() string   { return "todo" }
func (w *todoWindow) Title() string { return "Tasks" }

func (w *todoWindow) SetSize(width, height int) {}

func (w *todoWindow) Update(msg tea.Msg) (Window, tea.Cmd) {
	return w, nil
}

func (w *todoWindow) View(width, height int, focused bool) string {
	var b strings.Builder

	header := " Tasks"
	b.WriteString(todoHeaderStyle.Render(header))
	padLen := width - len(header)
	if padLen > 0 {
		b.WriteString(strings.Repeat(" ", padLen))
	}
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Faint(true).Render(strings.Repeat("─", width)))
	b.WriteString("\n")

	store := w.registry.GetTodoStore()
	if store == nil || len(store.List()) == 0 {
		b.WriteString(todoEmptyStyle.Render("  No tasks"))
		b.WriteString("\n")
	} else {
		items := store.List()
		bodyLines := height - 2
		for i, item := range items {
			if i >= bodyLines-1 && len(items) > bodyLines {
				remaining := len(items) - i
				moreLine := fmt.Sprintf("  … %d more", remaining)
				b.WriteString(todoEmptyStyle.Render(moreLine))
				b.WriteString("\n")
				break
			}
			marker := "☐"
			style := todoPendingStyle
			if item.Completed {
				marker = "☑"
				style = todoDoneStyle
			}
			line := fmt.Sprintf(" %s %d. %s", marker, item.ID, item.Task)
			if len(line) > width {
				line = line[:width-1] + "…"
			}
			b.WriteString(style.Render(line))
			b.WriteString("\n")
		}
	}

	linesWritten := strings.Count(b.String(), "\n")
	for i := linesWritten; i < height; i++ {
		b.WriteString(strings.Repeat(" ", width) + "\n")
	}

	return lipgloss.NewStyle().
		Border(lipgloss.Border{Left: "│"}, false, false, false, true).
		BorderForeground(lipgloss.Color("243")).
		Width(width).
		Height(height).
		Render(strings.TrimSuffix(b.String(), "\n"))
}
