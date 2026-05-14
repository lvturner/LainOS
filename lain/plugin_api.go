package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	lua "github.com/yuin/gopher-lua"
	tea "github.com/charmbracelet/bubbletea"
)

type luaCallback struct {
	L  *lua.LState
	Fn *lua.LFunction
}

type pluginRenderMsg struct {
	windowID string
	content  string
	err      error
}

type pluginRenderTickMsg time.Time

type pluginChatSendMsg struct {
	content string
}

func waitForPluginChatMsg(ch chan pluginChatSendMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

type PluginAPI struct {
	wm               *WindowManager
	chat             *chatWindow
	getSession       func() *Session
	getProfile       func() *Profile
	getLLMClient     func() *LLMClient
	pluginState      map[string]map[string]any
	messageCallbacks map[string][]luaCallback
	commandCallbacks map[string]func(string)
	keybindCallbacks map[string]func()
	pluginWindows    map[string]*pluginWindow
	errCh            chan PluginError
	pluginKeybinds   map[string]string
	onWindowChange   func()
	executors        map[string]*pluginExecutor
	renderResultCh   chan pluginRenderMsg
	renderTimeout    time.Duration
	callbackTimeout  time.Duration
	loadTimeout      time.Duration
	dataCallbacks    map[string][]luaCallback
	chatMsgCh        chan pluginChatSendMsg
}

type pluginWindow struct {
	id           string
	title        string
	pluginName   string
	L            *lua.LState
	renderFn     *lua.LFunction
	updateFn     *lua.LFunction
	interval     time.Duration
	tickFn       *lua.LFunction
	stopTick     chan struct{}
	width        int
	height       int
	state        map[string]any
	api          *PluginAPI
	executor     *pluginExecutor
	cachedRender string
	renderMu     sync.RWMutex
	lastWidth    int
	lastHeight   int
	renderDirty  bool
	pendingKeys  []string
	keyMu        sync.Mutex
}

func newPluginWindow(id, title, pluginName string, L *lua.LState, renderFn, updateFn *lua.LFunction, api *PluginAPI) *pluginWindow {
	return &pluginWindow{
		id:          id,
		title:       title,
		pluginName:  pluginName,
		L:           L,
		renderFn:    renderFn,
		updateFn:    updateFn,
		stopTick:    make(chan struct{}),
		state:       make(map[string]any),
		api:         api,
		renderDirty: true,
	}
}

func (w *pluginWindow) ID() string   { return w.id }
func (w *pluginWindow) Title() string { return w.title }

func (w *pluginWindow) SetSize(width, height int) {
	w.width = width
	w.height = height
}

func (w *pluginWindow) Update(msg tea.Msg) (Window, tea.Cmd) {
	if w.updateFn == nil {
		return w, nil
	}
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		w.keyMu.Lock()
		w.pendingKeys = append(w.pendingKeys, keyMsg.String())
		w.keyMu.Unlock()
		w.renderDirty = true
	}
	return w, nil
}

func (w *pluginWindow) View(width, height int, focused bool) string {
	if w.renderFn == nil {
		return fmt.Sprintf("[%s: no render function]", w.title)
	}

	w.renderMu.RLock()
	cached := w.cachedRender
	w.renderMu.RUnlock()

	if width != w.lastWidth || height != w.lastHeight {
		w.lastWidth = width
		w.lastHeight = height
		w.renderDirty = true
	}

	if cached == "" && w.renderFn != nil {
		return fmt.Sprintf("[%s: loading...]", w.title)
	}

	return cached
}

func (w *pluginWindow) triggerRender(resultCh chan<- pluginRenderMsg) {
	if w.executor == nil || w.renderFn == nil || w.executor.unhealthy {
		return
	}
	w.renderDirty = false
	width, height := w.lastWidth, w.lastHeight
	windowID := w.id
	pluginName := w.pluginName
	renderTimeout := w.api.renderTimeout
	errCh := w.api.errCh
	callbackTimeout := w.api.callbackTimeout

	w.keyMu.Lock()
	keys := w.pendingKeys
	w.pendingKeys = nil
	w.keyMu.Unlock()

	go func() {
		if len(keys) > 0 && w.updateFn != nil {
			for _, key := range keys {
				w.executor.call(w.updateFn,
					[]lua.LValue{lua.LString(key)},
					0, callbackTimeout)
			}
		}

		result, err := w.executor.call(w.renderFn,
			[]lua.LValue{lua.LNumber(width), lua.LNumber(height)},
			1, renderTimeout)
		msg := pluginRenderMsg{windowID: windowID}
		if err != nil {
			msg.err = err
			msg.content = fmt.Sprintf("[render error: %s]", err.Error())
		} else if len(result.values) > 0 {
			if str, ok := result.values[0].(lua.LString); ok {
				msg.content = string(str)
			}
		}
		if msg.err != nil && errCh != nil {
			select {
			case errCh <- PluginError{PluginName: pluginName, Error: msg.err.Error(), Source: "render", Timestamp: time.Now()}:
			default:
			}
			w.executor.consecutiveFailures++
			if w.executor.consecutiveFailures >= 3 {
				w.executor.unhealthy = true
			}
		} else {
			w.executor.consecutiveFailures = 0
			w.executor.unhealthy = false
		}
		resultCh <- msg
	}()
}

