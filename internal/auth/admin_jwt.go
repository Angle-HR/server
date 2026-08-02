package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	tokenUseAdmin    = "admin"
	adminRefreshType = "admin_refresh"
)

// AdminAccessClaims are validated on admin routes.
type AdminAccessClaims struct {
	TokenUse string `json:"token_use"`
	jwt.RegisteredClaims
}

// AdminRefreshClaims are used to mint new admin access tokens.
type AdminRefreshClaims struct {
	TokenType string `json:"token_type"`
	TokenUse  string `json:"token_use"`
	jwt.RegisteredClaims
}

// IssueAdminPair mints admin access and refresh tokens.
func (s *TokenService) IssueAdminPair(adminUserID uuid.UUID) (TokenPair, error) {
	now := time.Now()

	accessClaims := AdminAccessClaims{
		TokenUse: tokenUseAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   adminUserID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessLifetime)),
		},
	}

	accessToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString(s.secret)
	if err != nil {
		return TokenPair{}, fmt.Errorf("sign admin access token: %w", err)
	}

	refreshClaims := AdminRefreshClaims{
		TokenType: adminRefreshType,
		TokenUse:  tokenUseAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   adminUserID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.refreshLifetime)),
		},
	}

	refreshToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims).SignedString(s.secret)
	if err != nil {
		return TokenPair{}, fmt.Errorf("sign admin refresh token: %w", err)
	}

	return TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int(s.accessLifetime.Seconds()),
	}, nil
}

// ParseAdminAccess validates an admin access token.
func (s *TokenService) ParseAdminAccess(token string) (AdminAccessClaims, error) {
	parsed, err := jwt.ParseWithClaims(token, &AdminAccessClaims{}, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return s.secret, nil
	})
	if err != nil {
		return AdminAccessClaims{}, fmt.Errorf("parse admin access token: %w", err)
	}

	claims, ok := parsed.Claims.(*AdminAccessClaims)
	if !ok || !parsed.Valid || claims.TokenUse != tokenUseAdmin {
		return AdminAccessClaims{}, fmt.Errorf("invalid admin access token")
	}

	return *claims, nil
}

// ParseAdminRefresh validates an admin refresh token.
func (s *TokenService) ParseAdminRefresh(token string) (AdminRefreshClaims, error) {
	parsed, err := jwt.ParseWithClaims(token, &AdminRefreshClaims{}, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return s.secret, nil
	})
	if err != nil {
		return AdminRefreshClaims{}, fmt.Errorf("parse admin refresh token: %w", err)
	}

	claims, ok := parsed.Claims.(*AdminRefreshClaims)
	if !ok || !parsed.Valid || claims.TokenType != adminRefreshType || claims.TokenUse != tokenUseAdmin {
		return AdminRefreshClaims{}, fmt.Errorf("invalid admin refresh token")
	}

	return *claims, nil
}

// AdminUserIDFromAccess extracts the admin user UUID from access claims.
func AdminUserIDFromAccess(claims AdminAccessClaims) (uuid.UUID, error) {
	return uuid.Parse(claims.Subject)
}
