package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type OptionDef struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type QuestionPayload struct {
	Question string      `json:"question"`
	Options  []OptionDef `json:"options"`
	Multiple bool        `json:"multiple"`
}

type QuestionState struct {
	Question    string
	Options     []OptionDef
	Multiple    bool
	Cursor      int
	Selected    map[int]bool
	CustomInput textarea.Model
	FocusArea   string
	ResponseCh  chan<- string
}

func NewQuestionState(payload QuestionPayload, responseCh chan<- string) QuestionState {
	ci := textarea.New()
	ci.Placeholder = "Type your answer..."
	ci.Prompt = ""
	ci.CharLimit = 0
	ci.SetHeight(1)
	ci.ShowLineNumbers = false

	focusArea := "options"
	if len(payload.Options) == 0 {
		focusArea = "custom"
		ci.Focus()
	}

	return QuestionState{
		Question:    payload.Question,
		Options:     payload.Options,
		Multiple:    payload.Multiple,
		Cursor:      0,
		Selected:    make(map[int]bool),
		CustomInput: ci,
		FocusArea:   focusArea,
		ResponseCh:  responseCh,
	}
}

func (q *QuestionState) Update(msg tea.Msg) (tea.Cmd, string) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return nil, "__cancelled__"
		case "tab":
			if len(q.Options) > 0 {
				if q.FocusArea == "options" {
					q.FocusArea = "custom"
					q.CustomInput.Focus()
				} else {
					q.FocusArea = "options"
					q.CustomInput.Blur()
				}
			}
			return nil, ""
		case "up":
			if q.FocusArea == "options" && len(q.Options) > 0 {
				if q.Cursor > 0 {
					q.Cursor--
				}
			}
			return nil, ""
		case "down":
			if q.FocusArea == "options" && len(q.Options) > 0 {
				if q.Cursor < len(q.Options)-1 {
					q.Cursor++
				}
			}
			return nil, ""
		case " ":
			if q.FocusArea == "options" && q.Multiple {
				q.Selected[q.Cursor] = !q.Selected[q.Cursor]
				return nil, ""
			}
		case "enter":
			return nil, q.buildAnswer()
		}
	}

	if q.FocusArea == "custom" {
		var cmd tea.Cmd
		q.CustomInput, cmd = q.CustomInput.Update(msg)
		return cmd, ""
	}
	return nil, ""
}

func (q *QuestionState) buildAnswer() string {
	var parts []string

	for i, opt := range q.Options {
		if q.Selected[i] {
			parts = append(parts, opt.Label)
		}
	}

	if !q.Multiple && q.FocusArea == "options" && len(q.Options) > 0 {
		parts = nil
		parts = append(parts, q.Options[q.Cursor].Label)
	}

	custom := strings.TrimSpace(q.CustomInput.Value())
	if custom != "" {
		parts = append(parts, custom)
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ")
}

func (q *QuestionState) EstimatedHeight(width int) int {
	h := 3
	h += len(q.Options)
	if len(q.Options) > 0 {
		h++
	}
	h += 3
	return h
}

func (q *QuestionState) View(width int) string {
	var b strings.Builder

	questionStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213")).Width(width - 4)
	b.WriteString(questionStyle.Render(q.Question))
	b.WriteString("\n\n")

	for i, opt := range q.Options {
		cursor := "  "
		if q.FocusArea == "options" && i == q.Cursor {
			cursor = "> "
		}

		var checkbox string
		if q.Multiple {
			if q.Selected[i] {
				checkbox = "[x]"
			} else {
				checkbox = "[ ]"
			}
		} else {
			if q.FocusArea == "options" && i == q.Cursor {
				checkbox = "(●)"
			} else {
				checkbox = "(○)"
			}
		}

		optStyle := lipgloss.NewStyle()
		if q.FocusArea == "options" && i == q.Cursor {
			optStyle = optStyle.Bold(true).Foreground(lipgloss.Color("86"))
		}

		line := fmt.Sprintf("%s%s %s", cursor, checkbox, opt.Label)
		if opt.Description != "" {
			descStyle := lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("243"))
			line += " " + descStyle.Render("- "+opt.Description)
		}
		b.WriteString(optStyle.Render(line))
		b.WriteString("\n")
	}

	if len(q.Options) > 0 {
		b.WriteString("\n")
	}

	labelStyle := lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("243"))
	if len(q.Options) == 0 {
		b.WriteString(labelStyle.Render("Your answer:"))
	} else {
		b.WriteString(labelStyle.Render("Custom answer:"))
	}
	b.WriteString("\n")
	b.WriteString(q.CustomInput.View())
	b.WriteString("\n")

	helpStyle := lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("243"))
	switch {
	case q.Multiple:
		b.WriteString(helpStyle.Render("Space: toggle · ↑↓: navigate · Tab: switch · Enter: confirm · Esc: cancel"))
	case len(q.Options) > 0:
		b.WriteString(helpStyle.Render("↑↓: navigate · Tab: switch to input · Enter: select · Esc: cancel"))
	default:
		b.WriteString(helpStyle.Render("Enter: submit · Esc: cancel"))
	}

	return b.String()
}
