package handler

import (
	"encoding/json"
	"errors"
	"github.com/Angle-HR/server/internal/query"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/pkg/apperror"
)

func TestAuthSignup_invalidBody(t *testing.T) {
	t.Parallel()

	h := NewAuthHandler(nil, nil, nil, nil, nil, nil)
	router := chi.NewRouter()
	router.Route("/auth", h.RegisterRoutes)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/auth/signup", strings.NewReader(`{`))
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

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/onboarding/status", nil)
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

	h := NewProductOnboardingHandler(nil, nil, nil, nil)
	router := chi.NewRouter()
	router.Post("/onboarding/address/verify", h.verifyAddress)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/onboarding/address/verify", nil)
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

func TestAuthSignup_existingEmail(t *testing.T) {
	for _, state := range []string{"regional", "pending", "registry failure"} {
		for _, password := range []string{testLoginPassword, "different-password"} {
			t.Run(state+"/"+password, func(t *testing.T) {
				db, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				sql, args, err := query.LookupVerifiedRegistryEmail(testLoginEmail)
				if err != nil {
					t.Fatal(err)
				}
				expectation := db.ExpectQuery(sql).WithArgs(args...)
				wantStatus := http.StatusConflict
				switch state {
				case "regional":
					expectation.WillReturnRows(pgxmock.NewRows([]string{"user_id", "region"}).AddRow(uuid.New(), "uk"))
				case "pending":
					expectation.WillReturnError(pgx.ErrNoRows)
					sql, args, err = query.LookupPendingUserByEmail(testLoginEmail)
					if err != nil {
						t.Fatal(err)
					}
					db.ExpectQuery(sql).WithArgs(args...).WillReturnRows(userAccountRows("existing-password-hash", nil))
				case "registry failure":
					expectation.WillReturnError(errors.New("database unavailable"))
					wantStatus = http.StatusInternalServerError
				}
				h := NewAuthHandler(nil, db, nil, nil, nil, nil)
				body, err := json.Marshal(authSignupBody{Email: "JERRY@example.com", Password: password})
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/auth/signup", strings.NewReader(string(body)))
				rec := httptest.NewRecorder()
				h.signup(rec, req)
				if rec.Code != wantStatus {
					t.Fatalf("status: got %d want %d: %s", rec.Code, wantStatus, rec.Body.String())
				}
				if wantStatus == http.StatusConflict {
					got := decodeError(t, rec)
					if got.Code != apperror.CodeEmailAlreadyRegistered {
						t.Fatalf("code: got %q", got.Code)
					}
					if got.Message != "An account with this email already exists. Sign in instead." {
						t.Fatalf("message: got %q", got.Message)
					}
				}
				if err := db.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
