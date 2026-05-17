package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.sessionPicker != nil {
		return m.handleSessionPicker(msg)
	}
	if m.question != nil {
		return m.handleQuestion(msg)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.handleWindowSize(msg)
	case streamBatchMsg:
		return m.handleStreamBatch(msg.events)
	case titleGeneratedMsg:
		return m.handleTitleGenerated(msg)
	case statusClearMsg:
		m.statusMsg = ""
		m.chat.refreshView()
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.streaming {
			return m, cmd
		}
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case pluginEventMsg:
		switch msg.action {
		case "reload":
			m.pluginLoader.Load(msg.filename)
			name := strings.TrimSuffix(msg.filename, filepath.Ext(msg.filename))
			if m.pluginLoader.IsLoaded(name) && m.subAgentMgr.HasActive(m.fixAgentID) {
				m.subAgentMgr.EnqueueMessage(m.fixAgentID,
					fmt.Sprintf("Plugin %q reloaded successfully after your fix.", name))
				m.notifications.Add(fmt.Sprintf("✓ Plugin %q fixed and reloaded", name), 5*time.Second)
			}
		case "remove":
			m.pluginLoader.Unload(msg.filename)
		}
		return m, waitForPluginEvent(m.pluginLoader.Events())
	case pluginErrorMsg:
		var cmd tea.Cmd
		if m.profile.Config.SubAgent.AutoFix {
			cmd = m.spawnFixAgent(msg.err)
		} else {
			m.pendingFixErr = &msg.err
			m.mode = normalMode
			m.textarea.Blur()
		}
		return m, tea.Batch(cmd, waitForPluginError(m.pluginLoader.Errors()))
	case subAgentEventMsg:
		agent := m.subAgentMgr.agents[msg.agentID]
		if agent != nil && agent.window != nil {
			agent.window.AppendEvent(msg.event)
			if msg.event.Type == "done" || msg.event.Type == "error" {
				agent.window.streaming = false
				agent.window.done = true
			}
		}
		if msg.event.Type == "done" || msg.event.Type == "error" || msg.event.Type == "cancelled" {
			return m, nil
		}
		if agent != nil && agent.streamCh != nil {
			return m, waitForSubAgentEvent(msg.agentID, agent.streamCh)
		}
		return m, nil
	case notificationExpireMsg:
		m.notifications.Expire()
		if m.notifications.HasActive() {
			return m, notificationTick()
		}
		return m, nil
	case pluginRenderMsg:
		pw := m.pluginAPI.pluginWindows[msg.windowID]
		if pw != nil {
			pw.renderMu.Lock()
			pw.cachedRender = msg.content
			pw.renderMu.Unlock()
		}
		return m, waitForPluginRenderResult(m.pluginAPI.RenderResultChannel())
	case pluginChatSendMsg:
		m.chat.messages = append(m.chat.messages, ChatMessage{
			Role:   "plugin",
			Blocks: []MessageBlock{{Type: "content", Content: msg.content}},
		})
		m.chat.vp.GotoBottom()
		m.chat.refreshView()
		return m, waitForPluginChatMsg(m.pluginAPI.ChatMsgChannel())
	case pluginRenderTickMsg:
		for _, pw := range m.pluginAPI.pluginWindows {
			if pw.renderDirty && pw.executor != nil {
				pw.triggerRender(m.pluginAPI.RenderResultChannel())
			}
		}
		return m, pluginRenderTick()
	}

	var cmd tea.Cmd
	_, cmd = m.chat.Update(msg)
	m.chat.atBottom = m.chat.vp.AtBottom()
	if m.chat.atBottom {
		m.chat.newBelow = false
	}
	return m, cmd
}

func (m model) handleWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.width = msg.Width
	m.height = msg.Height
	wmHeight := m.wmHeight()
	m.wm.SetSize(msg.Width, wmHeight)
	m.chat.fullRedraw()
	m.textarea.SetWidth(msg.Width)
	m.pluginAPI.SetSessionGetter(func() *Session { return m.currentSession })
	m.ready = true
	return m, tea.ClearScreen
}

func (m *model) finalizeLastContent() {
	if m.chat.current == nil || len(m.chat.current.Blocks) == 0 {
		return
	}
	last := &m.chat.current.Blocks[len(m.chat.current.Blocks)-1]
	if last.Type == "content" && !last.Done {
		last.Done = true
	}
}