func NewPluginAPI() *PluginAPI {
	return &PluginAPI{
		pluginState:      make(map[string]map[string]any),
		messageCallbacks: make(map[string][]luaCallback),
		commandCallbacks: make(map[string]func(string)),
		keybindCallbacks: make(map[string]func()),
		pluginWindows:    make(map[string]*pluginWindow),
		pluginKeybinds:   make(map[string]string),
		executors:        make(map[string]*pluginExecutor),
		renderResultCh:   make(chan pluginRenderMsg, 32),
		dataCallbacks:    make(map[string][]luaCallback),
		chatMsgCh:        make(chan pluginChatSendMsg, 16),
	}
}

func (api *PluginAPI) SetWindowManager(wm *WindowManager) {
	api.wm = wm
}

func (api *PluginAPI) SetChat(chat *chatWindow) {
	api.chat = chat
}

func (api *PluginAPI) SetSessionGetter(fn func() *Session) {
	api.getSession = fn
}

func (api *PluginAPI) SetProfileGetter(fn func() *Profile) {
	api.getProfile = fn
}

func (api *PluginAPI) SetLLMClientGetter(fn func() *LLMClient) {
	api.getLLMClient = fn
}

func (api *PluginAPI) SetWindowChangeCallback(fn func()) {
	api.onWindowChange = fn
}

func (api *PluginAPI) SetErrorChannel(ch chan PluginError) {
	api.errCh = ch
}

func (api *PluginAPI) SetTimeouts(render, callback, load time.Duration) {
	api.renderTimeout = render
	api.callbackTimeout = callback
	api.loadTimeout = load
}

func (api *PluginAPI) setExecutor(pluginName string, exec *pluginExecutor) {
	api.executors[pluginName] = exec
	for _, pw := range api.pluginWindows {
		if pw.pluginName == pluginName {
			pw.executor = exec
			if pw.renderFn != nil {
				renderTimeout := api.renderTimeout
				if renderTimeout == 0 {
					renderTimeout = 50 * time.Millisecond
				}
				result, err := exec.call(pw.renderFn,
					[]lua.LValue{lua.LNumber(0), lua.LNumber(0)},
					1, renderTimeout)
				if err == nil && len(result.values) > 0 {
					if str, ok := result.values[0].(lua.LString); ok {
						pw.renderMu.Lock()
						pw.cachedRender = string(str)
						pw.renderMu.Unlock()
					}
				}
			}
		}
	}
}

func (api *PluginAPI) getExecutor(pluginName string) *pluginExecutor {
	return api.executors[pluginName]
}

func (api *PluginAPI) RenderResultChannel() chan pluginRenderMsg {
	return api.renderResultCh
}

func (api *PluginAPI) ChatMsgChannel() chan pluginChatSendMsg {
	return api.chatMsgCh
}

func (api *PluginAPI) sendPluginError(pluginName, errMsg, source string) {
	if api.errCh == nil {
		return
	}
	select {
	case api.errCh <- PluginError{
		PluginName: pluginName,
		Error:      errMsg,
		Source:     source,
		Timestamp:  time.Now(),
	}:
	default:
	}
}

