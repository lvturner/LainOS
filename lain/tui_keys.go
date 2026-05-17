package main

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

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

		focusedID := m.wm.FocusedID()
		if focusedID != "" && focusedID != "chat" && focusedID != "todo" && m.subAgentMgr.HasActive(focusedID) {
			m.textarea.Reset()
			ch, err := m.subAgentMgr.SendMessage(focusedID, input)
			if err != nil {
				m.statusMsg = fmt.Sprintf("Error: %s", err)
				m.refreshView()
				return m, m.clearStatus()
			}
			return m, waitForSubAgentEvent(focusedID, ch)
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
			cmd := m.spawnFixAgent(err)
			m.mode = insertMode
			m.textarea.Focus()
			return m, cmd
		case "c", "C":
			err := *m.pendingFixErr
			m.pendingFixErr = nil
			errMsg := fmt.Sprintf("Plugin %q failed with error: %s\nSource: %s\n\nRead the plugin file at ~/.config/lain/plugins/%s.lua and fix it.",
				err.PluginName, err.Error, err.Source, err.PluginName)
			m.chat.messages = append(m.chat.messages, ChatMessage{
				Role:   "user",
				Blocks: []MessageBlock{{Type: "content", Content: errMsg}},
			})
			m.pluginAPI.FireMessageCallbacks("user", errMsg)
			m.streaming = true
			m.chat.current = &ChatMessage{Role: "assistant"}
			m.chat.vp.GotoBottom()
			m.chat.refreshView()
			ctx, cancel := context.WithCancel(context.Background())
			m.cancelFn = cancel
			m.streamCh = m.llmClient.Chat(ctx, errMsg)
			m.mode = insertMode
			m.textarea.Focus()
			return m, tea.Batch(waitForStreamEvent(m.streamCh), m.spinner.Tick)
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

	if focusedID := m.wm.FocusedID(); focusedID != "" && focusedID != "chat" {
		if win := m.wm.Get(focusedID); win != nil {
			if pw, ok := win.(*pluginWindow); ok && pw.updateFn != nil {
				pw.Update(msg)
			}
		}
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
