package region

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/oschwald/geoip2-golang"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/Angle-HR/server/pkg/apperror"
)

const testJWTSecret = "test-secret"

func TestValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		region Region
		want   bool
	}{
		{RegionUK, true},
		{RegionUS, true},
		{RegionAfrica, true},
		{RegionEU, true},
		{RegionAsia, true},
		{RegionUnknown, false},
		{Region("invalid"), false},
		{Region("UK"), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.region), func(t *testing.T) {
			t.Parallel()
			if got := Valid(tt.region); got != tt.want {
				t.Fatalf("Valid(%q): got %v, want %v", tt.region, got, tt.want)
			}
		})
	}
}

func TestResolveJWT(t *testing.T) {
	t.Parallel()

	resolver := NewRegionResolver(nil, nil, []byte(testJWTSecret))

	token := signJWT(t, jwtClaims{
		Region: "uk",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: uuid.NewString(),
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	region, source, err := resolver.Resolve(req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if region != RegionUK || source != sourceJWT {
		t.Fatalf("got region=%q source=%q, want uk/jwt", region, source)
	}
}

func TestResolveSubdomain(t *testing.T) {
	t.Parallel()

	resolver := NewRegionResolver(nil, nil, nil)
	resolver.BaseDomain = "anglehr.com"

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "us.anglehr.com"

	region, source, err := resolver.Resolve(req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if region != RegionUS || source != sourceSubdomain {
		t.Fatalf("got region=%q source=%q, want us/subdomain", region, source)
	}
}

func TestResolveDB(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(
		`SELECT region FROM users_registry WHERE id = $1::uuid`,
	).WithArgs(userID).WillReturnRows(
		pgxmock.NewRows([]string{"region"}).AddRow("africa"),
	)

	resolver := NewRegionResolver(mock, nil, []byte(testJWTSecret))
	token := signJWT(t, jwtClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: userID.String(),
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	region, source, err := resolver.Resolve(req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if region != RegionAfrica || source != sourceDB {
		t.Fatalf("got region=%q source=%q, want africa/db", region, source)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestResolveParamQuery(t *testing.T) {
	t.Parallel()

	resolver := NewRegionResolver(nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/?region=eu", nil)

	region, source, err := resolver.Resolve(req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if region != RegionEU || source != sourceParam {
		t.Fatalf("got region=%q source=%q, want eu/param", region, source)
	}
}

func TestResolveParamJSON(t *testing.T) {
	t.Parallel()

	resolver := NewRegionResolver(nil, nil, nil)
	body := []byte(`{"region":"us","email":"a@example.com"}`)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	region, source, err := resolver.Resolve(req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if region != RegionUS || source != sourceParam {
		t.Fatalf("got region=%q source=%q, want us/param", region, source)
	}

	reread, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("reread body: %v", err)
	}
	if !bytes.Equal(reread, body) {
		t.Fatalf("body not restored for downstream handlers")
	}
}

func TestResolveGeo(t *testing.T) {
	t.Parallel()

	geoDB := openTestGeoDB(t)
	resolver := NewRegionResolver(nil, geoDB, nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "81.2.69.142:12345"

	region, source, err := resolver.Resolve(req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if region != RegionUK || source != sourceGeo {
		t.Fatalf("got region=%q source=%q, want uk/geo", region, source)
	}
}

func TestResolveUnknownRegionParam(t *testing.T) {
	t.Parallel()

	resolver := NewRegionResolver(nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/?region=antarctica", nil)

	_, _, err := resolver.Resolve(req)
	if !errors.Is(err, ErrInvalidRegion) {
		t.Fatalf("got err %v, want ErrInvalidRegion", err)
	}
}

func TestResolveUnresolved(t *testing.T) {
	t.Parallel()

	resolver := NewRegionResolver(nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	_, _, err := resolver.Resolve(req)
	if !errors.Is(err, ErrUnresolved) {
		t.Fatalf("got err %v, want ErrUnresolved", err)
	}
}

func TestResolvePriorityJWTOverSubdomain(t *testing.T) {
	t.Parallel()

	resolver := NewRegionResolver(nil, nil, []byte(testJWTSecret))
	token := signJWT(t, jwtClaims{Region: "eu"})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "uk.anglehr.com"
	req.Header.Set("Authorization", "Bearer "+token)

	region, source, err := resolver.Resolve(req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if region != RegionEU || source != sourceJWT {
		t.Fatalf("got region=%q source=%q, want eu/jwt", region, source)
	}
}

func TestMiddlewareUnresolvedReturns400(t *testing.T) {
	t.Parallel()

	resolver := NewRegionResolver(nil, nil, nil)
	handler := resolver.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not run")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/industries", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Error.Code != apperror.CodeValidationError {
		t.Fatalf("code: got %q, want %q", payload.Error.Code, apperror.CodeValidationError)
	}
}

func TestMiddlewareSetsContext(t *testing.T) {
	t.Parallel()

	resolver := NewRegionResolver(nil, nil, nil)
	var gotRegion Region
	var gotSource string

	handler := resolver.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ok bool
		gotRegion, ok = GetRegion(r.Context())
		if !ok {
			t.Fatal("region not on context")
		}
		gotSource, ok = GetRegionSource(r.Context())
		if !ok {
			t.Fatal("region source not on context")
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/?region=uk", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if gotRegion != RegionUK || gotSource != sourceParam {
		t.Fatalf("context: region=%q source=%q", gotRegion, gotSource)
	}
}

func TestMustGetRegionPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()

	_ = MustGetRegion(context.Background())
}

func TestMiddlewareChiStack(t *testing.T) {
	t.Parallel()

	resolver := NewRegionResolver(nil, nil, nil)
	router := chi.NewRouter()
	router.Route("/api/v1", func(r chi.Router) {
		r.Use(resolver.Middleware())
		r.Get("/ping", func(w http.ResponseWriter, r *http.Request) {
			region, ok := GetRegion(r.Context())
			if !ok || region != RegionUS {
				http.Error(w, "missing region", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ping?region=us", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
}

func signJWT(t *testing.T, claims jwtClaims) string {
	t.Helper()

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}

	return signed
}

func openTestGeoDB(t *testing.T) *geoip2.Reader {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}

	path := filepath.Join(filepath.Dir(file), "testdata", "GeoLite2-Country-Test.mmdb")
	db, err := geoip2.Open(path)
	if err != nil {
		t.Fatalf("open test geodb: %v", err)
	}

	t.Cleanup(func() { db.Close() })

	return db
}
