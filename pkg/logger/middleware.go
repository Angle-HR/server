package logger

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

const maxBodyLogBytes = 8192

// RequestLogger logs method, path, status, bytes, duration, and request ID for every HTTP request.
// When the logger is enabled at debug, it also logs request and response bodies (truncated, redacted).
func RequestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			logBodies := log.Enabled(r.Context(), slog.LevelDebug)

			var reqBody string
			if logBodies {
				reqBody = captureRequestBody(r)
			}

			ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			var respBuf limitedBuffer
			if logBodies {
				respBuf.max = maxBodyLogBytes
				ww.Tee(&respBuf)
			}

			defer func() {
				attrs := []any{
					"method", r.Method,
					"path", r.URL.Path,
					"query", r.URL.RawQuery,
					"status", ww.Status(),
					"bytes", ww.BytesWritten(),
					"duration_ms", time.Since(start).Milliseconds(),
					"request_id", chimiddleware.GetReqID(r.Context()),
					"remote", r.RemoteAddr,
					"user_agent", r.UserAgent(),
				}
				if logBodies {
					attrs = append(attrs,
						"request_body", reqBody,
						"response_body", formatLoggedBody(respBuf.Bytes(), respBuf.truncated),
					)
				}
				log.Info("http request", attrs...)
			}()

			next.ServeHTTP(ww, r)
		})
	}
}

func captureRequestBody(r *http.Request) string {
	if r.Body == nil || r.Body == http.NoBody {
		return ""
	}
	if !isLoggableContentType(r.Header.Get("Content-Type")) {
		return "[omitted]"
	}

	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(nil))
		return ""
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	truncated := len(body) > maxBodyLogBytes
	if truncated {
		body = body[:maxBodyLogBytes]
	}
	return formatLoggedBody(body, truncated)
}

func formatLoggedBody(body []byte, truncated bool) string {
	if len(body) == 0 {
		return ""
	}
	if !utf8.Valid(body) {
		return "[binary]"
	}

	s := redactSensitiveJSON(string(body))
	if truncated {
		return s + "...[truncated]"
	}
	return s
}

func isLoggableContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	if ct == "" {
		return true
	}
	switch {
	case strings.HasPrefix(ct, "application/json"),
		strings.HasPrefix(ct, "text/"),
		ct == "application/x-www-form-urlencoded",
		ct == "application/problem+json":
		return true
	default:
		return false
	}
}

func redactSensitiveJSON(s string) string {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	redactValue(v)
	b, err := json.Marshal(v)
	if err != nil {
		return s
	}
	return string(b)
}

func redactValue(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if isSensitiveKey(k) {
				t[k] = "[redacted]"
				continue
			}
			redactValue(child)
		}
	case []any:
		for _, child := range t {
			redactValue(child)
		}
	}
}

func isSensitiveKey(key string) bool {
	switch strings.ToLower(key) {
	case "password", "passwd", "secret", "token", "access_token", "refresh_token",
		"authorization", "api_key", "apikey", "current_password", "new_password":
		return true
	default:
		return false
	}
}

// limitedBuffer captures up to max bytes for response body logging.
type limitedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.max <= 0 {
		return len(p), nil
	}
	remain := l.max - l.buf.Len()
	if remain <= 0 {
		l.truncated = true
		return len(p), nil
	}
	if len(p) > remain {
		_, _ = l.buf.Write(p[:remain])
		l.truncated = true
		return len(p), nil
	}
	_, err := l.buf.Write(p)
	return len(p), err
}

func (l *limitedBuffer) Bytes() []byte {
	return l.buf.Bytes()
}
