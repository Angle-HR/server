package region

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oschwald/geoip2-golang"
)

const (
	sourceJWT       = "jwt"
	sourceSubdomain = "subdomain"
	sourceDB        = "db"
	sourceParam     = "param"
	sourceGeo       = "geo"

	defaultBaseDomain = "anglehr.com"
)

// ErrUnresolved indicates no resolution path produced a valid region.
var ErrUnresolved = errors.New("region: could not be resolved")

// ErrInvalidRegion indicates an explicit region value was present but invalid.
var ErrInvalidRegion = errors.New("region: invalid value")

type jwtClaims struct {
	Region string `json:"region"`
	jwt.RegisteredClaims
}

type registryQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// RegionResolver determines the geographic region for an HTTP request.
type RegionResolver struct {
	GlobalDB   registryQuerier
	GeoDB      *geoip2.Reader
	JWTSecret  []byte
	BaseDomain string
}

// NewRegionResolver returns a configured RegionResolver.
func NewRegionResolver(globalDB registryQuerier, geoDB *geoip2.Reader, secret []byte) *RegionResolver {
	return &RegionResolver{
		GlobalDB:   globalDB,
		GeoDB:      geoDB,
		JWTSecret:  secret,
		BaseDomain: defaultBaseDomain,
	}
}

// Resolve determines the region for r using the priority-ordered resolution chain.
// It returns region, source (jwt|subdomain|db|param|geo), and an error.
func (res *RegionResolver) Resolve(r *http.Request) (Region, string, error) {
	if res == nil {
		return RegionUnknown, "", ErrUnresolved
	}

	claims := res.parseJWT(r)

	if region, ok := regionFromJWT(claims); ok {
		return region, sourceJWT, nil
	}

	if region, ok := res.regionFromSubdomain(r.Host); ok {
		return region, sourceSubdomain, nil
	}

	if region, err := res.regionFromDB(r.Context(), claims); err != nil {
		return RegionUnknown, "", err
	} else if ok := Valid(region); ok {
		return region, sourceDB, nil
	}

	if region, err := res.regionFromParam(r); err != nil {
		return RegionUnknown, "", err
	} else if ok := Valid(region); ok {
		return region, sourceParam, nil
	}

	if region, ok := lookupRegionFromIP(res.GeoDB, clientIP(r)); ok {
		return region, sourceGeo, nil
	}

	return RegionUnknown, "", ErrUnresolved
}

func (res *RegionResolver) parseJWT(r *http.Request) *jwtClaims {
	if len(res.JWTSecret) == 0 {
		return nil
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return nil
	}

	tokenStr := strings.TrimSpace(parts[1])
	claims := &jwtClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}

		return res.JWTSecret, nil
	})
	if err != nil || !token.Valid {
		return nil
	}

	return claims
}

func regionFromJWT(claims *jwtClaims) (Region, bool) {
	if claims == nil || claims.Region == "" {
		return RegionUnknown, false
	}

	region := Region(claims.Region)
	if !Valid(region) {
		return RegionUnknown, false
	}

	return region, true
}

func (res *RegionResolver) regionFromSubdomain(host string) (Region, bool) {
	baseDomain := res.BaseDomain
	if baseDomain == "" {
		baseDomain = defaultBaseDomain
	}

	host = strings.TrimSpace(host)
	if host == "" {
		return RegionUnknown, false
	}

	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	host = strings.ToLower(host)
	labels := strings.Split(host, ".")
	if len(labels) < 3 {
		return RegionUnknown, false
	}

	subdomain := labels[0]
	domain := strings.Join(labels[1:], ".")
	if domain != baseDomain {
		return RegionUnknown, false
	}

	region := Region(subdomain)
	if !Valid(region) {
		return RegionUnknown, false
	}

	return region, true
}

func (res *RegionResolver) regionFromDB(ctx context.Context, claims *jwtClaims) (Region, error) {
	if res.GlobalDB == nil || claims == nil || claims.Subject == "" {
		return RegionUnknown, nil
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return RegionUnknown, nil
	}

	var region string
	err = res.GlobalDB.QueryRow(ctx,
		`SELECT region FROM users_registry WHERE id = $1::uuid`,
		userID,
	).Scan(&region)
	if errors.Is(err, pgx.ErrNoRows) {
		return RegionUnknown, nil
	}
	if err != nil {
		return RegionUnknown, fmt.Errorf("region db lookup: %w", err)
	}

	r := Region(region)
	if !Valid(r) {
		return RegionUnknown, nil
	}

	return r, nil
}

func (res *RegionResolver) regionFromParam(r *http.Request) (Region, error) {
	if raw := strings.TrimSpace(r.URL.Query().Get("region")); raw != "" {
		return parseExplicitRegion(raw)
	}

	if !isJSONContentType(r.Header.Get("Content-Type")) {
		return RegionUnknown, nil
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) 
	if err != nil {
		return RegionUnknown, fmt.Errorf("read request body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	if len(bytes.TrimSpace(body)) == 0 {
		return RegionUnknown, nil
	}

	var payload struct {
		Region string `json:"region"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return RegionUnknown, nil
	}

	raw := strings.TrimSpace(payload.Region)
	if raw == "" {
		return RegionUnknown, nil
	}

	return parseExplicitRegion(raw)
}

func parseExplicitRegion(raw string) (Region, error) {
	region := Region(raw)
	if Valid(region) {
		return region, nil
	}

	return RegionUnknown, ErrInvalidRegion
}

func isJSONContentType(contentType string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(contentType))
	if mediaType == "" {
		return false
	}

	if i := strings.Index(mediaType, ";"); i >= 0 {
		mediaType = strings.TrimSpace(mediaType[:i])
	}

	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}

	addr := r.RemoteAddr
	if addr == "" {
		return ""
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}

	return host
}
