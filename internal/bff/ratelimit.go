package bff

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Angle-HR/server/pkg/apperror"
)

// Limiter decides whether one more event under key fits in rule. retryAfter is only meaningful when !ok.
type Limiter interface {
	Allow(ctx context.Context, key string, rule Rule) (ok bool, retryAfter time.Duration, err error)
	Ping(ctx context.Context) error
}

// slidingWindow is an atomic sliding-window log: one sorted set per key, trimmed, counted and appended in one
// Lua call. Time comes from Redis (TIME) so replicas of the BFF never disagree about the window.
var slidingWindow = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local window = tonumber(ARGV[1])
local max = tonumber(ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[1], 0, now - window)
if redis.call('ZCARD', KEYS[1]) >= max then
  local oldest = redis.call('ZRANGE', KEYS[1], 0, 0, 'WITHSCORES')
  local retry = window
  if oldest[2] then retry = tonumber(oldest[2]) + window - now end
  if retry < 1 then retry = 1 end
  return {0, retry}
end
redis.call('ZADD', KEYS[1], now, ARGV[3])
redis.call('PEXPIRE', KEYS[1], window)
return {1, 0}
`)

// RedisLimiter is the Redis implementation of Limiter.
type RedisLimiter struct {
	Client redis.UniversalClient
	Prefix string
}

// Allow implements Limiter.
func (l *RedisLimiter) Allow(ctx context.Context, key string, rule Rule) (bool, time.Duration, error) {
	var nonce [8]byte
	_, _ = rand.Read(nonce[:])
	res, err := slidingWindow.Run(ctx, l.Client, []string{l.Prefix + key},
		rule.Window.Milliseconds(), rule.Max, hex.EncodeToString(nonce[:])).Int64Slice()
	if err != nil {
		return false, 0, err
	}
	if len(res) != 2 {
		return false, 0, redis.Nil
	}
	return res[0] == 1, time.Duration(res[1]) * time.Millisecond, nil
}

// Ping implements Limiter.
func (l *RedisLimiter) Ping(ctx context.Context) error { return l.Client.Ping(ctx).Err() }

// classRoute maps a path (API prefix stripped) to a class name from Config.Classes.
type classRoute struct {
	method string
	path   string // exact, or regexp when re != nil
	re     *regexp.Regexp
	class  string
}

var classRoutes = []classRoute{
	{method: http.MethodPost, path: "/auth/login", class: "LOGIN"},
	{method: http.MethodPost, path: "/auth/login/otp/request", class: "LOGIN_OTP_REQUEST"},
	{method: http.MethodPost, path: "/auth/login/otp/verify", class: "LOGIN_OTP_VERIFY"},
	{method: http.MethodPost, path: "/auth/login/totp", class: "LOGIN_TOTP"},
	{method: http.MethodPost, path: "/auth/signup", class: "SIGNUP"},
	{method: http.MethodPost, path: "/auth/verify-email", class: "VERIFY_EMAIL"},
	{method: http.MethodPost, path: "/auth/resend-verification", class: "RESEND_VERIFICATION"},
	{method: http.MethodPost, path: "/auth/forgot-password", class: "FORGOT_PASSWORD"},
	{method: http.MethodPost, path: "/auth/reset-password", class: "RESET_PASSWORD"},
	{method: http.MethodPost, re: regexp.MustCompile(`^/public/jobs/[^/]+/(applications|uploads)$`), class: "PUBLIC_APPLY"},
}

// Classify returns the rate-limit class for a request path, or "" for none. prefix is the API prefix to strip.
func Classify(method, path, prefix string) string {
	p := strings.TrimPrefix(path, prefix)
	p = strings.TrimSuffix(p, "/")
	for _, c := range classRoutes {
		if c.method != method {
			continue
		}
		if c.re != nil {
			if c.re.MatchString(p) {
				return c.class
			}
		} else if c.path == p {
			return c.class
		}
	}
	return ""
}

const maxPeekBytes = 64 << 10

// PeekEmail reads a JSON body's "email" field without consuming it: the body is always restored. It returns ""
// when the body is not JSON, has no email, or is larger than the peek limit.
func PeekEmail(r *http.Request) string {
	if r.Body == nil || r.Body == http.NoBody {
		return ""
	}
	orig := r.Body
	buf, _ := io.ReadAll(io.LimitReader(orig, maxPeekBytes+1))
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(buf), orig), orig}
	if len(buf) > maxPeekBytes {
		return ""
	}
	var v struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(buf, &v) != nil {
		return ""
	}
	return NormalizeEmail(v.Email)
}

// NormalizeEmail lower-cases and trims, so Foo@x.com and " foo@x.com" share one bucket.
func NormalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

// RateLimit is the middleware: a per-IP global limit for everything, then per-class per-IP and per-email limits.
func RateLimit(cfg Config, lim Limiter, m *Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ClientIP(r, cfg.TrustedProxies)
			class := Classify(r.Method, r.URL.Path, cfg.APIPrefix)

			if cfg.GlobalLimit.Max > 0 {
				if !check(w, r, lim, m, "global", "g:"+ip, cfg.GlobalLimit, cfg.FailClosedOthers) {
					return
				}
			}
			if class != "" {
				cc := cfg.Classes[class]
				if cc.IP.Max > 0 && !check(w, r, lim, m, class, "c:"+class+":ip:"+ip, cc.IP, cfg.FailClosedAuth) {
					return
				}
				if cc.Email.Max > 0 {
					if email := PeekEmail(r); email != "" {
						sum := sha256.Sum256([]byte(email))
						if !check(w, r, lim, m, class+"_email", "c:"+class+":em:"+hex.EncodeToString(sum[:16]), cc.Email, cfg.FailClosedAuth) {
							return
						}
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// check returns true when the request may proceed. On denial or fail-closed outage it writes the response.
func check(w http.ResponseWriter, r *http.Request, lim Limiter, m *Metrics, name, key string, rule Rule, failClosed bool) bool {
	ok, retry, err := lim.Allow(r.Context(), key, rule)
	if err != nil {
		m.Inc("bff_ratelimit_errors_total", "class", name)
		if failClosed {
			writeError(w, r, http.StatusServiceUnavailable, apperror.CodeServiceUnavailable, "service temporarily unavailable, try again", 5)
			return false
		}
		return true
	}
	if !ok {
		m.Inc("bff_ratelimit_rejected_total", "class", name)
		secs := int((retry + time.Second - 1) / time.Second)
		if secs < 1 {
			secs = 1
		}
		writeError(w, r, http.StatusTooManyRequests, apperror.CodeTooManyRequests, "too many requests, try again later", secs)
		return false
	}
	return true
}
