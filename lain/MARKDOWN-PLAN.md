# Markdown Rendering Plan

## Goal

Parse and render markdown in LLM output using `charmbracelet/glamour` so that assistant messages and tool output display with proper styling (headers, bold/italic, code blocks with syntax highlighting, lists, etc.) instead of plain monochrome text.

## Dependency

```
github.com/charmbracelet/glamour
```

From the same Charmbracelet ecosystem as bubbletea/lipgloss. Renders markdown → ANSI-styled terminal output with syntax-highlighted code blocks via chroma.

Run:

```bash
cd lain && go get github.com/charmbracelet/glamour
```

## Design Decisions

- **Theme**: Glamour default dark theme (auto-detects terminal capabilities)
- **One-shot mode** (`--prompt`): No changes — stays plain text on stdout (useful for piping)
- **Tool output**: Same glamour rendering as assistant content (not faint/gray overlay). Tool calls look like code output in the same theme. Only the `▸ tool_name` prefix distinguishes tool calls visually.
- **User messages**: No changes — keep current box-drawing style with `userStyle`
- **Streaming**: Glamour renders on every token (same rate as current `refreshView()`). Handles partial/incomplete markdown gracefully (unclosed fences, etc.)

## Changes

### New file: `lain/markdown.go`

Single rendering function:

```go
func renderMarkdown(content string, width int) string
```

- Creates a `glamour.NewTerminalRenderer()` with `glamour.WithWordWrap(width)` and `glamour.WithAutoStyle()`
- Returns rendered ANSI string
- Returns empty string for empty/whitespace-only content
- The renderer can be created once and reused (store as a package-level var, reset width on resize)

### Modify: `lain/tui.go`

#### `formatMessage()` — assistant content blocks

Current (line ~264):

```go
case "content":
    wrapped := wordwrap.String(block.Content, width)
    b.WriteString(assistantStyle.Render(wrapped))
```

New:

```go
case "content":
    rendered := renderMarkdown(block.Content, width)
    b.WriteString(rendered)
```

Drop `assistantStyle.Render()` — glamour handles all styling. The monochrome pink (color 213) is replaced by glamour's dark theme colors (bold headers, syntax-highlighted code, etc.).

#### `formatMessage()` — tool output

Current (line ~267-270):

```go
case "tool_call":
    b.WriteString(toolStyle.Render(fmt.Sprintf("▸ %s", block.Name)))
    if block.Output != "" {
        b.WriteString("\n")
        b.WriteString(toolStyle.Render(fmt.Sprintf("  %s", truncate(block.Output, 200))))
    }
```

New:

```go
case "tool_call":
    b.WriteString(toolStyle.Render(fmt.Sprintf("▸ %s", block.Name)))
    if block.Output != "" {
        b.WriteString("\n")
        rendered := renderMarkdown(block.Output, width-2)
        b.WriteString(indent(rendered, "  "))
    }
```

- `toolStyle` only applies to the `▸ tool_name` prefix
- Tool output body renders through glamour (same dark theme as assistant content)
- Remove or bump the 200-char truncation — rendered markdown is more readable, so 500-1000 chars is reasonable
- Add a small `indent()` helper that prefixes each line with `"  "` for visual grouping under the tool name

#### Imports

- Add `"github.com/charmbracelet/glamour"` (or in `markdown.go`)
- Keep `"github.com/muesli/reflow/wordwrap"` — still used for user messages

### No changes: `lain/oneshot.go`

`--prompt` mode keeps plain text output on stdout.

## Verification

```bash
cd lain && go build ./...
```

Then rebuild container and test interactively:

```bash
podman-compose up -d --build
```

Send a message that triggers markdown output (e.g., "Write a Python hello world with a markdown explanation") and verify:
- Headers are bold/colored
- Code blocks have syntax highlighting and background color
- Inline code is distinct
- Lists are properly formatted
- Tool output (if any) renders the same way
- No visual artifacts during streaming

## Future Considerations

- If glamour rendering is too slow on very long responses, consider debouncing renders during streaming (accumulate tokens, render every N ms instead of every token)
- Could add a config toggle to disable markdown rendering for users who prefer plain text