func (api *PluginAPI) Inject(L *lua.LState, pluginName string) {
	if api.pluginState[pluginName] == nil {
		api.pluginState[pluginName] = make(map[string]any)
	}

	lainTable := L.NewTable()

	windowTable := L.NewTable()
	L.SetField(windowTable, "register", L.NewFunction(func(L *lua.LState) int {
		opts := L.CheckTable(1)
		api.luaWindowRegister(L, pluginName, opts)
		return 0
	}))
	L.SetField(windowTable, "close", L.NewFunction(func(L *lua.LState) int {
		id := L.CheckString(1)
		api.luaWindowClose(id)
		return 0
	}))
	L.SetField(windowTable, "focus", L.NewFunction(func(L *lua.LState) int {
		id := L.CheckString(1)
		if api.wm != nil {
			api.wm.SetFocused(id)
		}
		return 0
	}))
	L.SetField(windowTable, "list", L.NewFunction(func(L *lua.LState) int {
		if api.wm == nil {
			L.Push(L.NewTable())
			return 1
		}
		t := L.NewTable()
		for i, id := range api.wm.ListWindows() {
			L.RawSet(t, lua.LNumber(i+1), lua.LString(id))
		}
		L.Push(t)
		return 1
	}))
	L.SetField(windowTable, "move", L.NewFunction(func(L *lua.LState) int {
		dirInt := int(L.CheckNumber(1))
		if api.wm != nil {
			api.wm.MoveFocused(FocusDir(dirInt))
			api.wm.SetSize(api.wm.width, api.wm.height)
		}
		return 0
	}))
	L.SetField(windowTable, "zoom", L.NewFunction(func(L *lua.LState) int {
		if api.wm != nil {
			api.wm.ToggleZoom()
		}
		return 0
	}))
	L.SetField(windowTable, "equalize", L.NewFunction(func(L *lua.LState) int {
		if api.wm != nil {
			api.wm.Equalize()
		}
		return 0
	}))
	L.SetField(windowTable, "borders", L.NewFunction(func(L *lua.LState) int {
		if L.GetTop() >= 1 {
			enabled := L.CheckBool(1)
			if api.wm != nil {
				if api.wm.bordersEnabled != enabled {
					api.wm.ToggleBorders()
				}
			}
		} else {
			if api.wm != nil {
				api.wm.ToggleBorders()
			}
		}
		return 0
	}))
	L.SetField(windowTable, "invalidate", L.NewFunction(func(L *lua.LState) int {
		id := L.CheckString(1)
		if pw, ok := api.pluginWindows[id]; ok {
			pw.renderDirty = true
		}
		return 0
	}))
	L.SetField(windowTable, "get_content", L.NewFunction(func(L *lua.LState) int {
		id := L.CheckString(1)
		if id == "chat" && api.chat != nil {
			api.chat.chatMu.RLock()
			content := api.chat.renderMessages()
			api.chat.chatMu.RUnlock()
			L.Push(lua.LString(content))
			return 1
		}
		if pw, ok := api.pluginWindows[id]; ok {
			pw.renderMu.RLock()
			content := pw.cachedRender
			pw.renderMu.RUnlock()
			L.Push(lua.LString(content))
			return 1
		}
		if api.wm != nil {
			if win := api.wm.Get(id); win != nil {
				content := win.View(api.wm.width, api.wm.height, false)
				L.Push(lua.LString(content))
				return 1
			}
		}
		L.Push(lua.LNil)
		return 1
	}))
	L.SetField(lainTable, "window", windowTable)

	chatTable := L.NewTable()
	L.SetField(chatTable, "on_message", L.NewFunction(func(L *lua.LState) int {
		fn := L.CheckFunction(1)
		api.messageCallbacks[pluginName] = append(api.messageCallbacks[pluginName], luaCallback{L: L, Fn: fn})
		return 0
	}))
	L.SetField(chatTable, "get_messages", L.NewFunction(func(L *lua.LState) int {
		if api.chat == nil {
			L.Push(L.NewTable())
			return 1
		}
		api.chat.chatMu.RLock()
		msgs := make([]ChatMessage, len(api.chat.messages))
		copy(msgs, api.chat.messages)
		api.chat.chatMu.RUnlock()
		t := L.NewTable()
		for i, msg := range msgs {
			msgTable := L.NewTable()
			L.SetField(msgTable, "role", lua.LString(msg.Role))
			var content strings.Builder
			for _, block := range msg.Blocks {
				if block.Type == "content" {
					content.WriteString(block.Content)
				}
			}
			L.SetField(msgTable, "content", lua.LString(content.String()))
			L.RawSet(t, lua.LNumber(i+1), msgTable)
		}
		L.Push(t)
		return 1
	}))
	L.SetField(chatTable, "send", L.NewFunction(func(L *lua.LState) int {
		content := L.CheckString(1)
		select {
		case api.chatMsgCh <- pluginChatSendMsg{content: content}:
		default:
		}
		return 0
	}))
	L.SetField(lainTable, "chat", chatTable)

	if api.getSession != nil {
		sessionTable := L.NewTable()
		L.SetField(sessionTable, "get_id", L.NewFunction(func(L *lua.LState) int {
			if s := api.getSession(); s != nil {
				L.Push(lua.LString(s.ID))
			} else {
				L.Push(lua.LString(""))
			}
			return 1
		}))
		L.SetField(sessionTable, "get_title", L.NewFunction(func(L *lua.LState) int {
			if s := api.getSession(); s != nil {
				L.Push(lua.LString(s.Title))
			} else {
				L.Push(lua.LString(""))
			}
			return 1
		}))
		L.SetField(sessionTable, "get_profile", L.NewFunction(func(L *lua.LState) int {
			if s := api.getSession(); s != nil {
				L.Push(lua.LString(s.Profile))
			} else {
				L.Push(lua.LString(""))
			}
			return 1
		}))
		L.SetField(lainTable, "session", sessionTable)
	}

	agentTable := L.NewTable()
	L.SetField(agentTable, "on_data", L.NewFunction(func(L *lua.LState) int {
		fn := L.CheckFunction(1)
		api.dataCallbacks[pluginName] = append(api.dataCallbacks[pluginName], luaCallback{L: L, Fn: fn})
		return 0
	}))
	L.SetField(lainTable, "agent", agentTable)

	stateTable := L.NewTable()
	L.SetField(stateTable, "set", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		val := L.CheckAny(2)
		api.pluginState[pluginName][key] = luaToGo(val)
		for _, pw := range api.pluginWindows {
			if pw.pluginName == pluginName {
				pw.renderDirty = true
			}
		}
		return 0
	}))
	L.SetField(stateTable, "get", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		if val, ok := api.pluginState[pluginName][key]; ok {
			L.Push(goToLua(L, val))
		} else {
			L.Push(lua.LNil)
		}
		return 1
	}))
	L.SetField(lainTable, "state", stateTable)

	logTable := L.NewTable()
	L.SetField(logTable, "info", L.NewFunction(func(L *lua.LState) int {
		slog.Info("plugin", "name", pluginName, "msg", L.CheckString(1))
		return 0
	}))
	L.SetField(logTable, "warn", L.NewFunction(func(L *lua.LState) int {
		slog.Warn("plugin", "name", pluginName, "msg", L.CheckString(1))
		return 0
	}))
	L.SetField(logTable, "error", L.NewFunction(func(L *lua.LState) int {
		slog.Error("plugin", "name", pluginName, "msg", L.CheckString(1))
		return 0
	}))
	L.SetField(lainTable, "log", logTable)

	commandTable := L.NewTable()
	L.SetField(commandTable, "register", L.NewFunction(func(L *lua.LState) int {
		name := L.CheckString(1)
		fn := L.CheckFunction(2)
		api.commandCallbacks[name] = func(arg string) {
			exec := api.getExecutor(pluginName)
			if exec == nil {
				return
			}
			exec.callAsync(fn,
				[]lua.LValue{lua.LString(arg)},
				0, api.callbackTimeout, api.errCh, pluginName, "command")
		}
		return 0
	}))
	L.SetField(lainTable, "command", commandTable)

	keybindTable := L.NewTable()
	L.SetField(keybindTable, "register", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		fn := L.CheckFunction(2)
		api.keybindCallbacks[key] = func() {
			exec := api.getExecutor(pluginName)
			if exec == nil {
				return
			}
			exec.callAsync(fn, nil, 0, api.callbackTimeout, api.errCh, pluginName, "keybind")
		}
		api.pluginKeybinds[key] = pluginName
		return 0
	}))
	L.SetField(lainTable, "keybind", keybindTable)

	execTable := L.NewTable()
	L.SetField(execTable, "exec", L.NewFunction(func(L *lua.LState) int {
		cmdStr := L.CheckString(1)
		timeout, cwd, env := parseExecOpts(L)
		stdout, stderr, exitCode, err := runCommand(cmdStr, timeout, cwd, env)
		pushExecResult(L, stdout, stderr, exitCode, err)
		return 1
	}))
	L.SetField(execTable, "exec_async", L.NewFunction(func(L *lua.LState) int {
		cmdStr := L.CheckString(1)
		timeout, cwd, env := parseExecOpts(L)
		callback := L.CheckFunction(3)
		executor := api.executors[pluginName]
		if executor == nil {
			L.Push(lua.LNil)
			return 1
		}
		go func() {
			stdout, stderr, exitCode, err := runCommand(cmdStr, timeout, cwd, env)
			select {
			case executor.execResultCh <- execResultMsg{
				callback: callback,
				stdout:   stdout,
				stderr:   stderr,
				exitCode: exitCode,
				err:      err,
			}:
			default:
			}
		}()
		return 0
	}))
	L.SetField(lainTable, "exec", execTable)

	L.SetGlobal("lain", lainTable)
}

