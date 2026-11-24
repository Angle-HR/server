package identityhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/pquerna/otp/totp"

	"github.com/Angle-HR/server/internal/admin"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/session"
	"github.com/Angle-HR/server/pkg/apperror"
)

const (
	adminTOTPSecret = "JBSWY3DPEHPK3PXP"
	adminTOTPEmail  = "root@example.com"
)

var (
	adminTOTPUserID  = uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	adminTOTPOtherID = uuid.MustParse("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
)

type fakeAdminStore struct{ perms []string }

func (f fakeAdminStore) GetAdminPrincipal(_ context.Context, id uuid.UUID) (auth.AdminPrincipal, error) {
	return auth.AdminPrincipal{ID: id, Email: adminTOTPEmail, IsActive: true}, nil
}

func (f fakeAdminStore) ListAdminPermissions(context.Context, uuid.UUID) ([]string, error) {
	return f.perms, nil
}

type adminTOTPEnv struct {
	router http.Handler
	mock   pgxmock.PgxPoolIface
	crypto *auth.TOTPCrypto
	h      *AdminHandler
}

func newAdminTOTPEnv(t *testing.T, required bool) adminTOTPEnv {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mock.Close)
	rdb := testRedisClient(t)
	crypto, err := auth.NewTOTPCrypto("admin-totp-test-key")
	if err != nil {
		t.Fatal(err)
	}
	h := newAdminTOTPHandler(mock, rdb)
	h.UseTOTP(crypto, required)
	mw := auth.NewAdminMiddleware(h.Sessions, fakeAdminStore{perms: []string{admin.PermAdminsWrite}})
	mw.RequireMFA = required

	r := chi.NewRouter()
	r.Route("/api/v1/admin", func(r chi.Router) {
		r.Post("/auth/login", h.login)
		r.Post("/auth/login/totp", h.loginTOTP)
		r.Group(func(r chi.Router) {
			r.Use(mw.RequireAdmin)
			r.Get("/auth/me", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
			r.Get("/users", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
			r.Post("/auth/totp/enroll", h.totpEnroll)
			r.Post("/auth/totp/confirm", h.totpConfirm)
			r.Post("/auth/totp/disable", h.totpDisable)
			r.With(mw.RequirePermission(admin.PermAdminsWrite)).Post("/staff/{id}/totp/reset", h.resetStaffTOTP)
		})
	})
	return adminTOTPEnv{router: r, mock: mock, crypto: crypto, h: h}
}

func newAdminTOTPHandler(mock pgxmock.PgxPoolIface, rdb *goredis.Client) *AdminHandler {
	return &AdminHandler{
		Store:    admin.NewStore(mock),
		Sessions: session.NewManager(rdb, session.ManagerConfig{Kind: session.KindAdmin, Secure: false}),
		PasswordLockout: auth.NewLoginLockout(rdb, "admin_pwd",
			adminPasswordLockoutMaxAttempts, adminPasswordLockoutWindow, adminPasswordLockoutDuration),
		TOTPLockout: auth.NewLoginLockout(rdb, "admin_totp",
			adminTOTPLockoutMaxAttempts, adminTOTPLockoutWindow, adminTOTPLockoutDuration),
		validate: validator.New(),
	}
}

var adminUserCols = []string{"id", "email", "name", "is_active", "created_at", "updated_at", "password_hash", "totp_secret", "totp_enabled_at"}

// expectAdminUser queues one admin.users read (by email or id); secret "" means no TOTP state.
func (e adminTOTPEnv) expectAdminUser(t *testing.T, id uuid.UUID, byEmail bool, passwordHash, secret string, enabled bool) {
	t.Helper()
	var enc *string
	var at *time.Time
	if secret != "" {
		s, err := e.crypto.Encrypt(secret)
		if err != nil {
			t.Fatal(err)
		}
		enc = &s
		if enabled {
			now := time.Now()
			at = &now
		}
	}
	now := time.Now()
	rows := pgxmock.NewRows(adminUserCols).AddRow(id, adminTOTPEmail, "Root", true, now, now, &passwordHash, enc, at)
	if byEmail {
		e.mock.ExpectQuery("WHERE email").WithArgs(adminTOTPEmail).WillReturnRows(rows)
		return
	}
	e.mock.ExpectQuery("WHERE id").WithArgs(id).WillReturnRows(rows)
}

func (e adminTOTPEnv) expectAudit() {
	e.mock.ExpectExec("INSERT INTO admin.audit_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
}

func (e adminTOTPEnv) do(t *testing.T, method, path, body, csrf string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, "/api/v1/admin"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func adminLoginBodyJSON(password string) string {
	return `{"email":"` + adminTOTPEmail + `","password":"` + password + `"}`
}

func dataOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	return env.Data
}

func TestAdminTOTPLoginFlow(t *testing.T) {
	e := newAdminTOTPEnv(t, false)
	hash, err := auth.HashPassword("correct-horse-1")
	if err != nil {
		t.Fatal(err)
	}

	// Password right, TOTP on: pre-auth cookie only, no session, no CSRF token.
	e.expectAdminUser(t, adminTOTPUserID, true, hash, adminTOTPSecret, true)
	rec := e.do(t, http.MethodPost, "/auth/login", adminLoginBodyJSON("correct-horse-1"), "")
	assertStatus(t, rec, http.StatusOK)
	d := dataOf(t, rec)
	if d["totp_required"] != true || d["expires_in"] != float64(300) || d["csrf_token"] != nil {
		t.Fatalf("unexpected login body: %v", d)
	}
	if sessionCookieFrom(rec, "openhr_admin_session") != nil {
		t.Fatal("no admin session may exist before the code is verified")
	}
	pre := sessionCookieFrom(rec, "openhr_admin_preauth")
	if pre == nil || pre.Value == "" || !pre.HttpOnly || pre.MaxAge != 300 {
		t.Fatalf("admin pre-auth cookie: %+v", pre)
	}

	// The product pre-auth cookie name is not accepted, and a pre-auth value is never a session.
	rec = e.do(t, http.MethodPost, "/auth/login/totp", `{"code":"123456"}`, "",
		&http.Cookie{Name: "openhr_preauth", Value: pre.Value})
	assertStatus(t, rec, http.StatusUnauthorized)
	rec = e.do(t, http.MethodGet, "/users", "", "", &http.Cookie{Name: "openhr_admin_session", Value: pre.Value})
	assertStatus(t, rec, http.StatusUnauthorized)

	// Wrong code: 400 and an audit row; pre-auth stays valid.
	e.expectAdminUser(t, adminTOTPUserID, false, hash, adminTOTPSecret, true)
	e.expectAudit()
	rec = e.do(t, http.MethodPost, "/auth/login/totp", `{"code":"000000"}`, "", pre)
	assertStatus(t, rec, http.StatusBadRequest)
	if got := decodeError(t, rec); got.Code != apperror.CodeInvalidVerificationCode {
		t.Fatalf("wrong code: %+v", got)
	}

	// Right code: session issued, pre-auth cleared, CSRF returned.
	code, err := totp.GenerateCode(adminTOTPSecret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.expectAdminUser(t, adminTOTPUserID, false, hash, adminTOTPSecret, true)
	e.expectAudit()
	rec = e.do(t, http.MethodPost, "/auth/login/totp", `{"code":"`+code+`"}`, "", pre)
	assertStatus(t, rec, http.StatusOK)
	full := sessionCookieFrom(rec, "openhr_admin_session")
	if full == nil || full.Value == "" || full.Value == pre.Value {
		t.Fatalf("session cookie: %+v", full)
	}
	if dataOf(t, rec)["csrf_token"] == "" {
		t.Fatalf("no csrf_token: %s", rec.Body.String())
	}
	if c := sessionCookieFrom(rec, "openhr_admin_preauth"); c == nil || c.MaxAge >= 0 {
		t.Fatalf("pre-auth cookie must be cleared: %+v", c)
	}
	assertStatus(t, e.do(t, http.MethodGet, "/users", "", "", full), http.StatusNoContent)

	// Single use.
	rec = e.do(t, http.MethodPost, "/auth/login/totp", `{"code":"`+code+`"}`, "", pre)
	assertStatus(t, rec, http.StatusUnauthorized)
	if err := e.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminTOTPLockout(t *testing.T) {
	e := newAdminTOTPEnv(t, false)
	hash, _ := auth.HashPassword("correct-horse-1")
	e.expectAdminUser(t, adminTOTPUserID, true, hash, adminTOTPSecret, true)
	rec := e.do(t, http.MethodPost, "/auth/login", adminLoginBodyJSON("correct-horse-1"), "")
	pre := sessionCookieFrom(rec, "openhr_admin_preauth")
	if pre == nil {
		t.Fatalf("no pre-auth: %s", rec.Body.String())
	}
	for range adminTOTPLockoutMaxAttempts {
		e.expectAdminUser(t, adminTOTPUserID, false, hash, adminTOTPSecret, true)
		e.expectAudit()
		assertStatus(t, e.do(t, http.MethodPost, "/auth/login/totp", `{"code":"000000"}`, "", pre), http.StatusBadRequest)
	}
	// Locked: even the right code is refused, and nothing is audited as a guess.
	code, _ := totp.GenerateCode(adminTOTPSecret, time.Now())
	e.expectAdminUser(t, adminTOTPUserID, false, hash, adminTOTPSecret, true)
	rec = e.do(t, http.MethodPost, "/auth/login/totp", `{"code":"`+code+`"}`, "", pre)
	assertStatus(t, rec, http.StatusTooManyRequests)
	if got := decodeError(t, rec); got.Code != apperror.CodeTooManyAttempts {
		t.Fatalf("lockout code: %+v", got)
	}
	if sessionCookieFrom(rec, "openhr_admin_session") != nil {
		t.Fatal("no session while locked")
	}
}

func TestAdminTOTPRequiredForcesEnrolment(t *testing.T) {
	e := newAdminTOTPEnv(t, true)
	hash, _ := auth.HashPassword("correct-horse-1")

	// No TOTP yet: a session is issued but flagged, and it can do nothing except enrol.
	e.expectAdminUser(t, adminTOTPUserID, true, hash, "", false)
	rec := e.do(t, http.MethodPost, "/auth/login", adminLoginBodyJSON("correct-horse-1"), "")
	assertStatus(t, rec, http.StatusOK)
	d := dataOf(t, rec)
	sess := sessionCookieFrom(rec, "openhr_admin_session")
	csrf, _ := d["csrf_token"].(string)
	if d["totp_enrollment_required"] != true || sess == nil || csrf == "" {
		t.Fatalf("login body: %v cookie %+v", d, sess)
	}
	rec = e.do(t, http.MethodGet, "/users", "", "", sess)
	assertStatus(t, rec, http.StatusForbidden)
	if got := decodeError(t, rec); got.Code != apperror.CodeTOTPEnrollmentRequired {
		t.Fatalf("gate: %+v", got)
	}
	assertStatus(t, e.do(t, http.MethodGet, "/auth/me", "", "", sess), http.StatusNoContent)

	// Required means it cannot be switched off either.
	rec = e.do(t, http.MethodPost, "/auth/totp/disable", `{"code":"123456","password":"correct-horse-1"}`, csrf, sess)
	assertStatus(t, rec, http.StatusForbidden)

	// Enrol: secret returned and stored encrypted.
	e.expectAdminUser(t, adminTOTPUserID, false, hash, "", false)
	e.mock.ExpectExec("UPDATE admin.users SET totp_secret").
		WithArgs(adminTOTPUserID, pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	e.expectAudit()
	rec = e.do(t, http.MethodPost, "/auth/totp/enroll", "", csrf, sess)
	assertStatus(t, rec, http.StatusOK)
	secret, _ := dataOf(t, rec)["secret"].(string)
	if secret == "" || !strings.HasPrefix(dataOf2(rec), "otpauth://") {
		t.Fatalf("enroll body: %s", rec.Body.String())
	}

	// Confirm with a wrong code first, then the right one: the session is replaced and the gate opens.
	e.expectAdminUser(t, adminTOTPUserID, false, hash, secret, false)
	e.expectAudit()
	assertStatus(t, e.do(t, http.MethodPost, "/auth/totp/confirm", `{"code":"000000"}`, csrf, sess), http.StatusBadRequest)
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.expectAdminUser(t, adminTOTPUserID, false, hash, secret, false)
	e.mock.ExpectExec("UPDATE admin.users SET totp_enabled_at").
		WithArgs(adminTOTPUserID).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	e.expectAudit()
	rec = e.do(t, http.MethodPost, "/auth/totp/confirm", `{"code":"`+code+`"}`, csrf, sess)
	assertStatus(t, rec, http.StatusOK)
	next := sessionCookieFrom(rec, "openhr_admin_session")
	if next == nil || next.Value == sess.Value || dataOf(t, rec)["csrf_token"] == "" {
		t.Fatalf("confirm must rotate the session: %+v %s", next, rec.Body.String())
	}
	assertStatus(t, e.do(t, http.MethodGet, "/users", "", "", sess), http.StatusUnauthorized)
	assertStatus(t, e.do(t, http.MethodGet, "/users", "", "", next), http.StatusNoContent)
	if err := e.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func dataOf2(rec *httptest.ResponseRecorder) string {
	var env struct {
		Data struct {
			URL string `json:"otpauth_url"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return env.Data.URL
}

func TestAdminTOTPResetNeedsSuperadmin(t *testing.T) {
	e := newAdminTOTPEnv(t, false)
	login := func() (*http.Cookie, string) {
		hash, _ := auth.HashPassword("correct-horse-1")
		e.expectAdminUser(t, adminTOTPUserID, true, hash, "", false)
		rec := e.do(t, http.MethodPost, "/auth/login", adminLoginBodyJSON("correct-horse-1"), "")
		assertStatus(t, rec, http.StatusOK)
		csrf, _ := dataOf(t, rec)["csrf_token"].(string)
		return sessionCookieFrom(rec, "openhr_admin_session"), csrf
	}
	sess, csrf := login()
	path := "/staff/" + adminTOTPOtherID.String() + "/totp/reset"

	// Has admins:write but not the superadmin role: refused.
	e.mock.ExpectQuery("FROM admin.user_roles").WithArgs(adminTOTPUserID).WillReturnRows(pgxmock.NewRows([]string{"slug"}).AddRow("support"))
	assertStatus(t, e.do(t, http.MethodPost, path, "", csrf, sess), http.StatusForbidden)

	// Superadmin resetting someone else: cleared, audited.
	e.mock.ExpectQuery("FROM admin.user_roles").WithArgs(adminTOTPUserID).WillReturnRows(pgxmock.NewRows([]string{"slug"}).AddRow("superadmin"))
	hash, _ := auth.HashPassword("x-correct-horse")
	e.expectAdminUser(t, adminTOTPOtherID, false, hash, adminTOTPSecret, true)
	e.mock.ExpectExec("UPDATE admin.users SET totp_secret = NULL").
		WithArgs(adminTOTPOtherID).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	e.expectAudit()
	rec := e.do(t, http.MethodPost, path, "", csrf, sess)
	assertStatus(t, rec, http.StatusOK)

	// Resetting yourself goes through disable instead.
	e.mock.ExpectQuery("FROM admin.user_roles").WithArgs(adminTOTPUserID).WillReturnRows(pgxmock.NewRows([]string{"slug"}).AddRow("superadmin"))
	assertStatus(t, e.do(t, http.MethodPost, "/staff/"+adminTOTPUserID.String()+"/totp/reset", "", csrf, sess), http.StatusForbidden)
	if err := e.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