func (m model) handleStreamEvent(event StreamEvent) (model, tea.Cmd) {
	switch event.Type {
	case "token":
		if m.chat.current != nil {
			if len(m.chat.current.Blocks) > 0 && m.chat.current.Blocks[len(m.chat.current.Blocks)-1].Type == "content" {
				m.chat.current.Blocks[len(m.chat.current.Blocks)-1].Content += event.Content
			} else {
				m.chat.current.Blocks = append(m.chat.current.Blocks, MessageBlock{Type: "content", Content: event.Content})
			}
		}
	case "tool_start":
		if m.chat.current != nil {
			m.finalizeLastContent()
			m.chat.current.Blocks = append(m.chat.current.Blocks, MessageBlock{
				Type: "tool_call",
				Name: event.Content,
			})
		}
	case "tool_output":
		if m.chat.current != nil && len(m.chat.current.Blocks) > 0 {
			last := &m.chat.current.Blocks[len(m.chat.current.Blocks)-1]
			if last.Type == "tool_call" {
				last.Output = event.Content
			}
		}
	case "compacting":
		if m.chat.current != nil {
			m.finalizeLastContent()
			m.chat.current.Blocks = append(m.chat.current.Blocks, MessageBlock{
				Type:    "compaction",
				Content: event.Content,
			})
		}
	case "compacted":
		if m.chat.current != nil {
			m.chat.current.Blocks = append(m.chat.current.Blocks, MessageBlock{
				Type:    "compaction",
				Content: event.Content,
			})
		}
	case "compaction_failed":
		if m.chat.current != nil {
			m.chat.current.Blocks = append(m.chat.current.Blocks, MessageBlock{
				Type:    "compaction",
				Content: event.Content,
			})
		}
	case "compaction_loop":
		if m.chat.current != nil {
			m.finalizeLastContent()
			m.chat.current.Blocks = append(m.chat.current.Blocks, MessageBlock{
				Type:    "compaction",
				Content: "⚠ " + event.Content,
			})
		}
	case "nudge":
		if m.chat.current != nil {
			m.finalizeLastContent()
			m.chat.current.Blocks = append(m.chat.current.Blocks, MessageBlock{
				Type:    "compaction",
				Content: event.Content,
			})
		}
	case "done":
		if m.chat.current != nil {
			if event.Content != "" {
				for i := range m.chat.current.Blocks {
					if m.chat.current.Blocks[i].Type == "content" && !m.chat.current.Blocks[i].Done {
						m.chat.current.Blocks[i].Content = event.Content
						break
					}
				}
			}
			m.finalizeLastContent()

			var assistantText string
			for _, block := range m.chat.current.Blocks {
				if block.Type == "content" {
					assistantText += block.Content
				}
			}
			if assistantText != "" {
				m.pluginAPI.FireMessageCallbacks("assistant", assistantText)
			}

			m.chat.messages = append(m.chat.messages, *m.chat.current)
			m.chat.current = nil
		}
		m.streaming = false
		m.cancelFn = nil
		m.chat.messageQueue = nil
		m.autoSave()
		if m.titleGenPending {
			m.titleGenPending = false
			return m, m.generateTitleCmd()
		}
		return m, nil
	case "error":
		if m.chat.current != nil {
			m.finalizeLastContent()
			m.chat.current.Blocks = append(m.chat.current.Blocks, MessageBlock{
				Type:    "error",
				Content: event.Content,
				Done:    true,
			})
			m.chat.messages = append(m.chat.messages, *m.chat.current)
			m.chat.current = nil
		}
		m.streaming = false
		m.cancelFn = nil
		m.chat.messageQueue = nil
		m.autoSave()
		return m, nil
	case "cancelled":
		if m.chat.current != nil {
			m.finalizeLastContent()
			m.chat.current.Blocks = append(m.chat.current.Blocks, MessageBlock{
				Type:    "error",
				Content: "[interrupted]",
				Done:    true,
			})
			m.chat.messages = append(m.chat.messages, *m.chat.current)
			m.chat.current = nil
		}
		m.streaming = false
		m.cancelFn = nil
		m.chat.messageQueue = nil
		m.autoSave()
		return m, nil
	case "injected":
		if len(m.chat.messageQueue) > 0 {
			m.chat.messages = append(m.chat.messages, m.chat.messageQueue[0])
			m.chat.messageQueue = m.chat.messageQueue[1:]
		}
	case "ask_question":
		if event.Question != nil && event.ResponseCh != nil {
			m.question = &QuestionState{}
			*m.question = NewQuestionState(*event.Question, event.ResponseCh)
			m.question.CustomInput.SetWidth(m.width)
			m.resizeWM()
			m.chat.refreshView()
			return m, nil
		}
	}
	return m, nil
}

