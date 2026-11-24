// Package session implements opaque, server-side sessions stored in Redis.
//
// A session id is 256 bits from crypto/rand. Only its SHA-256 is used as the Redis key, so a Redis dump does not
// yield usable cookies. Every session carries a CSRF secret (the synchronizer token) and is listed in a per-user
// index so all of a user's sessions can be revoked at once.
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// Stage is how far through authentication a session is.
type Stage string

const (
	// StageFull is a fully authenticated session.
	StageFull Stage = "full"
	// StageMFA is a short-lived pre-auth session: password/OTP passed, TOTP still required.
	StageMFA Stage = "mfa"
)

// Default lifetimes.
const (
	DefaultIdleTTL     = 2 * time.Hour
	DefaultAbsoluteTTL = 12 * time.Hour
	PreAuthTTL         = 5 * time.Minute

	maxCookieLen = 128
)

// Errors returned by the store.
var (
	// ErrInvalid means the session id is unknown, expired or revoked.
	ErrInvalid = errors.New("session invalid")
	// ErrUnavailable means the backing store failed; callers must fail closed.
	ErrUnavailable = errors.New("session store unavailable")
)

// Identity is what a validated session says about the caller. It is what a BFF forwards to services.
type Identity struct {
	UserID    uuid.UUID
	Region    string
	Stage     Stage
	MFA       bool
	CSRFToken string
	CreatedAt time.Time
	LastSeen  time.Time
	// Key is the hashed session id (hex); safe to log and to use as a stable handle.
	Key string
}

// Validator validates a raw cookie value. Implemented by Store so a BFF can reuse the same Redis namespace.
type Validator interface {
	Validate(ctx context.Context, cookieValue string) (Identity, error)
}