func (api *PluginAPI) luaWindowRegister(L *lua.LState, pluginName string, opts *lua.LTable) {
	id := getStringField(L, opts, "id")
	title := getStringField(L, opts, "title")
	if id == "" {
		slog.Error("plugin window register: missing id", "plugin", pluginName)
		api.sendPluginError(pluginName, "window register: missing id", "register")
		return
	}
	if title == "" {
		title = id
	}

	renderFn := getFunctionField(L, opts, "render")
	updateFn := getFunctionField(L, opts, "update")
	isFloat := getBoolField(L, opts, "float")

	pw := newPluginWindow(id, title, pluginName, L, renderFn, updateFn, api)

	if v := L.RawGet(opts, lua.LString("interval")); v != lua.LNil {
		if n, ok := v.(lua.LNumber); ok && float64(n) > 0 {
			pw.interval = time.Duration(float64(n)) * time.Millisecond
		}
	}
	pw.tickFn = getFunctionField(L, opts, "tick")

	if existing, ok := api.pluginWindows[id]; ok {
		close(existing.stopTick)
	}
	api.pluginWindows[id] = pw

	if pw.interval > 0 {
		interval := pw.interval
		tickFn := pw.tickFn
		stopTick := pw.stopTick
		exec := api.getExecutor(pluginName)
		callbackTimeout := api.callbackTimeout
		errCh := api.errCh
		go func() {
			for {
				select {
				case <-stopTick:
					return
				case <-time.After(interval):
					if tickFn != nil && exec != nil {
						_, err := exec.call(tickFn, nil, 0, callbackTimeout)
						if err != nil && errCh != nil {
							select {
							case errCh <- PluginError{PluginName: pluginName, Error: err.Error(), Source: "tick", Timestamp: time.Now()}:
							default:
							}
						}
					}
					pw.renderDirty = true
				}
			}
		}()
	}

	if api.wm != nil {
		if isFloat {
			w := 40
			h := 12
			x := (api.wm.width - w) / 2
			y := (api.wm.height - h) / 2
			if x < 0 {
				x = 0
			}
			if y < 0 {
				y = 0
			}
			api.wm.AddFloating(pw, x, y, w, h)
		} else {
			focusedID := api.wm.FocusedID()
			if focusedID == "" {
				focusedID = "chat"
			}
			api.wm.AddWithSplit(focusedID, SplitVertical, pw, 0)
			api.wm.SetSize(api.wm.width, api.wm.height)
		}
		if api.onWindowChange != nil {
			api.onWindowChange()
		}
	}
}

