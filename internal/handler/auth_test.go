package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
)

func TestAuthSignup_invalidBody(t *testing.T) {
	t.Parallel()

	h := NewAuthHandler(nil, nil, nil, nil, nil, region.RegionUK)
	router := chi.NewRouter()
	router.Route("/auth", h.RegisterRoutes)

	req := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(`{`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestAuthMiddleware_missingToken(t *testing.T) {
	t.Parallel()

	tokens, err := auth.NewTokenService("secret", time.Hour, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("NewTokenService: %v", err)
	}

	mw := auth.NewMiddleware(tokens)
	called := false
	handler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/onboarding/status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called {
		t.Fatal("handler should not run without token")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d", rec.Code)
	}
}

func TestGenerateOTP_format(t *testing.T) {
	t.Parallel()

	code, err := generateOTP()
	if err != nil {
		t.Fatalf("generateOTP: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("code length: got %d", len(code))
	}
}

func TestProductOnboarding_verifyAddressNotImplemented(t *testing.T) {
	t.Parallel()

	h := NewProductOnboardingHandler(nil, nil, nil)
	router := chi.NewRouter()
	router.Post("/onboarding/address/verify", h.verifyAddress)

	req := httptest.NewRequest(http.MethodPost, "/onboarding/address/verify", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status: got %d", rec.Code)
	}

	errBody := decodeError(t, rec)
	if errBody.Code != apperror.CodeNotImplemented {
		t.Fatalf("code: got %q", errBody.Code)
	}
}
