package main

import (
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

type PluginLoader struct {
	mu       sync.Mutex
	api      *PluginAPI
	watcher  *fsnotify.Watcher
	dir      string
	stopCh   chan struct{}
	eventCh  chan pluginEventMsg
	started  bool
}

func NewPluginLoader(dir string, api *PluginAPI) *PluginLoader {
	return &PluginLoader{
		api:     api,
		dir:     dir,
		stopCh:  make(chan struct{}),
		eventCh: make(chan pluginEventMsg, 20),
	}
}

func (pl *PluginLoader) Events() <-chan pluginEventMsg {
	return pl.eventCh
}

func (pl *PluginLoader) Start() error {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	if pl.started {
		return nil
	}

	os.MkdirAll(pl.dir, 0755)

	entries, err := os.ReadDir(pl.dir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".lua") {
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
	path := filepath.Join(pl.dir, filename)
	name := strings.TrimSuffix(filename, filepath.Ext(filename))

	pl.unloadPluginLocked(filename)

	L := newSandboxedState()
	pl.api.Inject(L, name)

	data, err := os.ReadFile(path)
	if err != nil {
		slog.Error("plugin load error", "plugin", name, "error", err)
		L.Close()
		return
	}

	if err := L.DoString(string(data)); err != nil {
		slog.Error("plugin exec error", "plugin", name, "error", err)
		L.Close()
		return
	}

	slog.Info("plugin loaded", "name", name)
}

func (pl *PluginLoader) unloadPluginLocked(filename string) {
	name := strings.TrimSuffix(filename, filepath.Ext(filename))
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

func newSandboxedState() *lua.LState {
	L := lua.NewState(lua.Options{
		SkipOpenLibs: true,
	})
	lua.OpenString(L)
	lua.OpenTable(L)
	lua.OpenMath(L)
	lua.OpenCoroutine(L)
	return L
}
