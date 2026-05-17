package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

func (c *LLMClient) agenticLoop(ctx context.Context, ch chan<- StreamEvent) {
	defer close(ch)
	var nudgeCount int
	var timeoutExtension time.Duration

outer:
	for {
		effectiveTimeout := c.idleTimeout + timeoutExtension
		timeoutExtension = 0

		req := openai.ChatCompletionRequest{
			Model:         c.model,
			Messages:      c.buildMessages(),
			Tools:         c.tools,
			Temperature:   float32(c.temperature),
			MaxTokens:     c.maxTokens,
			Stream:        !c.noStream,
			StreamOptions: &openai.StreamOptions{IncludeUsage: true},
		}

		var content strings.Builder
		toolCalls := make(map[int]*openai.ToolCall)

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
					for i, tc := range choice.Message.ToolCalls {
						tcCopy := tc
						toolCalls[i] = &tcCopy
					}
				}
				if r.resp.Usage.TotalTokens > 0 {
					c.lastUsage = &r.resp.Usage
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
						if r.resp.Usage != nil && r.resp.Usage.TotalTokens > 0 {
							c.lastUsage = r.resp.Usage
						}
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
		if len(toolCalls) > 0 {
			tcList := make([]openai.ToolCall, len(indices))
			for i, idx := range indices {
				tcList[i] = *toolCalls[idx]
			}

			if c.loopDetector.Enabled() {
				iterDetected, iterCount := c.loopDetector.CheckIteration(tcList)
				if iterDetected {
					slog.Warn("iteration-level loop detected", "count", iterCount, "tool_count", len(tcList))
					ch <- StreamEvent{
						Type:    "loop_detected",
						Content: fmt.Sprintf("Iteration loop: same tool call set repeated %d times", iterCount),
					}
					for _, idx := range indices {
						tc := toolCalls[idx]
						c.history = append(c.history, openai.ChatCompletionMessage{
							Role:       openai.ChatMessageRoleTool,
							Content:    fmt.Sprintf("LOOP DETECTED: You have made the same set of tool calls %d times in a row. The tool was not executed. Please try a completely different approach.", iterCount),
							ToolCallID: tc.ID,
						})
					}
					c.history = append(c.history, openai.ChatCompletionMessage{
						Role:    openai.ChatMessageRoleUser,
						Content: fmt.Sprintf("[System] You are stuck in a loop. You have repeated the same set of tool calls %d consecutive times. Stop repeating yourself and try a completely different approach to solve this problem.", iterCount),
					})
					continue outer
				}
			}

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

				if c.loopDetector.Enabled() {
					callDetected, callCount := c.loopDetector.CheckToolCall(tc.Function.Name, tc.Function.Arguments)
					if callDetected {
						slog.Warn("per-call loop detected", "tool", tc.Function.Name, "count", callCount)
						ch <- StreamEvent{
							Type:    "loop_detected",
							Content: fmt.Sprintf("Call loop: %s repeated %d times", tc.Function.Name, callCount),
						}
						c.history = append(c.history, openai.ChatCompletionMessage{
							Role:       openai.ChatMessageRoleTool,
							Content:    fmt.Sprintf("LOOP DETECTED: You have called %s with the same arguments %d times in a row. This tool call was not executed. Please try a different approach.", tc.Function.Name, callCount),
							ToolCallID: tc.ID,
						})
						continue
					}
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
						err    error
					}
					toolCh := make(chan toolExecResult, 1)
					go func() {
						out, err := c.registry.ExecuteTool(tc.Function.Name, args)
						toolCh <- toolExecResult{out, err}
					}()

					toolTimer := time.NewTimer(effectiveTimeout)
					select {
					case r := <-toolCh:
						toolTimer.Stop()
						output = r.output
						if r.err != nil {
							output = fmt.Sprintf("Error: %s", r.err.Error())
						}
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

				if output == "" && !timedOut {
					output = "Tool completed successfully."
				}
				ch <- StreamEvent{
					Type:    "tool_output",
					Content: fmt.Sprintf("%s: %s", tc.Function.Name, truncate(output, toolOutputTruncateLen)),
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
