package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
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

type streamEventMsg struct {
	event StreamEvent
}

type titleGeneratedMsg struct {
	title string
}

type statusClearMsg struct{}

var (
	userStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true)
	assistantStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("213"))
	toolStyle          = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("243"))
	errorStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	inputStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("86"))
	compactionStyle    = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("214"))
	questionModalStyle = lipgloss.NewStyle().Padding(0, 1)
	statusStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	todoHeaderStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86"))
	todoPendingStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	todoDoneStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("60")).Faint(true)
	todoEmptyStyle     = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("243"))
)

const todoSidebarW = 28

type model struct {
	profileName    string
	profile        *Profile
	registry       *ToolRegistry
	llmClient      *LLMClient
	viewport       viewport.Model
	textarea       textarea.Model
	messages       []ChatMessage
	current        *ChatMessage
	streaming      bool
	ready          bool
	width          int
	height         int
	streamCh       <-chan StreamEvent
	question       *QuestionState
	currentSession *Session
	sessionPicker  *SessionPickerState
	titleGenPending bool
	statusMsg      string
	cancelFn       context.CancelFunc
	messageQueue   []ChatMessage
}

func NewTUI(profileName string, profile *Profile, registry *ToolRegistry) model {
	ta := textarea.New()
	ta.Placeholder = "Type your message... (/new /sessions /save /rename)"
	ta.Prompt = "> "
	ta.CharLimit = 0
	ta.SetHeight(1)
	ta.ShowLineNumbers = false
	ta.Focus()

	llmClient := NewLLMClient(profile.Config, profile.Agents, registry.AllTools(), registry)
	session := NewSession(profileName)

	dir, _ := SessionDir(profileName)
	if dir != "" {
		todoPath := filepath.Join(dir, session.ID+".todos.json")
		registry.SetTodoStore(NewTodoStore(todoPath))
	}

	return model{
		profileName:     profileName,
		profile:         profile,
		registry:        registry,
		llmClient:       llmClient,
		viewport:        viewport.New(80, 20),
		textarea:        ta,
		messages:        []ChatMessage{},
		currentSession:  session,
		titleGenPending: true,
	}
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
	m.messages = messages
	m.llmClient.SetHistory(BuildHistoryFromMessages(messages))
	m.titleGenPending = false
	m.setupTodoStore()
	return nil
}

func (m model) Init() tea.Cmd {
	return nil
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
	case streamEventMsg:
		return m.handleStreamEvent(msg.event)
	case titleGeneratedMsg:
		return m.handleTitleGenerated(msg)
	case statusClearMsg:
		m.statusMsg = ""
		m.refreshView()
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m model) handleWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.width = msg.Width
	m.height = msg.Height
	for i := range m.messages {
		m.messages[i].rendered = ""
	}
	m.viewport = viewport.New(m.chatWidth(), m.viewportHeight())
	m.viewport.SetContent(m.renderMessages())
	m.textarea.SetWidth(msg.Width)
	m.ready = true
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.streaming {
			if m.cancelFn != nil {
				m.cancelFn()
			}
			m.streaming = false
			if m.current != nil {
				m.messages = append(m.messages, *m.current)
				m.current = nil
			}
			m.messageQueue = nil
			m.autoSave()
			m.refreshView()
			return m, nil
		}
		m.autoSave()
		return m, tea.Quit
	case "esc":
		if m.streaming {
			if m.cancelFn != nil {
				m.cancelFn()
			}
			m.streaming = false
			if m.current != nil {
				m.messages = append(m.messages, *m.current)
				m.current = nil
			}
			m.messageQueue = nil
			m.autoSave()
			m.refreshView()
			return m, nil
		}
		return m, nil
	case "ctrl+s":
		if m.streaming {
			return m, nil
		}
		m.openSessionPicker()
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
			m.messageQueue = append(m.messageQueue, queued)
			m.llmClient.InjectMessage(input)
			m.refreshView()
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
		m.messages = append(m.messages, ChatMessage{
			Role:   "user",
			Blocks: []MessageBlock{{Type: "content", Content: input}},
		})
		m.streaming = true
		m.current = &ChatMessage{Role: "assistant"}
		m.refreshView()
		ctx, cancel := context.WithCancel(context.Background())
		m.cancelFn = cancel
		m.streamCh = m.llmClient.Chat(ctx, input)
		return m, waitForStreamEvent(m.streamCh)
	default:
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		return m, cmd
	}
}

