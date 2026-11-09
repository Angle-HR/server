package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/pkg/apperror"
)

func postResend(t *testing.T, router http.Handler, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"verification_session_id": sessionID})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, "/api/v1/auth/resend-verification", bytes.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestResendVerification_freshCodeThenRateLimited(t *testing.T) {
	redisClient := testRedisClient(t)
	store := auth.NewVerificationStore(redisClient)
	sessionID := uuid.NewString()
	if err := store.CreateSession(t.Context(), auth.VerificationSession{
		SessionID: sessionID, UserID: uuid.New(), Email: "u@example.com", Region: "uk",
		Code: "000000", Purpose: auth.PurposeEmailVerify,
	}); err != nil {
		t.Fatal(err)
	}
	router := testAuthRouter(t, nil, nil, redisClient)

	rec := postResend(t, router, sessionID)
	assertStatus(t, rec, http.StatusOK)
	session, err := store.GetSession(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.Code == "000000" && session.Attempts != 0 {
		t.Fatal("expected a fresh code and reset attempts")
	}
	if _, err := store.ValidateCode(t.Context(), sessionID, "000000"); session.Code != "000000" && err == nil {
		t.Fatal("previous code must be invalidated")
	}

	rec = postResend(t, router, sessionID)
	assertStatus(t, rec, http.StatusTooManyRequests)
	errBody := decodeError(t, rec)
	if errBody.Code != apperror.CodeVerificationRateLimited {
		t.Fatalf("code = %q", errBody.Code)
	}
	secs, ok := errBody.Details["retry_after_seconds"].(float64)
	if !ok || secs < 1 {
		t.Fatalf("retry_after_seconds = %v", errBody.Details["retry_after_seconds"])
	}
	if got := rec.Header().Get("Retry-After"); got != strconv.Itoa(int(secs)) {
		t.Fatalf("Retry-After = %q want %d", got, int(secs))
	}
}
