package session

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"

	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

// CSRFHeader carries the synchronizer token on unsafe requests.
const CSRFHeader = "X-CSRF-Token"

const hostPrefix = "__Host-"

// Kind selects the cookie and Redis namespace family.
type Kind string

const (
	// KindProduct is the product (employer/candidate) session.
	KindProduct Kind = "product"
	// KindAdmin is the ops-console session, fully separate from product.
	KindAdmin Kind = "admin"
)

// ManagerConfig configures a Manager.
type ManagerConfig struct {
	Kind        Kind
	IdleTTL     time.Duration
	AbsoluteTTL time.Duration
	// Secure sets the Secure attribute and the __Host- cookie name prefix. Only disable for plain-http local dev.
	Secure bool
	// Now is overridable in tests.
	Now func() time.Time
}

// Manager ties the stores to HTTP cookies.
type Manager struct {
	Sessions *Store
	PreAuth  *Store

	sessionCookie string
	preAuthCookie string
	secure        bool
	absolute      time.Duration
}

// NewManager builds a manager over Redis.
func NewManager(rdb goredis.Cmdable, cfg ManagerConfig) *Manager {
	sessName, preName, ns := "openhr_session", "openhr_preauth", "sess"
	if cfg.Kind == KindAdmin {
		sessName, preName, ns = "openhr_admin_session", "openhr_admin_preauth", "admin_sess"
	}
	if cfg.Secure {
		sessName, preName = hostPrefix+sessName, hostPrefix+preName
	}
	st := NewStore(rdb, Options{Namespace: ns, IdleTTL: cfg.IdleTTL, AbsoluteTTL: cfg.AbsoluteTTL, Now: cfg.Now})
	pre := NewStore(rdb, Options{
		Namespace: ns + "_pre", IdleTTL: PreAuthTTL, AbsoluteTTL: PreAuthTTL, Now: cfg.Now,
	})
	return &Manager{
		Sessions: st, PreAuth: pre,
		sessionCookie: sessName, preAuthCookie: preName, secure: cfg.Secure, absolute: st.AbsoluteTTL(),
	}
}

// SessionCookieName is the cookie carrying the full session id.
func (m *Manager) SessionCookieName() string { return m.sessionCookie }

// PreAuthCookieName is the cookie carrying the MFA pre-auth id.
func (m *Manager) PreAuthCookieName() string { return m.preAuthCookie }

// Validator exposes session validation so a BFF can reuse the same store.
func (m *Manager) Validator() Validator { return m.Sessions }

func (m *Manager) setCookie(w http.ResponseWriter, name, value string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: int(maxAge.Seconds()),
		HttpOnly: true, Secure: m.secure, SameSite: http.SameSiteLaxMode,
	})
}

func (m *Manager) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: m.secure, SameSite: http.SameSiteLaxMode,
	})
}

func cookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// Start creates a fully authenticated session and sets the cookie. A session presented with the request is revoked
// first (session fixation defence); any pending pre-auth cookie is cleared.
func (m *Manager) Start(w http.ResponseWriter, r *http.Request, userID uuid.UUID, region string, mfa bool) (Identity, error) {
	if old := cookieValue(r, m.sessionCookie); old != "" {
		if err := m.Sessions.Revoke(r.Context(), old); err != nil {
			return Identity{}, err
		}
	}
	raw, ident, err := m.Sessions.Create(r.Context(), CreateParams{
		UserID: userID, Region: region, Stage: StageFull, MFA: mfa, UserAgent: r.UserAgent(),
	})
	if err != nil {
		return Identity{}, err
	}
	m.setCookie(w, m.sessionCookie, raw, m.absolute)
	if cookieValue(r, m.preAuthCookie) != "" {
		m.clearCookie(w, m.preAuthCookie)
	}
	return ident, nil
}

// StartPreAuth creates the single-use MFA pre-auth session and sets its cookie.
func (m *Manager) StartPreAuth(w http.ResponseWriter, r *http.Request, userID uuid.UUID, region string) error {
	raw, _, err := m.PreAuth.Create(r.Context(), CreateParams{
		UserID: userID, Region: region, Stage: StageMFA, UserAgent: r.UserAgent(),
	})
	if err != nil {
		return err
	}
	m.setCookie(w, m.preAuthCookie, raw, PreAuthTTL)
	return nil
}