func (m model) handleSlashCommand(input string) (tea.Model, tea.Cmd) {
	m.textarea.Reset()
	parts := strings.SplitN(input, " ", 2)
	cmd := parts[0]

	switch cmd {
	case "/quit", "/exit":
		m.autoSave()
		return m, tea.Quit
	case "/new":
		m.autoSave()
		m.currentSession = NewSession(m.profileName)
		m.messages = nil
		m.current = nil
		m.llmClient = NewLLMClient(m.profile.Config, m.profile.Agents, m.registry.AllTools(), m.registry)
		m.titleGenPending = true
		m.setupTodoStore()
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
	default:
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
				m.resizeViewport(m.viewportHeight())
				return m, m.clearStatus()
			}
			m.currentSession = &selected
			m.messages = messages
			m.llmClient = NewLLMClient(m.profile.Config, m.profile.Agents, m.registry.AllTools(), m.registry)
			m.llmClient.SetHistory(BuildHistoryFromMessages(messages))
			m.titleGenPending = false
			m.setupTodoStore()
			for i := range m.messages {
				m.messages[i].rendered = ""
			}
			m.resizeViewport(m.viewportHeight())
			m.refreshView()
			return m, nil
		}
		if m.sessionPicker.Cancelled {
			m.sessionPicker = nil
			m.resizeViewport(m.viewportHeight())
			m.refreshView()
			return m, nil
		}
		m.refreshView()
		return m, cmd
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		for i := range m.messages {
			m.messages[i].rendered = ""
		}
		pickerH := m.height - 9
		if pickerH < 10 {
			pickerH = 10
		}
		m.sessionPicker.width = msg.Width
		m.sessionPicker.height = pickerH
		m.ready = true
		return m, nil
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
			m.resizeViewport(m.viewportHeight())
			m.refreshView()
			return m, waitForStreamEvent(m.streamCh)
		}
		return m, cmd
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		for i := range m.messages {
			m.messages[i].rendered = ""
		}
		m.question.CustomInput.SetWidth(msg.Width)
		m.resizeViewport(m.viewportHeightQuestion())
		m.viewport.SetContent(m.renderMessages())
		m.ready = true
		return m, nil
	}
	return m, nil
}

func (m model) viewportHeight() int {
	bannerH := 9
	textareaH := 3
	sepH := 1
	vpHeight := m.height - bannerH - textareaH - sepH
	if vpHeight < 5 {
		vpHeight = 5
	}
	return vpHeight
}

func (m model) viewportHeightQuestion() int {
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

func (m model) chatWidth() int {
	return m.width - todoSidebarW
}

func (m model) renderTodoSidebar(height int) string {
	var b strings.Builder

	header := " Tasks"
	b.WriteString(todoHeaderStyle.Render(header))
	padLen := todoSidebarW - len(header)
	if padLen > 0 {
		b.WriteString(strings.Repeat(" ", padLen))
	}
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Faint(true).Render(strings.Repeat("─", todoSidebarW)))
	b.WriteString("\n")

	store := m.registry.GetTodoStore()
	if store == nil || len(store.List()) == 0 {
		b.WriteString(todoEmptyStyle.Render("  No tasks"))
		b.WriteString("\n")
	} else {
		items := store.List()
		bodyLines := height - 2
		for i, item := range items {
			if i >= bodyLines-1 && len(items) > bodyLines {
				remaining := len(items) - i
				moreLine := fmt.Sprintf("  … %d more", remaining)
				b.WriteString(todoEmptyStyle.Render(moreLine))
				b.WriteString("\n")
				break
			}
			marker := "☐"
			style := todoPendingStyle
			if item.Completed {
				marker = "☑"
				style = todoDoneStyle
			}
			line := fmt.Sprintf(" %s %d. %s", marker, item.ID, item.Task)
			if len(line) > todoSidebarW {
				line = line[:todoSidebarW-1] + "…"
			}
			b.WriteString(style.Render(line))
			b.WriteString("\n")
		}
	}

	linesWritten := strings.Count(b.String(), "\n")
	for i := linesWritten; i < height; i++ {
		b.WriteString(strings.Repeat(" ", todoSidebarW) + "\n")
	}

	return lipgloss.NewStyle().
		Border(lipgloss.Border{Left: "│"}, false, false, false, true).
		BorderForeground(lipgloss.Color("243")).
		Width(todoSidebarW).
		Height(height).
		Render(strings.TrimSuffix(b.String(), "\n"))
}

