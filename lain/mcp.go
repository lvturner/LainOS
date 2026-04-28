package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	openai "github.com/sashabaranov/go-openai"
)

type MCPServerTool struct {
	ServerName string
	ToolName   string
	Tool       mcp.Tool
}

type MCPManager struct {
	clients map[string]*client.Client
	tools   map[string]MCPServerTool
}

func NewMCPManager(servers *ServersConfig) *MCPManager {
	mgr := &MCPManager{
		clients: make(map[string]*client.Client),
		tools:   make(map[string]MCPServerTool),
	}
	ctx := context.Background()
	for name, cfg := range servers.MCPServers {
		env := os.Environ()
		for k, v := range cfg.Env {
			env = append(env, k+"="+v)
		}
		c, err := client.NewStdioMCPClient(cfg.Command, env, cfg.Args...)
		if err != nil {
			fmt.Fprintf(os.Stderr, "MCP: failed to create client %q: %v\n", name, err)
			continue
		}
		initReq := mcp.InitializeRequest{}
		initReq.Params.ClientInfo = mcp.Implementation{
			Name:    "lain",
			Version: "1.0.0",
		}
		if _, err := c.Initialize(ctx, initReq); err != nil {
			fmt.Fprintf(os.Stderr, "MCP: failed to initialize %q: %v\n", name, err)
			c.Close()
			continue
		}
		mgr.clients[name] = c
		toolsResult, err := c.ListTools(ctx, mcp.ListToolsRequest{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "MCP: failed to list tools for %q: %v\n", name, err)
			continue
		}
		for _, tool := range toolsResult.Tools {
			fullName := name + "__" + tool.Name
			mgr.tools[fullName] = MCPServerTool{
				ServerName: name,
				ToolName:   tool.Name,
				Tool:       tool,
			}
		}
	}
	return mgr
}

func (m *MCPManager) Tools() []openai.Tool {
	names := make([]string, 0, len(m.tools))
	for name := range m.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	var tools []openai.Tool
	for _, fullName := range names {
		st := m.tools[fullName]
		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        fullName,
				Description: st.Tool.Description,
				Parameters:  st.Tool.InputSchema,
			},
		})
	}
	return tools
}

func (m *MCPManager) ParseToolName(name string) (string, string, error) {
	parts := strings.SplitN(name, "__", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid MCP tool name: %s", name)
	}
	return parts[0], parts[1], nil
}

func (m *MCPManager) CallTool(serverName, toolName string, args json.RawMessage) (string, error) {
	c, ok := m.clients[serverName]
	if !ok {
		return "", fmt.Errorf("unknown MCP server: %s", serverName)
	}
	var arguments map[string]any
	if err := json.Unmarshal(args, &arguments); err != nil {
		return "", fmt.Errorf("parsing tool arguments: %w", err)
	}
	result, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      toolName,
			Arguments: arguments,
		},
	})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, content := range result.Content {
		b.WriteString(mcp.GetTextFromContent(content))
	}
	return b.String(), nil
}

func (m *MCPManager) Close() {
	for name, c := range m.clients {
		if err := c.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "MCP: error closing %q: %v\n", name, err)
		}
	}
}
