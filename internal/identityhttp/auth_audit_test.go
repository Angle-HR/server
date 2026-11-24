package identityhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

func newGlobalMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	m, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func postJSON(t *testing.T, router http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// expectNoAccount queues the two global lookups made for an email with no account.
func expectNoAccount(t *testing.T, m pgxmock.PgxPoolIface, email string) {
	t.Helper()
	sql, args, err := query.LookupUsersRegistryByEmail(email)
	if err != nil {
		t.Fatal(err)
	}
	m.ExpectQuery(sql).WithArgs(args...).WillReturnError(pgx.ErrNoRows)
	sql, args, err = query.LookupPendingUserByEmail(email)
	if err != nil {
		t.Fatal(err)
	}
	m.ExpectQuery(sql).WithArgs(args...).WillReturnError(pgx.ErrNoRows)
}

func assertPolicyFieldError(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	assertStatus(t, rec, http.StatusBadRequest)
	body := decodeError(t, rec)
	if body.Code != apperror.CodeValidationError {
		t.Fatalf("code = %q", body.Code)
	}
	fields := decodeDetailsFields(t, body)
	if len(fields) != 1 || fields[0]["path"] != "password" || fields[0]["message"] != auth.PasswordPolicyMessage {
		t.Fatalf("fields = %v", fields)
	}
}

func TestAuthSignup_weakPasswordRejected(t *testing.T) {
	router := testAuthRouter(t, newGlobalMock(t), newGlobalMock(t), testRedisClient(t))
	for _, pw := range []string{"aaaaaaaa", "12345678"} {
		rec := postJSON(t, router, http.MethodPost, "/api/v1/auth/signup", map[string]string{"email": "new@example.com", "password": pw})
		assertPolicyFieldError(t, rec)
	}
}

func TestAuthResetPassword_weakPasswordRejected(t *testing.T) {
	router := testAuthRouter(t, newGlobalMock(t), newGlobalMock(t), testRedisClient(t))
	rec := postJSON(t, router, http.MethodPost, "/api/v1/auth/reset-password",
		map[string]string{"token": "0123456789abcdef0123", "password": "aaaaaaaa"})
	assertPolicyFieldError(t, rec)
}

func TestAuthAcceptInvite_weakPasswordRejected(t *testing.T) {
	router := testAuthRouter(t, newGlobalMock(t), newGlobalMock(t), testRedisClient(t))
	rec := postJSON(t, router, http.MethodPost, "/api/v1/auth/accept-invite",
		map[string]string{"token": "0123456789abcdef0123", "password": "aaaaaaaa"})
	assertPolicyFieldError(t, rec)
}

func TestAuthLogin_unknownEmailIsLockedOut(t *testing.T) {
	email := "ghost@example.com"
	global := newGlobalMock(t)
	router := testAuthRouter(t, newGlobalMock(t), global, testRedisClient(t))

	for i := range passwordLockoutMaxAttempts {
		expectNoAccount(t, global, email)
		rec := postAuthLogin(t, router, email, "wrong-password1")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d", i+1, rec.Code)
		}
	}
	// No lookup is expected: the lockout answers first.
	rec := postAuthLogin(t, router, email, "wrong-password1")
	assertStatus(t, rec, http.StatusTooManyRequests)
	if got := decodeError(t, rec).Code; got != apperror.CodeTooManyAttempts {
		t.Fatalf("code = %q", got)
	}
	assertMocksMet(t, global)
}

func TestAuthLogin_unknownEmailSameResponseAsWrongPassword(t *testing.T) {
	global, regional := expectUnverifiedLoginMocks(t, nil)
	router := testAuthRouter(t, regional, global, testRedisClient(t))
	wrong := decodeError(t, postAuthLogin(t, router, testLoginEmail, "wrong-password1"))

	ghost := newGlobalMock(t)
	expectNoAccount(t, ghost, "ghost@example.com")
	router2 := testAuthRouter(t, newGlobalMock(t), ghost, testRedisClient(t))
	unknown := decodeError(t, postAuthLogin(t, router2, "ghost@example.com", "wrong-password1"))
	if wrong.Code != unknown.Code || wrong.Message != unknown.Message {
		t.Fatalf("responses differ: %+v vs %+v", wrong, unknown)
	}
}

