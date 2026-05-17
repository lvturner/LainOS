package main

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server ServerConfig `yaml:"server"`
	Routes []Route      `yaml:"routes"`
}

type ServerConfig struct {
	Listen      string        `yaml:"listen"`
	Timeout     time.Duration `yaml:"timeout"`
	MaxBodySize int64         `yaml:"max_body_size"`
}

type Route struct {
	Path    string `yaml:"path"`
	Command string `yaml:"command"`
	Method  string `yaml:"method"`
	Secret  string `yaml:"secret"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	if cfg.Server.Listen == "" {
		cfg.Server.Listen = "0.0.0.0:8080"
	}
	if cfg.Server.Timeout == 0 {
		cfg.Server.Timeout = 30 * time.Second
	}
	if cfg.Server.MaxBodySize == 0 {
		cfg.Server.MaxBodySize = 10 << 20
	}

	for i := range cfg.Routes {
		if cfg.Routes[i].Method == "" {
			cfg.Routes[i].Method = "POST"
		}
		if cfg.Routes[i].Secret != "" {
			cfg.Routes[i].Secret = os.ExpandEnv(cfg.Routes[i].Secret)
		}
	}

	return &cfg, nil
}
