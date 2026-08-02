package response

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Angle-HR/server/pkg/apperror"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

func TestErrorLogsInternalFailures(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	req := httptest.NewRequest(http.MethodGet, "/api/v1/things", nil)
	req = req.WithContext(context.WithValue(req.Context(), chimiddleware.RequestIDKey, "req-42"))
	rec := httptest.NewRecorder()

	Error(rec, req, errors.New("db connection refused"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal log: %v\nraw=%s", err, buf.String())
	}
	if entry["msg"] != "internal error" {
		t.Fatalf("msg = %v raw=%s", entry["msg"], buf.String())
	}
	if entry["err"] != "db connection refused" {
		t.Fatalf("err = %v", entry["err"])
	}
	if entry["path"] != "/api/v1/things" {
		t.Fatalf("path = %v", entry["path"])
	}
	if entry["request_id"] != "req-42" {
		t.Fatalf("request_id = %v", entry["request_id"])
	}
}

func TestErrorDoesNotLogClientFailures(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	req := httptest.NewRequest(http.MethodGet, "/api/v1/things", nil)
	rec := httptest.NewRecorder()

	Error(rec, req, apperror.New(apperror.CodeValidationError, "bad input"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.TrimSpace(buf.String()) != "" {
		t.Fatalf("expected no log, got %q", buf.String())
	}
}
