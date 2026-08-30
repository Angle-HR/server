package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	goredis "github.com/redis/go-redis/v9"

	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

const (
	testLoginEmail    = "jerry@example.com"
	testLoginPassword = "secure-password-here"
)

var testLoginUserID = uuid.MustParse("11111111-1111-4111-8111-111111111111")

func TestAuthLogin_wrongPassword(t *testing.T) {
	globalMock, regionalMock := expectUnverifiedLoginMocks(t, nil)
	router := testAuthRouter(t, regionalMock, globalMock, testRedisClient(t))

	rec := postAuthLogin(t, router, testLoginEmail, "wrong-password")
	assertStatus(t, rec, http.StatusUnauthorized)

	errBody := decodeError(t, rec)
	if errBody.Code != apperror.CodeUnauthorized {
		t.Fatalf("code: got %q want %q", errBody.Code, apperror.CodeUnauthorized)
	}

	assertMocksMet(t, globalMock, regionalMock)
}

func TestAuthLogin_unverifiedEmail(t *testing.T) {
	globalMock, regionalMock := expectUnverifiedLoginMocks(t, nil)
	router := testAuthRouter(t, regionalMock, globalMock, testRedisClient(t))

	rec := postAuthLogin(t, router, testLoginEmail, testLoginPassword)
	assertStatus(t, rec, http.StatusForbidden)

	errBody := decodeError(t, rec)
	if errBody.Code != apperror.CodeEmailNotVerified {
		t.Fatalf("code: got %q want %q", errBody.Code, apperror.CodeEmailNotVerified)
	}
	if errBody.Message != apperror.MsgEmailNotVerified {
		t.Fatalf("message: got %q", errBody.Message)
	}
	if errBody.Details["verification_session_id"] == "" {
		t.Fatal("expected verification_session_id in details")
	}
	if errBody.Details["email"] != testLoginEmail {
		t.Fatalf("email detail: got %v", errBody.Details["email"])
	}

	assertMocksMet(t, globalMock, regionalMock)
}

func TestAuthLogin_verifiedUser(t *testing.T) {
	verifiedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	globalMock, regionalMock := expectVerifiedLoginMocks(t, &verifiedAt)

	progressSQL, progressArgs, err := query.LookupOnboardingProgress(testLoginUserID)
	if err != nil {
		t.Fatalf("LookupOnboardingProgress: %v", err)
	}
	regionalMock.ExpectQuery(progressSQL).WithArgs(progressArgs...).WillReturnError(pgx.ErrNoRows)

	router := testAuthRouter(t, regionalMock, globalMock, testRedisClient(t))
	rec := postAuthLogin(t, router, testLoginEmail, testLoginPassword)
	assertStatus(t, rec, http.StatusOK)

	var envelope response.Envelope
	if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	raw, err := json.Marshal(envelope.Data)
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}
	var data AuthTokenData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("unmarshal token data: %v", err)
	}
	if data.AccessToken == "" {
		t.Fatal("expected access_token")
	}
	if data.RefreshToken == "" {
		t.Fatal("expected refresh_token")
	}

	assertMocksMet(t, globalMock, regionalMock)
}

func TestAuthLogin_unverifiedRateLimited(t *testing.T) {
	globalMock, regionalMock := expectUnverifiedLoginMocks(t, nil)
	redisClient := testRedisClient(t)
	store := auth.NewVerificationStore(redisClient)
	if err := store.MarkResentByEmail(t.Context(), testLoginEmail); err != nil {
		t.Fatalf("MarkResentByEmail: %v", err)
	}

	router := testAuthRouter(t, regionalMock, globalMock, redisClient)
	rec := postAuthLogin(t, router, testLoginEmail, testLoginPassword)
	assertStatus(t, rec, http.StatusTooManyRequests)

	errBody := decodeError(t, rec)
	if errBody.Code != apperror.CodeVerificationRateLimited {
		t.Fatalf("code: got %q want %q", errBody.Code, apperror.CodeVerificationRateLimited)
	}

	assertMocksMet(t, globalMock, regionalMock)
}

