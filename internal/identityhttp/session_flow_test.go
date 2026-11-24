package identityhttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/pquerna/otp/totp"

	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/internal/session"
	"github.com/Angle-HR/server/pkg/apperror"
)

// totpUserRows is an account row with TOTP enabled (secret encrypted like production).
func totpUserRows(t *testing.T, crypto *auth.TOTPCrypto, passwordHash, secret string) *pgxmock.Rows {
	t.Helper()
	enc, err := crypto.Encrypt(secret)
	if err != nil {
		t.Fatal(err)
	}
	verified := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	enabled := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	return pgxmock.NewRows([]string{
		"id", "email", "password_hash", "email_verified_at", "onboarding_completed_at",
		"account_type", "first_name", "last_name", "legal_full_name", "country_id",
		"totp_secret", "totp_enabled_at",
	}).AddRow(
		testLoginUserID, testLoginEmail, passwordHash, &verified, nil,
		nil, nil, nil, nil, nil,
		&enc, &enabled,
	)
}

func sessionCookieFrom(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func sessPostJSON(t *testing.T, router http.Handler, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestMFAFlow_preAuthCookieThenFullSession(t *testing.T) {
	crypto, err := auth.NewTOTPCrypto("secret")
	if err != nil {
		t.Fatal(err)
	}
	passwordHash, err := auth.HashPassword(testLoginPassword)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "JBSWY3DPEHPK3PXP"

	global, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(global.Close)
	regional, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(regional.Close)

	registrySQL, registryArgs, err := query.LookupUsersRegistryByEmail(testLoginEmail)
	if err != nil {
		t.Fatal(err)
	}
	global.ExpectQuery(registrySQL).WithArgs(registryArgs...).WillReturnRows(
		pgxmock.NewRows([]string{"id", "email", "region", "user_id"}).AddRow(testLoginUserID, testLoginEmail, "uk", &testLoginUserID),
	)
	userSQL, userArgs, err := query.LookupAccountUserByID(testLoginUserID)
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		regional.ExpectQuery(userSQL).WithArgs(userArgs...).WillReturnRows(totpUserRows(t, crypto, passwordHash, secret))
	}
	progressSQL, progressArgs, err := query.LookupOnboardingProgress(testLoginUserID)
	if err != nil {
		t.Fatal(err)
	}
	regional.ExpectQuery(progressSQL).WithArgs(progressArgs...).WillReturnError(pgx.ErrNoRows)

	redisClient := testRedisClient(t)
	router := testAuthRouter(t, regional, global, redisClient)

	// 1. Password is right but TOTP is on: pre-auth cookie only, no session, no CSRF token.
	rec := postAuthLogin(t, router, testLoginEmail, testLoginPassword)
	assertStatus(t, rec, http.StatusOK)
	if sessionCookieFrom(rec, "openhr_session") != nil {
		t.Fatal("no full session may be issued before TOTP")
	}
	pre := sessionCookieFrom(rec, "openhr_preauth")
	if pre == nil || pre.Value == "" || !pre.HttpOnly || pre.MaxAge != 300 {
		t.Fatalf("pre-auth cookie: %+v", pre)
	}
	if strings.Contains(rec.Body.String(), "mfa_token") || strings.Contains(rec.Body.String(), "csrf_token") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}

	// 2. No pre-auth cookie: rejected.
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rec = sessPostJSON(t, router, "/api/v1/auth/login/totp", `{"code":"`+code+`"}`)
	assertStatus(t, rec, http.StatusUnauthorized)

	// 3. Wrong code keeps the pre-auth session alive (lockout limits guesses), right code spends it.
	rec = sessPostJSON(t, router, "/api/v1/auth/login/totp", `{"code":"000000"}`, pre)
	assertStatus(t, rec, http.StatusBadRequest)
	rec = sessPostJSON(t, router, "/api/v1/auth/login/totp", `{"code":"`+code+`"}`, pre)
	assertStatus(t, rec, http.StatusOK)
	full := sessionCookieFrom(rec, "openhr_session")
	if full == nil || full.Value == "" || full.Value == pre.Value {
		t.Fatalf("full session cookie missing or not rotated: %+v", full)
	}
	var env struct {
		Data AuthSessionData `json:"data"`
	}
	if err = json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Data.CSRFToken == "" {
		t.Fatalf("expected csrf_token: %s %v", rec.Body.String(), err)
	}
	if cleared := sessionCookieFrom(rec, "openhr_preauth"); cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("pre-auth cookie must be cleared: %+v", cleared)
	}

	// 4. The pre-auth session is single use: replaying it fails.
	rec = sessPostJSON(t, router, "/api/v1/auth/login/totp", `{"code":"`+code+`"}`, pre)
	assertStatus(t, rec, http.StatusUnauthorized)

	// 5. A pre-auth cookie never works as a session.
	mw := auth.NewMiddleware(testSessionManager(redisClient))
	protected := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil)
	req.AddCookie(&http.Cookie{Name: "openhr_session", Value: pre.Value})
	prec := httptest.NewRecorder()
	protected.ServeHTTP(prec, req)
	if prec.Code != http.StatusUnauthorized {
		t.Fatalf("pre-auth value as session: %d", prec.Code)
	}
}

