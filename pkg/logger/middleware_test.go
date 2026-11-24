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
		if _, err := w.Write([]byte(`{"access_token":"abc","ok":true}`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/jobs?x=1",
		strings.NewReader(`{"email":"a@b.com","password":"secret"}`),
	)
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
	if status, isNumber := entry["status"].(float64); !isNumber || int(status) != http.StatusOK {
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

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/x", strings.NewReader(`{"password":"secret"}`))
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

func TestRequestLoggerSkipsSuccessfulHealthProbes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		status  int
		wantLog bool
	}{
		{http.StatusOK, false},
		{http.StatusServiceUnavailable, true},
	} {
		var buf bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
		h := RequestLogger(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
		}))

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, healthPath, http.NoBody)
		h.ServeHTTP(httptest.NewRecorder(), req)

		if logged := buf.Len() > 0; logged != tc.wantLog {
			t.Fatalf("status %d: logged = %v, want %v", tc.status, logged, tc.wantLog)
		}
	}
}

func debugLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestRequestLoggerNeverLogsAuthOrOnboardingBodies(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"/api/v1/auth/login", "/api/v1/auth/signup", "/api/v1/auth/verify-email", "/api/v1/auth/totp/confirm",
		"/api/v1/auth/reset-password", "/api/v1/admin/auth/login", "/api/v1/onboarding/individual",
		"/api/v1/onboarding/business", "/auth/refresh",
	} {
		var buf bytes.Buffer
		h := RequestLogger(debugLogger(&buf))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"eyJhbGciOi.eyJzdWIiOiIx.sig","code":"123456"}`))
		}))
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path,
			strings.NewReader(`{"email":"a@b.com","password":"hunter2hunter2","code":"123456","legal_name":"Ada"}`))
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(httptest.NewRecorder(), req)

		out := buf.String()
		for _, leak := range []string{"hunter2", "123456", "eyJhbGci", "Ada", "a@b.com", "request_body", "response_body"} {
			if strings.Contains(out, leak) {
				t.Errorf("%s: log contains %q: %s", path, leak, out)
			}
		}
		if !strings.Contains(out, "http request") {
			t.Errorf("%s: request line missing", path)
		}
	}
}

func TestRequestLoggerRedactsPathAndQuery(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	h := RequestLogger(debugLogger(&buf))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/api/v1/auth/invite/abc123-secret-token?token=zzz&page=2&code=987654", http.NoBody)
	h.ServeHTTP(httptest.NewRecorder(), req)
	out := buf.String()
	for _, leak := range []string{"abc123-secret-token", "zzz", "987654"} {
		if strings.Contains(out, leak) {
			t.Errorf("log contains %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, "page=2") {
		t.Errorf("non-sensitive query lost: %s", out)
	}
}

func TestRequestLoggerRedactsNonAuthPayloads(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	h := RequestLogger(debugLogger(&buf))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		// Larger than the capture limit, so the logged copy is truncated mid-value.
		_, _ = w.Write([]byte(`{"pad":"` + strings.Repeat("x", maxBodyLogBytes) + `","api_key":"sk-live-1234567890"}`))
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/integrations",
		strings.NewReader(`{"name":"n","api_key":"sk-live-abcdef","nested":{"totp_secret":"JBSWY3DPEHPK3PXP"}}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(httptest.NewRecorder(), req)

	out := buf.String()
	for _, leak := range []string{"sk-live", "JBSWY3DPEHPK3PXP"} {
		if strings.Contains(out, leak) {
			t.Errorf("log contains %q", leak)
		}
	}
	if !strings.Contains(out, "[truncated]") || !strings.Contains(out, `\"n\"`) {
		t.Errorf("expected truncated response and visible non-secret fields: %.300s", out)
	}
}

func TestRedactBody(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, ct, in string
		gone         []string
		kept         []string
	}{
		{"json", "application/json",
			`{"user":{"Password":"p4ss","otp":"123456","country_code":"GB"},"tokens":["a"],"csrf-token":"c"}`,
			[]string{"p4ss", "123456", `"c"`}, []string{"GB"}},
		{"form", "application/x-www-form-urlencoded",
			"email=a%40b.com&password=p4ss&verification_code=123456&next=%2Fhome",
			[]string{"p4ss", "123456"}, []string{"next=%2Fhome"}},
		{"truncated json", "application/json",
			`{"ok":true,"refresh_token":"abcdefghijklmnopqrstuvwxyz`,
			[]string{"abcdefghij"}, []string{`"ok":true`}},
		{"truncated json secret key", "application/json",
			`{"x":1,"secret":"JBSWY3DP`, []string{"JBSWY3DP"}, nil},
		{"text jwt and bearer", "text/plain",
			"got eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdef and Authorization: Bearer abcdef0123456789",
			[]string{"eyJhbGci", "abcdef0123456789"}, nil},
		{"text key=value", "text/plain", "csrf_token=zzz9 name=ok", []string{"zzz9"}, []string{"name=ok"}},
	}
	for _, c := range cases {
		out := RedactBody(c.in, c.ct)
		for _, g := range c.gone {
			if strings.Contains(out, g) {
				t.Errorf("%s: %q still contains %q", c.name, out, g)
			}
		}
		for _, k := range c.kept {
			if !strings.Contains(out, k) {
				t.Errorf("%s: %q lost %q", c.name, out, k)
			}
		}
	}
}

func TestRedactHeaders(t *testing.T) {
	t.Parallel()

	h := http.Header{}
	h.Set("Authorization", "Bearer abcdefghijkl")
	h.Set("Cookie", "openhr_session=s3cr3t")
	h.Set("Set-Cookie", "openhr_session=s3cr3t")
	h.Set("X-CSRF-Token", "csrf123")
	h.Set("X-Api-Key", "key123")
	h.Set("User-Agent", "curl/8")
	out := RedactHeaders(h)
	for k, v := range out {
		if k == "User-Agent" {
			if v != "curl/8" {
				t.Errorf("User-Agent = %q", v)
			}
			continue
		}
		if v != "[redacted]" {
			t.Errorf("%s = %q", k, v)
		}
	}
}

func TestSensitivePath(t *testing.T) {
	t.Parallel()

	for p, want := range map[string]bool{
		"/api/v1/auth/login": true, "/api/v1/onboarding/status": true, "/api/v1/auth": true,
		"/api/v1/jobs": false, "/api/v1/public/jobs/1/applications": false, "/healthz": false,
	} {
		if got := SensitivePath(p); got != want {
			t.Errorf("SensitivePath(%q) = %v", p, got)
		}
	}
}