func expectUnverifiedLoginMocks(t *testing.T, emailVerifiedAt *time.Time) (pgxmock.PgxPoolIface, pgxmock.PgxPoolIface) {
	t.Helper()

	passwordHash, err := auth.HashPassword(testLoginPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	globalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool global: %v", err)
	}
	t.Cleanup(func() { globalMock.Close() })

	registrySQL, registryArgs, err := query.LookupUsersRegistryByEmail(testLoginEmail)
	if err != nil {
		t.Fatalf("LookupUsersRegistryByEmail: %v", err)
	}
	globalMock.ExpectQuery(registrySQL).WithArgs(registryArgs...).WillReturnError(pgx.ErrNoRows)

	regionalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool regional: %v", err)
	}
	t.Cleanup(func() { regionalMock.Close() })

	emailSQL, emailArgs, err := query.LookupAccountUserByEmail(testLoginEmail)
	if err != nil {
		t.Fatalf("LookupAccountUserByEmail: %v", err)
	}
	regionalMock.ExpectQuery(emailSQL).WithArgs(emailArgs...).WillReturnRows(
		userAccountRows(passwordHash, emailVerifiedAt),
	)

	idSQL, idArgs, err := query.LookupAccountUserByID(testLoginUserID)
	if err != nil {
		t.Fatalf("LookupAccountUserByID: %v", err)
	}
	regionalMock.ExpectQuery(idSQL).WithArgs(idArgs...).WillReturnRows(
		userAccountRows(passwordHash, emailVerifiedAt),
	)

	return globalMock, regionalMock
}

func expectVerifiedLoginMocks(t *testing.T, emailVerifiedAt *time.Time) (pgxmock.PgxPoolIface, pgxmock.PgxPoolIface) {
	t.Helper()

	passwordHash, err := auth.HashPassword(testLoginPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	globalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool global: %v", err)
	}
	t.Cleanup(func() { globalMock.Close() })

	registrySQL, registryArgs, err := query.LookupUsersRegistryByEmail(testLoginEmail)
	if err != nil {
		t.Fatalf("LookupUsersRegistryByEmail: %v", err)
	}
	globalMock.ExpectQuery(registrySQL).WithArgs(registryArgs...).WillReturnRows(
		pgxmock.NewRows([]string{"id", "email", "region", "user_id"}).
			AddRow(uuid.New(), testLoginEmail, "uk", &testLoginUserID),
	)

	regionalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool regional: %v", err)
	}
	t.Cleanup(func() { regionalMock.Close() })

	userSQL, userArgs, err := query.LookupAccountUserByID(testLoginUserID)
	if err != nil {
		t.Fatalf("LookupAccountUserByID: %v", err)
	}
	rows := userAccountRows(passwordHash, emailVerifiedAt)
	regionalMock.ExpectQuery(userSQL).WithArgs(userArgs...).WillReturnRows(rows)
	regionalMock.ExpectQuery(userSQL).WithArgs(userArgs...).WillReturnRows(rows)

	return globalMock, regionalMock
}

func userAccountRows(passwordHash string, emailVerifiedAt *time.Time) *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "email", "password_hash", "email_verified_at", "onboarding_completed_at",
		"account_type", "first_name", "last_name", "legal_full_name", "country_id",
		"totp_secret", "totp_enabled_at",
	}).AddRow(
		testLoginUserID, testLoginEmail, passwordHash, emailVerifiedAt, nil,
		nil, nil, nil, nil, nil,
		nil, nil,
	)
}

func testAuthRouter(
	t *testing.T,
	regionalMock pgxmock.PgxPoolIface,
	globalMock pgxmock.PgxPoolIface,
	redisClient *goredis.Client,
) chi.Router {
	t.Helper()

	crypto, err := auth.NewTOTPCrypto("secret")
	if err != nil {
		t.Fatalf("NewTOTPCrypto: %v", err)
	}

	h := NewAuthHandler(
		dbrouter.NewWithPools(map[region.Region]dbrouter.PgxPool{
			region.RegionUK: regionalMock,
		}),
		globalMock,
		redisClient,
		mustTestTokenService(t),
		nil,
		region.RegionUK,
		crypto,
	)

	router := chi.NewRouter()
	router.Route("/api/v1/auth", func(r chi.Router) {
		h.RegisterRoutes(r)
	})

	return router
}

func mustTestTokenService(t *testing.T) *auth.TokenService {
	t.Helper()

	tokens, err := auth.NewTokenService("secret", time.Hour, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("NewTokenService: %v", err)
	}

	return tokens
}

func testRedisClient(t *testing.T) *goredis.Client {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)

	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return client
}

func postAuthLogin(t *testing.T, router chi.Router, email, password string) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(map[string]string{
		"email":    email,
		"password": password,
	})
	if err != nil {
		t.Fatalf("marshal login body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}
