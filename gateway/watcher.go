package main

import (
	"log/slog"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type Watcher struct {
	fw       *fsnotify.Watcher
	path     string
	updateCh chan<- *Config
	mu       sync.Mutex
	timer    *time.Timer
}

func NewWatcher(path string, updateCh chan<- *Config) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	if err := fw.Add(path); err != nil {
		fw.Close()
		return nil, err
	}

	w := &Watcher{
		fw:       fw,
		path:     path,
		updateCh: updateCh,
		timer:    nil,
	}

	go w.loop()

	return w, nil
}

func (w *Watcher) Close() {
	w.fw.Close()
}

func (w *Watcher) loop() {
	for {
		select {
		case event, ok := <-w.fw.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
				w.debouncedReload()
			}
		case err, ok := <-w.fw.Errors:
			if !ok {
				return
			}
			slog.Error("watcher error", "error", err)
		}
	}
}

func (w *Watcher) debouncedReload() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.timer != nil {
		w.timer.Stop()
	}

	w.timer = time.AfterFunc(500*time.Millisecond, func() {
		cfg, err := LoadConfig(w.path)
		if err != nil {
			slog.Error("failed to reload config", "error", err)
			return
		}
		slog.Info("config reloaded", "routes", len(cfg.Routes))

		select {
		case w.updateCh <- cfg:
		default:
		}
	})
}
