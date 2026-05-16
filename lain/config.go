package main

import (
	"encoding/json"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type SubAgentConfig struct {
	Profile string `yaml:"profile"`
	AutoFix bool   `yaml:"auto_fix"`
}

type PluginsConfig struct {
	Enabled         []string      `yaml:"enabled"`
	RenderTimeout   time.Duration `yaml:"render_timeout"`
	CallbackTimeout time.Duration `yaml:"callback_timeout"`
	LoadTimeout     time.Duration `yaml:"load_timeout"`
}

type SandboxConfig struct {
	TmpSize string `yaml:"tmp_size"`
}

type AskModeToolsConfig struct {
	Builtin    []string `yaml:"builtin"`
	MCPServers []string `yaml:"mcp_servers"`
}

type AskModeConfig struct {
	SystemPrompt string            `yaml:"system_prompt"`
	Sandbox      SandboxConfig     `yaml:"sandbox"`
	Tools        AskModeToolsConfig `yaml:"tools"`
}

type LainConfig struct {
	APIURL              string        `yaml:"api_url"`
	APIKey              string        `yaml:"api_key"`
	Model               string        `yaml:"model"`
	Temperature         float64       `yaml:"temperature"`
	MaxTokens           int           `yaml:"max_tokens"`
	ContextLength       int           `yaml:"context_length"`
	CompactionThreshold int           `yaml:"compaction_threshold"`
	CompactionStrategy  string        `yaml:"compaction_strategy"`
	IdleTimeout         time.Duration `yaml:"idle_timeout"`
	MaxNudges           int           `yaml:"max_nudges"`
	NudgeMessage        string        `yaml:"nudge_message"`
	NoStream            bool          `yaml:"no_stream"`
	LoopThreshold       int           `yaml:"loop_threshold"`
	SubAgent              SubAgentConfig `yaml:"sub_agent"`
	Plugins               PluginsConfig  `yaml:"plugins"`
	PluginRenderTimeout   time.Duration  `yaml:"plugin_render_timeout"`
	PluginCallbackTimeout time.Duration  `yaml:"plugin_callback_timeout"`
	PluginLoadTimeout     time.Duration  `yaml:"plugin_load_timeout"`
	AskMode               AskModeConfig  `yaml:"ask_mode"`
}

func (c *LainConfig) AskModeDefaults() {
	if c.AskMode.SystemPrompt == "" {
		c.AskMode.SystemPrompt = "You are a research assistant in read-only mode. You can read files, search the web, and execute read-only commands via the restricted_command tool. You cannot modify the filesystem — all paths are mounted read-only by the kernel. Use curl for web requests and standard file-reading tools (cat, grep, find, etc.) for inspection. If you need to download something, it can only go to /tmp which is ephemeral."
	}
	if c.AskMode.Sandbox.TmpSize == "" {
		c.AskMode.Sandbox.TmpSize = "100m"
	}
	if len(c.AskMode.Tools.Builtin) == 0 {
		c.AskMode.Tools.Builtin = []string{"restricted_command", "extend_timeout"}
	}
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
	if cfg.ContextLength == 0 {
		cfg.ContextLength = 128000
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
	if cfg.LoopThreshold == 0 {
		cfg.LoopThreshold = 3
	}
	if cfg.Plugins.RenderTimeout == 0 {
		cfg.Plugins.RenderTimeout = cfg.PluginRenderTimeout
	}
	if cfg.Plugins.CallbackTimeout == 0 {
		cfg.Plugins.CallbackTimeout = cfg.PluginCallbackTimeout
	}
	if cfg.Plugins.LoadTimeout == 0 {
		cfg.Plugins.LoadTimeout = cfg.PluginLoadTimeout
	}
	if cfg.Plugins.RenderTimeout == 0 {
		cfg.Plugins.RenderTimeout = 50 * time.Millisecond
	}
	if cfg.Plugins.CallbackTimeout == 0 {
		cfg.Plugins.CallbackTimeout = 5 * time.Second
	}
	if cfg.Plugins.LoadTimeout == 0 {
		cfg.Plugins.LoadTimeout = 10 * time.Second
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
