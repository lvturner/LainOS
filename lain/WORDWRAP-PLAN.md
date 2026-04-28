# lain TUI fixes: word wrap + chronological tool call display

## Problems

1. **No word wrapping** — Long lines overflow the viewport instead of wrapping at the terminal width.
2. **Tool calls always rendered after all text** — `ChatMessage` accumulates all tokens into one `Content` string and all tool calls into a flat `ToolCalls` slice, then `formatMessage` renders content first, then all tool calls. Real stream order is: text → tool → more text → tool → etc.

## Plan

### 1. Add `reflow/wordwrap` dependency

```bash
cd lain && go get github.com/muesli/reflow/wordwrap
```

Standard charmbracelet ecosystem package — handles ANSI escape codes properly.

### 2. Restructure `ChatMessage` in `tui.go` — block-based model

**Remove:**

```go
type ToolCallDisplay struct {
	Name      string
	Arguments string
	Output    string
}

type ChatMessage struct {
	Role      string
	Content   string
	ToolCalls []ToolCallDisplay
}
```

**Replace with:**

```go
type MessageBlock struct {
	Type    string // "content" or "tool_call"
	Content string // text content for "content", display string for "tool_call"
	Name    string // tool name (tool_call only)
	Output  string // tool output (tool_call only)
}

type ChatMessage struct {
	Role   string
	Blocks []MessageBlock
}
```

### 3. Update `handleStreamEvent` (`tui.go:164-201`)

- **`token`**: If last block exists and is `"content"`, append text to it. Otherwise append a new `"content"` block.
- **`tool_start`**: Append a new `"tool_call"` block with `Content` set to the display string (name + args) and `Name` set to tool name.
- **`tool_output`**: Update the last `"tool_call"` block's `Output`.
- **`done`** / **`error`**: Same logic — move `current` into `messages`, set `current = nil`.

### 4. Update `formatMessage` (`tui.go:223-246`)

- Accept a `width int` parameter: `func formatMessage(msg ChatMessage, width int) string`
- For user messages:
  - Word-wrap content with `wordwrap.String(content, width-2)` (subtract border chars)
  - Make box border lines match `width` instead of hardcoded `─────` length
- For assistant messages:
  - Iterate `Blocks` in order:
    - `"content"` blocks → word-wrap with `wordwrap.String(block.Content, width)`, apply `assistantStyle`
    - `"tool_call"` blocks → render `▸ Name` and output as before, apply `toolStyle`
  - Join blocks with newlines

### 5. Update all callers

- `renderMessages()` (`tui.go:211-221`): pass `m.width` to `formatMessage(msg, m.width)`
- User message creation in `Update()` (line 126-129): change to `Blocks: []MessageBlock{{Type: "content", Content: input}}`
- Ctrl+C handler (line 107): no change needed — `*m.current` carries blocks through as-is
- `m.current` initialization (line 131): `&ChatMessage{Role: "assistant"}` — no change needed (nil Blocks)

### 6. Cleanup

- Remove `ToolCallDisplay` type
- Verify `go build ./...` compiles cleanly

## Files changed

| File | Changes |
|------|---------|
| `lain/go.mod` / `lain/go.sum` | Add `github.com/muesli/reflow/wordwrap` |
| `lain/tui.go` | Block-based `ChatMessage`, updated `handleStreamEvent`, `formatMessage` with wrapping, `renderMessages` passes width |
