package main

import (
	"context"
	"fmt"
	"os"
)

func RunOneShot(profile *Profile, registry *ToolRegistry, prompt string) error {
	tools := registry.AllTools()
	client := NewLLMClient(profile.Config, profile.Agents, profile.AgentsPath, tools, registry)
	ch := client.Chat(context.Background(), prompt)
	for event := range ch {
		switch event.Type {
		case "token":
			fmt.Print(event.Content)
		case "tool_start":
			fmt.Fprintf(os.Stderr, "▸ %s\n", event.Content)
		case "tool_output":
			fmt.Fprintf(os.Stderr, "  %s\n", event.Content)
		case "compacting":
			fmt.Fprintf(os.Stderr, "⟳ %s\n", event.Content)
		case "compacted":
			fmt.Fprintf(os.Stderr, "⟳ %s\n", event.Content)
		case "compaction_failed":
			fmt.Fprintf(os.Stderr, "⟳ %s\n", event.Content)
		case "nudge":
			fmt.Fprintf(os.Stderr, "⟳ %s\n", event.Content)
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
	return nil
}
