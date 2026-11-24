package logger

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

// maxBodyLogBytes caps how much of a request or response body is captured for the debug log.
const maxBodyLogBytes = 4096

// healthPath is polled every few seconds by the load balancer and the container health check; successful probes are
// not logged. It must match handler.HealthPath.
const healthPath = "/healthz"

// RequestLogger logs method, path, status, bytes, duration, and request ID for every HTTP request except successful
// health probes.
// When the logger is enabled at debug, it also logs request and response bodies (truncated, redacted).
func RequestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			// Bodies of the auth and onboarding paths are never logged, at any level.
			logBodies := log.Enabled(r.Context(), slog.LevelDebug) && !SensitivePath(r.URL.Path)

			var reqBuf limitedBuffer
			reqLoggable := logBodies && captureRequestBody(r, &reqBuf)

			ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			var respBuf limitedBuffer
			if logBodies {
				respBuf.max = maxBodyLogBytes
				reqBuf.max = maxBodyLogBytes
				ww.Tee(&respBuf)
			}

			defer func() { //nolint:contextcheck // r.Context() is the request context; flagged falsely for a closure capturing r
				if r.URL.Path == healthPath && ww.Status() < http.StatusBadRequest {
					return
				}
				attrs := requestAttrs(r, ww, start)
				if logBodies {
					reqBody := "[omitted]"
					if reqLoggable {
						reqBody = formatLoggedBody(reqBuf.Bytes(), reqBuf.truncated, r.Header.Get("Content-Type"))
					}
					attrs = append(attrs,
						"request_body", reqBody,
						"response_body", formatLoggedBody(respBuf.Bytes(), respBuf.truncated, ww.Header().Get("Content-Type")),
					)
				}
				log.InfoContext(r.Context(), "http request", attrs...)
			}()

			next.ServeHTTP(ww, r)
		})
	}
}

// requestAttrs returns the log attributes describing a finished request.
func requestAttrs(r *http.Request, ww chimiddleware.WrapResponseWriter, start time.Time) []any {
	return []any{
		"method", r.Method,
		"path", RedactPath(r.URL.Path),
		"query", RedactQuery(r.URL.RawQuery),
		"status", ww.Status(),
		"bytes", ww.BytesWritten(),
		"duration_ms", time.Since(start).Milliseconds(),
		"request_id", chimiddleware.GetReqID(r.Context()),
		"remote", r.RemoteAddr,
		"user_agent", r.UserAgent(),
	}
}

// captureRequestBody tees the first maxBodyLogBytes of the request body into buf while the handler reads it, so the
// body is never buffered whole. It reports whether the body is worth logging (content type and presence).
func captureRequestBody(r *http.Request, buf *limitedBuffer) bool {
	if r.Body == nil || r.Body == http.NoBody || !isLoggableContentType(r.Header.Get("Content-Type")) {
		return false
	}
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.TeeReader(r.Body, buf), r.Body}
	return true
}

// formatLoggedBody redacts and bounds a captured body. Redaction happens before the truncation marker is added and
// also covers truncated (unparseable) JSON, form bodies and plain text.
func formatLoggedBody(body []byte, truncated bool, contentType string) string {
	if len(body) == 0 {
		return ""
	}
	if !utf8.Valid(body) {
		if !truncated {
			return "[binary]"
		}
		body = bytes.ToValidUTF8(body, nil)
	}

	s := RedactBody(string(body), contentType)
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

const redacted = "[redacted]"

// sensitivePathMarkers are path fragments of routes whose bodies carry credentials, one-time codes or personal
// onboarding data. Bodies of these routes are never logged.
var sensitivePathMarkers = []string{
	"/auth", "/onboarding", "/totp", "/login", "/signup", "/password", "/invite", "/verify", "/session", "/token",
	"/oauth", "/mfa", "/otp",
}

// SensitivePath reports whether path belongs to an auth or onboarding route (bodies must not be logged).
func SensitivePath(path string) bool {
	p := strings.ToLower(path)
	for _, m := range sensitivePathMarkers {
		if strings.Contains(p, m) {
			return true
		}
	}
	return false
}

// pathSecretMarkers are path segments whose next segment is a secret (for example /auth/invite/{token}).
var pathSecretMarkers = map[string]bool{"invite": true, "token": true, "reset": true, "verify": true, "confirm": true}

// RedactPath masks the segment after invite/token/reset/verify/confirm markers: those routes carry one-time tokens in
// the URL.
func RedactPath(path string) string {
	if !SensitivePath(path) {
		return path
	}
	parts := strings.Split(path, "/")
	for i := 0; i+1 < len(parts); i++ {
		if pathSecretMarkers[strings.ToLower(parts[i])] && parts[i+1] != "" {
			parts[i+1] = redacted
		}
	}
	return strings.Join(parts, "/")
}

// RedactQuery masks the values of sensitive query parameters (token, code, otp, key, ...). Unparseable input is
// dropped rather than logged.
func RedactQuery(raw string) string {
	if raw == "" {
		return ""
	}
	vals, err := url.ParseQuery(raw)
	if err != nil {
		return redacted
	}
	for k, vs := range vals {
		if isSensitiveKey(k) {
			for i := range vs {
				vs[i] = redacted
			}
		}
	}
	return vals.Encode()
}

// RedactHeaders returns a copy of h safe to log: sensitive headers (Authorization, Cookie, Set-Cookie, CSRF and API
// key headers, ...) have their values replaced.
func RedactHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vs := range h {
		if isSensitiveKey(k) {
			out[k] = redacted
			continue
		}
		out[k] = RedactString(strings.Join(vs, ", "))
	}
	return out
}