// PeekPreAuth reads the pre-auth session without consuming it.
func (m *Manager) PeekPreAuth(r *http.Request) (Identity, error) {
	ident, err := m.PreAuth.Peek(r.Context(), cookieValue(r, m.preAuthCookie))
	if err != nil {
		return Identity{}, err
	}
	if ident.Stage != StageMFA {
		return Identity{}, ErrInvalid
	}
	return ident, nil
}

// ConsumePreAuth atomically spends the pre-auth session (single use) and clears its cookie.
func (m *Manager) ConsumePreAuth(w http.ResponseWriter, r *http.Request) (Identity, error) {
	ident, err := m.PreAuth.Consume(r.Context(), cookieValue(r, m.preAuthCookie))
	if err != nil {
		return Identity{}, err
	}
	m.clearCookie(w, m.preAuthCookie)
	if ident.Stage != StageMFA {
		return Identity{}, ErrInvalid
	}
	return ident, nil
}

// Authenticate validates the request's session cookie. Only full sessions authenticate.
func (m *Manager) Authenticate(r *http.Request) (Identity, error) {
	ident, err := m.Sessions.Validate(r.Context(), cookieValue(r, m.sessionCookie))
	if err != nil {
		return Identity{}, err
	}
	if ident.Stage != StageFull {
		return Identity{}, ErrInvalid
	}
	return ident, nil
}

// Rotate swaps the request's session for a fresh id and CSRF token, optionally changing region, and sets the cookie.
func (m *Manager) Rotate(w http.ResponseWriter, r *http.Request, region string) (Identity, error) {
	raw, ident, err := m.Sessions.Rotate(r.Context(), cookieValue(r, m.sessionCookie), region, false, r.UserAgent())
	if err != nil {
		return Identity{}, err
	}
	m.setCookie(w, m.sessionCookie, raw, m.absolute)
	return ident, nil
}

// Logout revokes the request's session and clears both cookies. Idempotent.
func (m *Manager) Logout(w http.ResponseWriter, r *http.Request) error {
	err := m.Sessions.Revoke(r.Context(), cookieValue(r, m.sessionCookie))
	if pre := cookieValue(r, m.preAuthCookie); pre != "" {
		if _, consumeErr := m.PreAuth.Consume(r.Context(), pre); consumeErr != nil && !errors.Is(consumeErr, ErrInvalid) && err == nil {
			err = consumeErr
		}
	}
	m.clearCookie(w, m.sessionCookie)
	m.clearCookie(w, m.preAuthCookie)
	return err
}

// RevokeAll ends every session and pending pre-auth of a user.
func (m *Manager) RevokeAll(ctx context.Context, userID uuid.UUID) error {
	if err := m.Sessions.RevokeAll(ctx, userID); err != nil {
		return err
	}
	return m.PreAuth.RevokeAll(ctx, userID)
}

// IsUnsafeMethod reports whether a method changes state and therefore needs CSRF protection.
func IsUnsafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	}
	return true
}

// CheckCSRF verifies the X-CSRF-Token header against the session's synchronizer token.
func CheckCSRF(r *http.Request, ident Identity) bool {
	return TokensEqual(r.Header.Get(CSRFHeader), ident.CSRFToken)
}

// OriginGuard rejects unsafe requests whose Origin (or, lacking that, Referer) is not on the allow-list.
// Requests that send neither header (non-browser clients) pass; same-host requests always pass. The CSRF token
// remains required on authenticated routes regardless.
func OriginGuard(allowed []string) func(http.Handler) http.Handler {
	set := make(map[string]struct{}, len(allowed))
	for _, o := range allowed {
		set[strings.ToLower(strings.TrimRight(strings.TrimSpace(o), "/"))] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !IsUnsafeMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			if origin == "" {
				if ref := strings.TrimSpace(r.Header.Get("Referer")); ref != "" {
					if u, err := url.Parse(ref); err == nil && u.Scheme != "" && u.Host != "" {
						origin = u.Scheme + "://" + u.Host
					} else {
						origin = "null"
					}
				}
			}
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}
			if _, ok := set[strings.ToLower(origin)]; ok {
				next.ServeHTTP(w, r)
				return
			}
			if u, err := url.Parse(origin); err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host) {
				next.ServeHTTP(w, r)
				return
			}
			response.Error(w, r, apperror.New(apperror.CodeForbidden, "origin not allowed"))
		})
	}
}
