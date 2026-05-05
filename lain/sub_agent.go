package main

import (
	"context"
	"fmt"
	"log/slog"
)

type SubAgentOpts struct {
	ID           string
	Title        string
	ProfileName  string
	SystemPrompt string
	InitialMsg   string
}

type SubAgent struct {
	id          string
	profileName string
	client      *LLMClient
	window      *agentWindow
	registry    *ToolRegistry
	mcpMgr      *MCPManager
	cancelFn    context.CancelFunc
	streamCh    <-chan StreamEvent
}

type SubAgentManager struct {
	agents    map[string]*SubAgent
	fixAgentID string
	model     *model
}

func NewSubAgentManager(m *model) *SubAgentManager {
	return &SubAgentManager{
		agents: make(map[string]*SubAgent),
		model:  m,
	}
}

func (sam *SubAgentManager) Spawn(opts SubAgentOpts) (*SubAgent, error) {
	profile, err := LoadProfile(opts.ProfileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile %q: %w", opts.ProfileName, err)
	}

	mcpMgr := NewMCPManager(profile.Servers)
	registry := NewToolRegistry(mcpMgr)
	registry.AddBuiltinTool(AskQuestionTool)
	registry.AddBuiltinTool(TodoTool)

	client := NewLLMClient(profile.Config, opts.SystemPrompt, profile.AgentsPath, registry.AllTools(), registry)

	win := newAgentWindow(opts.ID, opts.Title)
	win.streaming = true

	agentW := 60
	agentH := 20
	x := (sam.model.width - agentW) / 2
	y := (sam.model.wmHeight() - agentH) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	sam.model.wm.AddFloating(win, x, y, agentW, agentH)

	ctx, cancel := context.WithCancel(context.Background())
	streamCh := client.Chat(ctx, opts.InitialMsg)

	agent := &SubAgent{
		id:          opts.ID,
		profileName: opts.ProfileName,
		client:      client,
		window:      win,
		registry:    registry,
		mcpMgr:      mcpMgr,
		cancelFn:    cancel,
		streamCh:    streamCh,
	}

	sam.agents[opts.ID] = agent
	return agent, nil
}

func (sam *SubAgentManager) EnqueueMessage(agentID, message string) {
	agent, ok := sam.agents[agentID]
	if !ok {
		return
	}
	agent.client.InjectMessage(message)
	if agent.window != nil {
		agent.window.AppendEvent(StreamEvent{Type: "injected", Content: message})
	}
}

func (sam *SubAgentManager) Stop(agentID string) {
	agent, ok := sam.agents[agentID]
	if !ok {
		return
	}
	if agent.cancelFn != nil {
		agent.cancelFn()
	}
	if agent.mcpMgr != nil {
		agent.mcpMgr.Close()
	}
	if sam.model.wm.HasFloating(agentID) {
		sam.model.wm.RemoveFloating(agentID)
	} else if sam.model.wm.Has(agentID) {
		sam.model.wm.Remove(agentID)
		sam.model.wm.SetSize(sam.model.width, sam.model.wmHeight())
	}
	delete(sam.agents, agentID)
	if sam.fixAgentID == agentID {
		sam.fixAgentID = ""
	}
}

func (sam *SubAgentManager) StopAll() {
	for id := range sam.agents {
		sam.Stop(id)
	}
}

func (sam *SubAgentManager) HasActive(agentID string) bool {
	_, ok := sam.agents[agentID]
	return ok
}

func (sam *SubAgentManager) SetFixAgentID(id string) {
	sam.fixAgentID = id
}

func (sam *SubAgentManager) GetFixAgentID() string {
	return sam.fixAgentID
}

func (sam *SubAgentManager) LogStatus() {
	slog.Info("sub-agent manager", "active_agents", len(sam.agents), "fix_agent", sam.fixAgentID)
}
