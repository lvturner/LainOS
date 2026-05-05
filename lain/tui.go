package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"
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
)

const todoSidebarW = 28

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
	ta.Placeholder = "Type your message... (/new /sessions /save /rename /compact /plugins /windows)"
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
		if err := m.pluginLoader.Start(); err != nil {
			slog.Error("plugin loader start failed", "error", err)
		}
		return nil
	})
	cmds = append(cmds, waitForPluginEvent(m.pluginLoader.Events()))
	cmds = append(cmds, waitForPluginError(m.pluginLoader.Errors()))
	return tea.Batch(cmds...)
}

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
		if m.profile.Config.SubAgent.AutoFix {
			m.spawnFixAgent(msg.err)
		} else {
			m.pendingFixErr = &msg.err
			m.mode = normalMode
			m.textarea.Blur()
		}
		return m, waitForPluginError(m.pluginLoader.Errors())
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

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.streaming {
			if m.cancelFn != nil {
				m.cancelFn()
			}
			m.streaming = false
			if m.chat.current != nil {
				m.chat.messages = append(m.chat.messages, *m.chat.current)
				m.chat.current = nil
			}
			m.chat.messageQueue = nil
			m.autoSave()
			m.chat.refreshView()
			return m, nil
		}
		m.autoSave()
		m.subAgentMgr.StopAll()
		return m, tea.Quit
	case "ctrl+l":
		m.fullRedraw()
		return m, tea.ClearScreen
	case "ctrl+s":
		if m.streaming {
			return m, nil
		}
		m.openSessionPicker()
		return m, nil
	case "ctrl+w":
		if m.mode == insertMode {
			m.mode = normalMode
			m.textarea.Blur()
			return m, nil
		}
		return m, nil
	}

	switch m.mode {
	case insertMode:
		return m.handleInsertKey(msg)
	case normalMode:
		return m.handleNormalKey(msg)
	}
	return m, nil
}

