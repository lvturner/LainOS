package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

func newCommandHandler(route Route, timeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if route.Method != "" && r.Method != route.Method {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			slog.Error("failed to read body", "error", err)
			http.Error(w, "failed to read body", http.StatusInternalServerError)
			return
		}
		r.Body.Close()

		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, route.Command)
		cmd.Stdin = bytes.NewReader(body)

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		cmd.Env = os.Environ()
		cmd.Env = append(cmd.Env,
			"WH_METHOD="+r.Method,
			"WH_PATH="+r.URL.Path,
			"WH_QUERY="+r.URL.RawQuery,
		)

		for key, values := range r.Header {
			envKey := "WH_HEADER_" + strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", envKey, values[0]))
		}

		if err := cmd.Run(); err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				slog.Error("command timed out", "path", route.Path, "command", route.Command)
				http.Error(w, "command timed out", http.StatusGatewayTimeout)
				return
			}
			slog.Error("command failed", "command", route.Command, "error", err, "stderr", stderr.String())
			http.Error(w, stderr.String(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write(stdout.Bytes())
	})
}
