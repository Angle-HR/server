package app

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Angle-HR/server/pkg/config"
)

const testFluvioOrigin = "https://fluvio.staging.example.com"

func fluvioTestConfig(env, user, pass string) config.Config {
	return config.Config{AppEnv: env, FluvioUIOrigin: testFluvioOrigin, FluvioBasicAuthUser: user, FluvioBasicAuthPass: pass}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestFluvioUIHandlerNeedsCredentialsOutsideDevelopment(t *testing.T) {
	t.Parallel()
	for _, env := range []string{"staging", "production"} {
		if h := fluvioUIHandler(fluvioTestConfig(env, "", ""), nil, discardLogger()); h != nil {
			t.Errorf("%s without credentials: handler mounted, want none", env)
		}
	}
	if h := fluvioUIHandler(fluvioTestConfig("development", "", ""), nil, discardLogger()); h == nil {
		t.Error("development without credentials: no handler, want an open one")
	}
}

func TestFluvioUIHandlerRequiresBasicAuth(t *testing.T) {
	t.Parallel()
	// The client is nil: every request here must be stopped before it reaches a queue handler.
	h := fluvioUIHandler(fluvioTestConfig("staging", "ops", "s3cret"), nil, discardLogger())
	if h == nil {
		t.Fatal("handler not mounted")
	}

	tests := []struct {
		name       string
		user, pass string
		withAuth   bool
	}{
		{name: "no credentials"},
		{name: "wrong password", user: "ops", pass: "nope", withAuth: true},
		{name: "wrong user", user: "admin", pass: "s3cret", withAuth: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/fluvio/api/queues", nil)
			r.Header.Set("Origin", testFluvioOrigin)
			if tt.withAuth {
				r.SetBasicAuth(tt.user, tt.pass)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status: got %d, want 401", w.Code)
			}
			if !strings.HasPrefix(w.Header().Get("WWW-Authenticate"), "Basic ") {
				t.Errorf("WWW-Authenticate: got %q", w.Header().Get("WWW-Authenticate"))
			}
			// The dashboard is cross-origin: without CORS headers on the 401 the browser hides the status.
			if got := w.Header().Get("Access-Control-Allow-Origin"); got != testFluvioOrigin {
				t.Errorf("Access-Control-Allow-Origin: got %q, want %q", got, testFluvioOrigin)
			}
		})
	}
}

func TestFluvioUIHandlerPreflightSkipsAuth(t *testing.T) {
	t.Parallel()
	h := fluvioUIHandler(fluvioTestConfig("staging", "ops", "s3cret"), nil, discardLogger())
	r := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/fluvio/api/queues", nil)
	r.Header.Set("Origin", testFluvioOrigin)
	r.Header.Set("Access-Control-Request-Method", http.MethodGet)
	r.Header.Set("Access-Control-Request-Headers", "authorization")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want 204", w.Code)
	}
	if !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Errorf("Access-Control-Allow-Headers: got %q, want Authorization allowed", w.Header().Get("Access-Control-Allow-Headers"))
	}
}

func TestBasicAuthPassesValidCredentials(t *testing.T) {
	t.Parallel()
	called := false
	h := basicAuth("fluvio", "ops", "s3cret")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/fluvio/api/queues", nil)
	r.SetBasicAuth("ops", "s3cret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !called {
		t.Fatalf("status %d, next called %v; want 200 and true", w.Code, called)
	}
}
