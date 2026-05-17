package main

import (
	"github.com/charmbracelet/lipgloss"
)

const todoSidebarW = 28

var (
	userStyle            = lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true)
	assistantStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("213"))
	toolStyle            = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("243"))
	errorStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	inputStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("86"))
	compactionStyle      = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("214"))
	questionModalStyle   = lipgloss.NewStyle().Padding(0, 1)
	statusStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	ctxStyle             = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("243"))
	scrollIndicatorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Faint(true)
	todoHeaderStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86"))
	todoPendingStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	todoDoneStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("60")).Faint(true)
	todoEmptyStyle       = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("243"))
	normalModeStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	pluginStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("183")).Italic(true)
)
