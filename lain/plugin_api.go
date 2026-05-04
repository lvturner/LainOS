package main

import (
	"fmt"
	"log/slog"
	"strings"

	lua "github.com/yuin/gopher-lua"
	tea "github.com/charmbracelet/bubbletea"
)

type luaCallback struct {
	L  *lua.LState
	Fn *lua.LFunction
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
	onWindowChange   func()
}

type pluginWindow struct {
	id         string
	title      string
	pluginName string
	L          *lua.LState
	renderFn   *lua.LFunction
	updateFn   *lua.LFunction
	width      int
	height     int
	state      map[string]any
}

func newPluginWindow(id, title, pluginName string, L *lua.LState, renderFn, updateFn *lua.LFunction) *pluginWindow {
	return &pluginWindow{
		id:         id,
		title:      title,
		pluginName: pluginName,
		L:          L,
		renderFn:   renderFn,
		updateFn:   updateFn,
		state:      make(map[string]any),
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
	return w, nil
}

func (w *pluginWindow) View(width, height int, focused bool) string {
	if w.renderFn == nil {
		return fmt.Sprintf("[%s: no render function]", w.title)
	}

	L := w.L
	top := L.GetTop()
	defer L.SetTop(top)

	err := L.CallByParam(lua.P{
		Fn:      w.renderFn,
		NRet:    1,
		Protect: true,
	}, lua.LNumber(width), lua.LNumber(height))

	if err != nil {
		slog.Error("plugin render error", "plugin", w.pluginName, "window", w.id, "error", err)
		return fmt.Sprintf("[render error: %s]", err.Error())
	}

	result := L.Get(-1)
	if str, ok := result.(lua.LString); ok {
		return string(str)
	}
	if result == lua.LNil {
		return ""
	}
	return fmt.Sprintf("[%v]", result)
}

func NewPluginAPI() *PluginAPI {
	return &PluginAPI{
		pluginState:      make(map[string]map[string]any),
		messageCallbacks: make(map[string][]luaCallback),
		commandCallbacks: make(map[string]func(string)),
		keybindCallbacks: make(map[string]func()),
		pluginWindows:    make(map[string]*pluginWindow),
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
		t := L.NewTable()
		for i, msg := range api.chat.messages {
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

	stateTable := L.NewTable()
	L.SetField(stateTable, "set", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		val := L.CheckAny(2)
		api.pluginState[pluginName][key] = luaToGo(val)
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
			top := L.GetTop()
			defer L.SetTop(top)
			if err := L.CallByParam(lua.P{Fn: fn, NRet: 0, Protect: true}, lua.LString(arg)); err != nil {
				slog.Error("plugin command error", "command", name, "error", err)
			}
		}
		return 0
	}))
	L.SetField(lainTable, "command", commandTable)

	keybindTable := L.NewTable()
	L.SetField(keybindTable, "register", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		fn := L.CheckFunction(2)
		api.keybindCallbacks[key] = func() {
			top := L.GetTop()
			defer L.SetTop(top)
			if err := L.CallByParam(lua.P{Fn: fn, NRet: 0, Protect: true}); err != nil {
				slog.Error("plugin keybind error", "key", key, "error", err)
			}
		}
		return 0
	}))
	L.SetField(lainTable, "keybind", keybindTable)

	L.SetGlobal("lain", lainTable)
}

func (api *PluginAPI) luaWindowRegister(L *lua.LState, pluginName string, opts *lua.LTable) {
	id := getStringField(L, opts, "id")
	title := getStringField(L, opts, "title")
	if id == "" {
		slog.Error("plugin window register: missing id", "plugin", pluginName)
		return
	}
	if title == "" {
		title = id
	}

	renderFn := getFunctionField(L, opts, "render")
	updateFn := getFunctionField(L, opts, "update")
	isFloat := getBoolField(L, opts, "float")

	pw := newPluginWindow(id, title, pluginName, L, renderFn, updateFn)
	api.pluginWindows[id] = pw

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
	if api.wm == nil {
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
		for _, cb := range callbacks {
			L := cb.L
			if L == nil {
				continue
			}
			top := L.GetTop()
			err := L.CallByParam(lua.P{Fn: cb.Fn, NRet: 0, Protect: true},
				lua.LString(role), lua.LString(text))
			L.SetTop(top)
			if err != nil {
				slog.Error("plugin message callback error", "plugin", plugin, "error", err)
			}
		}
	}
}

func (api *PluginAPI) ClearPlugin(pluginName string) {
	delete(api.pluginState, pluginName)
	delete(api.messageCallbacks, pluginName)
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
	for key := range api.keybindCallbacks {
		delete(api.keybindCallbacks, key)
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
