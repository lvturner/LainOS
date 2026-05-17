package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m model) handleSlashCommand(input string) (tea.Model, tea.Cmd) {
	m.textarea.Reset()
	parts := strings.SplitN(input, " ", 3)
	cmd := parts[0]

	switch cmd {
	case "/quit", "/exit":
		m.autoSave()
		m.subAgentMgr.StopAll()
		return m, tea.Quit
	case "/new":
		m.autoSave()
		m.currentSession = NewSession(m.profileName)
		m.chat.messages = nil
		m.chat.current = nil
		m.llmClient = NewLLMClient(m.profile.Config, m.profile.Agents, m.profile.AgentsPath, m.registry.AllTools(), m.registry)
		m.pluginAPI.SetLLMClientGetter(func() *LLMClient { return m.llmClient })
		m.titleGenPending = true
		m.setupTodoStore()
		m.pluginAPI.SetSessionGetter(func() *Session { return m.currentSession })
		m.statusMsg = "New session started"
		m.refreshView()
		return m, m.clearStatus()
	case "/save":
		m.autoSave()
		m.statusMsg = "Session saved"
		m.refreshView()
		return m, m.clearStatus()
	case "/rename":
		if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
			m.statusMsg = "Usage: /rename <title>"
			m.refreshView()
			return m, m.clearStatus()
		}
		title := strings.TrimSpace(parts[1])
		if m.currentSession != nil {
			if err := RenameSession(m.currentSession, title); err != nil {
				m.statusMsg = "Error renaming session"
			} else {
				m.statusMsg = "Renamed to: " + title
			}
		}
		m.refreshView()
		return m, m.clearStatus()
	case "/sessions":
		m.openSessionPicker()
		return m, nil
	case "/compact":
		estimated, threshold := m.llmClient.CompactionInfo()
		if len(m.chat.messages) == 0 {
			m.statusMsg = "Nothing to compact (empty session)"
			m.refreshView()
			return m, m.clearStatus()
		}
		ctx := context.Background()
		if err := m.llmClient.CompactNow(ctx); err != nil {
			m.statusMsg = fmt.Sprintf("Compaction failed: %s", err)
		} else {
			estimated, threshold = m.llmClient.CompactionInfo()
			m.statusMsg = fmt.Sprintf("Context compacted (~%d/%d tokens)", estimated, threshold)
		}
		m.refreshView()
		return m, m.clearStatus()
	case "/plugins":
		if len(parts) >= 2 && parts[1] == "reload" {
			if len(parts) >= 3 && parts[2] != "" {
				name := strings.TrimSpace(parts[2])
				if err := m.pluginLoader.Reload(name); err != nil {
					m.statusMsg = err.Error()
				} else {
					m.statusMsg = "Plugin reloaded: " + name
				}
			} else {
				m.pluginLoader.ReloadAll()
				m.statusMsg = "All plugins reloaded"
			}
		} else if len(parts) >= 2 && parts[1] == "start" {
			if len(parts) >= 3 && parts[2] != "" {
				name := strings.TrimSpace(parts[2])
				if !strings.HasSuffix(name, ".lua") {
					name += ".lua"
				}
				m.pluginLoader.Load(name)
				pluginName := strings.TrimSuffix(name, ".lua")
				if m.pluginLoader.IsLoaded(pluginName) {
					m.statusMsg = "Plugin started: " + pluginName
				} else {
					m.statusMsg = "Failed to start plugin: " + pluginName
				}
			} else {
				m.pluginLoader.ReloadAll()
				m.statusMsg = "All plugins started"
			}
		} else if len(parts) >= 2 && parts[1] == "stop" {
			if len(parts) >= 3 && parts[2] != "" {
				name := strings.TrimSpace(parts[2])
				filename := name
				if !strings.HasSuffix(filename, ".lua") {
					filename += ".lua"
				}
				m.pluginLoader.Unload(filename)
				m.statusMsg = "Plugin stopped: " + name
			} else {
				m.statusMsg = "Usage: /plugins stop <name>"
			}
		} else {
			plugins := m.pluginLoader.ListPlugins()
			if len(plugins) == 0 {
				m.statusMsg = "No plugins loaded"
			} else {
				m.statusMsg = "Plugins: " + strings.Join(plugins, ", ")
			}
		}
		m.refreshView()
		return m, m.clearStatus()
	case "/windows":
		windows := m.wm.ListWindows()
		focused := m.wm.FocusedID()
		var items []string
		for _, id := range windows {
			prefix := "  "
			if id == focused {
				prefix = "▸ "
			}
			w := m.wm.Get(id)
			if w != nil {
				items = append(items, prefix+w.ID()+": "+w.Title())
			}
		}
		if len(items) == 0 {
			m.statusMsg = "No windows"
		} else {
			m.statusMsg = "Windows: " + strings.Join(items, " | ")
		}
		m.refreshView()
		return m, m.clearStatus()
	case "/float":
		if len(parts) < 2 {
			m.statusMsg = "Usage: /float <window-id>"
			m.refreshView()
			return m, m.clearStatus()
		}
		id := strings.TrimSpace(parts[1])
		win := m.wm.Get(id)
		if win == nil {
			m.statusMsg = "Window not found: " + id
			m.refreshView()
			return m, m.clearStatus()
		}
		if m.wm.HasFloating(id) {
			m.wm.RemoveFloating(id)
			targetID := m.wm.FocusedID()
			if targetID == "" || targetID == id {
				targetID = "chat"
			}
			m.wm.AddWithSplit(targetID, SplitVertical, win, 0)
			m.wm.SetSize(m.width, m.wmHeight())
			m.statusMsg = "Window tiled: " + id
		} else {
			m.wm.Remove(id)
			w := 40
			h := 12
			x := (m.width - w) / 2
			y := (m.wmHeight() - h) / 2
			m.wm.AddFloating(win, x, y, w, h)
			m.statusMsg = "Window floated: " + id
		}
		m.refreshView()
		return m, m.clearStatus()
	case "/ask":
		question := ""
		if len(parts) >= 2 {
			question = strings.Join(parts[1:], " ")
		}
		if strings.TrimSpace(question) == "" {
			m.statusMsg = "Usage: /ask <question>"
			m.refreshView()
			return m, m.clearStatus()
		}
		m.profile.Config.AskModeDefaults()
		cfg := m.profile.Config.AskMode
		agentID := fmt.Sprintf("ask-%d", time.Now().UnixNano())
		opts := SubAgentOpts{
			ID:           agentID,
			Title:        "Ask: " + question,
			ProfileName:  m.profileName,
			SystemPrompt: cfg.SystemPrompt,
			InitialMsg:   question,
			AskMode:      true,
			AskModeCfg:   cfg,
		}
		agent, err := m.subAgentMgr.Spawn(opts)
		if err != nil {
			m.statusMsg = fmt.Sprintf("Failed to spawn ask agent: %s", err)
			m.refreshView()
			return m, m.clearStatus()
		}
		m.statusMsg = "Ask agent started"
		m.refreshView()
		return m, tea.Batch(waitForSubAgentEvent(agent.id, agent.streamCh), m.clearStatus())
	default:
		pluginCmds := m.pluginAPI.GetCommandCallbacks()
		if fn, ok := pluginCmds[cmd]; ok {
			arg := ""
			if len(parts) >= 2 {
				arg = strings.Join(parts[1:], " ")
			}
			fn(arg)
			return m, nil
		}
		m.statusMsg = "Unknown command: " + cmd
		m.refreshView()
		return m, m.clearStatus()
	}
}
