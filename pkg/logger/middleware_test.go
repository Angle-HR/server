package logger

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestLoggerLogsBodiesAtDebug(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	h := RequestLogger(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("handler read body: %v", err)
		}
		if string(body) != `{"email":"a@b.com","password":"secret"}` {
			t.Fatalf("handler got body %q", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"access_token":"abc","ok":true}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login?x=1", strings.NewReader(`{"email":"a@b.com","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal log: %v\nraw=%s", err, buf.String())
	}
	if entry["msg"] != "http request" {
		t.Fatalf("msg = %v", entry["msg"])
	}
	if entry["request_body"] != `{"email":"a@b.com","password":"[redacted]"}` {
		t.Fatalf("request_body = %v", entry["request_body"])
	}
	if entry["response_body"] != `{"access_token":"[redacted]","ok":true}` {
		t.Fatalf("response_body = %v", entry["response_body"])
	}
	if int(entry["status"].(float64)) != http.StatusOK {
		t.Fatalf("status = %v", entry["status"])
	}
}

func TestRequestLoggerOmitsBodiesAboveDebug(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	h := RequestLogger(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal log: %v", err)
	}
	if _, ok := entry["request_body"]; ok {
		t.Fatalf("request_body should be omitted at info: %v", entry)
	}
	if _, ok := entry["response_body"]; ok {
		t.Fatalf("response_body should be omitted at info: %v", entry)
	}
}
