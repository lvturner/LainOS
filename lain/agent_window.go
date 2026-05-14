package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"
)

type agentWindow struct {
	id        string
	title     string
	messages  []ChatMessage
	current   *ChatMessage
	width     int
	height    int
	streaming bool
	done      bool
	spinner   spinner.Model
	vp        viewport.Model
	vpMu      sync.RWMutex
}

var (
	agentUserStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("86"))
	agentAsstStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("213"))
	agentToolStyle = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("243"))
	agentDoneStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("86"))
	agentErrStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)

func newAgentWindow(id, title string) *agentWindow {
	sp := spinner.New(spinner.WithSpinner(spinner.Spinner{
		Frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		FPS:    100 * time.Millisecond,
	}))
	return &agentWindow{
		id:      id,
		title:   title,
		spinner: sp,
		vp:      viewport.New(60, 20),
	}
}

func (w *agentWindow) ID() string   { return w.id }
func (w *agentWindow) Title() string { return w.title }

func (w *agentWindow) SetSize(width, height int) {
	w.width = width
	w.height = height
	if width <= 0 {
		width = 60
	}
	if height <= 0 {
		height = 20
	}
	w.vpMu.Lock()
	wasAtBottom := w.vp.AtBottom()
	w.vp = viewport.New(width, height)
	w.vp.SetContent(w.renderContent())
	if wasAtBottom {
		w.vp.GotoBottom()
	}
	w.vpMu.Unlock()
}

func (w *agentWindow) IsDone() bool     { return w.done }
func (w *agentWindow) IsStreaming() bool { return w.streaming }

func (w *agentWindow) Update(msg tea.Msg) (Window, tea.Cmd) {
	switch msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		w.spinner, cmd = w.spinner.Update(msg)
		if w.streaming {
			return w, cmd
		}
		return w, nil
	}
	w.vpMu.Lock()
	var cmd tea.Cmd
	w.vp, cmd = w.vp.Update(msg)
	w.vpMu.Unlock()
	return w, cmd
}

func (w *agentWindow) AppendEvent(event StreamEvent) {
	switch event.Type {
	case "token":
		if w.current == nil {
			w.current = &ChatMessage{Role: "assistant"}
		}
		if len(w.current.Blocks) > 0 && w.current.Blocks[len(w.current.Blocks)-1].Type == "content" {
			w.current.Blocks[len(w.current.Blocks)-1].Content += event.Content
		} else {
			w.current.Blocks = append(w.current.Blocks, MessageBlock{Type: "content", Content: event.Content})
		}
	case "tool_start":
		if w.current != nil {
			if len(w.current.Blocks) > 0 {
				last := &w.current.Blocks[len(w.current.Blocks)-1]
				if last.Type == "content" && !last.Done {
					last.Done = true
				}
			}
			w.current.Blocks = append(w.current.Blocks, MessageBlock{
				Type: "tool_call",
				Name: event.Content,
			})
		}
	case "tool_output":
		if w.current != nil && len(w.current.Blocks) > 0 {
			last := &w.current.Blocks[len(w.current.Blocks)-1]
			if last.Type == "tool_call" {
				last.Output = event.Content
			}
		}
	case "done":
		if w.current != nil {
			if len(w.current.Blocks) > 0 {
				last := &w.current.Blocks[len(w.current.Blocks)-1]
				if last.Type == "content" && !last.Done {
					last.Done = true
				}
			}
			if event.Content != "" {
				for i := range w.current.Blocks {
					if w.current.Blocks[i].Type == "content" && !w.current.Blocks[i].Done {
						w.current.Blocks[i].Content = event.Content
						break
					}
				}
			}
			w.messages = append(w.messages, *w.current)
			w.current = nil
		}
		w.streaming = false
		w.done = true
	case "error":
		if w.current != nil {
			if len(w.current.Blocks) > 0 {
				last := &w.current.Blocks[len(w.current.Blocks)-1]
				if last.Type == "content" && !last.Done {
					last.Done = true
				}
			}
			w.current.Blocks = append(w.current.Blocks, MessageBlock{
				Type:    "error",
				Content: event.Content,
				Done:    true,
			})
			w.messages = append(w.messages, *w.current)
			w.current = nil
		}
		w.streaming = false
		w.done = true
	case "cancelled":
		if w.current != nil {
			if len(w.current.Blocks) > 0 {
				last := &w.current.Blocks[len(w.current.Blocks)-1]
				if last.Type == "content" && !last.Done {
					last.Done = true
				}
			}
			w.messages = append(w.messages, *w.current)
			w.current = nil
		}
		w.streaming = false
		w.done = true
	case "injected":
		if w.current == nil {
			w.current = &ChatMessage{Role: "user"}
		}
		w.current.Blocks = append(w.current.Blocks, MessageBlock{Type: "content", Content: event.Content})
	case "user_message":
		if w.current != nil {
			w.messages = append(w.messages, *w.current)
			w.current = nil
		}
		w.current = &ChatMessage{Role: "user"}
		w.current.Blocks = append(w.current.Blocks, MessageBlock{Type: "content", Content: event.Content})
		w.messages = append(w.messages, *w.current)
		w.current = nil
	}
	w.refreshViewport()
}

