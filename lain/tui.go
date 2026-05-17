package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type MessageBlock struct {
	Type    string
	Content string
	Name    string
	Output  string
	Done    bool
}

type ChatMessage struct {
	Role     string
	Blocks   []MessageBlock
	rendered string
}

type streamBatchMsg struct {
	events []StreamEvent
}

type titleGeneratedMsg struct {
	title string
}

type statusClearMsg struct{}

type pluginErrorMsg struct {
	err PluginError
}

type subAgentEventMsg struct {
	agentID string
	event   StreamEvent
}

type editorMode int

const (
	insertMode editorMode = iota
	normalMode
)

type model struct {
	profileName     string
	profile         *Profile
	registry        *ToolRegistry
	llmClient       *LLMClient
	textarea        textarea.Model
	spinner         spinner.Model
	streaming       bool
	ready           bool
	width           int
	height          int
	streamCh        <-chan StreamEvent
	question        *QuestionState
	currentSession  *Session
	sessionPicker   *SessionPickerState
	titleGenPending bool
	statusMsg       string
	cancelFn        context.CancelFunc
	mode            editorMode

	wm           *WindowManager
	chat         *chatWindow
	todo         *todoWindow
	pluginAPI    *PluginAPI
	pluginLoader *PluginLoader

	subAgentMgr   *SubAgentManager
	notifications *NotificationManager
	pendingFixErr *PluginError
	fixAgentID    string
}

func NewTUI(profileName string, profile *Profile, registry *ToolRegistry) model {
	ta := textarea.New()
	ta.Placeholder = "Type your message... (/new /sessions /save /rename /compact /ask /plugins /windows)"
	ta.Prompt = "> "
	ta.CharLimit = 0
	ta.SetHeight(1)
	ta.ShowLineNumbers = false
	ta.Focus()

	sp := spinner.New(spinner.WithSpinner(spinner.Spinner{
		Frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		FPS:    100 * time.Millisecond,
	}))

	llmClient := NewLLMClient(profile.Config, profile.Agents, profile.AgentsPath, registry.AllTools(), registry)
	session := NewSession(profileName)

	dir, _ := SessionDir(profileName)
	if dir != "" {
		todoPath := filepath.Join(dir, session.ID+".todos.json")
		registry.SetTodoStore(NewTodoStore(todoPath))
	}

	chat := newChatWindow()
	todo := newTodoWindow(registry)
	wm := NewWindowManager(80, 20)
	wm.Add(chat)
	wm.AddWithSplit("chat", SplitVertical, todo, todoSidebarW)

	pluginAPI := NewPluginAPI()
	pluginAPI.SetWindowManager(wm)
	pluginAPI.SetChat(chat)
	pluginAPI.SetSessionGetter(func() *Session { return session })
	pluginAPI.SetProfileGetter(func() *Profile { return profile })
	pluginAPI.SetLLMClientGetter(func() *LLMClient { return llmClient })

	home, _ := os.UserHomeDir()
	pluginDir := filepath.Join(home, ".config", "lain", "plugins")
	pluginLoader := NewPluginLoader(pluginDir, pluginAPI)
	pluginAPI.SetErrorChannel(pluginLoader.ErrorChannel())
	pluginAPI.SetTimeouts(profile.Config.Plugins.RenderTimeout, profile.Config.Plugins.CallbackTimeout, profile.Config.Plugins.LoadTimeout)

	registry.SetPluginAPI(pluginAPI)
	registry.AddBuiltinTool(PluginQueryTool)
	registry.AddBuiltinTool(PluginSendTool)

	m := model{
		profileName:    profileName,
		profile:        profile,
		registry:       registry,
		llmClient:      llmClient,
		textarea:       ta,
		spinner:        sp,
		currentSession: session,
		titleGenPending: true,
		mode:           insertMode,
		wm:             wm,
		chat:           chat,
		todo:           todo,
		pluginAPI:      pluginAPI,
		pluginLoader:   pluginLoader,
		notifications:  NewNotificationManager(),
	}
	m.subAgentMgr = NewSubAgentManager(&m)
	return m
}

