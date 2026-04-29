package main

import (
	"encoding/json"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type LainConfig struct {
	APIURL              string        `yaml:"api_url"`
	APIKey              string        `yaml:"api_key"`
	Model               string        `yaml:"model"`
	Temperature         float64       `yaml:"temperature"`
	MaxTokens           int           `yaml:"max_tokens"`
	ContextWindow       int           `yaml:"context_window"`
	CompactionThreshold int           `yaml:"compaction_threshold"`
	CompactionStrategy  string        `yaml:"compaction_strategy"`
	IdleTimeout         time.Duration `yaml:"idle_timeout"`
	MaxNudges           int           `yaml:"max_nudges"`
	NudgeMessage        string        `yaml:"nudge_message"`
	NoStream            bool          `yaml:"no_stream"`
}

type ServersConfig struct {
	MCPServers map[string]MCPServerConfig `json:"mcpServers"`
}

type MCPServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

func LoadConfig(path string) (*LainConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg LainConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.ContextWindow == 0 {
		cfg.ContextWindow = 128000
	}
	if cfg.CompactionThreshold == 0 {
		cfg.CompactionThreshold = 60
	}
	if cfg.CompactionStrategy == "" {
		cfg.CompactionStrategy = "keep_last"
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 120 * time.Second
	}
	if cfg.MaxNudges == 0 {
		cfg.MaxNudges = 3
	}
	if cfg.NudgeMessage == "" {
		cfg.NudgeMessage = "You haven't produced output in a while. If you're about to run something slow, use extend_timeout. Otherwise, continue your task."
	}
	return &cfg, nil
}

func LoadServers(path string) (*ServersConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ServersConfig{MCPServers: make(map[string]MCPServerConfig)}, nil
		}
		return nil, err
	}
	var cfg ServersConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = make(map[string]MCPServerConfig)
	}
	return &cfg, nil
}

func LoadAgents(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}
