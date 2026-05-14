package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

var RunCommandTool = openai.Tool{
	Type: openai.ToolTypeFunction,
	Function: &openai.FunctionDefinition{
		Name:        "run_command",
		Description: "Execute a system command and return its output",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The shell command to execute",
				},
			},
			"required": []string{"command"},
		},
	},
}

var AskQuestionTool = openai.Tool{
	Type: openai.ToolTypeFunction,
	Function: &openai.FunctionDefinition{
		Name:        "ask_question",
		Description: "Ask the user a question and wait for their response. Use this when you need clarification, a decision, or user input before proceeding.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{
					"type":        "string",
					"description": "The question to ask the user",
				},
				"options": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"label": map[string]any{
								"type":        "string",
								"description": "Display text for the option",
							},
							"description": map[string]any{
								"type":        "string",
								"description": "Short explanation of the option",
							},
						},
						"required": []string{"label"},
					},
					"description": "Predefined options for the user to choose from",
				},
				"multiple": map[string]any{
					"type":        "boolean",
					"description": "Whether to allow multiple selections from the options list",
				},
			},
			"required": []string{"question"},
		},
	},
}

var ExtendTimeoutTool = openai.Tool{
	Type: openai.ToolTypeFunction,
	Function: &openai.FunctionDefinition{
		Name:        "extend_timeout",
		Description: "Extend the idle timeout before the system nudges you. Use this before running commands or operations that you expect to take a long time.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"duration_seconds": map[string]any{
					"type":        "integer",
					"description": "Additional seconds before nudge (max 3600)",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Why you need more time",
				},
			},
			"required": []string{"duration_seconds"},
		},
	},
}

var TodoTool = openai.Tool{
	Type: openai.ToolTypeFunction,
	Function: &openai.FunctionDefinition{
		Name:        "todo",
		Description: "Manage a task list. Use this to track progress on multi-step work. Tasks persist across the session. After context compaction, check your task list to recover context.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"description": "The action to perform",
					"enum":        []string{"add", "list", "complete", "uncomplete", "remove", "clear"},
				},
				"task": map[string]any{
					"type":        "string",
					"description": "Task description (required for 'add')",
				},
				"id": map[string]any{
					"type":        "integer",
					"description": "Task ID (required for 'complete', 'uncomplete', 'remove')",
				},
			},
			"required": []string{"action"},
		},
	},
}

var PluginQueryTool = openai.Tool{
	Type: openai.ToolTypeFunction,
	Function: &openai.FunctionDefinition{
		Name:        "plugin_query",
		Description: "Query plugin state and data. List all plugins, read plugin window content, or read plugin state.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"description": "The action to perform",
					"enum":        []string{"list", "window", "state"},
				},
				"plugin": map[string]any{
					"type":        "string",
					"description": "Plugin name (required for 'window' and 'state' actions)",
				},
				"window_id": map[string]any{
					"type":        "string",
					"description": "Window ID (required for 'window' action)",
				},
			},
			"required": []string{"action"},
		},
	},
}

var PluginSendTool = openai.Tool{
	Type: openai.ToolTypeFunction,
	Function: &openai.FunctionDefinition{
		Name:        "plugin_send",
		Description: "Send data to a plugin's on_data handler. The plugin must have registered a handler via lain.agent.on_data().",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"plugin": map[string]any{
					"type":        "string",
					"description": "Plugin name to send data to",
				},
				"data": map[string]any{
					"type":        "string",
					"description": "Arbitrary string data to send (typically JSON)",
				},
			},
			"required": []string{"plugin", "data"},
		},
	},
}

var RestrictedCommandTool = openai.Tool{
	Type: openai.ToolTypeFunction,
	Function: &openai.FunctionDefinition{
		Name:        "restricted_command",
		Description: "Execute a command in a read-only sandbox. The entire filesystem is mounted read-only by the kernel — writes will fail with EROFS. Only /tmp is writable (ephemeral, size-limited tmpfs). Network access is available for curl and HTTP requests. Use this for file inspection, web requests, and read-only operations.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The shell command to execute (read-only sandbox)",
				},
			},
			"required": []string{"command"},
		},
	},
}

