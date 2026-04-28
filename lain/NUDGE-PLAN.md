# Idle Watchdog & Nudge System — Implementation Plan

## Overview

Add a configurable idle watchdog to `lain`'s agentic loop that detects stalls at both the API connection and stream-reading phases. When the watchdog fires, it kills the stalled stream, injects a nudge message into history, and retries. The LLM gets an `extend_timeout` tool to proactively request more time before long operations. All behavior is documented in the lain profile's `agents.md`.

## Files to Change

| File | Changes |
|---|---|
| `lain/config.go` | Add `idle_timeout`, `max_nudges`, `nudge_message` to `LainConfig` |
| `lain/llm.go` | Add idle watchdog to `agenticLoop`, nudge injection, `extend_timeout` handling |
| `lain/tools.go` | Add `ExtendTimeoutTool` definition |
| `lain/tui.go` | Handle `"nudge"` stream events in the TUI |
| `lain/oneshot.go` | Handle `"nudge"` events in one-shot mode |
| `config/lain/profiles/default/agents.md` | Document timeout behavior and `extend_timeout` usage |
| `docs/lain.md` | Document new config fields, tool, and behavior |

## 1. Config (`config.go`)

New fields on `LainConfig`:

```go
IdleTimeout  time.Duration `yaml:"idle_timeout"`   // default: 120s
MaxNudges    int           `yaml:"max_nudges"`      // default: 3
NudgeMessage string        `yaml:"nudge_message"`   // default: see below
```

Defaults in `LoadConfig()`:

- `idle_timeout`: `120s`
- `max_nudges`: `3`
- `nudge_message`: `"You haven't produced output in a while. If you're about to run something slow, use extend_timeout. Otherwise, continue your task."`

## 2. Agentic Loop Watchdog (`llm.go`)

### Core Mechanism

Both `CreateChatCompletionStream()` and `stream.Recv()` are wrapped in goroutines with `select` + `time.Timer`. Every activity resets the timer.

### New Fields on `LLMClient`

```go
idleTimeout  time.Duration
maxNudges    int
nudgeMessage string
```

### Phase 1 — Connection Watchdog

- `CreateChatCompletionStream()` runs in a goroutine
- `select` waits for either the result or the idle timer
- On timeout: increment `nudgeCount`, inject nudge message into `c.history`, `continue` the outer loop (retry)

```go
type connResult struct {
    stream *openai.ChatCompletionStream
    err    error
}

ch := make(chan connResult, 1)
go func() {
    s, e := c.client.CreateChatCompletionStream(ctx, req)
    ch <- connResult{s, e}
}()

timer := time.NewTimer(idleTimeout)
defer timer.Stop()

select {
case r := <-ch:
    if r.err != nil {
        // error
        return
    }
    stream = r.stream
case <-timer.C:
    // nudge
}
```

### Phase 2 — Stream Reading Watchdog

- `stream.Recv()` runs in a goroutine
- `select` waits for either the result or the idle timer
- On timeout: close stream, increment `nudgeCount`, inject nudge, `continue` outer loop
- Timer resets on every token received

```go
type recvResult struct {
    resp openai.ChatCompletionStreamResponse
    err  error
}

rc := make(chan recvResult, 1)
go func() {
    resp, err := stream.Recv()
    rc <- recvResult{resp, err}
}()

timer.Reset(idleTimeout)

select {
case r := <-rc:
    // process response, reset timer
case <-timer.C:
    // close stream, nudge, retry
}
```

### Nudge Injection

- Appends a user message to `c.history` with `c.nudgeMessage`
- Sends a `"nudge"` `StreamEvent` to the TUI with content like `"Agent stalled, nudging... (attempt 1/3)"`
- If `nudgeCount > maxNudges`, emits an error and returns

### Nudge Count Reset

Reset `nudgeCount` to 0 after any successful iteration that produces output (tokens received or tools executed without stall).

### `extend_timeout` Handling

- Special-cased in the tool execution block (like `ask_question` already is)
- Parses `duration_seconds` from args
- Stores the extension in a local `timeoutExtension time.Duration` variable
- Applied to the next iteration's idle timeout: `effectiveTimeout := c.idleTimeout + timeoutExtension`
- `timeoutExtension` is reset to 0 at the start of each iteration
- Returns confirmation message to the LLM
- Max extension capped at 3600s (1 hour)

