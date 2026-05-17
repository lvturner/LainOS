package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandlerMethodMismatch(t *testing.T) {
	route := Route{Path: "/test", Command: "/bin/echo", Method: "POST"}
	handler := newCommandHandler(route, 5*time.Second, 1<<20)

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandlerBodyTooLarge(t *testing.T) {
	route := Route{Path: "/test", Command: "/bin/echo", Method: "POST"}
	handler := newCommandHandler(route, 5*time.Second, 10)

	body := make([]byte, 20)
	req := httptest.NewRequest("POST", "/test", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestHandlerSuccess(t *testing.T) {
	route := Route{Path: "/test", Command: "/bin/cat", Method: "POST"}
	handler := newCommandHandler(route, 5*time.Second, 1<<20)

	body := []byte("hello world")
	req := httptest.NewRequest("POST", "/test", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "hello world" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "hello world")
	}
}

func TestHandlerEnvVars(t *testing.T) {
	route := Route{Path: "/test/path?foo=bar", Command: "/usr/bin/env", Method: "GET"}
	handler := newCommandHandler(route, 5*time.Second, 1<<20)

	req := httptest.NewRequest("GET", "/test/path?foo=bar", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	output := rec.Body.String()
	for _, expected := range []string{"WH_METHOD=GET", "WH_PATH=/test/path", "WH_QUERY=foo=bar"} {
		if !strings.Contains(output, expected) {
			t.Errorf("output missing %q\ngot: %s", expected, output)
		}
	}
}

func TestHandlerCommandFailure(t *testing.T) {
	route := Route{Path: "/test", Command: "/bin/false", Method: "POST"}
	handler := newCommandHandler(route, 5*time.Second, 1<<20)

	req := httptest.NewRequest("POST", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestHandlerTimeout(t *testing.T) {
	route := Route{Path: "/test", Command: "/bin/sh", Method: "POST"}
	handler := newCommandHandler(route, 200*time.Millisecond, 1<<20)

	req := httptest.NewRequest("POST", "/test", bytes.NewReader([]byte("exec sleep 10")))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusGatewayTimeout)
	}
}

func TestHandlerMissingSignature(t *testing.T) {
	route := Route{Path: "/test", Command: "/bin/echo", Method: "POST", Secret: "secret"}
	handler := newCommandHandler(route, 5*time.Second, 1<<20)

	req := httptest.NewRequest("POST", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestHandlerInvalidSignature(t *testing.T) {
	route := Route{Path: "/test", Command: "/bin/echo", Method: "POST", Secret: "secret"}
	handler := newCommandHandler(route, 5*time.Second, 1<<20)

	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set("X-Hub-Signature-256", "sha256=invalid")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestHandlerValidSignature(t *testing.T) {
	secret := "mysecret"
	route := Route{Path: "/test", Command: "/bin/echo", Method: "POST", Secret: secret}
	handler := newCommandHandler(route, 5*time.Second, 1<<20)

	body := []byte(`{"test": true}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest("POST", "/test", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sig)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestValidateHMAC(t *testing.T) {
	body := []byte(`{"test": true}`)
	secret := "mysecret"

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !validateHMAC(body, secret, sig) {
		t.Error("validateHMAC() = false, want true")
	}

	if validateHMAC(body, "wrongsecret", sig) {
		t.Error("validateHMAC() with wrong secret = true, want false")
	}

	if validateHMAC(body, secret, "sha256=wrong") {
		t.Error("validateHMAC() with wrong sig = true, want false")
	}

	if validateHMAC(body, secret, "invalid-prefix") {
		t.Error("validateHMAC() with invalid prefix = true, want false")
	}
}