func (m model) handleStreamBatch(events []StreamEvent) (tea.Model, tea.Cmd) {
	for _, event := range events {
		var cmd tea.Cmd
		m, cmd = m.handleStreamEvent(event)
		if !m.streaming || m.question != nil {
			m.chat.refreshView()
			return m, cmd
		}
	}
	m.chat.refreshView()
	return m, waitForStreamEvent(m.streamCh)
}

func (m model) handleTitleGenerated(msg titleGeneratedMsg) (tea.Model, tea.Cmd) {
	if msg.title != "" && m.currentSession != nil {
		RenameSession(m.currentSession, msg.title)
	}
	m.chat.refreshView()
	return m, nil
}

func (m model) handleSessionPicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		cmd := m.sessionPicker.Update(msg)
		if m.sessionPicker.Selected != nil {
			selected := *m.sessionPicker.Selected
			m.sessionPicker = nil
			m.autoSave()
			_, messages, err := LoadSession(selected.FilePath)
			if err != nil {
				m.statusMsg = "Error loading session"
				m.resizeWM()
				return m, m.clearStatus()
			}
			m.currentSession = &selected
			m.chat.messages = messages
			m.llmClient = NewLLMClient(m.profile.Config, m.profile.Agents, m.profile.AgentsPath, m.registry.AllTools(), m.registry)
			m.llmClient.SetHistory(BuildHistoryFromMessages(messages))
			m.pluginAPI.SetLLMClientGetter(func() *LLMClient { return m.llmClient })
			m.titleGenPending = false
			m.setupTodoStore()
			m.pluginAPI.SetSessionGetter(func() *Session { return m.currentSession })
			for i := range m.chat.messages {
				m.chat.messages[i].rendered = ""
			}
			m.resizeWM()
			m.chat.refreshView()
			return m, nil
		}
		if m.sessionPicker.Cancelled {
			m.sessionPicker = nil
			m.resizeWM()
			m.chat.refreshView()
			return m, nil
		}
		m.chat.refreshView()
		return m, cmd
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		for i := range m.chat.messages {
			m.chat.messages[i].rendered = ""
		}
		pickerH := m.height - 9
		if pickerH < 10 {
			pickerH = 10
		}
		m.sessionPicker.width = msg.Width
		m.sessionPicker.height = pickerH
		m.ready = true
		return m, nil
	case pluginEventMsg:
		switch msg.action {
		case "reload":
			m.pluginLoader.Load(msg.filename)
			name := strings.TrimSuffix(msg.filename, filepath.Ext(msg.filename))
			if m.pluginLoader.IsLoaded(name) && m.subAgentMgr.HasActive(m.fixAgentID) {
				m.subAgentMgr.EnqueueMessage(m.fixAgentID,
					fmt.Sprintf("Plugin %q reloaded successfully after your fix.", name))
				m.notifications.Add(fmt.Sprintf("✓ Plugin %q fixed and reloaded", name), 5*time.Second)
			}
		case "remove":
			m.pluginLoader.Unload(msg.filename)
		}
		return m, waitForPluginEvent(m.pluginLoader.Events())
	}
	return m, nil
}

func (m model) handleQuestion(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		cmd, answer := m.question.Update(msg)
		if answer != "" {
			resp := answer
			if answer == "__cancelled__" {
				resp = ""
			}
			m.question.ResponseCh <- resp
			m.question = nil
			m.resizeWM()
			m.chat.refreshView()
			return m, waitForStreamEvent(m.streamCh)
		}
		return m, cmd
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		for i := range m.chat.messages {
			m.chat.messages[i].rendered = ""
		}
		m.question.CustomInput.SetWidth(msg.Width)
		m.resizeWM()
		m.chat.refreshView()
		m.ready = true
		return m, nil
	case pluginEventMsg:
		switch msg.action {
		case "reload":
			m.pluginLoader.Load(msg.filename)
			name := strings.TrimSuffix(msg.filename, filepath.Ext(msg.filename))
			if m.pluginLoader.IsLoaded(name) && m.subAgentMgr.HasActive(m.fixAgentID) {
				m.subAgentMgr.EnqueueMessage(m.fixAgentID,
					fmt.Sprintf("Plugin %q reloaded successfully after your fix.", name))
				m.notifications.Add(fmt.Sprintf("✓ Plugin %q fixed and reloaded", name), 5*time.Second)
			}
		case "remove":
			m.pluginLoader.Unload(msg.filename)
		}
		return m, waitForPluginEvent(m.pluginLoader.Events())
	}
	return m, nil
}
