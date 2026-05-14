package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	prompt := flag.String("prompt", "", "one-shot prompt (non-interactive mode)")
	ask := flag.String("ask", "", "ask mode: read-only research question (non-interactive)")
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

	if *ask != "" {
		profile.Config.AskModeDefaults()
		cfg := profile.Config.AskMode
		mgr := NewMCPManager(filterMCPServers(profile.Servers, cfg.Tools.MCPServers))
		defer mgr.Close()
		registry := NewToolRegistry(mgr)
		sandbox := NewSandboxedExecutor(cfg.Sandbox.TmpSize)
		registry.AddBuiltinTool(RestrictedCommandTool)
		tools := registry.FilteredTools(cfg.Tools.Builtin, cfg.Tools.MCPServers)
		registry.SetAllowedTools(cfg.Tools.Builtin, sandbox)
		systemPrompt := cfg.SystemPrompt
		if profile.Agents != "" {
			systemPrompt = profile.Agents + "\n\n" + systemPrompt
		}
		client := NewLLMClient(profile.Config, systemPrompt, profile.AgentsPath, tools, registry)
		ch := client.Chat(context.Background(), *ask)
		for event := range ch {
			switch event.Type {
			case "token":
				fmt.Print(event.Content)
			case "tool_start":
				fmt.Fprintf(os.Stderr, "▸ %s\n", event.Content)
			case "tool_output":
				fmt.Fprintf(os.Stderr, "  %s\n", event.Content)
			case "error":
				fmt.Fprintf(os.Stderr, "error: %s\n", event.Content)
				os.Exit(1)
			case "ask_question":
				if event.ResponseCh != nil {
					event.ResponseCh <- "Cannot ask questions in non-interactive mode."
				}
			case "done":
				fmt.Println()
			}
		}
		return
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

func filterMCPServers(servers *ServersConfig, allow []string) *ServersConfig {
	if len(allow) == 0 {
		return &ServersConfig{MCPServers: make(map[string]MCPServerConfig)}
	}
	filtered := &ServersConfig{MCPServers: make(map[string]MCPServerConfig)}
	allowSet := make(map[string]bool, len(allow))
	for _, name := range allow {
		allowSet[name] = true
	}
	for name, cfg := range servers.MCPServers {
		if allowSet[name] {
			filtered.MCPServers[name] = cfg
		}
	}
	return filtered
}
