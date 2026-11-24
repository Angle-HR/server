package identityhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
)

func TestAuthResetPassword_rejectsCurrentPasswordAndAllowsRetry(t *testing.T) {
	db, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := testRedisClient(t)
	h := NewAuthHandler(dbrouter.NewWithPools(map[region.Region]dbrouter.PgxPool{region.RegionUK: db}), nil, client, nil, nil, nil)
	token, err := h.Resetter.CreateToken(t.Context(), auth.ResetSession{UserID: testLoginUserID, Email: testLoginEmail, Region: "uk"})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword(testLoginPassword)
	if err != nil {
		t.Fatal(err)
	}
	sql, args, err := query.LookupAccountUserByID(testLoginUserID)
	if err != nil {
		t.Fatal(err)
	}
	verified := time.Now()
	for range 2 {
		db.ExpectQuery(sql).WithArgs(args...).WillReturnRows(userAccountRows(hash, &verified))
	}
	updateSQL, _, err := query.UpdateAccountUserPassword(testLoginUserID, "hash")
	if err != nil {
		t.Fatal(err)
	}
	db.ExpectQuery(updateSQL).WithArgs(pgxmock.AnyArg(), testLoginUserID).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(testLoginUserID))
	reset := func(password string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/auth/reset-password", strings.NewReader(`{"token":"`+token+`","password":"`+password+`"}`))
		rec := httptest.NewRecorder()
		h.resetPassword(rec, req)
		return rec
	}
	rec := reset(testLoginPassword)
	assertStatus(t, rec, http.StatusBadRequest)
	got := decodeError(t, rec)
	if got.Code != apperror.CodeValidationError || got.Message != "New password can’t be the same as your current one." {
		t.Fatalf("unexpected error: %+v", got)
	}
	if _, err := h.Resetter.LoadToken(t.Context(), token); err != nil {
		t.Fatalf("token should remain usable: %v", err)
	}
	assertStatus(t, reset("different-password1"), http.StatusOK)
	if _, err := h.Resetter.LoadToken(t.Context(), token); err == nil {
		t.Fatal("successful reset should consume token")
	}
	assertStatus(t, reset("another-password1"), http.StatusBadRequest)
	if err := db.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