func (m *model) resizeViewport(h int) {
	m.viewport = viewport.New(m.chatWidth(), h)
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
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
		sidebar := m.renderTodoSidebar(m.viewportHeightQuestion())
		chatWithSidebar := lipgloss.JoinHorizontal(lipgloss.Top,
			m.viewport.View(),
			sidebar,
		)
		return lipgloss.JoinVertical(lipgloss.Left,
			banner,
			chatWithSidebar,
			sep,
			questionView,
		)
	}

	var inputArea string
	if m.statusMsg != "" {
		inputArea = statusStyle.Render("  " + m.statusMsg + "\n")
	} else {
		inputArea = inputStyle.Render(m.textarea.View())
	}

	sidebar := m.renderTodoSidebar(m.viewportHeight())
	chatWithSidebar := lipgloss.JoinHorizontal(lipgloss.Top,
		m.viewport.View(),
		sidebar,
	)
	return lipgloss.JoinVertical(lipgloss.Left,
		banner,
		chatWithSidebar,
		sep,
		inputArea,
	)
}

func (m *model) finalizeLastContent() {
	if m.current == nil || len(m.current.Blocks) == 0 {
		return
	}
	last := &m.current.Blocks[len(m.current.Blocks)-1]
	if last.Type == "content" && !last.Done {
		last.Done = true
	}
}