```go
if tc.Function.Name == "extend_timeout" {
    var args struct {
        DurationSeconds int    `json:"duration_seconds"`
        Reason          string `json:"reason"`
    }
    json.Unmarshal(json.RawMessage(tc.Function.Arguments), &args)
    if args.DurationSeconds > 0 && args.DurationSeconds <= 3600 {
        timeoutExtension = time.Duration(args.DurationSeconds) * time.Second
    }
    output = fmt.Sprintf("Idle timeout extended by %d seconds.", args.DurationSeconds)
}
```

### New `StreamEvent` Type

`"nudge"` — content describes what happened, e.g. `"Agent stalled, nudging... (attempt 1/3)"`

## 3. Tool Definition (`tools.go`)

New `ExtendTimeoutTool`:

```go
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
```

Added to `builtinTools` in `NewToolRegistry()` alongside `RunCommandTool`.

Added in TUI mode alongside `AskQuestionTool`:

```go
registry.AddBuiltinTool(ExtendTimeoutTool)
registry.AddBuiltinTool(AskQuestionTool)
```

## 4. TUI Changes (`tui.go`)

Handle `"nudge"` events in `handleStreamEvent()`:

```go
case "nudge":
    if m.current != nil {
        m.finalizeLastContent()
        m.current.Blocks = append(m.current.Blocks, MessageBlock{
            Type:    "compaction", // reuse compaction styling
            Content: event.Content,
        })
    }
```

Renders as a styled line (using existing compaction style): `⟳ Agent stalled, nudging... (attempt 1/3)`

## 5. One-Shot Changes (`oneshot.go`)

Handle `"nudge"` events:

```go
case "nudge":
    fmt.Fprintf(os.Stderr, "⟳ %s\n", event.Content)
```

## 6. agents.md Updates

Add to `config/lain/profiles/default/agents.md`:

```markdown
## Timeout Awareness

The system monitors your activity. If you don't produce output for 120 seconds, you will be nudged automatically.

If you're about to run a long command or need more time to think, call `extend_timeout` first:

    extend_timeout(duration_seconds=300, reason="building project")

This gives you an additional 300 seconds before the next nudge.

The default `run_command` timeout is 30 seconds. For longer tasks, consider breaking them into steps or using `extend_timeout` before running the command.
```

## 7. Docs Updates (`docs/lain.md`)

### New config.yaml fields in the table

| Field | Description | Default |
|---|---|---|
| `idle_timeout` | Seconds before nudging a stalled agent | `120s` |
| `max_nudges` | Maximum automatic nudges before giving up | `3` |
| `nudge_message` | Message injected when nudging | *(see defaults)* |

### New tool documentation

Add `extend_timeout` section alongside `run_command`:

```
## Built-in Tool: extend_timeout

Asks the system to wait longer before nudging:

{
  "name": "extend_timeout",
  "parameters": {
    "duration_seconds": 300,
    "reason": "Running a long build"
  }
}

Behavior:
- Extends idle timeout by the requested seconds (max 3600)
- Should be called before long operations
- Returns confirmation message
```

### Idle Watchdog section

New section explaining the watchdog behavior, nudging, and how to configure it.

## Sequence Diagram

```
agenticLoop starts
  │
  ├─ CreateChatCompletionStream (watchdog: idleTimeout + extension)
  │   │
  │   ├─ Success → continue to stream reading
  │   └─ Timeout → inject nudge, increment count, retry
  │
  ├─ stream.Recv() loop (watchdog: idleTimeout, resets on each token)
  │   │
  │   ├─ Token received → reset timer, continue
  │   ├─ EOF → process tool calls or return
  │   └─ Timeout → close stream, inject nudge, increment count, retry
  │
  ├─ Tool call: extend_timeout(300, "building")
  │   └─ Sets timeoutExtension = 300s for next iteration
  │
  ├─ Tool call: run_command("make build")
  │   └─ Executes with 30s timeout, returns output
  │
  └─ finishReason=stop → emit "done", return
```

## Implementation Order

1. `config.go` — Add new fields and defaults
2. `tools.go` — Add `ExtendTimeoutTool`
3. `llm.go` — Rewrite `agenticLoop` with watchdog + extend_timeout handling
4. `tui.go` — Handle nudge events
5. `oneshot.go` — Handle nudge events
6. `config/lain/profiles/default/agents.md` — Add timeout docs
7. `docs/lain.md` — Update documentation