var knownBuiltinTools = map[string]bool{
	"run_command":         true,
	"restricted_command":  true,
	"ask_question":        true,
	"extend_timeout":      true,
	"todo":                true,
	"plugin_query":        true,
	"plugin_send":         true,
}

type SandboxedExecutor struct {
	BwrapPath string
	TmpSize   string
}

func NewSandboxedExecutor(tmpSize string) *SandboxedExecutor {
	return &SandboxedExecutor{
		BwrapPath: "bwrap",
		TmpSize:   tmpSize,
	}
}

func (se *SandboxedExecutor) Exec(ctx context.Context, command string, stdinData []byte) (string, int, error) {
	args := []string{
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp:size=" + se.TmpSize,
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-cgroup",
		"--die-with-parent",
		"--new-session",
		"--", "sh", "-c", command,
	}
	cmd := exec.CommandContext(ctx, se.BwrapPath, args...)
	if len(stdinData) > 0 {
		cmd.Stdin = strings.NewReader(string(stdinData))
	}
	output, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return "", -1, fmt.Errorf("sandbox execution error: %w", err)
		}
	}
	return string(output), exitCode, nil
}

func (se *SandboxedExecutor) IsAvailable() bool {
	_, err := exec.LookPath(se.BwrapPath)
	if err != nil {
		home, _ := os.UserHomeDir()
		if home != "" {
			candidate := home + "/.nix-profile/bin/bwrap"
			if _, statErr := os.Stat(candidate); statErr == nil {
				se.BwrapPath = candidate
				return true
			}
		}
		return false
	}
	return true
}

type ToolRegistry struct {
	builtinTools []openai.Tool
	mcpTools     []openai.Tool
	mcpManager   *MCPManager
	todoStore    *TodoStore
	pluginAPI    *PluginAPI
	allowedTools []string
	sandbox      *SandboxedExecutor
}

func NewToolRegistry(mgr *MCPManager) *ToolRegistry {
	return &ToolRegistry{
		builtinTools: []openai.Tool{RunCommandTool, ExtendTimeoutTool},
		mcpTools:     mgr.Tools(),
		mcpManager:   mgr,
	}
}

func (r *ToolRegistry) AddBuiltinTool(tool openai.Tool) {
	r.builtinTools = append(r.builtinTools, tool)
}

func (r *ToolRegistry) SetTodoStore(store *TodoStore) {
	r.todoStore = store
}

func (r *ToolRegistry) SetPluginAPI(api *PluginAPI) {
	r.pluginAPI = api
}

func (r *ToolRegistry) GetTodoStore() *TodoStore {
	return r.todoStore
}

func (r *ToolRegistry) SetAllowedTools(allowed []string, sandbox *SandboxedExecutor) {
	r.allowedTools = allowed
	r.sandbox = sandbox
}

func (r *ToolRegistry) AllTools() []openai.Tool {
	tools := make([]openai.Tool, 0, len(r.builtinTools)+len(r.mcpTools))
	tools = append(tools, r.builtinTools...)
	tools = append(tools, r.mcpTools...)
	return tools
}

func (r *ToolRegistry) FilteredTools(builtinAllow []string, mcpAllow []string) []openai.Tool {
	var tools []openai.Tool
	builtinSet := make(map[string]bool, len(builtinAllow))
	for _, name := range builtinAllow {
		builtinSet[name] = true
	}
	for _, t := range r.builtinTools {
		if builtinSet[t.Function.Name] {
			tools = append(tools, t)
		}
	}
	if len(mcpAllow) > 0 {
		for _, t := range r.mcpTools {
			for _, server := range mcpAllow {
				if strings.HasPrefix(t.Function.Name, server+"__") {
					tools = append(tools, t)
					break
				}
			}
		}
	}
	return tools
}