// RedactBody redacts secrets from a request or response body. JSON is redacted structurally; anything else (form
// bodies, truncated JSON, text) is redacted by pattern. It never returns the original text of a sensitive value.
func RedactBody(s, contentType string) string {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "x-www-form-urlencoded") {
		if vals, err := url.ParseQuery(s); err == nil {
			for k, vs := range vals {
				if isSensitiveKey(k) {
					for i := range vs {
						vs[i] = redacted
					}
				}
			}
			return RedactString(vals.Encode())
		}
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		redactValue(v)
		if b, mErr := json.Marshal(v); mErr == nil {
			return string(b)
		}
	}
	return RedactString(redactPairs(s))
}

var (
	// "key": "value" (value may be unterminated when the body was cut) and key=value pairs.
	jsonPairRe = regexp.MustCompile(`(?i)("[^"\\]*"\s*:\s*)("(?:[^"\\]|\\.)*(?:"|$)|[^\s,}\]]+)`)
	formPairRe = regexp.MustCompile(`(?i)([?&;\s]|^)([A-Za-z0-9_.\-]+)(=)([^&\s;]*)`)
	jwtRe      = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}(?:\.[A-Za-z0-9_-]*)?`)
	bearerRe   = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`)
)

// RedactString masks JWTs and Authorization-style credentials wherever they appear in free text.
func RedactString(s string) string {
	s = jwtRe.ReplaceAllString(s, redacted)
	return bearerRe.ReplaceAllString(s, "$1 "+redacted)
}

// redactPairs masks values of sensitive keys in non-parseable text.
func redactPairs(s string) string {
	s = jsonPairRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := jsonPairRe.FindStringSubmatch(m)
		key := strings.Trim(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sub[1]), ":")), `"`)
		if isSensitiveKey(key) {
			return sub[1] + `"` + redacted + `"`
		}
		return m
	})
	return formPairRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := formPairRe.FindStringSubmatch(m)
		if isSensitiveKey(sub[2]) {
			return sub[1] + sub[2] + sub[3] + redacted
		}
		return m
	})
}

func redactValue(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if isSensitiveKey(k) {
				t[k] = redacted
				continue
			}
			if str, ok := child.(string); ok {
				t[k] = RedactString(str)
				continue
			}
			redactValue(child)
		}
	case []any:
		for i, child := range t {
			if str, ok := child.(string); ok {
				t[i] = RedactString(str)
				continue
			}
			redactValue(child)
		}
	}
}

// sensitiveKeyParts: a key containing any of these (after lower-casing and dropping - _ and spaces) is secret.
var sensitiveKeyParts = []string{
	"password", "passwd", "passphrase", "secret", "token", "authorization", "cookie", "csrf", "xsrf", "apikey",
	"accesskey", "privatekey", "credential", "otp", "totp", "signature", "sessionid", "session",
}

// sensitiveKeyExact are short keys that are only secret when they are the whole key (a substring match on "code" or
// "pin" would hide country_code and the like).
var sensitiveKeyExact = map[string]bool{
	"code": true, "pin": true, "verificationcode": true, "mfacode": true, "smscode": true, "emailcode": true,
	"verifycode": true, "recoverycode": true, "recoverycodes": true, "backupcode": true, "backupcodes": true,
	"twofactorcode": true, "authcode": true, "key": true, "sig": true,
}

func isSensitiveKey(key string) bool {
	k := strings.NewReplacer("-", "", "_", "", " ", "", ".", "").Replace(strings.ToLower(key))
	if sensitiveKeyExact[k] {
		return true
	}
	for _, part := range sensitiveKeyParts {
		if strings.Contains(k, part) {
			return true
		}
	}
	return false
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
