package main

import (
	"fmt"
	"os"
	"path/filepath"
)

type Profile struct {
	Config     *LainConfig
	Servers    *ServersConfig
	Agents     string
	AgentsPath string
	BasePath   string
}

func ProfileDir(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "lain", "profiles", name), nil
}

func LoadProfile(name string) (*Profile, error) {
	dir, err := ProfileDir(name)
	if err != nil {
		return nil, err
	}

	cfgPath := filepath.Join(dir, "config.yaml")
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("loading profile config: %w", err)
	}

	serversPath := filepath.Join(dir, "servers.json")
	servers, err := LoadServers(serversPath)
	if err != nil {
		return nil, fmt.Errorf("loading servers config: %w", err)
	}

	agentsPath := filepath.Join(dir, "agents.md")
	agents, err := LoadAgents(agentsPath)
	if err != nil {
		return nil, fmt.Errorf("loading agents: %w", err)
	}

	return &Profile{
		Config:     cfg,
		Servers:    servers,
		Agents:     agents,
		AgentsPath: agentsPath,
		BasePath:   dir,
	}, nil
}