func (r *ToolRegistry) ExecuteTool(name string, args json.RawMessage) (string, error) {
	if r.allowedTools != nil {
		allowed := false
		for _, a := range r.allowedTools {
			if name == a {
				allowed = true
				break
			}
			if strings.Contains(name, "__") && strings.HasPrefix(name, a+"__") {
				allowed = true
				break
			}
		}
		if !allowed {
			if knownBuiltinTools[name] {
				return fmt.Sprintf(
					"Tool '%s' is not available in ask mode. You are in a read-only research mode. Available tools: %s. Use 'restricted_command' for read-only file inspection and web requests.",
					name, strings.Join(r.allowedTools, ", "),
				), nil
			}
			serverName, _, parseErr := r.mcpManager.ParseToolName(name)
			if parseErr == nil && serverName != "" {
				return fmt.Sprintf(
					"Tool '%s' is not available in ask mode (server '%s' is not allowlisted). Available tools: %s.",
					name, serverName, strings.Join(r.allowedTools, ", "),
				), nil
			}
			return fmt.Sprintf(
				"Unknown tool '%s'. Available tools: %s",
				name, strings.Join(r.allowedTools, ", "),
			), nil
		}
	}
	if name == "restricted_command" {
		return r.executeRestrictedCommand(args)
	}
	if name == "run_command" {
		return r.executeRunCommand(args)
	}
	if name == "todo" {
		return r.executeTodo(args)
	}
	if name == "plugin_query" {
		return r.executePluginQuery(args)
	}
	if name == "plugin_send" {
		return r.executePluginSend(args)
	}
	serverName, toolName, err := r.mcpManager.ParseToolName(name)
	if err != nil {
		return "", err
	}
	return r.mcpManager.CallTool(serverName, toolName, args)
}

func isAllowedSudoCommand(cmd string) bool {
	trimmed := strings.TrimSpace(cmd)
	if !strings.HasPrefix(trimmed, "sudo ") {
		return false
	}
	after := strings.TrimSpace(strings.TrimPrefix(trimmed, "sudo"))
	allowed := []string{"start ", "stop ", "restart ", "status "}
	for _, sub := range allowed {
		if strings.HasPrefix(after, "systemctl "+sub) {
			return true
		}
	}
	return false
}

type commandArgs struct {
	Command string `json:"command"`
}

func (r *ToolRegistry) executeRunCommand(args json.RawMessage) (string, error) {
	var a commandArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("parsing args: %w", err)
	}
	if strings.HasPrefix(strings.TrimSpace(a.Command), "sudo") && !isAllowedSudoCommand(a.Command) {
		return "Error: sudo is not available in this environment. If you need to install software, use `nix profile install nixpkgs#<package>`.", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", a.Command)
	output, _ := cmd.CombinedOutput()
	return string(output), nil
}

func (r *ToolRegistry) executeRestrictedCommand(args json.RawMessage) (string, error) {
	var a commandArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("parsing args: %w", err)
	}
	if r.sandbox == nil {
		return "", fmt.Errorf("sandbox executor not configured")
	}
	if !r.sandbox.IsAvailable() {
		return "", fmt.Errorf("bubblewrap (bwrap) is not installed. Install via: nix profile install nixpkgs#bubblewrap")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, exitCode, err := r.sandbox.Exec(ctx, a.Command, nil)
	if err != nil {
		return "", err
	}
	if exitCode != 0 {
		return output, fmt.Errorf("exit code %d", exitCode)
	}
	return output, nil
}

type todoArgs struct {
	Action string `json:"action"`
	Task   string `json:"task"`
	ID     int    `json:"id"`
}

