package identityhttp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/pkg/apperror"
)

func TestAuthVerifyEmailIncorrectOTP(t *testing.T) {
	for _, code := range []string{"654321", "123", "abcdef", ""} {
		t.Run("code="+code, func(t *testing.T) {
			client := testRedisClient(t)
			sessionID := uuid.NewString()
			store := auth.NewVerificationStore(client)
			if err := store.CreateSession(t.Context(), auth.VerificationSession{
				SessionID: sessionID, Code: "123456", Purpose: auth.PurposeEmailVerify,
			}); err != nil {
				t.Fatal(err)
			}
			router := testAuthRouter(t, nil, nil, client)
			body, err := json.Marshal(map[string]string{"verification_session_id": sessionID, "code": code})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/verify-email", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			assertStatus(t, rec, http.StatusBadRequest)
			got := decodeError(t, rec)
			if got.Code != apperror.CodeInvalidVerificationCode || got.Message != "incorrect otp" {
				t.Fatalf("unexpected error: %+v", got)
			}
		})
	}
}

func TestAuthVerifyEmailAttemptsRemainingAndLockout(t *testing.T) {
	client := testRedisClient(t)
	sessionID := uuid.NewString()
	store := auth.NewVerificationStore(client)
	if err := store.CreateSession(t.Context(), auth.VerificationSession{
		SessionID: sessionID, Code: "123456", Purpose: auth.PurposeEmailVerify,
	}); err != nil {
		t.Fatal(err)
	}
	router := testAuthRouter(t, nil, nil, client)
	post := func(code string) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]string{"verification_session_id": sessionID, "code": code})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/verify-email", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	for want := 4; want >= 0; want-- {
		rec := post("000000")
		assertStatus(t, rec, http.StatusBadRequest)
		got := decodeError(t, rec)
		if got.Code != apperror.CodeInvalidVerificationCode || got.Details["attempts_remaining"] != float64(want) {
			t.Fatalf("want attempts_remaining %d, got %+v", want, got)
		}
	}
	rec := post("000000")
	assertStatus(t, rec, http.StatusTooManyRequests)
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("expected Retry-After")
	}
	if got := decodeError(t, rec); got.Code != apperror.CodeTooManyAttempts || got.Details["attempts_remaining"] != float64(0) {
		t.Fatalf("locked error: %+v", got)
	}
	// The right code is refused while locked.
	assertStatus(t, post("123456"), http.StatusTooManyRequests)
}
