package main

import (
	"context"
	"fmt"
	"log/slog"

	openai "github.com/sashabaranov/go-openai"
)

const (
	compactionSystemPrompt = `You are a conversation compaction assistant. Produce an extremely concise summary of the conversation below. Target no more than 15% of the original conversation length.

Compression rules:
- Tool outputs (file reads, grep results, command outputs): keep only the key findings or results. Drop raw output verbatim.
- Preserve ALL decisions, reasoning, and action items — these are more important than raw data.
- Preserve all factual information: names, paths, values, configurations, code snippets still relevant.
- Preserve the current task/goal and what remains to be done.
- Merge repetitive exchanges into single statements.
- Use abbreviated notation where unambiguous (e.g., "edited foo.go:42-58 to add X" instead of full file contents).

Output only the summary. No commentary, greetings, or explanations.`

	charsPerToken         = 4
	tokenSafetyMultiplier = 1.2
	compactionLoopThreshold = 2
	compactionThresholdBoost = 25
)

func estimateTokens(messages []openai.ChatCompletionMessage) int {
	total := 0
	for _, msg := range messages {
		total += len(msg.Content) / charsPerToken
		total += 8
		total += len(msg.Role) / charsPerToken
		if msg.Name != "" {
			total += len(msg.Name) / charsPerToken
			total += 2
		}
		for _, tc := range msg.ToolCalls {
			total += len(tc.ID) / charsPerToken
			total += len(tc.Type) / charsPerToken
			total += len(tc.Function.Name) / charsPerToken
			total += len(tc.Function.Arguments) / charsPerToken
			total += 6
		}
		if msg.ToolCallID != "" {
			total += len(msg.ToolCallID) / charsPerToken
			total += 2
		}
	}
	return int(float64(total) * tokenSafetyMultiplier)
}

func (c *LLMClient) needsCompaction() bool {
	var estimated int
	if c.lastUsage != nil && c.lastUsage.PromptTokens > 0 {
		estimated = c.lastUsage.PromptTokens
	} else {
		estimated = estimateTokens(c.history)
	}
	effectiveThreshold := c.compactionThreshold + c.thresholdBoost
	if effectiveThreshold > maxCompactionThresholdPct {
		effectiveThreshold = maxCompactionThresholdPct
	}
	threshold := int(float64(c.contextLength) * float64(effectiveThreshold) / 100.0)
	return estimated >= threshold
}

func (c *LLMClient) compact(ctx context.Context, ch chan<- StreamEvent) {
	ch <- StreamEvent{Type: "compacting", Content: "compacting context..."}

	var toSummarize []openai.ChatCompletionMessage
	var lastUserMsg *openai.ChatCompletionMessage

	if c.compactionStrategy == "keep_last" {
		for i := len(c.history) - 1; i >= 0; i-- {
			if c.history[i].Role == openai.ChatMessageRoleUser {
				msg := c.history[i]
				lastUserMsg = &msg
				toSummarize = c.history[:i]
				break
			}
		}
		if lastUserMsg == nil {
			toSummarize = c.history
		}
	} else {
		toSummarize = c.history
	}

	summaryMsgs := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: compactionSystemPrompt},
	}
	summaryMsgs = append(summaryMsgs, toSummarize...)
	summaryMsgs = append(summaryMsgs, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: "Summarize the conversation above now.",
	})

	req := openai.ChatCompletionRequest{
		Model:       c.model,
		Messages:    summaryMsgs,
		Temperature: 0.3,
		MaxTokens:   4096,
	}

	resp, err := c.client.CreateChatCompletion(ctx, req)
	if err != nil {
		slog.Error("compaction failed", "error", err)
		ch <- StreamEvent{Type: "compaction_failed", Content: fmt.Sprintf("compaction failed: %v", err)}
		return
	}

	if len(resp.Choices) == 0 {
		slog.Error("compaction returned no choices")
		ch <- StreamEvent{Type: "compaction_failed", Content: "compaction returned no response"}
		return
	}

	summary := resp.Choices[0].Message.Content
	slog.Info("context compacted", "summary_tokens", len(summary)/charsPerToken, "previous_messages", len(c.history))

	newHistory := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: fmt.Sprintf("[Previous conversation summary]\n%s", summary)},
	}

	if lastUserMsg != nil {
		newHistory = append(newHistory, *lastUserMsg)
	}

	if c.registry != nil {
		store := c.registry.GetTodoStore()
		if store != nil {
			todoList := store.FormatList()
			if todoList != "No tasks." {
				newHistory = append(newHistory, openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleSystem,
					Content: fmt.Sprintf("[Current Todo List — review this to recover context after compaction]\n%s", todoList),
				})
			}
		}
	}

	c.history = newHistory
	c.lastUsage = nil
	c.compactionCount++
	if c.compactionCount >= compactionLoopThreshold && c.thresholdBoost == 0 {
		c.thresholdBoost = compactionThresholdBoost
		effectiveThreshold := c.compactionThreshold + c.thresholdBoost
		if effectiveThreshold > maxCompactionThresholdPct {
			effectiveThreshold = maxCompactionThresholdPct
		}
		slog.Warn("compaction loop detected, raising threshold", "original", c.compactionThreshold, "boosted", effectiveThreshold)
		ch <- StreamEvent{Type: "compaction_loop", Content: fmt.Sprintf("compaction loop detected — threshold raised to %d%% to prevent repeated compaction", effectiveThreshold)}
	}
	ch <- StreamEvent{Type: "compacted", Content: "context compacted"}
}
