package main

import (
	"os"
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	tmpfile.WriteString("routes:\n  - path: /test\n    command: /bin/echo\n")
	tmpfile.Close()

	cfg, err := LoadConfig(tmpfile.Name())
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if cfg.Server.Listen != "0.0.0.0:8080" {
		t.Errorf("default listen = %q, want %q", cfg.Server.Listen, "0.0.0.0:8080")
	}
	if cfg.Server.Timeout != 30*time.Second {
		t.Errorf("default timeout = %v, want %v", cfg.Server.Timeout, 30*time.Second)
	}
	if cfg.Server.MaxBodySize != 10<<20 {
		t.Errorf("default maxBodySize = %d, want %d", cfg.Server.MaxBodySize, 10<<20)
	}
	if len(cfg.Routes) != 1 {
		t.Fatalf("routes count = %d, want 1", len(cfg.Routes))
	}
	if cfg.Routes[0].Method != "POST" {
		t.Errorf("default method = %q, want %q", cfg.Routes[0].Method, "POST")
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	tmpfile.WriteString(`server:
  listen: "127.0.0.1:9090"
  timeout: 10s
  max_body_size: 1048576
routes:
  - path: /webhook
    command: /usr/bin/true
    method: GET
`)
	tmpfile.Close()

	cfg, err := LoadConfig(tmpfile.Name())
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if cfg.Server.Listen != "127.0.0.1:9090" {
		t.Errorf("listen = %q, want %q", cfg.Server.Listen, "127.0.0.1:9090")
	}
	if cfg.Server.Timeout != 10*time.Second {
		t.Errorf("timeout = %v, want %v", cfg.Server.Timeout, 10*time.Second)
	}
	if cfg.Server.MaxBodySize != 1048576 {
		t.Errorf("maxBodySize = %d, want %d", cfg.Server.MaxBodySize, 1048576)
	}
	if cfg.Routes[0].Method != "GET" {
		t.Errorf("method = %q, want %q", cfg.Routes[0].Method, "GET")
	}
}

func TestLoadConfigInvalidYAML(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	tmpfile.WriteString("::invalid\nyaml::\n")
	tmpfile.Close()

	_, err = LoadConfig(tmpfile.Name())
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestLoadConfigFileNotFound(t *testing.T) {
	_, err := LoadConfig("/nonexistent/path.yaml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadConfigSecretEnvExpansion(t *testing.T) {
	os.Setenv("TEST_SECRET_VAL", "expanded_secret")
	defer os.Unsetenv("TEST_SECRET_VAL")

	tmpfile, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	tmpfile.WriteString(`routes:
  - path: /hook
    command: /bin/echo
    secret: "$TEST_SECRET_VAL"
`)
	tmpfile.Close()

	cfg, err := LoadConfig(tmpfile.Name())
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if cfg.Routes[0].Secret != "expanded_secret" {
		t.Errorf("secret = %q, want %q", cfg.Routes[0].Secret, "expanded_secret")
	}
}