func (m *model) handleStreamEvent(event StreamEvent) (tea.Model, tea.Cmd) {
	switch event.Type {
	case "token":
		if m.current != nil {
			if len(m.current.Blocks) > 0 && m.current.Blocks[len(m.current.Blocks)-1].Type == "content" {
				m.current.Blocks[len(m.current.Blocks)-1].Content += event.Content
			} else {
				m.current.Blocks = append(m.current.Blocks, MessageBlock{Type: "content", Content: event.Content})
			}
		}
	case "tool_start":
		if m.current != nil {
			m.finalizeLastContent()
			m.current.Blocks = append(m.current.Blocks, MessageBlock{
				Type: "tool_call",
				Name: event.Content,
			})
		}
	case "tool_output":
		if m.current != nil && len(m.current.Blocks) > 0 {
			last := &m.current.Blocks[len(m.current.Blocks)-1]
			if last.Type == "tool_call" {
				last.Output = event.Content
			}
		}
	case "compacting":
		if m.current != nil {
			m.finalizeLastContent()
			m.current.Blocks = append(m.current.Blocks, MessageBlock{
				Type:    "compaction",
				Content: event.Content,
			})
		}
	case "compacted":
		if m.current != nil {
			m.current.Blocks = append(m.current.Blocks, MessageBlock{
				Type:    "compaction",
				Content: event.Content,
			})
		}
	case "compaction_failed":
		if m.current != nil {
			m.current.Blocks = append(m.current.Blocks, MessageBlock{
				Type:    "compaction",
				Content: event.Content,
			})
		}
	case "nudge":
		if m.current != nil {
			m.finalizeLastContent()
			m.current.Blocks = append(m.current.Blocks, MessageBlock{
				Type:    "compaction",
				Content: event.Content,
			})
		}
	case "done":
		if m.current != nil {
			m.finalizeLastContent()
			m.messages = append(m.messages, *m.current)
			m.current = nil
		}
		m.streaming = false
		m.cancelFn = nil
		m.messageQueue = nil
		m.autoSave()
		m.refreshView()
		if m.titleGenPending {
			m.titleGenPending = false
			return m, m.generateTitleCmd()
		}
		return m, nil
	case "error":
		if m.current != nil {
			m.finalizeLastContent()
			m.current.Blocks = append(m.current.Blocks, MessageBlock{
				Type:    "error",
				Content: event.Content,
				Done:    true,
			})
			m.messages = append(m.messages, *m.current)
			m.current = nil
		}
		m.streaming = false
		m.cancelFn = nil
		m.messageQueue = nil
		m.autoSave()
		m.refreshView()
		return m, nil
	case "cancelled":
		if m.current != nil {
			m.finalizeLastContent()
			m.current.Blocks = append(m.current.Blocks, MessageBlock{
				Type:    "error",
				Content: "[interrupted]",
				Done:    true,
			})
			m.messages = append(m.messages, *m.current)
			m.current = nil
		}
		m.streaming = false
		m.cancelFn = nil
		m.messageQueue = nil
		m.autoSave()
		m.refreshView()
		return m, nil
	case "injected":
		if len(m.messageQueue) > 0 {
			m.messageQueue = m.messageQueue[1:]
		}
		m.refreshView()
		return m, waitForStreamEvent(m.streamCh)
	case "ask_question":
		if event.Question != nil && event.ResponseCh != nil {
			m.question = &QuestionState{}
			*m.question = NewQuestionState(*event.Question, event.ResponseCh)
			m.question.CustomInput.SetWidth(m.width)
			m.resizeViewport(m.viewportHeightQuestion())
			m.refreshView()
			return m, nil
		}
	}
	m.refreshView()
	return m, waitForStreamEvent(m.streamCh)
}

func (m model) handleTitleGenerated(msg titleGeneratedMsg) (tea.Model, tea.Cmd) {
	if msg.title != "" && m.currentSession != nil {
		RenameSession(m.currentSession, msg.title)
	}
	m.refreshView()
	return m, nil
}

func (m *model) refreshView() {
	if !m.ready {
		return
	}
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
}

func (m *model) renderMessages() string {
	var b strings.Builder
	for i := range m.messages {
		msg := &m.messages[i]
		if msg.rendered == "" {
			msg.rendered = formatMessage(*msg, m.chatWidth())
		}
		b.WriteString(msg.rendered)
		b.WriteString("\n\n")
	}
	if m.current != nil {
		b.WriteString(formatMessage(*m.current, m.chatWidth()))
		b.WriteString("\n\n")
	}
	for _, qm := range m.messageQueue {
		b.WriteString(formatQueuedMessage(qm, m.chatWidth()))
		b.WriteString("\n\n")
	}
	return b.String()
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
		top := userStyle.Render("┌─ you " + strings.Repeat("─", width-8) + "┐")
		lines := strings.Split(wrapped, "\n")
		mid := ""
		for _, line := range lines {
			mid += userStyle.Render("│ " + line) + "\n"
		}
		bot := userStyle.Render("└" + strings.Repeat("─", width-2) + "┘")
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

func (m *model) autoSave() {
	if m.currentSession == nil || len(m.messages) == 0 {
		return
	}
	SaveSession(m.currentSession, m.messages)
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
	for _, msg := range m.messages {
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
	top := userStyle.Faint(true).Render("┌─ queued " + strings.Repeat("─", width-12) + "┐")
	lines := strings.Split(wrapped, "\n")
	mid := ""
	for _, line := range lines {
		mid += userStyle.Faint(true).Render("│ " + line) + "\n"
	}
	bot := userStyle.Faint(true).Render("└" + strings.Repeat("─", width-2) + "┘")
	return top + "\n" + mid + bot
}

func waitForStreamEvent(ch <-chan StreamEvent) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return streamEventMsg{StreamEvent{Type: "done"}}
		}
		return streamEventMsg{e}
	}
}