func (w *agentWindow) refreshViewport() {
	w.vpMu.Lock()
	wasAtBottom := w.vp.AtBottom()
	w.vp.SetContent(w.renderContent())
	if wasAtBottom {
		w.vp.GotoBottom()
	}
	w.vpMu.Unlock()
}

func (w *agentWindow) renderContent() string {
	width := w.width
	if width <= 0 {
		width = 60
	}

	var b strings.Builder

	for _, msg := range w.messages {
		b.WriteString(w.formatMsg(msg, width))
		b.WriteString("\n")
	}

	if w.current != nil {
		b.WriteString(w.formatMsg(*w.current, width))
		b.WriteString("\n")
	}

	if w.streaming {
		b.WriteString(lipgloss.NewStyle().Faint(true).Render(w.spinner.View()+" working...") + "\n")
	} else if w.done {
		b.WriteString(agentDoneStyle.Render("✓ done"))
		b.WriteString(lipgloss.NewStyle().Faint(true).Render(" (x to close)"))
		b.WriteString("\n")
	}

	return b.String()
}

func (w *agentWindow) View(width, height int, focused bool) string {
	w.vpMu.RLock()
	defer w.vpMu.RUnlock()
	return w.vp.View()
}

func (w *agentWindow) formatMsg(msg ChatMessage, width int) string {
	if width <= 0 {
		width = 60
	}
	var b strings.Builder

	switch msg.Role {
	case "user":
		var content string
		for _, block := range msg.Blocks {
			if block.Type == "content" {
				content = block.Content
				break
			}
		}
		wrapped := wordwrap.String(content, width-4)
		top := agentUserStyle.Render("┌─ you " + strings.Repeat("─", max(0, width-8)) + "┐")
		lines := strings.Split(wrapped, "\n")
		mid := ""
		for _, line := range lines {
			mid += agentUserStyle.Render("│ "+line) + "\n"
		}
		bot := agentUserStyle.Render("└" + strings.Repeat("─", max(0, width-2)) + "┘")
		b.WriteString(top + "\n" + mid + bot)
	case "assistant":
		for i, block := range msg.Blocks {
			if i > 0 {
				b.WriteString("\n")
			}
			switch block.Type {
			case "content":
				if !block.Done {
					wrapped := wordwrap.String(block.Content, width)
					b.WriteString(agentAsstStyle.Render(wrapped))
				} else {
					rendered := renderMarkdown(block.Content, width)
					b.WriteString(rendered)
				}
			case "tool_call":
				b.WriteString(agentToolStyle.Render(fmt.Sprintf("▸ %s", block.Name)))
			case "error":
				b.WriteString(agentErrStyle.Render(fmt.Sprintf("[error: %s]", block.Content)))
			}
		}
	}
	return b.String()
}
