package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"
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
	contextWindow       int
	compactionThreshold int
	compactionStrategy  string
	idleTimeout         time.Duration
	maxNudges           int
	nudgeMessage        string
	noStream            bool
	injectCh            chan string
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
		contextWindow:       cfg.ContextWindow,
		compactionThreshold: cfg.CompactionThreshold,
		compactionStrategy:  cfg.CompactionStrategy,
		idleTimeout:         cfg.IdleTimeout,
		maxNudges:           cfg.MaxNudges,
		nudgeMessage:        cfg.NudgeMessage,
		noStream:            cfg.NoStream,
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
	estimated = estimateTokens(c.history)
	threshold = int(float64(c.contextWindow) * float64(c.compactionThreshold) / 100.0)
	return
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

func (c *LLMClient) agenticLoop(ctx context.Context, ch chan<- StreamEvent) {
	defer close(ch)
	var nudgeCount int
	var timeoutExtension time.Duration

outer:
	for {
		effectiveTimeout := c.idleTimeout + timeoutExtension
		timeoutExtension = 0

		req := openai.ChatCompletionRequest{
			Model:       c.model,
			Messages:    c.buildMessages(),
			Tools:       c.tools,
			Temperature: float32(c.temperature),
			MaxTokens:   c.maxTokens,
			Stream:      !c.noStream,
		}

		var content strings.Builder
		toolCalls := make(map[int]*openai.ToolCall)
		var finishReason openai.FinishReason

		if c.noStream {
			type nonStreamResult struct {
				resp openai.ChatCompletionResponse
				err  error
			}
			nsc := make(chan nonStreamResult, 1)
			go func() {
				r, e := c.client.CreateChatCompletion(ctx, req)
				nsc <- nonStreamResult{r, e}
			}()

			timer := time.NewTimer(effectiveTimeout)
			select {
			case r := <-nsc:
				timer.Stop()
				if r.err != nil {
					ch <- StreamEvent{Type: "error", Content: r.err.Error()}
					return
				}
				if len(r.resp.Choices) > 0 {
					choice := r.resp.Choices[0]
					if choice.Message.Content != "" {
						content.WriteString(choice.Message.Content)
						ch <- StreamEvent{Type: "token", Content: content.String()}
					}
					finishReason = choice.FinishReason
					for i, tc := range choice.Message.ToolCalls {
						tcCopy := tc
						toolCalls[i] = &tcCopy
					}
				}
			case <-timer.C:
				nudgeCount++
				if nudgeCount > c.maxNudges {
					ch <- StreamEvent{Type: "error", Content: "Agent stalled: max nudges exceeded"}
					return
				}
				ch <- StreamEvent{Type: "nudge", Content: fmt.Sprintf("Agent stalled, nudging... (attempt %d/%d)", nudgeCount, c.maxNudges)}
				c.history = append(c.history, openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleUser,
					Content: c.nudgeMessage,
				})
				continue outer
			case <-ctx.Done():
				ch <- StreamEvent{Type: "cancelled"}
				return
			}
		} else {
			type connResult struct {
				stream *openai.ChatCompletionStream
				err    error
			}
			connCh := make(chan connResult, 1)
			go func() {
				s, e := c.client.CreateChatCompletionStream(ctx, req)
				connCh <- connResult{s, e}
			}()

			connTimer := time.NewTimer(effectiveTimeout)
			var stream *openai.ChatCompletionStream
			select {
			case r := <-connCh:
				connTimer.Stop()
				if r.err != nil {
					ch <- StreamEvent{Type: "error", Content: r.err.Error()}
					return
				}
				stream = r.stream
			case <-connTimer.C:
				nudgeCount++
				if nudgeCount > c.maxNudges {
					ch <- StreamEvent{Type: "error", Content: "Agent stalled: max nudges exceeded during connection"}
					return
				}
				ch <- StreamEvent{Type: "nudge", Content: fmt.Sprintf("Agent stalled, nudging... (attempt %d/%d)", nudgeCount, c.maxNudges)}
				c.history = append(c.history, openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleUser,
					Content: c.nudgeMessage,
				})
				continue outer
			case <-ctx.Done():
				ch <- StreamEvent{Type: "cancelled"}
				return
			}

			streamTimer := time.NewTimer(effectiveTimeout)

		recvLoop:
			for {
				type recvResult struct {
					resp openai.ChatCompletionStreamResponse
					err  error
				}
				rc := make(chan recvResult, 1)
				go func() {
					resp, err := stream.Recv()
					rc <- recvResult{resp, err}
				}()

				select {
				case r := <-rc:
					if r.err == io.EOF {
						break recvLoop
					}
					if r.err != nil {
						ch <- StreamEvent{Type: "error", Content: r.err.Error()}
						stream.Close()
						streamTimer.Stop()
						return
					}
					if len(r.resp.Choices) == 0 {
						continue
					}
					choice := r.resp.Choices[0]
					delta := choice.Delta
					gotContent := false
					if delta.Content != "" {
						slog.Debug("stream token", "len", len(delta.Content), "content", delta.Content)
						content.WriteString(delta.Content)
						ch <- StreamEvent{Type: "token", Content: delta.Content}
						gotContent = true
					}
					for _, tc := range delta.ToolCalls {
						if tc.Index == nil {
							continue
						}
						idx := *tc.Index
						if existing, ok := toolCalls[idx]; ok {
							existing.Function.Arguments += tc.Function.Arguments
						} else {
							toolCalls[idx] = &openai.ToolCall{
								ID:   tc.ID,
								Type: tc.Type,
								Function: openai.FunctionCall{
									Name:      tc.Function.Name,
									Arguments: tc.Function.Arguments,
								},
							}
						}
						gotContent = true
					}
					if choice.FinishReason != "" {
						finishReason = choice.FinishReason
						gotContent = true
					}
					if gotContent {
						streamTimer.Reset(effectiveTimeout)
					}
				case <-streamTimer.C:
					stream.Close()
					nudgeCount++
					if nudgeCount > c.maxNudges {
						ch <- StreamEvent{Type: "error", Content: "Agent stalled: max nudges exceeded during stream"}
						return
					}
					ch <- StreamEvent{Type: "nudge", Content: fmt.Sprintf("Agent stalled, nudging... (attempt %d/%d)", nudgeCount, c.maxNudges)}
					c.history = append(c.history, openai.ChatCompletionMessage{
						Role:    openai.ChatMessageRoleUser,
						Content: c.nudgeMessage,
					})
					continue outer
				case <-ctx.Done():
					stream.Close()
					ch <- StreamEvent{Type: "cancelled"}
					return
				}
			}
			streamTimer.Stop()
			stream.Close()
		}

		select {
		case <-ctx.Done():
			ch <- StreamEvent{Type: "cancelled"}
			return
		default:
		}

		assistantMsg := openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleAssistant,
			Content: content.String(),
		}
		var indices []int
		if len(toolCalls) > 0 {
			indices = sortedIntKeys(toolCalls)
			var tcList []openai.ToolCall
			for _, idx := range indices {
				tcList = append(tcList, *toolCalls[idx])
			}
			assistantMsg.ToolCalls = tcList
		}
		c.history = append(c.history, assistantMsg)
		if finishReason == openai.FinishReasonToolCalls && len(toolCalls) > 0 {
			for _, idx := range indices {
				select {
				case <-ctx.Done():
					ch <- StreamEvent{Type: "cancelled"}
					return
				default:
				}

				tc := toolCalls[idx]
				ch <- StreamEvent{
					Type:    "tool_start",
					Content: fmt.Sprintf("%s(%s)", tc.Function.Name, tc.Function.Arguments),
				}
				args := json.RawMessage(tc.Function.Arguments)
				if len(args) == 0 {
					args = json.RawMessage("{}")
				}

				var output string
				timedOut := false

				if tc.Function.Name == "ask_question" {
					output = c.handleAskQuestion(tc.ID, args, ch)
				} else if tc.Function.Name == "extend_timeout" {
					var extArgs struct {
						DurationSeconds int    `json:"duration_seconds"`
						Reason          string `json:"reason"`
					}
					json.Unmarshal(args, &extArgs)
					if extArgs.DurationSeconds > 0 && extArgs.DurationSeconds <= 3600 {
						timeoutExtension = time.Duration(extArgs.DurationSeconds) * time.Second
					}
					output = fmt.Sprintf("Idle timeout extended by %d seconds.", extArgs.DurationSeconds)
				} else {
					type toolExecResult struct {
						output string
					}
					toolCh := make(chan toolExecResult, 1)
					go func() {
						out, _ := c.registry.ExecuteTool(tc.Function.Name, args)
						toolCh <- toolExecResult{out}
					}()

					toolTimer := time.NewTimer(effectiveTimeout)
					select {
					case r := <-toolCh:
						toolTimer.Stop()
						output = r.output
					case <-toolTimer.C:
						timedOut = true
					case <-ctx.Done():
						toolTimer.Stop()
						ch <- StreamEvent{Type: "cancelled"}
						return
					}
				}

				if timedOut {
					c.history = append(c.history, openai.ChatCompletionMessage{
						Role:       openai.ChatMessageRoleTool,
						Content:    "Tool execution timed out.",
						ToolCallID: tc.ID,
					})
					for _, remainingIdx := range indices {
						if remainingIdx <= idx {
							continue
						}
						c.history = append(c.history, openai.ChatCompletionMessage{
							Role:       openai.ChatMessageRoleTool,
							Content:    "Tool execution skipped due to timeout.",
							ToolCallID: toolCalls[remainingIdx].ID,
						})
					}
					nudgeCount++
					if nudgeCount > c.maxNudges {
						ch <- StreamEvent{Type: "error", Content: "Agent stalled: max nudges exceeded during tool execution"}
						return
					}
					ch <- StreamEvent{Type: "nudge", Content: fmt.Sprintf("Agent stalled, nudging... (attempt %d/%d)", nudgeCount, c.maxNudges)}
					c.history = append(c.history, openai.ChatCompletionMessage{
						Role:    openai.ChatMessageRoleUser,
						Content: c.nudgeMessage,
					})
					continue outer
				}

				if output == "" {
					output = "Tool completed successfully."
				}
				ch <- StreamEvent{
					Type:    "tool_output",
					Content: fmt.Sprintf("%s: %s", tc.Function.Name, truncate(output, 500)),
				}
				c.history = append(c.history, openai.ChatCompletionMessage{
					Role:       openai.ChatMessageRoleTool,
					Content:    output,
					ToolCallID: tc.ID,
				})
			}
			nudgeCount = 0

			for {
				select {
				case msg := <-c.injectCh:
					c.history = append(c.history, openai.ChatCompletionMessage{
						Role:    openai.ChatMessageRoleUser,
						Content: msg,
					})
					ch <- StreamEvent{Type: "injected", Content: msg}
				default:
					goto doneDrain
				}
			}
		doneDrain:

			if c.needsCompaction() {
				c.compact(ctx, ch)
			}

			continue outer
		}
		nudgeCount = 0

		select {
		case msg := <-c.injectCh:
			c.history = append(c.history, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleUser,
				Content: msg,
			})
			ch <- StreamEvent{Type: "injected", Content: msg}
			for {
				select {
				case msg := <-c.injectCh:
					c.history = append(c.history, openai.ChatCompletionMessage{
						Role:    openai.ChatMessageRoleUser,
						Content: msg,
					})
					ch <- StreamEvent{Type: "injected", Content: msg}
				default:
					goto continueAfterInject
				}
			}
		continueAfterInject:
			continue outer
		default:
		}

		ch <- StreamEvent{Type: "done", Content: content.String()}
		return
	}
}

func (c *LLMClient) buildMessages() []openai.ChatCompletionMessage {
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