func (api *PluginAPI) luaWindowClose(id string) {
	if pw, ok := api.pluginWindows[id]; ok {
		close(pw.stopTick)
	}
	if api.wm == nil {
		delete(api.pluginWindows, id)
		return
	}
	if api.wm.HasFloating(id) {
		api.wm.RemoveFloating(id)
	} else {
		api.wm.Remove(id)
	}
	delete(api.pluginWindows, id)
	if api.onWindowChange != nil {
		api.onWindowChange()
	}
}

func (api *PluginAPI) FireMessageCallbacks(role, text string) {
	for plugin, callbacks := range api.messageCallbacks {
		exec := api.getExecutor(plugin)
		if exec == nil {
			continue
		}
		for _, cb := range callbacks {
			fn := cb.Fn
			exec.callAsync(fn,
				[]lua.LValue{lua.LString(role), lua.LString(text)},
				0, api.callbackTimeout, api.errCh, plugin, "callback")
		}
	}
}

func (api *PluginAPI) FireDataCallbacks(pluginName, data string) {
	callbacks, ok := api.dataCallbacks[pluginName]
	if !ok {
		return
	}
	exec := api.getExecutor(pluginName)
	if exec == nil {
		return
	}
	for _, cb := range callbacks {
		fn := cb.Fn
		exec.callAsync(fn,
			[]lua.LValue{lua.LString(data)},
			0, api.callbackTimeout, api.errCh, pluginName, "on_data")
	}
}

