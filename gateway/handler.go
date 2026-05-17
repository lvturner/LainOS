package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

func newCommandHandler(route Route, timeout time.Duration, maxBodySize int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if route.Method != "" && r.Method != route.Method {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		defer r.Body.Close()

		body, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize+1))
		if err != nil {
			slog.Error("failed to read body", "error", err)
			http.Error(w, "failed to read body", http.StatusInternalServerError)
			return
		}
		if int64(len(body)) > maxBodySize {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}

		if route.Secret != "" {
			sigHeader := r.Header.Get("X-Hub-Signature-256")
			if sigHeader == "" {
				slog.Warn("missing signature", "path", route.Path)
				http.Error(w, "missing signature", http.StatusUnauthorized)
				return
			}
			if !validateHMAC(body, route.Secret, sigHeader) {
				slog.Warn("invalid signature", "path", route.Path)
				http.Error(w, "invalid signature", http.StatusForbidden)
				return
			}
		}

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
			envVal := strings.Join(values, ",")
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", envKey, envVal))
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

func validateHMAC(body []byte, secret, sigHeader string) bool {
	if !strings.HasPrefix(sigHeader, "sha256=") {
		return false
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(sigHeader, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)
	return hmac.Equal(sig, expected)
}