type record struct {
	UserID    string    `json:"user_id"`
	Region    string    `json:"region"`
	Stage     Stage     `json:"stage"`
	MFA       bool      `json:"mfa"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
	UAHash    string    `json:"ua_hash"`
	CSRF      string    `json:"csrf"`
}

// Options configures a Store.
type Options struct {
	// Namespace separates key spaces, e.g. "sess", "admin_sess", "preauth".
	Namespace   string
	IdleTTL     time.Duration
	AbsoluteTTL time.Duration
	// Now is overridable in tests.
	Now func() time.Time
}

// Store is a Redis-backed session store for one namespace.
type Store struct {
	rdb  goredis.Cmdable
	opts Options
}

// NewStore returns a store. Zero TTLs take the defaults.
func NewStore(rdb goredis.Cmdable, opts Options) *Store {
	if opts.IdleTTL <= 0 {
		opts.IdleTTL = DefaultIdleTTL
	}
	if opts.AbsoluteTTL <= 0 {
		opts.AbsoluteTTL = DefaultAbsoluteTTL
	}
	if opts.AbsoluteTTL < opts.IdleTTL {
		opts.AbsoluteTTL = opts.IdleTTL
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Store{rdb: rdb, opts: opts}
}

// NewPreAuthStore returns a store for single-use MFA pre-auth sessions (fixed 5 minute lifetime).
func NewPreAuthStore(rdb goredis.Cmdable, namespace string) *Store {
	return NewStore(rdb, Options{Namespace: namespace, IdleTTL: PreAuthTTL, AbsoluteTTL: PreAuthTTL})
}

// IdleTTL returns the sliding idle timeout.
func (s *Store) IdleTTL() time.Duration { return s.opts.IdleTTL }

// AbsoluteTTL returns the hard lifetime.
func (s *Store) AbsoluteTTL() time.Duration { return s.opts.AbsoluteTTL }

// CreateParams describes a new session.
type CreateParams struct {
	UserID    uuid.UUID
	Region    string
	Stage     Stage
	MFA       bool
	UserAgent string
	// CreatedAt carries the original creation time across a rotation so the absolute timeout is not extended.
	CreatedAt time.Time
}

func (s *Store) key(hash string) string   { return s.opts.Namespace + ":s:" + hash }
func (s *Store) userKey(id string) string { return s.opts.Namespace + ":u:" + id }

func hashID(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func hashUA(ua string) string {
	sum := sha256.Sum256([]byte(ua))
	return hex.EncodeToString(sum[:8])
}

func newSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func unavailable(err error) error { return fmt.Errorf("%w: %w", ErrUnavailable, err) }

// Create stores a new session and returns the raw id (the cookie value) and its identity.
func (s *Store) Create(ctx context.Context, p CreateParams) (string, Identity, error) {
	raw, err := newSecret()
	if err != nil {
		return "", Identity{}, err
	}
	csrf, err := newSecret()
	if err != nil {
		return "", Identity{}, err
	}
	now := s.opts.Now().UTC()
	created := p.CreatedAt
	if created.IsZero() {
		created = now
	}
	stage := p.Stage
	if stage == "" {
		stage = StageFull
	}
	rec := record{
		UserID: p.UserID.String(), Region: p.Region, Stage: stage, MFA: p.MFA,
		CreatedAt: created.UTC(), LastSeen: now, UAHash: hashUA(p.UserAgent), CSRF: csrf,
	}
	ttl := s.ttl(rec, now)
	if ttl <= 0 {
		return "", Identity{}, ErrInvalid
	}
	body, err := json.Marshal(rec)
	if err != nil {
		return "", Identity{}, fmt.Errorf("marshal session: %w", err)
	}
	hash := hashID(raw)
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, s.key(hash), body, ttl)
	pipe.SAdd(ctx, s.userKey(rec.UserID), hash)
	pipe.Expire(ctx, s.userKey(rec.UserID), s.opts.AbsoluteTTL)
	if _, err = pipe.Exec(ctx); err != nil {
		return "", Identity{}, unavailable(err)
	}
	return raw, identityOf(hash, rec), nil
}

// ttl is the remaining Redis lifetime: the idle window, capped by the absolute deadline.
func (s *Store) ttl(rec record, now time.Time) time.Duration {
	remaining := rec.CreatedAt.Add(s.opts.AbsoluteTTL).Sub(now)
	if remaining > s.opts.IdleTTL {
		return s.opts.IdleTTL
	}
	return remaining
}

func identityOf(hash string, rec record) Identity {
	id, _ := uuid.Parse(rec.UserID)
	return Identity{
		UserID: id, Region: rec.Region, Stage: rec.Stage, MFA: rec.MFA, CSRFToken: rec.CSRF,
		CreatedAt: rec.CreatedAt, LastSeen: rec.LastSeen, Key: hash,
	}
}

func (s *Store) load(ctx context.Context, raw string) (string, record, error) {
	if raw == "" || len(raw) > maxCookieLen {
		return "", record{}, ErrInvalid
	}
	hash := hashID(raw)
	val, err := s.rdb.Get(ctx, s.key(hash)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return "", record{}, ErrInvalid
	}
	if err != nil {
		return "", record{}, unavailable(err)
	}
	var rec record
	if err = json.Unmarshal(val, &rec); err != nil {
		return "", record{}, ErrInvalid
	}
	return hash, rec, nil
}

// Validate implements Validator: it checks idle and absolute timeouts and slides the idle window.
func (s *Store) Validate(ctx context.Context, raw string) (Identity, error) {
	hash, rec, err := s.load(ctx, raw)
	if err != nil {
		return Identity{}, err
	}
	now := s.opts.Now().UTC()
	if !now.Before(rec.CreatedAt.Add(s.opts.AbsoluteTTL)) || now.Sub(rec.LastSeen) >= s.opts.IdleTTL {
		s.dropHash(ctx, hash, rec.UserID)
		return Identity{}, ErrInvalid
	}
	// Slide the idle window, without resurrecting a session revoked in the meantime (XX).
	if now.Sub(rec.LastSeen) > time.Second {
		rec.LastSeen = now
		body, mErr := json.Marshal(rec)
		if mErr == nil {
			args := goredis.SetArgs{Mode: "XX", TTL: s.ttl(rec, now)}
			if sErr := s.rdb.SetArgs(ctx, s.key(hash), body, args).Err(); sErr != nil && !errors.Is(sErr, goredis.Nil) {
				return Identity{}, unavailable(sErr)
			}
		}
	}
	return identityOf(hash, rec), nil
}

// Peek loads a session without sliding it (used by pre-auth).
func (s *Store) Peek(ctx context.Context, raw string) (Identity, error) {
	hash, rec, err := s.load(ctx, raw)
	if err != nil {
		return Identity{}, err
	}
	return identityOf(hash, rec), nil
}

// Consume atomically deletes a session and returns it; a second call fails with ErrInvalid (single use).
func (s *Store) Consume(ctx context.Context, raw string) (Identity, error) {
	if raw == "" || len(raw) > maxCookieLen {
		return Identity{}, ErrInvalid
	}
	hash := hashID(raw)
	val, err := s.rdb.GetDel(ctx, s.key(hash)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return Identity{}, ErrInvalid
	}
	if err != nil {
		return Identity{}, unavailable(err)
	}
	var rec record
	if err = json.Unmarshal(val, &rec); err != nil {
		return Identity{}, ErrInvalid
	}
	s.dropHash(ctx, hash, rec.UserID)
	return identityOf(hash, rec), nil
}

// Rotate replaces a session id with a fresh one (new CSRF token), keeping created_at so the absolute timeout
// still applies. region updates the stored region (empty keeps it); mfa can only be raised, never lowered.
func (s *Store) Rotate(ctx context.Context, raw, region string, mfa bool, userAgent string) (string, Identity, error) {
	_, rec, err := s.load(ctx, raw)
	if err != nil {
		return "", Identity{}, err
	}
	uid, _ := uuid.Parse(rec.UserID)
	if region == "" {
		region = rec.Region
	}
	newRaw, ident, err := s.Create(ctx, CreateParams{
		UserID: uid, Region: region, Stage: rec.Stage, MFA: rec.MFA || mfa, UserAgent: userAgent, CreatedAt: rec.CreatedAt,
	})
	if err != nil {
		return "", Identity{}, err
	}
	s.dropHash(ctx, hashID(raw), rec.UserID)
	return newRaw, ident, nil
}

// Revoke deletes one session. It is idempotent.
func (s *Store) Revoke(ctx context.Context, raw string) error {
	if raw == "" || len(raw) > maxCookieLen {
		return nil
	}
	hash := hashID(raw)
	userID := ""
	if val, err := s.rdb.Get(ctx, s.key(hash)).Bytes(); err == nil {
		var rec record
		if json.Unmarshal(val, &rec) == nil {
			userID = rec.UserID
		}
	}
	if err := s.rdb.Del(ctx, s.key(hash)).Err(); err != nil {
		return unavailable(err)
	}
	if userID != "" {
		s.rdb.SRem(ctx, s.userKey(userID), hash)
	}
	return nil
}

// RevokeAll deletes every session of a user (password change/reset, TOTP disable, forced logout).
func (s *Store) RevokeAll(ctx context.Context, userID uuid.UUID) error {
	uk := s.userKey(userID.String())
	hashes, err := s.rdb.SMembers(ctx, uk).Result()
	if err != nil {
		return unavailable(err)
	}
	keys := make([]string, 0, len(hashes)+1)
	for _, h := range hashes {
		keys = append(keys, s.key(h))
	}
	keys = append(keys, uk)
	if err = s.rdb.Del(ctx, keys...).Err(); err != nil {
		return unavailable(err)
	}
	return nil
}

// CountForUser returns the number of live sessions indexed for a user (for tests and ops).
func (s *Store) CountForUser(ctx context.Context, userID uuid.UUID) (int, error) {
	hashes, err := s.rdb.SMembers(ctx, s.userKey(userID.String())).Result()
	if err != nil {
		return 0, unavailable(err)
	}
	n := 0
	for _, h := range hashes {
		if ok, _ := s.rdb.Exists(ctx, s.key(h)).Result(); ok > 0 {
			n++
		}
	}
	return n, nil
}

func (s *Store) dropHash(ctx context.Context, hash, userID string) {
	s.rdb.Del(ctx, s.key(hash))
	if userID != "" {
		s.rdb.SRem(ctx, s.userKey(userID), hash)
	}
}

// TokensEqual compares two secrets in constant time.
func TokensEqual(a, b string) bool {
	return a != "" && len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
