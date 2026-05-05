package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
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
	}
}

func (w *agentWindow) ID() string   { return w.id }
func (w *agentWindow) Title() string { return w.title }

func (w *agentWindow) SetSize(width, height int) {
	w.width = width
	w.height = height
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
	return w, nil
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
	}
}

func (w *agentWindow) View(width, height int, focused bool) string {
	if width <= 0 {
		width = 60
	}
	if height <= 0 {
		height = 20
	}

	var b strings.Builder

	for _, msg := range w.messages {
		b.WriteString(w.formatMsg(msg, width))
	}

	if w.current != nil {
		b.WriteString(w.formatMsg(*w.current, width))
	}

	if w.streaming {
		b.WriteString(lipgloss.NewStyle().Faint(true).Render(w.spinner.View() + " working..."))
		b.WriteString("\n")
	} else if w.done {
		b.WriteString(agentDoneStyle.Render("✓ done"))
		b.WriteString(lipgloss.NewStyle().Faint(true).Render(" (x to close)"))
		b.WriteString("\n")
	}

	content := b.String()
	lines := strings.Split(content, "\n")

	maxLines := height
	if maxLines < 3 {
		maxLines = 3
	}
	if len(lines) > maxLines {
		start := len(lines) - maxLines
		lines = lines[start:]
	}
	content = strings.Join(lines, "\n")

	return content
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
		if len(content) > 120 {
			content = content[:117] + "..."
		}
		wrapped := wordwrap.String(content, width-4)
		lines := strings.Split(wrapped, "\n")
		if len(lines) > 4 {
			lines = lines[:4]
			lines = append(lines, "...")
		}
		for _, line := range lines {
			b.WriteString(agentUserStyle.Render("▸ " + line))
			b.WriteString("\n")
		}
	case "assistant":
		for _, block := range msg.Blocks {
			switch block.Type {
			case "content":
				text := block.Content
				if len(text) > 500 {
					text = text[:497] + "..."
				}
				wrapped := wordwrap.String(text, width-2)
				lines := strings.Split(wrapped, "\n")
				if len(lines) > 8 {
					lines = lines[:8]
					lines = append(lines, "...")
				}
				for _, line := range lines {
					b.WriteString(agentAsstStyle.Render(line))
					b.WriteString("\n")
				}
			case "tool_call":
				toolName := block.Name
				if len(toolName) > width-4 {
					toolName = toolName[:width-7] + "..."
				}
				b.WriteString(agentToolStyle.Render(fmt.Sprintf("▸ %s", toolName)))
				b.WriteString("\n")
			case "error":
				errText := block.Content
				if len(errText) > width-4 {
					errText = errText[:width-7] + "..."
				}
				b.WriteString(agentErrStyle.Render(fmt.Sprintf("✗ %s", errText)))
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}