func (r *ToolRegistry) executeTodo(args json.RawMessage) (string, error) {
	var a todoArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("parsing args: %w", err)
	}
	if r.todoStore == nil {
		return "", fmt.Errorf("todo store not initialized")
	}

	switch a.Action {
	case "add":
		if a.Task == "" {
			return "", fmt.Errorf("task is required for add action")
		}
		item := r.todoStore.Add(a.Task)
		return fmt.Sprintf("Added task %d: %s", item.ID, item.Task), nil
	case "list":
		return r.todoStore.FormatList(), nil
	case "complete":
		item, err := r.todoStore.Complete(a.ID)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Completed task %d: %s", item.ID, item.Task), nil
	case "uncomplete":
		item, err := r.todoStore.Uncomplete(a.ID)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Uncompleted task %d: %s", item.ID, item.Task), nil
	case "remove":
		err := r.todoStore.Remove(a.ID)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Removed task %d", a.ID), nil
	case "clear":
		cleared := r.todoStore.Clear()
		return fmt.Sprintf("Cleared %d completed tasks", cleared), nil
	default:
		return "", fmt.Errorf("unknown action: %s", a.Action)
	}
}

type pluginQueryArgs struct {
	Action   string `json:"action"`
	Plugin   string `json:"plugin"`
	WindowID string `json:"window_id"`
}

func (r *ToolRegistry) executePluginQuery(args json.RawMessage) (string, error) {
	var a pluginQueryArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("parsing args: %w", err)
	}
	if r.pluginAPI == nil {
		return "", fmt.Errorf("plugin system not initialized")
	}

	switch a.Action {
	case "list":
		var items []string
		for name, exec := range r.pluginAPI.executors {
			if exec == nil {
				continue
			}
			windows := []string{}
			for id, pw := range r.pluginAPI.pluginWindows {
				if pw.pluginName == name {
					windows = append(windows, fmt.Sprintf("%s (%s)", id, pw.title))
				}
			}
			if len(windows) > 0 {
				items = append(items, fmt.Sprintf("%s: windows=[%s]", name, strings.Join(windows, ", ")))
			} else {
				items = append(items, name)
			}
		}
		if len(items) == 0 {
			return "No plugins loaded", nil
		}
		return strings.Join(items, "\n"), nil
	case "window":
		if a.WindowID == "" {
			return "", fmt.Errorf("window_id is required for window action")
		}
		pw, ok := r.pluginAPI.pluginWindows[a.WindowID]
		if !ok {
			return "", fmt.Errorf("window %q not found", a.WindowID)
		}
		pw.renderMu.RLock()
		content := pw.cachedRender
		pw.renderMu.RUnlock()
		if content == "" {
			return "[empty]", nil
		}
		return content, nil
	case "state":
		if a.Plugin == "" {
			return "", fmt.Errorf("plugin is required for state action")
		}
		state, ok := r.pluginAPI.pluginState[a.Plugin]
		if !ok {
			return "", fmt.Errorf("plugin %q not found", a.Plugin)
		}
		data, err := json.Marshal(state)
		if err != nil {
			return "", fmt.Errorf("marshaling state: %w", err)
		}
		return string(data), nil
	default:
		return "", fmt.Errorf("unknown action: %s", a.Action)
	}
}

type pluginSendArgs struct {
	Plugin string `json:"plugin"`
	Data   string `json:"data"`
}

func (r *ToolRegistry) executePluginSend(args json.RawMessage) (string, error) {
	var a pluginSendArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("parsing args: %w", err)
	}
	if r.pluginAPI == nil {
		return "", fmt.Errorf("plugin system not initialized")
	}
	callbacks, ok := r.pluginAPI.dataCallbacks[a.Plugin]
	if !ok || len(callbacks) == 0 {
		return "", fmt.Errorf("plugin %q has no data handler registered", a.Plugin)
	}
	r.pluginAPI.FireDataCallbacks(a.Plugin, a.Data)
	return fmt.Sprintf("Data sent to plugin %q (%d handler(s))", a.Plugin, len(callbacks)), nil
}
