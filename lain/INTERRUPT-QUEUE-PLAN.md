# Interrupt & Message Queue Plan for lain

## Overview

Two features for the `lain` TUI agent:

1. **ESC to interrupt** — Cancel the agent mid-stream, killing both the LLM connection and any running tool executions
2. **Message queue** — While the agent is working, type additional messages that get injected as separate user messages between agentic loop iterations

---

## Feature 1: ESC to Interrupt

### Current behavior

- `Ctrl+C` cancels streaming in `tui.go:159-169` but only stops consuming the stream channel
- The `agenticLoop` goroutine keeps running in the background until it naturally finishes or the channel blocks
- No actual cancellation of LLM connections or tool executions

### New behavior

- ESC cancels everything: LLM stream, running tool executions, and the agentic loop
- Ctrl+C retains its current dual behavior (cancel when streaming, quit when not)
- A new `"cancelled"` stream event cleanly terminates the loop

### Changes to `tui.go`

**Model struct** — add field:
```go
cancelFn context.CancelFunc
```

**`handleKey()`** — add ESC case:
```go
case "esc":
    if m.streaming {
        if m.cancelFn != nil {
            m.cancelFn()
        }
        m.streaming = false
        if m.current != nil {
            m.messages = append(m.messages, *m.current)
            m.current = nil
        }
        m.autoSave()
        m.refreshView()
        return m, nil
    }
```

**Starting a chat** — in the `"enter"` case, replace:
```go
m.streamCh = m.llmClient.Chat(context.Background(), input)
```
with:
```go
ctx, cancel := context.WithCancel(context.Background())
m.cancelFn = cancel
m.streamCh = m.llmClient.Chat(ctx, input)
```

**`handleStreamEvent()`** — add handler for `"cancelled"` event type:
```go
case "cancelled":
    if m.current != nil {
        m.finalizeLastContent()
        m.current.Blocks = append(m.current.Blocks, MessageBlock{
            Type:    "error",
            Content: "[interrupted]",
            Done:    true,
        })
        m.messages = append(m.messages, *m.current)
        m.current = nil
    }
    m.streaming = false
    m.cancelFn = nil
    m.autoSave()
    m.refreshView()
    return m, nil
```

**Existing `"done"` and `"error"` handlers** — add cleanup:
```go
m.cancelFn = nil
```

### Changes to `llm.go`

**`agenticLoop()`** — add `ctx.Done()` checks at six points:

1. Before the LLM API call (in the `select` with `connCh`):
```go
case <-ctx.Done():
    ch <- StreamEvent{Type: "cancelled"}
    return
```

2. During stream reading (in the `select` with `rc`):
```go
case <-ctx.Done():
    stream.Close()
    ch <- StreamEvent{Type: "cancelled"}
    return
```

3. Before executing each tool call:
```go
select {
case <-ctx.Done():
    ch <- StreamEvent{Type: "cancelled"}
    return
default:
}
```

4. During tool execution timeout wait (in the `select` with `toolCh`):
```go
case <-ctx.Done():
    ch <- StreamEvent{Type: "cancelled"}
    return
```

5. Before continuing the outer loop after tool calls (at line 314):
```go
select {
case <-ctx.Done():
    ch <- StreamEvent{Type: "cancelled"}
    return
default:
}
```

6. After stream reading but before checking finish reason:
```go
select {
case <-ctx.Done():
    ch <- StreamEvent{Type: "cancelled"}
    return
default:
}
```

---

## Feature 2: Message Queue

### Current behavior

- While streaming, all keyboard input is discarded (`tui.go:203-208`)
- User must wait for the agent to finish before providing additional context

### New behavior

- While streaming, the textarea remains active and accepts input
- Pressing Enter queues the typed message
- Queued messages render inline as user message blocks in the viewport with a subtle "queued" indicator
- The agentic loop drains queued messages at two checkpoints:
  1. After all tool calls in a batch complete (before the next LLM call)
  2. Before sending the final `"done"` event (if messages are queued, inject them and continue instead of finishing)
- Each queued message becomes a separate user message in the conversation history

### Changes to `llm.go`

**LLMClient struct** — add field:
```go
injectCh chan string
```

**New method:**
```go
func (c *LLMClient) InjectMessage(msg string) {
    if c.injectCh != nil {
        c.injectCh <- msg
    }
}
```

**`Chat()` method** — initialize the channel:
```go
c.injectCh = make(chan string, 20)
```

**`agenticLoop()`** — add drain at checkpoint 1 (after all tool calls complete, before `continue outer` at current line 314):

```go
// drain injected messages
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
```

Then replace the existing `continue outer` with this drain + continue pattern.

**`agenticLoop()`** — add drain at checkpoint 2 (before sending `"done"` at current line 317):

```go
// check for injected messages before finishing
select {
case msg := <-c.injectCh:
    c.history = append(c.history, openai.ChatCompletionMessage{
        Role:    openai.ChatMessageRoleUser,
        Content: msg,
    })
    ch <- StreamEvent{Type: "injected", Content: msg}
    // drain remaining
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
// no injected messages, finish
ch <- StreamEvent{Type: "done", Content: content.String()}
return
```

