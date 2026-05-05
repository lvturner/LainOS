package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	lua "github.com/yuin/gopher-lua"
)

type pluginEventMsg struct {
	action   string
	filename string
}

type PluginError struct {
	PluginName string
	Error      string
	Source     string
	Timestamp  time.Time
}

type PluginLoader struct {
	mu         sync.Mutex
	api        *PluginAPI
	watcher    *fsnotify.Watcher
	dir        string
	stopCh     chan struct{}
	eventCh    chan pluginEventMsg
	errCh      chan PluginError
	loaded     map[string]bool
	executors  map[string]*pluginExecutor
	started    bool
}

func NewPluginLoader(dir string, api *PluginAPI) *PluginLoader {
	return &PluginLoader{
		api:        api,
		dir:        dir,
		stopCh:     make(chan struct{}),
		eventCh:    make(chan pluginEventMsg, 20),
		errCh:      make(chan PluginError, 16),
		loaded:     make(map[string]bool),
		executors:  make(map[string]*pluginExecutor),
	}
}

func (pl *PluginLoader) Errors() <-chan PluginError {
	return pl.errCh
}

func (pl *PluginLoader) ErrorChannel() chan PluginError {
	return pl.errCh
}

func (pl *PluginLoader) IsLoaded(name string) bool {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return pl.loaded[name]
}

func (pl *PluginLoader) sendError(pluginName, errMsg, source string) {
	select {
	case pl.errCh <- PluginError{
		PluginName: pluginName,
		Error:      errMsg,
		Source:     source,
		Timestamp:  time.Now(),
	}:
	default:
	}
}

func (pl *PluginLoader) Events() <-chan pluginEventMsg {
	return pl.eventCh
}

func (pl *PluginLoader) Start(enabled []string) error {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	if pl.started {
		return nil
	}

	os.MkdirAll(pl.dir, 0755)

	enabledSet := make(map[string]bool, len(enabled))
	for _, name := range enabled {
		enabledSet[name] = true
	}

	entries, err := os.ReadDir(pl.dir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".lua") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if len(enabledSet) > 0 && !enabledSet[name] {
			continue
		}
		pl.loadPluginLocked(entry.Name())
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	pl.watcher = watcher
	if err := watcher.Add(pl.dir); err != nil {
		slog.Warn("plugin watcher: cannot watch directory", "dir", pl.dir, "error", err)
	}

	pl.started = true
	go pl.watchLoop()
	return nil
}

func (pl *PluginLoader) Stop() {
	select {
	case <-pl.stopCh:
		return
	default:
		close(pl.stopCh)
	}
	if pl.watcher != nil {
		pl.watcher.Close()
	}
	pl.mu.Lock()
	defer pl.mu.Unlock()
	for name, exec := range pl.executors {
		exec.stop()
		delete(pl.executors, name)
	}
	pl.started = false
}

func (pl *PluginLoader) Load(filename string) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	pl.loadPluginLocked(filename)
}

func (pl *PluginLoader) Unload(filename string) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	pl.unloadPluginLocked(filename)
}

func (pl *PluginLoader) loadPluginLocked(filename string) {
	path := filepath.Join(filename)
	name := strings.TrimSuffix(filename, filepath.Ext(filename))

	pl.unloadPluginLocked(filename)

	L := newPluginState()
	pl.api.Inject(L, name)

	data, err := os.ReadFile(filepath.Join(pl.dir, path))
	if err != nil {
		slog.Error("plugin load error", "plugin", name, "error", err)
		L.Close()
		pl.loaded[name] = false
		pl.sendError(name, err.Error(), "load")
		return
	}

	exec := newPluginExecutor(L)
	exec.start()

	loadTimeout := pl.api.loadTimeout
	if loadTimeout == 0 {
		loadTimeout = 10 * time.Second
	}
	if err := exec.doString(string(data), loadTimeout); err != nil {
		slog.Error("plugin exec error", "plugin", name, "error", err)
		exec.stop()
		pl.loaded[name] = false
		pl.sendError(name, err.Error(), "exec")
		return
	}

	pl.executors[name] = exec
	pl.loaded[name] = true
	pl.api.setExecutor(name, exec)
	slog.Info("plugin loaded", "name", name)
}

func (pl *PluginLoader) unloadPluginLocked(filename string) {
	name := strings.TrimSuffix(filename, filepath.Ext(filename))
	if exec, ok := pl.executors[name]; ok {
		exec.stop()
		delete(pl.executors, name)
	}
	pl.api.ClearPlugin(name)
}

func (pl *PluginLoader) Reload(name string) error {
	filename := name
	if !strings.HasSuffix(filename, ".lua") {
		filename += ".lua"
	}
	fullPath := filepath.Join(pl.dir, filename)
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		return fmt.Errorf("plugin %q not found", name)
	}
	pl.Load(filename)
	return nil
}

func (pl *PluginLoader) ReloadAll() {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	entries, err := os.ReadDir(pl.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".lua") {
			pl.loadPluginLocked(entry.Name())
		}
	}
}

func (pl *PluginLoader) ListPlugins() []string {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	var names []string
	entries, err := os.ReadDir(pl.dir)
	if err != nil {
		return names
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".lua") {
			names = append(names, strings.TrimSuffix(entry.Name(), ".lua"))
		}
	}
	return names
}

func (pl *PluginLoader) PluginDir() string {
	return pl.dir
}

func (pl *PluginLoader) GetExecutor(name string) *pluginExecutor {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return pl.executors[name]
}

