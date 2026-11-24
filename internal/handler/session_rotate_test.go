package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"

	"github.com/Angle-HR/server/internal/session"
)

var testRotateUserID = uuid.MustParse("11111111-1111-4111-8111-111111111111")

func rotateRedis(t *testing.T) *goredis.Client {
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

func rotateCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestOnboardingRotatesSessionOnRegionChange(t *testing.T) {
	redisClient := rotateRedis(t)
	mgr := session.NewManager(redisClient, session.ManagerConfig{Kind: session.KindProduct, Secure: false})

	startRec := httptest.NewRecorder()
	first, err := mgr.Start(startRec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil), testRotateUserID, "global", false)
	if err != nil {
		t.Fatal(err)
	}
	old := startRec.Result().Cookies()[0]

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/onboarding/profile", bytes.NewReader(nil))
	req.AddCookie(old)
	rec := httptest.NewRecorder()
	marker := &RegionReissue{Region: "uk"}
	if rotateErr := rotateIfMarked(rec, req, mgr, &marker); rotateErr != nil {
		t.Fatal(rotateErr)
	}
	if marker.CSRFToken == "" || marker.CSRFToken == first.CSRFToken {
		t.Fatal("rotation must hand out a new CSRF token")
	}
	next := rotateCookie(rec, "openhr_session")
	if next == nil || next.Value == old.Value {
		t.Fatalf("expected a new session cookie: %+v", next)
	}
	probe := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil)
	probe.AddCookie(old)
	if _, err = mgr.Authenticate(probe); err == nil {
		t.Fatal("old session id must be dead after rotation")
	}
	probe2 := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil)
	probe2.AddCookie(next)
	got, err := mgr.Authenticate(probe2)
	if err != nil || got.Region != "uk" {
		t.Fatalf("rotated session: %+v %v", got, err)
	}
}
