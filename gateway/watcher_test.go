package main

import (
	"os"
	"testing"
	"time"
)

func TestWatcherDebounce(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "watcher-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	tmpfile.WriteString("server:\n  listen: ':8080'\nroutes: []\n")
	tmpfile.Close()

	updateCh := make(chan *Config, 1)
	w, err := NewWatcher(tmpfile.Name(), updateCh)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	for i := 0; i < 5; i++ {
		f, err := os.OpenFile(tmpfile.Name(), os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString("server:\n  listen: ':9090'\nroutes: []\n")
		f.Close()
		time.Sleep(50 * time.Millisecond)
	}

	select {
	case cfg := <-updateCh:
		if cfg.Server.Listen != ":9090" {
			t.Errorf("listen = %q, want %q", cfg.Server.Listen, ":9090")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for config reload")
	}

	time.Sleep(600 * time.Millisecond)
	select {
	case <-updateCh:
		t.Error("unexpected extra config update (debounce should have coalesced)")
	default:
	}
}

func TestWatcherDetectsNewFile(t *testing.T) {
	dir, err := os.MkdirTemp("", "watcher-dir-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	tmpfile := dir + "/config.yaml"
	os.WriteFile(tmpfile, []byte("server:\n  listen: ':8080'\nroutes: []\n"), 0644)

	updateCh := make(chan *Config, 1)
	w, err := NewWatcher(tmpfile, updateCh)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	os.WriteFile(tmpfile, []byte("server:\n  listen: ':7070'\nroutes: []\n"), 0644)

	select {
	case cfg := <-updateCh:
		if cfg.Server.Listen != ":7070" {
			t.Errorf("listen = %q, want %q", cfg.Server.Listen, ":7070")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for config reload after write")
	}
}