func (api *PluginAPI) ClearPlugin(pluginName string) {
	delete(api.pluginState, pluginName)
	delete(api.messageCallbacks, pluginName)
	delete(api.executors, pluginName)
	delete(api.dataCallbacks, pluginName)
	for id, pw := range api.pluginWindows {
		if pw.pluginName == pluginName {
			api.luaWindowClose(id)
		}
	}
	for name := range api.commandCallbacks {
		if strings.HasPrefix(name, pluginName+":") {
			delete(api.commandCallbacks, name)
		}
	}
	for key, plugin := range api.pluginKeybinds {
		if plugin == pluginName {
			delete(api.keybindCallbacks, key)
			delete(api.pluginKeybinds, key)
		}
	}
}

func (api *PluginAPI) GetCommandCallbacks() map[string]func(string) {
	return api.commandCallbacks
}

func (api *PluginAPI) GetKeybindCallbacks() map[string]func() {
	return api.keybindCallbacks
}

func luaToGo(val lua.LValue) any {
	switch v := val.(type) {
	case lua.LString:
		return string(v)
	case lua.LNumber:
		return float64(v)
	case lua.LBool:
		return bool(v)
	case *lua.LTable:
		result := make(map[string]any)
		v.ForEach(func(k, lv lua.LValue) {
			result[k.String()] = luaToGo(lv)
		})
		return result
	default:
		return nil
	}
}

func goToLua(L *lua.LState, val any) lua.LValue {
	switch v := val.(type) {
	case string:
		return lua.LString(v)
	case float64:
		return lua.LNumber(v)
	case int:
		return lua.LNumber(v)
	case bool:
		return lua.LBool(v)
	case map[string]any:
		t := L.NewTable()
		for k, lv := range v {
			L.SetField(t, k, goToLua(L, lv))
		}
		return t
	default:
		return lua.LNil
	}
}

func getStringField(L *lua.LState, tbl *lua.LTable, key string) string {
	val := L.RawGet(tbl, lua.LString(key))
	if str, ok := val.(lua.LString); ok {
		return string(str)
	}
	return ""
}

func getBoolField(L *lua.LState, tbl *lua.LTable, key string) bool {
	val := L.RawGet(tbl, lua.LString(key))
	if b, ok := val.(lua.LBool); ok {
		return bool(b)
	}
	return false
}

func getFunctionField(L *lua.LState, tbl *lua.LTable, key string) *lua.LFunction {
	val := L.RawGet(tbl, lua.LString(key))
	if fn, ok := val.(*lua.LFunction); ok {
		return fn
	}
	return nil
}

func parseExecOpts(L *lua.LState) (timeout time.Duration, cwd string, env map[string]string) {
	timeout = 30 * time.Second
	if L.GetTop() >= 2 {
		if opts, ok := L.Get(2).(*lua.LTable); ok {
			if v := L.RawGet(opts, lua.LString("timeout")); v != lua.LNil {
				if n, ok := v.(lua.LNumber); ok {
					timeout = time.Duration(float64(n)) * time.Second
				}
			}
			if v := L.RawGet(opts, lua.LString("cwd")); v != lua.LNil {
				if s, ok := v.(lua.LString); ok {
					cwd = string(s)
				}
			}
			if v := L.RawGet(opts, lua.LString("env")); v != lua.LNil {
				if t, ok := v.(*lua.LTable); ok {
					env = make(map[string]string)
					t.ForEach(func(k, lv lua.LValue) {
						env[k.String()] = lv.String()
					})
				}
			}
		}
	}
	return
}

func runCommand(cmdStr string, timeout time.Duration, cwd string, env map[string]string) (stdout, stderr string, exitCode int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	if cwd != "" {
		cmd.Dir = cwd
	}
	if env != nil {
		for k, v := range env {
			cmd.Env = append(cmd.Environ(), k+"="+v)
		}
	}
	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err = cmd.Run()
	stdout = stdoutBuf.String()
	stderr = stderrBuf.String()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return stdout, "command timed out", -1, err
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
		return stdout, stderr, exitCode, err
	}
	return stdout, stderr, 0, nil
}

func pushExecResult(L *lua.LState, stdout, stderr string, exitCode int, err error) {
	t := L.NewTable()
	L.SetField(t, "stdout", lua.LString(stdout))
	L.SetField(t, "stderr", lua.LString(stderr))
	L.SetField(t, "exit_code", lua.LNumber(exitCode))
	L.SetField(t, "success", lua.LBool(exitCode == 0 && err == nil))
	L.Push(t)
}
