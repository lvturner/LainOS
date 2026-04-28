package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	prompt := flag.String("prompt", "", "one-shot prompt (non-interactive mode)")
	sessionID := flag.String("session", "", "session ID to resume")
	flag.Parse()

	args := flag.Args()
	profileName := "default"
	if len(args) > 0 {
		profileName = args[0]
	}

	profile, err := LoadProfile(profileName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading profile %q: %v\n", profileName, err)
		os.Exit(1)
	}

	mgr := NewMCPManager(profile.Servers)
	defer mgr.Close()

	registry := NewToolRegistry(mgr)
	registry.AddBuiltinTool(TodoTool)

	if *prompt != "" {
		registry.SetTodoStore(NewMemoryTodoStore())
		if err := RunOneShot(profile, registry, *prompt); err != nil {
			os.Exit(1)
		}
		return
	}

	registry.AddBuiltinTool(AskQuestionTool)

	m := NewTUI(profileName, profile, registry)
	if *sessionID != "" {
		if err := m.LoadSessionByID(*sessionID); err != nil {
			fmt.Fprintf(os.Stderr, "error loading session %q: %v\n", *sessionID, err)
			os.Exit(1)
		}
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