func (pl *PluginLoader) watchLoop() {
	var debounceTimer *time.Timer
	pendingEvents := make(map[string]bool)

	for {
		select {
		case <-pl.stopCh:
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return
		case event, ok := <-pl.watcher.Events:
			if !ok {
				return
			}
			base := filepath.Base(event.Name)
			if filepath.Ext(base) != ".lua" {
				continue
			}
			pendingEvents[base] = true
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(500*time.Millisecond, func() {
				events := make(map[string]bool)
				pl.mu.Lock()
				for k, v := range pendingEvents {
					events[k] = v
					delete(pendingEvents, k)
				}
				pl.mu.Unlock()

				for name := range events {
					fullPath := filepath.Join(pl.dir, name)
					if _, err := os.Stat(fullPath); os.IsNotExist(err) {
						select {
						case pl.eventCh <- pluginEventMsg{action: "remove", filename: name}:
						default:
						}
					} else {
						select {
						case pl.eventCh <- pluginEventMsg{action: "reload", filename: name}:
						default:
						}
					}
				}
			})
		case err, ok := <-pl.watcher.Errors:
			if !ok {
				return
			}
			slog.Error("plugin watcher error", "error", err)
		}
	}
}

func newPluginState() *lua.LState {
	L := lua.NewState(lua.Options{
		SkipOpenLibs: true,
	})
	lua.OpenBase(L)
	lua.OpenString(L)
	lua.OpenTable(L)
	lua.OpenMath(L)
	lua.OpenCoroutine(L)
	lua.OpenDebug(L)
	lua.OpenIo(L)
	lua.OpenOs(L)
	lua.OpenPackage(L)
	return L
}

type pluginCall struct {
	fn      *lua.LFunction
	args    []lua.LValue
	nRet    int
	ret     chan<- pluginResult
	timeout time.Duration
}

type pluginResult struct {
	values []lua.LValue
	err    error
}

type execResultMsg struct {
	callback *lua.LFunction
	stdout   string
	stderr   string
	exitCode int
	err      error
}

type pluginExecutor struct {
	L                   *lua.LState
	callCh              chan pluginCall
	execResultCh        chan execResultMsg
	stopCh              chan struct{}
	doneCh              chan struct{}
	consecutiveFailures int
	unhealthy           bool
}

func newPluginExecutor(L *lua.LState) *pluginExecutor {
	return &pluginExecutor{
		L:            L,
		callCh:       make(chan pluginCall, 8),
		execResultCh: make(chan execResultMsg, 8),
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

func (e *pluginExecutor) start() {
	go e.run()
}

func (e *pluginExecutor) stop() {
	close(e.stopCh)
	<-e.doneCh
	e.L.Close()
}

func (e *pluginExecutor) call(fn *lua.LFunction, args []lua.LValue, nRet int, timeout time.Duration) (pluginResult, error) {
	retCh := make(chan pluginResult, 1)
	c := pluginCall{
		fn:      fn,
		args:    args,
		nRet:    nRet,
		ret:     retCh,
		timeout: timeout,
	}
	select {
	case e.callCh <- c:
	default:
		return pluginResult{}, fmt.Errorf("executor queue full")
	}
	select {
	case r := <-retCh:
		return r, nil
	case <-time.After(timeout + 100*time.Millisecond):
		return pluginResult{}, fmt.Errorf("executor call timed out")
	}
}

func (e *pluginExecutor) callAsync(fn *lua.LFunction, args []lua.LValue, nRet int, timeout time.Duration, errCh chan<- PluginError, pluginName, source string) {
	c := pluginCall{
		fn:      fn,
		args:    args,
		nRet:    nRet,
		ret:     make(chan pluginResult, 1),
		timeout: timeout,
	}
	select {
	case e.callCh <- c:
	default:
		select {
		case errCh <- PluginError{PluginName: pluginName, Error: "executor queue full", Source: source, Timestamp: time.Now()}:
		default:
		}
	}
}

func (e *pluginExecutor) run() {
	defer close(e.doneCh)
	for {
		select {
		case <-e.stopCh:
			return
		case c := <-e.callCh:
			ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
			e.L.SetContext(ctx)
			err := e.L.CallByParam(lua.P{
				Fn:      c.fn,
				NRet:    c.nRet,
				Protect: true,
			}, c.args...)
			cancel()
			var values []lua.LValue
			for i := 0; i < c.nRet; i++ {
				values = append(values, e.L.Get(-1))
				e.L.Pop(1)
			}
			c.ret <- pluginResult{values: values, err: err}
		case r := <-e.execResultCh:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			e.L.SetContext(ctx)
			t := e.L.NewTable()
			e.L.SetField(t, "stdout", lua.LString(r.stdout))
			e.L.SetField(t, "stderr", lua.LString(r.stderr))
			e.L.SetField(t, "exit_code", lua.LNumber(r.exitCode))
			e.L.SetField(t, "success", lua.LBool(r.exitCode == 0 && r.err == nil))
			e.L.CallByParam(lua.P{Fn: r.callback, NRet: 0, Protect: true}, t)
			cancel()
		}
	}
}

func (e *pluginExecutor) doString(src string, timeout time.Duration) error {
	retCh := make(chan pluginResult, 1)
	fn, err := e.L.LoadString(src)
	if err != nil {
		return err
	}
	c := pluginCall{
		fn:      fn,
		args:    nil,
		nRet:    0,
		ret:     retCh,
		timeout: timeout,
	}
	select {
	case e.callCh <- c:
	default:
		return fmt.Errorf("executor queue full")
	}
	select {
	case r := <-retCh:
		return r.err
	case <-time.After(timeout + 100*time.Millisecond):
		return fmt.Errorf("plugin load timed out")
	}
}
