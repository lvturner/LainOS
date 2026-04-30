package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type SessionPickerState struct {
	sessions    []Session
	filtered    []Session
	cursor      int
	filter      string
	width       int
	height      int
	showPreview bool
	Selected    *Session
	Cancelled   bool
}

func NewSessionPickerState(sessions []Session, width, height int) SessionPickerState {
	return SessionPickerState{
		sessions:    sessions,
		filtered:    sessions,
		cursor:      0,
		filter:      "",
		width:       width,
		height:      height,
		showPreview: width >= 120,
	}
}

func (s *SessionPickerState) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			s.Cancelled = true
			return nil
		case "enter":
			if len(s.filtered) > 0 && s.cursor < len(s.filtered) {
				sel := s.filtered[s.cursor]
				s.Selected = &sel
			} else {
				s.Cancelled = true
			}
			return nil
		case "up", "ctrl+p":
			if s.cursor > 0 {
				s.cursor--
			}
			return nil
		case "down", "ctrl+n":
			if s.cursor < len(s.filtered)-1 {
				s.cursor++
			}
			return nil
		case "tab":
			s.showPreview = !s.showPreview
			return nil
		case "backspace":
			if len(s.filter) > 0 {
				s.filter = s.filter[:len(s.filter)-1]
				s.applyFilter()
				s.cursor = 0
			}
			return nil
		default:
			ch := msg.String()
			if len(ch) == 1 && ch[0] >= 32 && ch[0] < 127 {
				s.filter += ch
				s.applyFilter()
				s.cursor = 0
			}
			return nil
		}
	}
	return nil
}

func (s *SessionPickerState) applyFilter() {
	if s.filter == "" {
		s.filtered = s.sessions
		return
	}
	lower := strings.ToLower(s.filter)
	var filtered []Session
	for _, sess := range s.sessions {
		if strings.Contains(strings.ToLower(sess.Title), lower) {
			filtered = append(filtered, sess)
		}
	}
	s.filtered = filtered
}

func (s *SessionPickerState) View(width int) string {
	h := s.height
	if h < 10 {
		h = 10
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86"))
	dimStyle := lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("243"))
	cursorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true).Background(lipgloss.Color("237"))
	cursorDimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color("237"))
	previewStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))

	var b strings.Builder

	b.WriteString(titleStyle.Render("Sessions"))
	b.WriteString("\n")

	if s.filter != "" {
		b.WriteString("Filter: " + s.filter + "█\n")
	} else {
		b.WriteString(dimStyle.Render("Filter: type to search...\n"))
	}
	b.WriteString("\n")

	linesPerEntry := 3

	listH := h - 6
	if listH < 2 {
		listH = 2
	}
	listH = max(1, listH/linesPerEntry)

	if s.showPreview {
		listH = max(1, listH/2)
	}

	if len(s.filtered) == 0 {
		b.WriteString(dimStyle.Render("No sessions found"))
	} else {
		n := len(s.filtered)
		start := max(0, min(s.cursor-listH+1, n-listH))
		end := min(start+listH, n)

	for i := start; i < end; i++ {
		sess := s.filtered[i]
		relTime := relativeTime(sess.Updated)

		if i == s.cursor {
			b.WriteString(cursorStyle.Render("▸ " + sess.Title))
			b.WriteString("\n")
			b.WriteString(cursorDimStyle.Render("  " + relTime))
		} else {
			b.WriteString("  " + sess.Title)
			b.WriteString("\n")
			b.WriteString(dimStyle.Render("  " + relTime))
		}
		if i < end-1 {
			b.WriteString("\n")
		}
	}
	}

	if s.showPreview && len(s.filtered) > 0 && s.cursor < len(s.filtered) {
		sess := s.filtered[s.cursor]
		preview := SessionPreview(sess.FilePath, 800)
		b.WriteString("\n")
		sepW := max(0, width-12)
		b.WriteString(dimStyle.Render("── Preview " + strings.Repeat("─", sepW)))
		b.WriteString("\n")

		previewLines := strings.Split(preview, "\n")
		maxPreview := max(3, h/2-3)
		for i, line := range previewLines {
			if i >= maxPreview {
				b.WriteString(dimStyle.Render("..."))
				break
			}
			if width > 0 && len(line) > width-4 {
				line = line[:width-7] + "..."
			}
			b.WriteString(previewStyle.Render(line))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render("↑/↓ navigate · Enter load · Tab preview · Esc cancel"))

	return b.String()
}

func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}
