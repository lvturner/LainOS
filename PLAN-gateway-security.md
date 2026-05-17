# Plan: Gateway Security & Correctness

## Problem

The webhook gateway has a data race, no request body limits, and no command validation — making it vulnerable to OOM attacks and misconfiguration.

## Issues

### 1. Data race in `atomicHandler` (HIGH)

**File:** `gateway/server.go:17-19, 62-64`

`s.handler.handler = mux` (line 53) is a non-atomic write to a pointer that's read concurrently by `ServeHTTP` (line 63). The name `atomicHandler` is misleading — nothing is atomic.

**Fix:** Use `sync/atomic.Value`:

```go
type atomicHandler struct {
    handler atomic.Value // stores http.Handler
}

func (h *atomicHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    h.handler.Load().(http.Handler).ServeHTTP(w, r)
}
```

In `NewServer`:
```go
h := &atomicHandler{}
h.handler.Store(mux)
```

In `watchUpdates`:
```go
s.handler.handler.Store(mux)
```

Remove the `currentMux` field from `Server` (unused).

### 2. No request body size limit (HIGH)

**File:** `gateway/handler.go:23`

`io.ReadAll(r.Body)` reads the entire body into memory with no limit. A malicious request with a multi-GB body OOMs the process.

**Fix:** Wrap with `io.LimitReader`:

```go
const maxBodySize = 10 << 20 // 10 MB

body, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize))
if err != nil {
    slog.Error("failed to read body", "error", err)
    http.Error(w, "failed to read body", http.StatusInternalServerError)
    return
}
if int64(len(body)) >= maxBodySize {
    http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
    return
}
```

Make `maxBodySize` configurable via the YAML config:

```yaml
server:
  max_body_size: 10485760  # 10 MB
```

### 3. Race in watcher timer (MEDIUM)

**File:** `gateway/watcher.go`

`Close()` calls `fw.Close()` but doesn't stop a pending `AfterFunc` timer. The timer callback may fire after `Close()`, accessing `w.path` and `w.updateCh` on a closed watcher.

**Fix:**

```go
func (w *Watcher) Close() {
    w.mu.Lock()
    if w.timer != nil {
        w.timer.Stop()
    }
    w.mu.Unlock()
    w.fw.Close()
}
```

### 4. Multi-value header truncation (LOW)

**File:** `gateway/handler.go:50`

Only `values[0]` is used for multi-value headers. Headers like `Accept` or custom headers with multiple values are silently truncated.

**Fix:** Join all values:

```go
envVal := strings.Join(values, ",")
cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", envKey, envVal))
```

### 5. Route command validation (LOW)

**File:** `gateway/handler.go:34`

No validation that `route.Command` is an absolute path, exists, or is executable. A typo in `gateway.yaml` produces a generic "command not found" error at runtime with no config-time feedback.

**Fix:** In `buildMux()` or `LoadConfig()`, log a warning for routes whose command doesn't exist:

```go
if _, err := os.Stat(route.Command); err != nil {
    slog.Warn("route command not found", "path", route.Path, "command", route.Command)
}
```

Don't fail — the file might be created later. But a warning helps debugging.

### 6. `r.Body.Close()` placement (LOW)

**File:** `gateway/handler.go:29`

`r.Body.Close()` is called explicitly after `ReadAll`, but `ReadAll` already reads to EOF and the server handles closing. The explicit close is harmless but unnecessary and deviates from standard patterns. More importantly, if `ReadAll` returns an error, the body is never closed.

**Fix:** Use `defer r.Body.Close()` immediately after the method check, before reading.

## Implementation Order

1. Fix `atomicHandler` data race
2. Add body size limit
3. Fix watcher timer cleanup in `Close()`
4. Fix multi-value headers
5. Add command existence warnings
6. Fix body close defer

## Testing

```bash
cd gateway

# Build
go build ./...

# Race detector
go test -race ./...

# Manual test: send large body
dd if=/dev/zero bs=1M count=100 | curl -X POST -d @- http://localhost:8080/webhook/test
# Should get 413, not OOM
```
