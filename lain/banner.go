package main

import "github.com/charmbracelet/lipgloss"

var bannerText = "  |          _)        _ \\   ___|  \n" +
	"  |      _` | | __ \\  |   |\\___ \\  \n" +
	"  |     (   | | |   | |   |      | \n" +
	" _____|\\__,_|_|_|  _|\\___/ _____/  "

func Banner(profile, model, session string) string {
	cyan := lipgloss.NewStyle().Foreground(lipgloss.Color("36")).Bold(true)
	dim := lipgloss.NewStyle().Faint(true)

	s := cyan.Render(bannerText)
	s += "\n"
	info := "profile: " + profile + "  model: " + model
	if session != "" && session != "New Session" {
		info += "  session: " + session
	}
	s += dim.Render("  " + info)
	return s
}