### Changes to `tui.go`

**Model struct** — add field:
```go
messageQueue []ChatMessage
```

**`handleKey()` default case** — allow textarea input during streaming:
```go
default:
    var cmd tea.Cmd
    m.textarea, cmd = m.textarea.Update(msg)
    return m, cmd
```

**`handleKey()` enter case** — change to support queuing while streaming:
```go
case "enter":
    if m.streaming {
        input := strings.TrimSpace(m.textarea.Value())
        if input == "" {
            return m, nil
        }
        m.textarea.Reset()
        queued := ChatMessage{
            Role:   "user",
            Blocks: []MessageBlock{{Type: "content", Content: input, Name: "queued"}},
        }
        m.messageQueue = append(m.messageQueue, queued)
        m.llmClient.InjectMessage(input)
        m.refreshView()
        return m, waitForStreamEvent(m.streamCh)
    }
    // ... existing non-streaming enter handling
```

**`handleStreamEvent()`** — add handler for `"injected"` event:
```go
case "injected":
    if len(m.messageQueue) > 0 {
        m.messageQueue = m.messageQueue[1:]
    }
    m.refreshView()
    return m, waitForStreamEvent(m.streamCh)
```

**`renderMessages()`** — append queued messages after the current message:
```go
if m.current != nil {
    b.WriteString(formatMessage(*m.current, m.width))
    b.WriteString("\n\n")
}
for _, qm := range m.messageQueue {
    b.WriteString(formatQueuedMessage(qm, m.width))
    b.WriteString("\n\n")
}
```

**New helper function:**
```go
func formatQueuedMessage(msg ChatMessage, width int) string {
    var content string
    for _, block := range msg.Blocks {
        if block.Type == "content" {
            content = block.Content
            break
        }
    }
    wrapped := wordwrap.String(content, width-4)
    top := userStyle.Faint(true).Render("┌─ queued " + strings.Repeat("─", width-12) + "┐")
    lines := strings.Split(wrapped, "\n")
    mid := ""
    for _, line := range lines {
        mid += userStyle.Faint(true).Render("│ " + line) + "\n"
    }
    bot := userStyle.Faint(true).Render("└" + strings.Repeat("─", width-2) + "┘")
    return top + "\n" + mid + bot
}
```

**Cleanup on cancel/done/error** — clear the message queue:
```go
m.messageQueue = nil
```

---

## File Change Summary

| File | Changes |
|---|---|
| `lain/tui.go` | Add `cancelFn`, `messageQueue` fields. ESC cancels via context. Enter during streaming queues messages. New handlers for `"cancelled"` and `"injected"` events. `formatQueuedMessage()` helper. Textarea active during streaming. Cleanup queue on done/cancel/error. |
| `lain/llm.go` | Add `injectCh`, `InjectMessage()` method. Context cancellation checks at 6 points in `agenticLoop()`. Message drain at tool-done and response-done boundaries. New `"cancelled"` and `"injected"` event types. |

No new files. No changes to `config.go`, `session.go`, `tools.go`, `question.go`, `oneshot.go`, `mcp.go`, or `main.go`.

---

## Stream Event Types (final)

| Type | When | Payload |
|---|---|---|
| `token` | LLM text delta | `Content` = text |
| `tool_start` | Tool call begins | `Content` = `name(args)` |
| `tool_output` | Tool call finishes | `Content` = `name: output` |
| `injected` | Queued message added to history | `Content` = message text |
| `cancelled` | ESC pressed / context cancelled | — |
| `nudge` | Agent stalled, nudged | `Content` = status |
| `compacting` | Compaction started | `Content` = status |
| `compacted` | Compaction done | `Content` = summary |
| `compaction_failed` | Compaction errored | `Content` = error |
| `done` | Agent finished, no queued messages | `Content` = full response |
| `error` | Unrecoverable error | `Content` = error |
| `ask_question` | Agent asks user | `Question` + `ResponseCh` |

---

## Interaction Flows

### ESC interrupt during streaming
```
Agent streaming text...
User presses ESC
  → context cancelled
  → agenticLoop sends "cancelled" event
  → TUI saves partial response with [interrupted] marker
  → User back in input mode
```

### ESC interrupt during tool execution
```
Agent running: run_command("sleep 60")
User presses ESC
  → context cancelled
  → agenticLoop breaks out of toolCh select
  → sends "cancelled" event
  → TUI saves partial response with [interrupted] marker
```

### Message queue during tool execution
```
Agent running: run_command("build project")
User types "use the staging config" and presses Enter
  → message queued, shown faintly in viewport with "queued" label
  → InjectMessage() sends to injectCh
Tool finishes → agenticLoop drains injectCh
  → "use the staging config" added to history as user message
  → "injected" event sent → TUI removes from queue display
  → Next LLM call includes the injected message as context
```

### Message queue during text response
```
Agent streaming response text...
User types "actually, check the logs first" and presses Enter
  → message queued, shown in viewport
Agent finishes response → agenticLoop checks injectCh before "done"
  → message found, added to history
  → "injected" event sent
  → continues outer loop instead of sending "done"
  → Next LLM call includes the injected message as context
```
