package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"
)

func (m model) View() string {
	sessionTitle := ""
	if m.currentSession != nil {
		sessionTitle = m.currentSession.Title
	}

	if !m.ready {
		return "\n" + Banner(m.profileName, m.profile.Config.Model, sessionTitle) + "\n\n  Loading..."
	}
	banner := Banner(m.profileName, m.profile.Config.Model, sessionTitle)
	sep := lipgloss.NewStyle().Faint(true).Render(strings.Repeat("─", m.width))

	if m.sessionPicker != nil {
		return lipgloss.JoinVertical(lipgloss.Left,
			banner,
			m.sessionPicker.View(m.width),
		)
	}

	if m.question != nil {
		questionView := questionModalStyle.Render(m.question.View(m.width))
		return lipgloss.JoinVertical(lipgloss.Left,
			banner,
			m.wm.View(),
			sep,
			questionView,
		)
	}

	var statusBar string
	if m.pendingFixErr != nil {
		truncErr := m.pendingFixErr.Error
		if len(truncErr) > 60 {
			truncErr = truncErr[:60] + "..."
		}
		statusBar = errorStyle.Render(fmt.Sprintf(
			"⚠ plugin %q: %s — F:fix  C:chat  Esc:dismiss",
			m.pendingFixErr.PluginName, truncErr))
	} else if m.statusMsg != "" {
		statusBar = statusStyle.Render("  " + m.statusMsg)
	} else if m.mode == normalMode {
		statusBar = normalModeStyle.Render("  -- NORMAL --")
	} else if m.streaming {
		statusBar = statusStyle.Render("  " + m.spinner.View() + " thinking...")
	} else {
		pct := m.llmClient.ContextPercent()
		ctxLen := m.llmClient.ContextLength()
		ctxLabel := fmt.Sprintf("ctx: %d%% (%dk/%dk)", pct, pct*ctxLen/100/1000, ctxLen/1000)
		statusBar = ctxStyle.Render("  " + ctxLabel)
	}

	inputArea := m.textarea.View()
	focusedID := m.wm.FocusedID()
	if focusedID != "" && focusedID != "chat" && focusedID != "todo" && m.subAgentMgr.HasActive(focusedID) {
		agentPrompt := lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true).Render("▸ ask ")
		inputArea = strings.Replace(inputArea, "> ", agentPrompt, 1)
	}
	inputArea = inputStyle.Render(inputArea)

	parts := []string{banner, m.wm.View()}
	if m.chat.newBelow {
		parts = append(parts, scrollIndicatorStyle.Render("  ↓ new messages (end to jump)"))
	}
	parts = append(parts, sep, statusBar, inputArea)
	result := lipgloss.JoinVertical(lipgloss.Left, parts...)
	result = m.wm.FloatingView(result, m.width)
	if m.notifications.HasActive() {
		result = m.notifications.Render(result, m.width)
	}
	return result
}

func formatMessage(msg ChatMessage, width int) string {
	if width <= 0 {
		width = 80
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
		top := userStyle.Render("┌─ you " + strings.Repeat("─", max(0, width-8)) + "┐")
		lines := strings.Split(wrapped, "\n")
		mid := ""
		for _, line := range lines {
			mid += userStyle.Render("│ " + line) + "\n"
		}
		bot := userStyle.Render("└" + strings.Repeat("─", max(0, width-2)) + "┘")
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
					b.WriteString(assistantStyle.Render(wrapped))
				} else {
					rendered := renderMarkdown(block.Content, width)
					b.WriteString(rendered)
				}
			case "tool_call":
				b.WriteString(toolStyle.Render(fmt.Sprintf("▸ %s", block.Name)))
			case "compaction":
				b.WriteString(compactionStyle.Render(fmt.Sprintf("⟳ %s", block.Content)))
			case "error":
				b.WriteString(errorStyle.Render(fmt.Sprintf("[error: %s]", block.Content)))
			}
		}
	case "plugin":
		for _, block := range msg.Blocks {
			if block.Type == "content" {
				wrapped := wordwrap.String(block.Content, width)
				b.WriteString(pluginStyle.Render(wrapped))
			}
		}
	}
	return b.String()
}

func formatQueuedMessage(msg ChatMessage, width int) string {
	if width <= 0 {
		width = 80
	}
	var content string
	for _, block := range msg.Blocks {
		if block.Type == "content" {
			content = block.Content
			break
		}
	}
	wrapped := wordwrap.String(content, width-4)
	top := userStyle.Faint(true).Render("┌─ queued " + strings.Repeat("─", max(0, width-12)) + "┐")
	lines := strings.Split(wrapped, "\n")
	mid := ""
	for _, line := range lines {
		mid += userStyle.Faint(true).Render("│ " + line) + "\n"
	}
	bot := userStyle.Faint(true).Render("└" + strings.Repeat("─", max(0, width-2)) + "┘")
	return top + "\n" + mid + bot
}