func (m model) handleInsertKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		if m.streaming {
			if m.cancelFn != nil {
				m.cancelFn()
			}
			m.streaming = false
			if m.chat.current != nil {
				m.chat.messages = append(m.chat.messages, *m.chat.current)
				m.chat.current = nil
			}
			m.chat.messageQueue = nil
			m.autoSave()
			m.chat.refreshView()
			return m, nil
		}
		return m, nil
	case "enter":
		if m.streaming {
			input := strings.TrimSpace(m.textarea.Value())
			if input == "" {
				return m, nil
			}
			m.textarea.Reset()
			queued := ChatMessage{
				Role:   "user",
				Blocks: []MessageBlock{{Type: "content", Content: input, Name: "queued"}},
			}
			m.chat.messageQueue = append(m.chat.messageQueue, queued)
			m.llmClient.InjectMessage(input)
			m.chat.vp.GotoBottom()
			m.chat.refreshView()
			return m, waitForStreamEvent(m.streamCh)
		}
		input := strings.TrimSpace(m.textarea.Value())
		if input == "" {
			return m, nil
		}
		m.statusMsg = ""

		if strings.HasPrefix(input, "/") {
			return m.handleSlashCommand(input)
		}

		m.textarea.Reset()
		m.chat.messages = append(m.chat.messages, ChatMessage{
			Role:   "user",
			Blocks: []MessageBlock{{Type: "content", Content: input}},
		})
		m.pluginAPI.FireMessageCallbacks("user", input)
		m.streaming = true
		m.chat.current = &ChatMessage{Role: "assistant"}
		m.chat.vp.GotoBottom()
		m.chat.refreshView()
		ctx, cancel := context.WithCancel(context.Background())
		m.cancelFn = cancel
		m.streamCh = m.llmClient.Chat(ctx, input)
		return m, tea.Batch(waitForStreamEvent(m.streamCh), m.spinner.Tick)
	case "pgup":
		m.chat.vp.HalfPageUp()
		m.chat.atBottom = m.chat.vp.AtBottom()
		if m.chat.atBottom {
			m.chat.newBelow = false
		}
		return m, nil
	case "pgdown":
		m.chat.vp.HalfPageDown()
		m.chat.atBottom = m.chat.vp.AtBottom()
		if m.chat.atBottom {
			m.chat.newBelow = false
		}
		return m, nil
	case "home":
		m.chat.vp.GotoTop()
		m.chat.atBottom = m.chat.vp.AtBottom()
		m.chat.newBelow = true
		return m, nil
	case "end":
		m.chat.vp.GotoBottom()
		m.chat.atBottom = true
		m.chat.newBelow = false
		return m, nil
	case "up":
		if m.textarea.Value() == "" {
			m.chat.vp.LineUp(1)
			m.chat.atBottom = m.chat.vp.AtBottom()
			if m.chat.atBottom {
				m.chat.newBelow = false
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		return m, cmd
	case "down":
		if m.textarea.Value() == "" {
			m.chat.vp.LineDown(1)
			m.chat.atBottom = m.chat.vp.AtBottom()
			if m.chat.atBottom {
				m.chat.newBelow = false
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		return m, cmd
	default:
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		return m, cmd
	}
}

func (m model) handleNormalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if m.pendingFixErr != nil {
		switch key {
		case "f", "F":
			err := *m.pendingFixErr
			m.pendingFixErr = nil
			m.spawnFixAgent(err)
			m.mode = insertMode
			m.textarea.Focus()
			return m, nil
		case "esc", "i":
			m.pendingFixErr = nil
			m.mode = insertMode
			m.textarea.Focus()
			m.statusMsg = "Plugin error dismissed"
			m.refreshView()
			return m, m.clearStatus()
		}
		return m, nil
	}

	keybinds := m.pluginAPI.GetKeybindCallbacks()
	if fn, ok := keybinds[key]; ok {
		fn()
		return m, nil
	}

	switch key {
	case "esc", "i":
		m.mode = insertMode
		m.textarea.Focus()
		return m, nil
	case "h", "left":
		m.wm.FocusSpatial(FocusLeft)
		return m, nil
	case "j", "down":
		m.wm.FocusSpatial(FocusDown)
		return m, nil
	case "k", "up":
		m.wm.FocusSpatial(FocusUp)
		return m, nil
	case "l", "right":
		m.wm.FocusSpatial(FocusRight)
		return m, nil
	case "G":
		m.chat.vp.GotoBottom()
		m.chat.atBottom = true
		m.chat.newBelow = false
		return m, nil
	case "ctrl+left":
		if m.wm.HasFloating(m.wm.FocusedID()) {
			m.wm.MoveFloating(m.wm.FocusedID(), -2, 0)
		} else {
			m.wm.MoveFocused(FocusLeft)
			m.resizeWM()
		}
		return m, nil
	case "ctrl+right":
		if m.wm.HasFloating(m.wm.FocusedID()) {
			m.wm.MoveFloating(m.wm.FocusedID(), 2, 0)
		} else {
			m.wm.MoveFocused(FocusRight)
			m.resizeWM()
		}
		return m, nil
	case "ctrl+up":
		if m.wm.HasFloating(m.wm.FocusedID()) {
			m.wm.MoveFloating(m.wm.FocusedID(), 0, -1)
		} else {
			m.wm.MoveFocused(FocusUp)
			m.resizeWM()
		}
		return m, nil
	case "ctrl+down":
		if m.wm.HasFloating(m.wm.FocusedID()) {
			m.wm.MoveFloating(m.wm.FocusedID(), 0, 1)
		} else {
			m.wm.MoveFocused(FocusDown)
			m.resizeWM()
		}
		return m, nil
	case "s":
		focused := m.wm.FocusedID()
		if focused != "" {
			scratch := &blankWindow{id: "scratch-" + focused, title: "Scratch"}
			m.wm.AddWithSplit(focused, SplitHorizontal, scratch, 0)
			m.resizeWM()
		}
		return m, nil
	case "v":
		focused := m.wm.FocusedID()
		if focused != "" {
			scratch := &blankWindow{id: "scratch-" + focused, title: "Scratch"}
			m.wm.AddWithSplit(focused, SplitVertical, scratch, 0)
			m.resizeWM()
		}
		return m, nil
	case "x":
		focused := m.wm.FocusedID()
		if focused != "" && focused != "chat" {
			m.subAgentMgr.Stop(focused)
			m.wm.Remove(focused)
			m.resizeWM()
		}
		return m, nil
	case "X":
		windows := m.wm.ListWindows()
		for _, id := range windows {
			if id != "chat" {
				m.subAgentMgr.Stop(id)
				m.wm.Remove(id)
			}
		}
		m.resizeWM()
		return m, nil
	case "f":
		focused := m.wm.FocusedID()
		if focused != "" && focused != "chat" {
			if m.wm.HasFloating(focused) {
				win := m.wm.Get(focused)
				m.wm.RemoveFloating(focused)
				targetID := m.wm.FocusedID()
				if targetID == "" || targetID == focused {
					targetID = "chat"
				}
				m.wm.AddWithSplit(targetID, SplitVertical, win, 0)
				m.resizeWM()
			} else {
				win := m.wm.Get(focused)
				if win != nil {
					m.wm.Remove(focused)
					w := 40
					h := 12
					x := (m.width - w) / 2
					y := (m.wmHeight() - h) / 2
					m.wm.AddFloating(win, x, y, w, h)
					m.resizeWM()
				}
			}
		}
		return m, nil
	case "+":
		m.wm.ResizeFocused(0.05)
		return m, nil
	case "-":
		m.wm.ResizeFocused(-0.05)
		return m, nil
	case "b":
		m.wm.ToggleBorders()
		m.resizeWM()
		return m, nil
	case "z":
		m.wm.ToggleZoom()
		return m, nil
	case "=":
		m.wm.Equalize()
		return m, nil
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		idx := int(key[0] - '1')
		m.wm.FocusByIndex(idx)
		return m, nil
	case "?":
		m.statusMsg = "hjkl:focus x:close f:float +/-:resize b:borders z:zoom =:equalize s:hsplit v:vsplit 1-9:goto Esc:insert"
		m.refreshView()
		return m, m.clearStatus()
	}
	return m, nil
}

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

func (m model) View() string {
	sessionTitle := ""
	if m.currentSession != nil {
		sessionTitle = m.currentSession.Title
	}

	if !m.ready {
		return "\n" + Banner(m.profileName, m.profile.Config.Model, sessionTitle) + "\n\n  Loading..."
	}
	banner := Banner(m.profileName, m.profile.Config.Model, sessionTitle)
	sep := lipgloss.NewStyle().Faint(true).Render(strings.Repeat("─", m.width))

	if m.sessionPicker != nil {
		return lipgloss.JoinVertical(lipgloss.Left,
			banner,
			m.sessionPicker.View(m.width),
		)
	}

	if m.question != nil {
		questionView := questionModalStyle.Render(m.question.View(m.width))
		return lipgloss.JoinVertical(lipgloss.Left,
			banner,
			m.wm.View(),
			sep,
			questionView,
		)
	}

	var statusBar string
	if m.pendingFixErr != nil {
		truncErr := m.pendingFixErr.Error
		if len(truncErr) > 60 {
			truncErr = truncErr[:60] + "..."
		}
		statusBar = errorStyle.Render(fmt.Sprintf(
			"⚠ plugin %q: %s — F:fix  Esc:dismiss",
			m.pendingFixErr.PluginName, truncErr))
	} else if m.statusMsg != "" {
		statusBar = statusStyle.Render("  " + m.statusMsg)
	} else if m.mode == normalMode {
		statusBar = normalModeStyle.Render("  -- NORMAL --")
	} else if m.streaming {
		statusBar = statusStyle.Render("  " + m.spinner.View() + " thinking...")
	} else {
		pct := m.llmClient.ContextPercent()
		ctxLen := m.llmClient.ContextLength()
		ctxLabel := fmt.Sprintf("ctx: %d%% (%dk/%dk)", pct, pct*ctxLen/100/1000, ctxLen/1000)
		statusBar = ctxStyle.Render("  " + ctxLabel)
	}

	inputArea := inputStyle.Render(m.textarea.View())

	parts := []string{banner, m.wm.View()}
	if m.chat.newBelow {
		parts = append(parts, scrollIndicatorStyle.Render("  ↓ new messages (end to jump)"))
	}
	parts = append(parts, sep, statusBar, inputArea)
	result := lipgloss.JoinVertical(lipgloss.Left, parts...)
	result = m.wm.FloatingView(result, m.width)
	if m.notifications.HasActive() {
		result = m.notifications.Render(result, m.width)
	}
	return result
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

func formatMessage(msg ChatMessage, width int) string {
	if width <= 0 {
		width = 80
	}
	var b strings.Builder
	switch msg.Role {
	case "user":
		var content string
		for _, block := range msg.Blocks {
			if block.Type == "content" {
				content = block.Content
				break
			}
		}
		wrapped := wordwrap.String(content, width-4)
		top := userStyle.Render("┌─ you " + strings.Repeat("─", max(0, width-8)) + "┐")
		lines := strings.Split(wrapped, "\n")
		mid := ""
		for _, line := range lines {
			mid += userStyle.Render("│ " + line) + "\n"
		}
		bot := userStyle.Render("└" + strings.Repeat("─", max(0, width-2)) + "┘")
		b.WriteString(top + "\n" + mid + bot)
	case "assistant":
		for i, block := range msg.Blocks {
			if i > 0 {
				b.WriteString("\n")
			}
			switch block.Type {
			case "content":
				if !block.Done {
					wrapped := wordwrap.String(block.Content, width)
					b.WriteString(assistantStyle.Render(wrapped))
				} else {
					rendered := renderMarkdown(block.Content, width)
					b.WriteString(rendered)
				}
			case "tool_call":
				b.WriteString(toolStyle.Render(fmt.Sprintf("▸ %s", block.Name)))
			case "compaction":
				b.WriteString(compactionStyle.Render(fmt.Sprintf("⟳ %s", block.Content)))
			case "error":
				b.WriteString(errorStyle.Render(fmt.Sprintf("[error: %s]", block.Content)))
			}
		}
	}
	return b.String()
}

func formatQueuedMessage(msg ChatMessage, width int) string {
	if width <= 0 {
		width = 80
	}
	var content string
	for _, block := range msg.Blocks {
		if block.Type == "content" {
			content = block.Content
			break
		}
	}
	wrapped := wordwrap.String(content, width-4)
	top := userStyle.Faint(true).Render("┌─ queued " + strings.Repeat("─", max(0, width-12)) + "┐")
	lines := strings.Split(wrapped, "\n")
	mid := ""
	for _, line := range lines {
		mid += userStyle.Faint(true).Render("│ " + line) + "\n"
	}
	bot := userStyle.Faint(true).Render("└" + strings.Repeat("─", max(0, width-2)) + "┘")
	return top + "\n" + mid + bot
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

func waitForSubAgentEvent(agentID string, ch <-chan StreamEvent) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return subAgentEventMsg{agentID: agentID, event: StreamEvent{Type: "done"}}
		}
		return subAgentEventMsg{agentID: agentID, event: e}
	}
}

func (m *model) spawnFixAgent(pluginErr PluginError) {
	if m.subAgentMgr.HasActive(m.fixAgentID) {
		m.subAgentMgr.EnqueueMessage(m.fixAgentID,
			fmt.Sprintf("New plugin error:\nPlugin: %s\nError: %s\nSource: %s",
				pluginErr.PluginName, pluginErr.Error, pluginErr.Source))
		m.notifications.Add(
			fmt.Sprintf("Error queued for plugin %q", pluginErr.PluginName),
			5*time.Second)
		return
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
		return
	}

	m.fixAgentID = agent.id
	m.subAgentMgr.SetFixAgentID(agent.id)
	m.wm.SetFocused("chat")
	m.notifications.Add(
		fmt.Sprintf("Fix agent spawned for plugin %q", pluginErr.PluginName),
		5*time.Second)
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
Available Lua libraries: string, table, math, coroutine (no os, io, debug, package)
Plugin API: lain.window, lain.chat, lain.session, lain.state, lain.log, lain.command, lain.keybind`
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