func (m *model) LoadSessionByID(sessionID string) error {
	dir, err := SessionDir(m.profileName)
	if err != nil {
		return err
	}
	session, messages, err := LoadSession(dir + "/" + sessionID + ".md")
	if err != nil {
		return err
	}
	m.currentSession = session
	m.chat.messages = messages
	m.llmClient.SetHistory(BuildHistoryFromMessages(messages))
	m.titleGenPending = false
	m.setupTodoStore()
	return nil
}

func (m model) Init() tea.Cmd {
	var cmds []tea.Cmd
	cmds = append(cmds, func() tea.Msg {
		if err := m.pluginLoader.Start(m.profile.Config.Plugins.Enabled); err != nil {
			slog.Error("plugin loader start failed", "error", err)
		}
		return nil
	})
	cmds = append(cmds, waitForPluginEvent(m.pluginLoader.Events()))
	cmds = append(cmds, waitForPluginError(m.pluginLoader.Errors()))
	cmds = append(cmds, pluginRenderTick())
	cmds = append(cmds, waitForPluginRenderResult(m.pluginAPI.RenderResultChannel()))
	cmds = append(cmds, waitForPluginChatMsg(m.pluginAPI.ChatMsgChannel()))
	return tea.Batch(cmds...)
}

func (m model) wmHeight() int {
	bannerH := 9
	statusH := 1
	textareaH := 3
	sepH := 1
	vpHeight := m.height - bannerH - statusH - textareaH - sepH
	if vpHeight < 5 {
		vpHeight = 5
	}
	return vpHeight
}

func (m model) wmHeightQuestion() int {
	bannerH := 9
	sepH := 1
	questionH := 3
	if m.question != nil {
		questionH = m.question.EstimatedHeight(m.width)
	}
	vpHeight := m.height - bannerH - questionH - sepH
	if vpHeight < 5 {
		vpHeight = 5
	}
	return vpHeight
}

func (m *model) resizeWM() {
	if m.question != nil {
		m.wm.SetSize(m.width, m.wmHeightQuestion())
	} else {
		m.wm.SetSize(m.width, m.wmHeight())
	}
}

func (m *model) fullRedraw() {
	for i := range m.chat.messages {
		m.chat.messages[i].rendered = ""
	}
	h := m.wmHeight()
	if m.question != nil {
		h = m.wmHeightQuestion()
	}
	m.wm.SetSize(m.width, h)
	m.chat.resizeViewport(h)
}

func (m *model) refreshView() {
	m.chat.refreshView()
}

func (m *model) autoSave() {
	if m.currentSession == nil || len(m.chat.messages) == 0 {
		return
	}
	SaveSession(m.currentSession, m.chat.messages)
}

func (m *model) setupTodoStore() {
	if m.currentSession == nil {
		return
	}
	dir, err := SessionDir(m.profileName)
	if err != nil {
		return
	}
	todoPath := filepath.Join(dir, m.currentSession.ID+".todos.json")
	m.registry.SetTodoStore(NewTodoStore(todoPath))
}

func (m *model) openSessionPicker() {
	sessions, err := ListSessions(m.profileName)
	if err != nil {
		sessions = nil
	}
	pickerH := m.height - 9
	if pickerH < 10 {
		pickerH = 10
	}
	m.sessionPicker = &SessionPickerState{}
	*m.sessionPicker = NewSessionPickerState(sessions, m.width, pickerH)
}

func (m model) generateTitleCmd() tea.Cmd {
	var userMsg, assistantMsg string
	for _, msg := range m.chat.messages {
		if msg.Role == "user" && userMsg == "" {
			for _, block := range msg.Blocks {
				if block.Type == "content" {
					userMsg = block.Content
					break
				}
			}
		}
		if msg.Role == "assistant" && assistantMsg == "" {
			for _, block := range msg.Blocks {
				if block.Type == "content" {
					assistantMsg += block.Content
				}
			}
		}
	}
	if userMsg == "" || assistantMsg == "" {
		return nil
	}
	client := m.llmClient.client
	modelName := m.llmClient.model
	return func() tea.Msg {
		title := GenerateTitle(client, modelName, userMsg, assistantMsg)
		return titleGeneratedMsg{title: title}
	}
}

func (m model) clearStatus() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return statusClearMsg{}
	})
}

