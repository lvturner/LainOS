package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

const (
	maxCompactionThresholdPct = 90
	toolOutputTruncateLen     = 500
)

type StreamEvent struct {
	Type       string
	Content    string
	Question   *QuestionPayload
	ResponseCh chan string
}

type LLMClient struct {
	client              *openai.Client
	model               string
	systemMsg           string
	agentsPath          string
	temperature         float64
	maxTokens           int
	history             []openai.ChatCompletionMessage
	tools               []openai.Tool
	registry            *ToolRegistry
	contextLength       int
	compactionThreshold int
	compactionStrategy  string
	idleTimeout         time.Duration
	maxNudges           int
	nudgeMessage        string
	noStream            bool
	injectCh            chan string
	compactionCount     int
	thresholdBoost      int
	lastUsage           *openai.Usage
	loopDetector        *LoopDetector
}

func NewLLMClient(cfg *LainConfig, systemPrompt string, agentsPath string, tools []openai.Tool, registry *ToolRegistry) *LLMClient {
	config := openai.DefaultConfig(cfg.APIKey)
	config.BaseURL = cfg.APIURL
	return &LLMClient{
		client:              openai.NewClientWithConfig(config),
		model:               cfg.Model,
		systemMsg:           systemPrompt,
		agentsPath:          agentsPath,
		temperature:         cfg.Temperature,
		maxTokens:           cfg.MaxTokens,
		tools:               tools,
		registry:            registry,
		contextLength:       cfg.ContextLength,
		compactionThreshold: cfg.CompactionThreshold,
		compactionStrategy:  cfg.CompactionStrategy,
		idleTimeout:         cfg.IdleTimeout,
		maxNudges:           cfg.MaxNudges,
		nudgeMessage:        cfg.NudgeMessage,
		noStream:            cfg.NoStream,
		loopDetector:        NewLoopDetector(cfg.LoopThreshold),
	}
}

func (c *LLMClient) CompactNow(ctx context.Context) error {
	ch := make(chan StreamEvent, 10)
	c.compact(ctx, ch)
	close(ch)
	for evt := range ch {
		if evt.Type == "compaction_failed" {
			return fmt.Errorf("%s", evt.Content)
		}
	}
	return nil
}

func (c *LLMClient) CompactionInfo() (estimated, threshold int) {
	if c.lastUsage != nil && c.lastUsage.PromptTokens > 0 {
		estimated = c.lastUsage.PromptTokens
	} else {
		estimated = estimateTokens(c.history)
	}
	effectiveThreshold := c.compactionThreshold + c.thresholdBoost
	if effectiveThreshold > maxCompactionThresholdPct {
		effectiveThreshold = maxCompactionThresholdPct
	}
	threshold = int(float64(c.contextLength) * float64(effectiveThreshold) / 100.0)
	return
}

func (c *LLMClient) ContextPercent() int {
	var used int
	if c.lastUsage != nil && c.lastUsage.PromptTokens > 0 {
		used = c.lastUsage.PromptTokens
	} else {
		used = estimateTokens(c.history)
	}
	if c.contextLength <= 0 {
		return 0
	}
	pct := used * 100 / c.contextLength
	if pct > 100 {
		pct = 100
	}
	return pct
}

func (c *LLMClient) ContextLength() int {
	return c.contextLength
}

func (c *LLMClient) InjectMessage(msg string) {
	if c.injectCh != nil {
		select {
		case c.injectCh <- msg:
		default:
			slog.Warn("inject channel full, dropping message")
		}
	}
}

func (c *LLMClient) reloadAgents() {
	if c.agentsPath == "" {
		return
	}
	data, err := os.ReadFile(c.agentsPath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("failed to reload agents", "path", c.agentsPath, "error", err)
		}
		return
	}
	updated := string(data)
	if updated != c.systemMsg {
		c.systemMsg = updated
		slog.Info("agents.md reloaded", "path", c.agentsPath)
	}
}

func (c *LLMClient) Chat(ctx context.Context, userMsg string) <-chan StreamEvent {
	ch := make(chan StreamEvent, 100)
	c.injectCh = make(chan string, 20)
	c.compactionCount = 0
	c.thresholdBoost = 0
	c.loopDetector.Reset()
	c.reloadAgents()
	c.history = append(c.history, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: userMsg,
	})
	go func() {
		if c.needsCompaction() {
			c.compact(ctx, ch)
		}
		c.agenticLoop(ctx, ch)
	}()
	return ch
}

func (c *LLMClient) buildMessages() []openai.ChatCompletionMessage {
	c.validateAndRepairHistory()
	msgs := make([]openai.ChatCompletionMessage, 0, len(c.history)+1)
	if c.systemMsg != "" {
		msgs = append(msgs, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: c.systemMsg,
		})
	}
	msgs = append(msgs, c.history...)
	return msgs
}

func (c *LLMClient) validateAndRepairHistory() {
	var repaired []openai.ChatCompletionMessage
	for i := 0; i < len(c.history); i++ {
		msg := c.history[i]
		repaired = append(repaired, msg)
		if len(msg.ToolCalls) == 0 {
			continue
		}
		responded := make(map[string]bool)
		for j := i + 1; j < len(c.history); j++ {
			if c.history[j].Role == openai.ChatMessageRoleTool {
				responded[c.history[j].ToolCallID] = true
			} else {
				break
			}
		}
		for _, tc := range msg.ToolCalls {
			if !responded[tc.ID] {
				slog.Warn("repairing orphaned tool_call", "tool", tc.Function.Name, "id", tc.ID)
				repaired = append(repaired, openai.ChatCompletionMessage{
					Role:       openai.ChatMessageRoleTool,
					Content:    fmt.Sprintf("Error: tool '%s' was not executed (history repair).", tc.Function.Name),
					ToolCallID: tc.ID,
				})
			}
		}
	}
	if len(repaired) != len(c.history) {
		slog.Warn("history repaired", "original", len(c.history), "repaired", len(repaired))
		c.history = repaired
	}
}

func sortedIntKeys(m map[int]*openai.ToolCall) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func (c *LLMClient) History() []openai.ChatCompletionMessage {
	return c.history
}

func (c *LLMClient) SetHistory(msgs []openai.ChatCompletionMessage) {
	c.history = msgs
	c.lastUsage = nil
}

type askQuestionArgs struct {
	Question string      `json:"question"`
	Options  []OptionDef `json:"options"`
	Multiple bool        `json:"multiple"`
}

func (c *LLMClient) handleAskQuestion(toolCallID string, args json.RawMessage, ch chan<- StreamEvent) string {
	var a askQuestionArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return fmt.Sprintf("Error parsing ask_question args: %v", err)
	}

	respCh := make(chan string, 1)
	ch <- StreamEvent{
		Type: "ask_question",
		Question: &QuestionPayload{
			Question: a.Question,
			Options:  a.Options,
			Multiple: a.Multiple,
		},
		ResponseCh: respCh,
	}

	answer := <-respCh
	if answer == "" || answer == "__cancelled__" {
		return "User cancelled the question."
	}
	return answer
}
