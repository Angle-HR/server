package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/region"
)

// TokenPair holds access and refresh tokens.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
}

// AccessClaims are validated on protected routes.
type AccessClaims struct {
	Region        string `json:"region"`
	EmailVerified bool   `json:"email_verified"`
	jwt.RegisteredClaims
}

// RefreshClaims are used to mint new access tokens.
type RefreshClaims struct {
	TokenType string `json:"token_type"`
	jwt.RegisteredClaims
}

const (
	refreshTokenType = "refresh"
	mfaTokenUse      = "mfa"
	mfaLifetime      = 5 * time.Minute
)

// MFAClaims are short-lived tokens issued when TOTP is required after password/OTP login.
type MFAClaims struct {
	TokenUse string `json:"token_use"`
	Region   string `json:"region"`
	jwt.RegisteredClaims
}

// TokenService issues and parses JWTs.
type TokenService struct {
	secret          []byte
	accessLifetime  time.Duration
	refreshLifetime time.Duration
}

// NewTokenService returns a configured token service.
func NewTokenService(secret string, accessTTL, refreshTTL time.Duration) (*TokenService, error) {
	if secret == "" {
		return nil, fmt.Errorf("jwt secret is required")
	}

	return &TokenService{
		secret:          []byte(secret),
		accessLifetime:  accessTTL,
		refreshLifetime: refreshTTL,
	}, nil
}

// IssuePair mints access and refresh tokens for a verified user.
func (s *TokenService) IssuePair(userID uuid.UUID, reg region.Region, emailVerified bool) (TokenPair, error) {
	now := time.Now()

	accessClaims := AccessClaims{
		Region:        string(reg),
		EmailVerified: emailVerified,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessLifetime)),
		},
	}

	accessToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString(s.secret)
	if err != nil {
		return TokenPair{}, fmt.Errorf("sign access token: %w", err)
	}

	refreshClaims := RefreshClaims{
		TokenType: refreshTokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        uuid.NewString(),
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.refreshLifetime)),
		},
	}

	refreshToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims).SignedString(s.secret)
	if err != nil {
		return TokenPair{}, fmt.Errorf("sign refresh token: %w", err)
	}

	return TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int(s.accessLifetime.Seconds()),
	}, nil
}

// IssueAccess mints a new access token from a valid refresh token.
func (s *TokenService) IssueAccess(refreshToken string, reg region.Region, emailVerified bool) (string, int, error) {
	claims, err := s.ParseRefresh(refreshToken)
	if err != nil {
		return "", 0, err
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return "", 0, fmt.Errorf("invalid refresh subject: %w", err)
	}

	pair, err := s.IssuePair(userID, reg, emailVerified)
	if err != nil {
		return "", 0, err
	}

	return pair.AccessToken, pair.ExpiresIn, nil
}

// IssueMFAToken mints a short-lived MFA challenge token.
func (s *TokenService) IssueMFAToken(userID uuid.UUID, reg region.Region) (string, int, error) {
	now := time.Now()
	claims := MFAClaims{
		TokenUse: mfaTokenUse,
		Region:   string(reg),
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        uuid.NewString(),
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(mfaLifetime)),
		},
	}

	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
	if err != nil {
		return "", 0, fmt.Errorf("sign mfa token: %w", err)
	}
	return token, int(mfaLifetime.Seconds()), nil
}

// ParseAccess validates an access token and returns claims.
func (s *TokenService) ParseAccess(token string) (AccessClaims, error) {
	parsed, err := jwt.ParseWithClaims(token, &AccessClaims{}, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}

		return s.secret, nil
	})
	if err != nil {
		return AccessClaims{}, fmt.Errorf("parse access token: %w", err)
	}

	claims, ok := parsed.Claims.(*AccessClaims)
	if !ok || !parsed.Valid {
		return AccessClaims{}, fmt.Errorf("invalid access token")
	}

	return *claims, nil
}

// ParseRefresh validates a refresh token.
func (s *TokenService) ParseRefresh(token string) (RefreshClaims, error) {
	parsed, err := jwt.ParseWithClaims(token, &RefreshClaims{}, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}

		return s.secret, nil
	})
	if err != nil {
		return RefreshClaims{}, fmt.Errorf("parse refresh token: %w", err)
	}

	claims, ok := parsed.Claims.(*RefreshClaims)
	if !ok || !parsed.Valid || claims.TokenType != refreshTokenType {
		return RefreshClaims{}, fmt.Errorf("invalid refresh token")
	}

	return *claims, nil
}

// ParseMFA validates an MFA challenge token.
func (s *TokenService) ParseMFA(token string) (MFAClaims, error) {
	parsed, err := jwt.ParseWithClaims(token, &MFAClaims{}, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return s.secret, nil
	})
	if err != nil {
		return MFAClaims{}, fmt.Errorf("parse mfa token: %w", err)
	}

	claims, ok := parsed.Claims.(*MFAClaims)
	if !ok || !parsed.Valid || claims.TokenUse != mfaTokenUse {
		return MFAClaims{}, fmt.Errorf("invalid mfa token")
	}
	return *claims, nil
}

// UserIDFromAccess extracts the user UUID from access claims.
func UserIDFromAccess(claims AccessClaims) (uuid.UUID, error) {
	return uuid.Parse(claims.Subject)
}

// RegionFromAccess extracts region from access claims.
func RegionFromAccess(claims AccessClaims) (region.Region, error) {
	reg := region.Region(claims.Region)
	if !region.Valid(reg) {
		return region.RegionUnknown, fmt.Errorf("invalid token region")
	}

	return reg, nil
}

// AccessLifetime returns configured access token TTL.
func (s *TokenService) AccessLifetime() time.Duration {
	return s.accessLifetime
}

// RefreshLifetime returns configured refresh token TTL.
func (s *TokenService) RefreshLifetime() time.Duration {
	return s.refreshLifetime
}