func otpEnvelopeData(t *testing.T, rec *httptest.ResponseRecorder) AuthSignupData {
	t.Helper()
	var env response.Envelope
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(env.Data)
	if err != nil {
		t.Fatal(err)
	}
	var data AuthSignupData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAuthLoginOTP_unknownEmailGetsDecoySession(t *testing.T) {
	email := "ghost@example.com"
	global := newGlobalMock(t)
	redisClient := testRedisClient(t)
	router := testAuthRouter(t, newGlobalMock(t), global, redisClient)

	expectNoAccount(t, global, email)
	rec := postJSON(t, router, http.MethodPost, "/api/v1/auth/login/otp/request", map[string]string{"email": email})
	assertStatus(t, rec, http.StatusOK)
	data := otpEnvelopeData(t, rec)
	if _, err := uuid.Parse(data.VerificationSessionID); err != nil || data.Email != email ||
		data.CodeExpiresInSeconds != auth.CodeExpiresInSeconds() {
		t.Fatalf("implausible decoy data: %+v", data)
	}

	// A decoy session fails like a wrong code on a real session.
	store := auth.NewVerificationStore(redisClient)
	realID := uuid.NewString()
	if err := store.CreateSession(t.Context(), auth.VerificationSession{
		SessionID: realID, UserID: uuid.New(), Email: "real@example.com", Region: "uk",
		Code: "111111", Purpose: auth.PurposeLoginOTP,
	}); err != nil {
		t.Fatal(err)
	}
	verify := func(id, code string) *httptest.ResponseRecorder {
		return postJSON(t, router, http.MethodPost, "/api/v1/auth/login/otp/verify",
			map[string]string{"verification_session_id": id, "code": code})
	}
	decoyRec, realRec := verify(data.VerificationSessionID, "111111"), verify(realID, "222222")
	if decoyRec.Code != realRec.Code {
		t.Fatalf("status differs: decoy %d real %d", decoyRec.Code, realRec.Code)
	}
	d, r := decodeError(t, decoyRec), decodeError(t, realRec)
	if d.Code != r.Code || d.Message != r.Message || d.Code != apperror.CodeInvalidVerificationCode {
		t.Fatalf("errors differ: %+v vs %+v", d, r)
	}
	assertMocksMet(t, global)
}

func TestAuthLoginOTP_verifyLockedPerEmailAcrossSessions(t *testing.T) {
	redisClient := testRedisClient(t)
	router := testAuthRouter(t, newGlobalMock(t), newGlobalMock(t), redisClient)
	store := auth.NewVerificationStore(redisClient)
	email := "victim@example.com"

	mk := func() string {
		id := uuid.NewString()
		if err := store.CreateSession(t.Context(), auth.VerificationSession{
			SessionID: id, UserID: uuid.New(), Email: email, Region: "uk", Code: "111111", Purpose: auth.PurposeLoginOTP,
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// Spread wrong guesses over fresh sessions, so the per-session cap never trips.
	for range otpLockoutMaxAttempts {
		rec := postJSON(t, router, http.MethodPost, "/api/v1/auth/login/otp/verify",
			map[string]string{"verification_session_id": mk(), "code": "000000"})
		if rec.Code == http.StatusTooManyRequests {
			t.Fatal("locked too early")
		}
	}
	rec := postJSON(t, router, http.MethodPost, "/api/v1/auth/login/otp/verify",
		map[string]string{"verification_session_id": mk(), "code": "111111"})
	assertStatus(t, rec, http.StatusTooManyRequests)
}

type countingDB struct {
	globalDB
	n atomic.Int32
}

func (c *countingDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	c.n.Add(1)
	return c.globalDB.QueryRow(ctx, sql, args...)
}

func TestAuthForgotPassword_cooldownKeepsGenericResponse(t *testing.T) {
	email := "ghost@example.com"
	mock := newGlobalMock(t)
	counting := &countingDB{globalDB: mock}
	h := NewAuthHandler(nil, counting, testRedisClient(t), nil, nil, nil)
	router := chi.NewRouter()
	router.Post("/forgot", h.forgotPassword)

	expectNoAccount(t, mock, email)
	first := postJSON(t, router, http.MethodPost, "/forgot", map[string]string{"email": email})
	assertStatus(t, first, http.StatusOK)
	lookups := counting.n.Load()
	if lookups == 0 {
		t.Fatal("first request should look the account up")
	}
	second := postJSON(t, router, http.MethodPost, "/forgot", map[string]string{"email": "GHOST@example.com"})
	assertStatus(t, second, http.StatusOK)
	if counting.n.Load() != lookups {
		t.Fatal("request inside cooldown must not reach the database")
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("bodies differ: %s vs %s", first.Body.String(), second.Body.String())
	}
}

func TestPatchSignup_cooldownAndRegistry(t *testing.T) {
	redisClient := testRedisClient(t)
	mock := newGlobalMock(t)
	h := NewAuthHandler(nil, mock, redisClient, nil, nil, nil)
	router := chi.NewRouter()
	router.Patch("/signup", h.patchSignup)
	store := auth.NewVerificationStore(redisClient)

	sessionID := uuid.NewString()
	if err := store.CreateSession(t.Context(), auth.VerificationSession{
		SessionID: sessionID, UserID: uuid.New(), Email: "old@example.com", Region: "global",
		Code: "123456", Purpose: auth.PurposeEmailVerify,
	}); err != nil {
		t.Fatal(err)
	}

	// Session cooldown (a resend just happened) -> 429 with retry_after_seconds.
	if err := store.MarkResent(t.Context(), sessionID); err != nil {
		t.Fatal(err)
	}
	body := map[string]string{"verification_session_id": sessionID, "email": "new@example.com"}
	rec := postJSON(t, router, http.MethodPatch, "/signup", body)
	assertStatus(t, rec, http.StatusTooManyRequests)
	if secs, ok := decodeError(t, rec).Details["retry_after_seconds"].(float64); !ok || secs < 1 {
		t.Fatalf("retry_after_seconds missing: %s", rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("expected Retry-After header")
	}

	// Per-email cooldown on the target address.
	other := uuid.NewString()
	if err := store.CreateSession(t.Context(), auth.VerificationSession{
		SessionID: other, UserID: uuid.New(), Email: "old2@example.com", Region: "global", Purpose: auth.PurposeEmailVerify,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkResentByEmail(t.Context(), "busy@example.com"); err != nil {
		t.Fatal(err)
	}
	rec = postJSON(t, router, http.MethodPatch, "/signup",
		map[string]string{"verification_session_id": other, "email": "busy@example.com"})
	assertStatus(t, rec, http.StatusTooManyRequests)

	// Registered (verified) email elsewhere -> 409.
	third := uuid.NewString()
	if err := store.CreateSession(t.Context(), auth.VerificationSession{
		SessionID: third, UserID: uuid.New(), Email: "old3@example.com", Region: "global", Purpose: auth.PurposeEmailVerify,
	}); err != nil {
		t.Fatal(err)
	}
	sql, args, err := query.LookupVerifiedRegistryEmail("taken@example.com")
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(sql).WithArgs(args...).WillReturnRows(pgxmock.NewRows([]string{"user_id", "region"}).AddRow(uuid.New(), "uk"))
	rec = postJSON(t, router, http.MethodPatch, "/signup",
		map[string]string{"verification_session_id": third, "email": "taken@example.com"})
	assertStatus(t, rec, http.StatusConflict)
	assertMocksMet(t, mock)
}

func TestPatchSignup_secondPatchRightAfterFirstIsRateLimited(t *testing.T) {
	redisClient := testRedisClient(t)
	mock := newGlobalMock(t)
	h := NewAuthHandler(nil, mock, redisClient, nil, nil, nil)
	router := chi.NewRouter()
	router.Patch("/signup", h.patchSignup)
	store := auth.NewVerificationStore(redisClient)
	userID := uuid.New()
	sessionID := uuid.NewString()
	if err := store.CreateSession(t.Context(), auth.VerificationSession{
		SessionID: sessionID, UserID: userID, Email: "old@example.com", Region: "global", Purpose: auth.PurposeEmailVerify,
	}); err != nil {
		t.Fatal(err)
	}

	sql, args, err := query.LookupVerifiedRegistryEmail("a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(sql).WithArgs(args...).WillReturnError(pgx.ErrNoRows)
	sql, args, err = query.LookupPendingUserByEmail("a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(sql).WithArgs(args...).WillReturnError(pgx.ErrNoRows)
	sql, args, err = query.UpdatePendingUserEmail(userID, "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(sql).WithArgs(args...).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(userID))

	rec := postJSON(t, router, http.MethodPatch, "/signup", map[string]string{"verification_session_id": sessionID, "email": "a@example.com"})
	assertStatus(t, rec, http.StatusOK)
	newSession := otpEnvelopeData(t, rec).VerificationSessionID

	rec = postJSON(t, router, http.MethodPatch, "/signup", map[string]string{"verification_session_id": newSession, "email": "b@example.com"})
	assertStatus(t, rec, http.StatusTooManyRequests)
	assertMocksMet(t, mock)
}

func TestAuthSignup_abandonedPendingAccountIsRestarted(t *testing.T) {
	email := "abandoned@example.com"
	redisClient := testRedisClient(t)
	mock := newGlobalMock(t)
	h := NewAuthHandler(nil, mock, redisClient, nil, nil, nil)
	router := chi.NewRouter()
	router.Post("/signup", h.signup)

	expectPending := func() {
		sql, args, err := query.LookupVerifiedRegistryEmail(email)
		if err != nil {
			t.Fatal(err)
		}
		mock.ExpectQuery(sql).WithArgs(args...).WillReturnError(pgx.ErrNoRows)
		sql, args, err = query.LookupPendingUserByEmail(email)
		if err != nil {
			t.Fatal(err)
		}
		mock.ExpectQuery(sql).WithArgs(args...).WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "password_hash", "email_verified_at", "onboarding_completed_at",
			"account_type", "first_name", "last_name", "legal_full_name", "country_id", "totp_secret", "totp_enabled_at",
		}).AddRow(testLoginUserID, email, "old-hash", (*time.Time)(nil), nil, nil, nil, nil, nil, nil, nil, nil))
	}

	expectPending()
	updateSQL, _, err := query.UpdatePendingUserPassword(testLoginUserID, "x")
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(updateSQL).WithArgs(testLoginUserID, pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(testLoginUserID))

	rec := postJSON(t, router, http.MethodPost, "/signup", map[string]string{"email": email, "password": "brand-new-pass1"})
	assertStatus(t, rec, http.StatusCreated)
	data := otpEnvelopeData(t, rec)
	if data.VerificationSessionID == "" || data.Email != email {
		t.Fatalf("data = %+v", data)
	}
	session, err := h.Verifier.GetSession(t.Context(), data.VerificationSessionID)
	if err != nil || session.UserID != testLoginUserID || session.Purpose != auth.PurposeEmailVerify {
		t.Fatalf("session = %+v, %v", session, err)
	}

	// Immediately again: the email cooldown applies.
	expectPending()
	rec = postJSON(t, router, http.MethodPost, "/signup", map[string]string{"email": email, "password": "brand-new-pass2"})
	assertStatus(t, rec, http.StatusTooManyRequests)
	if got := decodeError(t, rec).Code; got != apperror.CodeVerificationRateLimited {
		t.Fatalf("code = %q", got)
	}
	assertMocksMet(t, mock)
}
