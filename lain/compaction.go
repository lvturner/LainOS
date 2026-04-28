package main

import (
	"context"
	"fmt"
	"log/slog"

	openai "github.com/sashabaranov/go-openai"
)

const compactionSystemPrompt = `You are a conversation compaction assistant. Your sole task is to produce a concise but complete summary of the conversation below.

The summary MUST preserve:
- All factual information exchanged (names, paths, values, configurations)
- All decisions made and reasoning behind them
- All actions taken (commands run, files written/modified, tool invocations and their results)
- The current task or goal the user is working toward
- Any code snippets, command outputs, or technical details still relevant
- The state of any ongoing work (what is done, what remains)

Write the summary so that another AI assistant can seamlessly continue the conversation without missing any context. Do not add commentary, greetings, or explanations — output only the summary itself.`

const charsPerToken = 4

func estimateTokens(messages []openai.ChatCompletionMessage) int {
	total := 0
	for _, msg := range messages {
		total += len(msg.Content) / charsPerToken
		total += 4
		for _, tc := range msg.ToolCalls {
			total += len(tc.Function.Name) / charsPerToken
			total += len(tc.Function.Arguments) / charsPerToken
			total += 4
		}
		if msg.Role == openai.ChatMessageRoleTool {
			total += len(msg.Content) / charsPerToken
		}
	}
	return total
}

func (c *LLMClient) needsCompaction() bool {
	estimated := estimateTokens(c.history)
	threshold := int(float64(c.contextWindow) * float64(c.compactionThreshold) / 100.0)
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
	ch <- StreamEvent{Type: "compacted", Content: "context compacted"}
}
