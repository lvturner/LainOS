package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type Notification struct {
	ID        string
	Text      string
	ExpiresAt time.Time
}

type NotificationManager struct {
	notifications []Notification
	nextID       int
}

type notificationExpireMsg struct {
	ids []string
}

func NewNotificationManager() *NotificationManager {
	return &NotificationManager{}
}

func (nm *NotificationManager) Add(text string, duration time.Duration) string {
	nm.nextID++
	id := fmt.Sprintf("notif-%d", nm.nextID)
	nm.notifications = append(nm.notifications, Notification{
		ID:        id,
		Text:      text,
		ExpiresAt: time.Now().Add(duration),
	})
	return id
}

func (nm *NotificationManager) Expire() {
	now := time.Now()
	var active []Notification
	for _, n := range nm.notifications {
		if now.Before(n.ExpiresAt) {
			active = append(active, n)
		}
	}
	nm.notifications = active
}

func (nm *NotificationManager) HasActive() bool {
	return len(nm.notifications) > 0
}

func (nm *NotificationManager) Render(baseContent string, termWidth int) string {
	if len(nm.notifications) == 0 {
		return baseContent
	}

	baseLines := strings.Split(baseContent, "\n")

	notifStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("214")).
		Foreground(lipgloss.Color("214")).
		Padding(0, 1)

	y := 1
	for _, n := range nm.notifications {
		maxWidth := min(termWidth/2, 60)
		text := n.Text
		if len(text) > maxWidth {
			text = text[:maxWidth-3] + "..."
		}
		rendered := notifStyle.Render(text)
		notifLines := strings.Split(rendered, "\n")
		notifWidth := lipgloss.Width(rendered)
		x := termWidth - notifWidth - 2
		if x < 0 {
			x = 0
		}
		baseLines = overlayLines(baseLines, notifLines, x, y, termWidth)
		y += len(notifLines)
	}

	return strings.Join(baseLines, "\n")
}

func notificationTick() tea.Cmd {
	return tea.Tick(1*time.Second, func(t time.Time) tea.Msg {
		return notificationExpireMsg{}
	})
}