func TestRequireAuth_cookieAndCSRF(t *testing.T) {
	redisClient := testRedisClient(t)
	mgr := testSessionManager(redisClient)
	mw := auth.NewMiddleware(mgr)
	var gotUser string
	protected := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _, _ := auth.UserFromContext(r.Context())
		gotUser = id.String()
		w.WriteHeader(http.StatusNoContent)
	}))

	startRec := httptest.NewRecorder()
	ident, err := mgr.Start(startRec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil), testLoginUserID, "uk", false)
	if err != nil {
		t.Fatal(err)
	}
	cookie := startRec.Result().Cookies()[0]

	do := func(method string, csrf string, withCookie bool) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), method, "/x", nil)
		if withCookie {
			req.AddCookie(cookie)
		}
		if csrf != "" {
			req.Header.Set(session.CSRFHeader, csrf)
		}
		rec := httptest.NewRecorder()
		protected.ServeHTTP(rec, req)
		return rec
	}

	if rec := do(http.MethodGet, "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie: %d", rec.Code)
	}
	if rec := do(http.MethodGet, "", true); rec.Code != http.StatusNoContent || gotUser != testLoginUserID.String() {
		t.Fatalf("GET with cookie needs no CSRF token: %d user=%s", rec.Code, gotUser)
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := do(m, "", true)
		if rec.Code != http.StatusForbidden || decodeError(t, rec).Code != apperror.CodeCSRFInvalid {
			t.Fatalf("%s without token: %d %s", m, rec.Code, rec.Body.String())
		}
		if rec = do(m, "wrong", true); rec.Code != http.StatusForbidden {
			t.Fatalf("%s wrong token: %d", m, rec.Code)
		}
		if rec = do(m, ident.CSRFToken, true); rec.Code != http.StatusNoContent {
			t.Fatalf("%s right token: %d", m, rec.Code)
		}
	}
	// A bearer header is ignored entirely.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+cookie.Value)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bearer must not authenticate: %d", rec.Code)
	}
}

func TestLogoutRevokesSession(t *testing.T) {
	redisClient := testRedisClient(t)
	mgr := testSessionManager(redisClient)
	h := NewAuthHandler(nil, nil, redisClient, mgr, nil, nil)
	router := chi.NewRouter()
	router.Route("/auth", h.RegisterRoutes)

	startRec := httptest.NewRecorder()
	if _, err := mgr.Start(startRec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil), testLoginUserID, "uk", false); err != nil {
		t.Fatal(err)
	}
	cookie := startRec.Result().Cookies()[0]

	rec := sessPostJSON(t, router, "/auth/logout", `{}`, cookie)
	assertStatus(t, rec, http.StatusOK)
	if c := sessionCookieFrom(rec, "openhr_session"); c == nil || c.MaxAge >= 0 {
		t.Fatalf("session cookie must be cleared: %+v", c)
	}
	probe := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil)
	probe.AddCookie(cookie)
	if _, err := mgr.Authenticate(probe); err == nil {
		t.Fatal("session must be revoked server-side after logout")
	}
	// Idempotent without a session.
	assertStatus(t, sessPostJSON(t, router, "/auth/logout", `{}`), http.StatusOK)
}

func TestRefreshIsGone(t *testing.T) {
	h := NewAuthHandler(nil, nil, nil, nil, nil, nil)
	router := chi.NewRouter()
	router.Route("/auth", h.RegisterRoutes)
	rec := sessPostJSON(t, router, "/auth/refresh", `{}`)
	assertStatus(t, rec, http.StatusGone)
	if got := decodeError(t, rec).Code; got != apperror.CodeSessionRefreshRemoved {
		t.Fatalf("code = %q", got)
	}
}

func TestPasswordResetRevokesAllSessions(t *testing.T) {
	db, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := testRedisClient(t)
	mgr := testSessionManager(client)
	h := NewAuthHandler(dbrouter.NewWithPools(map[region.Region]dbrouter.PgxPool{region.RegionUK: db}), nil, client, mgr, nil, nil)

	var cookies []*http.Cookie
	for range 2 {
		rec := httptest.NewRecorder()
		if _, startErr := mgr.Start(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil), testLoginUserID, "uk", false); startErr != nil {
			t.Fatal(startErr)
		}
		cookies = append(cookies, rec.Result().Cookies()[0])
	}

	token, err := h.Resetter.CreateToken(t.Context(), auth.ResetSession{UserID: testLoginUserID, Email: testLoginEmail, Region: "uk"})
	if err != nil {
		t.Fatal(err)
	}
	oldHash, err := auth.HashPassword(testLoginPassword)
	if err != nil {
		t.Fatal(err)
	}
	sql, args, err := query.LookupAccountUserByID(testLoginUserID)
	if err != nil {
		t.Fatal(err)
	}
	verified := time.Now()
	db.ExpectQuery(sql).WithArgs(args...).WillReturnRows(userAccountRows(oldHash, &verified))
	updateSQL, _, err := query.UpdateAccountUserPassword(testLoginUserID, "hash")
	if err != nil {
		t.Fatal(err)
	}
	db.ExpectQuery(updateSQL).WithArgs(pgxmock.AnyArg(), testLoginUserID).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(testLoginUserID))

	body := `{"token":"` + token + `","password":"a-brand-new-password1"}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/auth/reset-password", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.resetPassword(rec, req)
	assertStatus(t, rec, http.StatusOK)

	for i, c := range cookies {
		probe := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil)
		probe.AddCookie(c)
		if _, authErr := mgr.Authenticate(probe); authErr == nil {
			t.Fatalf("session %d survived a password reset", i)
		}
	}
}