func waitForStreamEvent(ch <-chan StreamEvent) tea.Cmd {
	return func() tea.Msg {
		var events []StreamEvent
		e, ok := <-ch
		if !ok {
			return streamBatchMsg{events: []StreamEvent{{Type: "done"}}}
		}
		events = append(events, e)
		for {
			select {
			case e, ok := <-ch:
				if !ok {
					return streamBatchMsg{events: append(events, StreamEvent{Type: "done"})}
				}
				events = append(events, e)
				if e.Type == "done" || e.Type == "error" || e.Type == "cancelled" || e.Type == "ask_question" {
					return streamBatchMsg{events: events}
				}
			default:
				return streamBatchMsg{events: events}
			}
		}
	}
}

func waitForPluginEvent(ch <-chan pluginEventMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func waitForPluginError(ch <-chan PluginError) tea.Cmd {
	return func() tea.Msg {
		err, ok := <-ch
		if !ok {
			return nil
		}
		return pluginErrorMsg{err: err}
	}
}

func pluginRenderTick() tea.Cmd {
	return tea.Tick(16*time.Millisecond, func(t time.Time) tea.Msg {
		return pluginRenderTickMsg(t)
	})
}

func waitForPluginRenderResult(ch chan pluginRenderMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func waitForSubAgentEvent(agentID string, ch <-chan StreamEvent) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return subAgentEventMsg{agentID: agentID, event: StreamEvent{Type: "done"}}
		}
		return subAgentEventMsg{agentID: agentID, event: e}
	}
}

func (m *model) spawnFixAgent(pluginErr PluginError) tea.Cmd {
	if m.subAgentMgr.HasActive(m.fixAgentID) {
		m.subAgentMgr.EnqueueMessage(m.fixAgentID,
			fmt.Sprintf("New plugin error:\nPlugin: %s\nError: %s\nSource: %s",
				pluginErr.PluginName, pluginErr.Error, pluginErr.Source))
		m.notifications.Add(
			fmt.Sprintf("Error queued for plugin %q", pluginErr.PluginName),
			5*time.Second)
		return nil
	}

	profileName := m.profile.Config.SubAgent.Profile
	if profileName == "" {
		profileName = m.profileName
	}

	agent, err := m.subAgentMgr.Spawn(SubAgentOpts{
		ID:           "fix-plugins",
		Title:        "Plugin Fix Agent",
		ProfileName:  profileName,
		SystemPrompt: buildFixAgentPrompt(),
		InitialMsg:   fmt.Sprintf("Plugin %q failed with error: %s\nRead the file and fix it.",
			pluginErr.PluginName, pluginErr.Error),
	})
	if err != nil {
		m.statusMsg = "Failed to spawn fix agent: " + err.Error()
		m.refreshView()
		return nil
	}

	m.fixAgentID = agent.id
	m.subAgentMgr.SetFixAgentID(agent.id)
	m.wm.SetFocused("chat")
	m.notifications.Add(
		fmt.Sprintf("Fix agent spawned for plugin %q", pluginErr.PluginName),
		5*time.Second)
	return waitForSubAgentEvent(agent.id, agent.streamCh)
}

func buildFixAgentPrompt() string {
	return `You are a plugin debugger for the lain TUI application.

You fix Lua plugin errors. When you receive a plugin error:
1. Read the plugin source file from ~/.config/lain/plugins/
2. Identify the issue
3. Edit the file to fix it
4. The plugin will auto-reload after you save — you will be notified of the result

If you receive multiple errors, fix them one at a time.

Available tools: run_command (use to cat, sed, or rewrite files).
Plugin directory: ~/.config/lain/plugins/
Plugin extension: .lua
Available Lua libraries: string, table, math, coroutine, io, os, package (no debug)
Plugin API: lain.window (register with id, title, render, update, interval, tick, float), lain.chat, lain.session, lain.state, lain.log, lain.command, lain.keybind`
}

type blankWindow struct {
	id    string
	title string
}

func (w *blankWindow) ID() string                      { return w.id }
func (w *blankWindow) Title() string                    { return w.title }
func (w *blankWindow) Update(tea.Msg) (Window, tea.Cmd) { return w, nil }
func (w *blankWindow) View(width, height int, focused bool) string {
	return lipgloss.NewStyle().Width(width).Height(height).Render("")
}
func (w *blankWindow) SetSize(width, height int) {}
