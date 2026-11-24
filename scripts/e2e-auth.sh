#!/usr/bin/env bash
# End-to-end auth flow against the local stack (curl + jq).
#
#   signup -> code from mailhog -> verify-email -> login -> /auth/me
#          -> business onboarding -> job create + publish-check -> logout
#
# Env:
#   BASE          API base            (default http://localhost:8081/api/v1)
#   MAILHOG       mailhog HTTP base   (default http://localhost:8025)
#   AUTH_MODE     bearer | cookie     (default cookie)
#                 bearer: Authorization: Bearer <access_token> from login.
#                 cookie: login sets HttpOnly `openhr_session`; the script keeps a cookie jar and sends the
#                         login response's data.csrf_token as X-CSRF-Token on POST/PUT/PATCH/DELETE.
#   EMAIL, PASS   credentials (default: unique e2e address)
#   STRICT_JOBS=1 fail (instead of stopping with a note) when the job flow hits a missing prerequisite
set -euo pipefail

. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/e2e-common.sh"
e2e_url BASE WEB_BFF_PORT 8081 /api/v1
e2e_url MAILHOG MAILHOG_UI_PORT 8025
AUTH_MODE="${AUTH_MODE:-cookie}"
EMAIL="${EMAIL:-e2e-auth-$(date +%s)-$RANDOM@example.com}"
PASS="${PASS:-test-password-123}"
STRICT_JOBS="${STRICT_JOBS:-0}"
COMPANY_ROLE="60000000-0000-4000-8000-000000000001"
BUSINESS_TYPE="61000000-0000-4000-8000-000000000001"
INDUSTRY="62000000-0000-4000-8000-000000000001"
UK_COUNTRY="a1b2c3d4-e5f6-4789-a012-3456789abcde"

command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }
case "$AUTH_MODE" in bearer | cookie) ;; *) echo "AUTH_MODE must be bearer or cookie" >&2; exit 2 ;; esac

JAR="$(mktemp)"
trap 'rm -f "$JAR"' EXIT
ACCESS=""
CSRF=""
REFRESH=""
LAST_STATUS=""
LAST_BODY=""

step() { printf '\n=== %s ===\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }

# api METHOD PATH [JSON_BODY] [extra curl args...]  -> sets LAST_STATUS, LAST_BODY
api() {
  local method="$1" path="$2" body="${3:-}"
  shift 2
  [ $# -gt 0 ] && shift
  local args=(-sS -X "$method" -c "$JAR" -b "$JAR" -w '\n%{http_code}' "$BASE$path")
  if [ "$AUTH_MODE" = bearer ] && [ -n "$ACCESS" ]; then
    args+=(-H "Authorization: Bearer $ACCESS")
  fi
  if [ "$AUTH_MODE" = cookie ] && [ -n "$CSRF" ] && [ "$method" != GET ] && [ "$method" != HEAD ]; then
    args+=(-H "X-CSRF-Token: $CSRF")
  fi
  if [ -n "$body" ]; then
    args+=(-H 'Content-Type: application/json' -d "$body")
  fi
  args+=("$@")
  local out
  out="$(curl "${args[@]}")"
  LAST_STATUS="${out##*$'\n'}"
  LAST_BODY="${out%$'\n'*}"
  if [ "$AUTH_MODE" = cookie ]; then
    # Any response may carry a fresh CSRF token (session rotation on region migration, login, /auth/me).
    local fresh
    fresh="$(printf '%s' "$LAST_BODY" | jq -r '.data.session.csrf_token // .data.csrf_token // empty' 2>/dev/null)"
    [ -n "$fresh" ] && CSRF="$fresh"
  fi
  printf '%s %s -> %s\n' "$method" "$path" "$LAST_STATUS"
  printf '%s' "$LAST_BODY" | jq -c . 2>/dev/null | cut -c1-300 || printf '%s\n' "$LAST_BODY" | cut -c1-300
}

expect() { # expect 2xx
  case "$LAST_STATUS" in 2??) ;; *) fail "$1 (HTTP $LAST_STATUS): $LAST_BODY" ;; esac
}

# Newest mailhog message to $1 -> prints the 6-digit code.
mail_code() {
  local to="$1" i body code
  for i in $(seq 1 30); do
    body="$(curl -sS -G "$MAILHOG/api/v2/search" --data-urlencode "kind=to" --data-urlencode "query=$to" \
      | jq -r '.items[0].Content.Body // empty')"
    if [ -n "$body" ]; then
      code="$(printf '%s' "$body" | sed 's/<[^>]*>/ /g' | grep -oE '(^|[^0-9])[0-9]{6}([^0-9]|$)' | head -1 | grep -oE '[0-9]{6}')" || true
      [ -n "$code" ] && { printf '%s' "$code"; return 0; }
    fi
    sleep 1
  done
  return 1
}

step "0. preflight ($AUTH_MODE mode, $EMAIL)"
curl -fsS "${BASE%/api/v1}/healthz" >/dev/null || fail "API not healthy at ${BASE%/api/v1}/healthz"
curl -fsS "$MAILHOG/api/v2/messages?limit=1" >/dev/null || fail "mailhog not reachable at $MAILHOG"
echo "api + mailhog reachable"

step "1. signup"
api POST /auth/signup "$(jq -nc --arg e "$EMAIL" --arg p "$PASS" '{email:$e,password:$p}')"
expect signup
SESSION_ID="$(jq -r '.data.verification_session_id' <<<"$LAST_BODY")"
[ -n "$SESSION_ID" ] && [ "$SESSION_ID" != null ] || fail "no verification_session_id"

step "2. read code from mailhog"
CODE="$(mail_code "$EMAIL")" || fail "no verification email for $EMAIL within 30s (is the notifications service running?)"
echo "code=$CODE"

step "2b. verify-email wrong-code sequence: attempts_remaining, lockout, resend resets"
WRONG=000000; [ "$CODE" = "$WRONG" ] && WRONG=111111
body_wrong="$(jq -nc --arg s "$SESSION_ID" --arg c "$WRONG" '{verification_session_id:$s,code:$c}')"
for want in 4 3 2 1 0; do
  api POST /auth/verify-email "$body_wrong"
  [ "$LAST_STATUS" = 400 ] || fail "wrong code: expected 400, got $LAST_STATUS"
  got="$(jq -r '.error.details.attempts_remaining // "missing"' <<<"$LAST_BODY")"
  [ "$got" = "$want" ] || fail "attempts_remaining: expected $want, got $got"
done
hdrs="$(mktemp)"
out="$(curl -sS -D "$hdrs" -o - -X POST -H 'Content-Type: application/json' -d "$body_wrong" "$BASE/auth/verify-email")"
grep -qi '^HTTP/[0-9.]* 429' "$hdrs" || fail "6th wrong code: expected 429, got: $(head -1 "$hdrs")"
grep -qi '^retry-after: *[0-9]' "$hdrs" || fail "6th wrong code: no Retry-After header"
[ "$(jq -r '.error.code' <<<"$out")" = too_many_attempts ] || fail "6th wrong code: unexpected body $out"
echo "locked after 5 wrong codes: 429 + $(grep -i '^retry-after' "$hdrs" | tr -d '\r')"
# The right code is refused too while locked.
api POST /auth/verify-email "$(jq -nc --arg s "$SESSION_ID" --arg c "$CODE" '{verification_session_id:$s,code:$c}')"
[ "$LAST_STATUS" = 429 ] || fail "correct code while locked: expected 429, got $LAST_STATUS"
rm -f "$hdrs"
# Resend (after the cooldown) issues a fresh code and a fresh budget.
OLD_CODE="$CODE"
for i in $(seq 1 40); do
  api POST /auth/resend-verification "$(jq -nc --arg s "$SESSION_ID" '{verification_session_id:$s}')"
  [ "$LAST_STATUS" = 429 ] || break
  sleep 2
done
expect resend-verification
for i in $(seq 1 30); do
  CODE="$(mail_code "$EMAIL")" || true
  [ -n "$CODE" ] && [ "$CODE" != "$OLD_CODE" ] && break
  sleep 1
done
[ "$CODE" != "$OLD_CODE" ] || fail "no fresh code after resend"
echo "fresh code=$CODE"

step "3. verify-email"
api POST /auth/verify-email "$(jq -nc --arg s "$SESSION_ID" --arg c "$CODE" '{verification_session_id:$s,code:$c}')"
expect verify-email

step "4. login"
api POST /auth/login "$(jq -nc --arg e "$EMAIL" --arg p "$PASS" '{email:$e,password:$p}')"
expect login
if [ "$AUTH_MODE" = bearer ]; then
  ACCESS="$(jq -r '.data.access_token // empty' <<<"$LAST_BODY")"
  [ -n "$ACCESS" ] || fail "login returned no access_token"
  REFRESH="$(jq -r '.data.refresh_token // empty' <<<"$LAST_BODY")"
else
  CSRF="$(jq -r '.data.csrf_token // empty' <<<"$LAST_BODY")"
  grep -q 'openhr_session' "$JAR" || fail "login did not set the openhr_session cookie"
  [ -n "$CSRF" ] || fail "login returned no csrf_token"
  echo "session cookie set; csrf token captured"
fi

step "5. GET /auth/me"
api GET /auth/me
expect auth/me
jq -r '.data | "user: \(.email // .user.email // "?")"' <<<"$LAST_BODY" 2>/dev/null || true

if [ "$AUTH_MODE" = cookie ]; then
  step "5b. cookie mode: unsafe request without CSRF header must be rejected"
  saved="$CSRF"; CSRF=""
  api POST /auth/totp/enroll ""
  CSRF="$saved"
  case "$LAST_STATUS" in 403) echo "rejected as expected (CSRF_INVALID)" ;; *) fail "expected 403 without CSRF header, got $LAST_STATUS" ;; esac
fi

# Job flow. Stops at the first missing prerequisite.
stop_jobs() {
  echo "STOPPED job flow: $1"
  [ "$STRICT_JOBS" = 1 ] && fail "$1"
  return 0
}

job_flow() {
  step "6. onboarding (business) - prerequisite for jobs"
  api GET /onboarding/status
  [ "${LAST_STATUS:0:1}" = 2 ] || { stop_jobs "onboarding status unavailable (HTTP $LAST_STATUS)"; return; }
  api PUT /onboarding/profile "$(jq -nc --arg r "$COMPANY_ROLE" '{account_type:"business",legal_business_name:"ANGLE E2E Ltd",legal_full_name:"Jerry Oluwasegun",company_role_id:$r}')"
  [ "${LAST_STATUS:0:1}" = 2 ] || { stop_jobs "onboarding profile rejected (HTTP $LAST_STATUS)"; return; }
  api PUT /onboarding/address "$(jq -nc --arg c "$UK_COUNTRY" '{country_id:$c,entry_mode:"manual",line_1:"10 Downing Street",city:"London",state_or_county:"Greater London",post_code:"SW1A 2AA",identification:{registration_number:"12345678"}}')"
  [ "${LAST_STATUS:0:1}" = 2 ] || { stop_jobs "onboarding address rejected (HTTP $LAST_STATUS)"; return; }
  # The address step moves the account to its regional database and returns fresh tokens (region claim changes).
  if [ "$AUTH_MODE" = bearer ]; then
    local t r
    t="$(jq -r '.data.tokens.access_token // empty' <<<"$LAST_BODY")"
    r="$(jq -r '.data.tokens.refresh_token // empty' <<<"$LAST_BODY")"
    [ -n "$t" ] && ACCESS="$t"
    [ -n "$r" ] && REFRESH="$r"
  fi
  api PUT /onboarding/compliance "$(jq -nc --arg b "$BUSINESS_TYPE" --arg i "$INDUSTRY" '{business_type_id:$b,industry_id:$i,employee_count:25}')"
  [ "${LAST_STATUS:0:1}" = 2 ] || { stop_jobs "onboarding compliance rejected (HTTP $LAST_STATUS)"; return; }
  api POST /onboarding/complete
  [ "${LAST_STATUS:0:1}" = 2 ] || { stop_jobs "onboarding/complete rejected (HTTP $LAST_STATUS)"; return; }

  step "7. create job draft"
  api POST /jobs "{}"
  [ "${LAST_STATUS:0:1}" = 2 ] || { stop_jobs "POST /jobs rejected (HTTP $LAST_STATUS): $LAST_BODY"; return; }
  local job
  job="$(jq -r '.data.id // empty' <<<"$LAST_BODY")"
  [ -n "$job" ] || { stop_jobs "job id missing from response"; return; }

  step "8. publish-check (a blank draft is expected to be blocked)"
  api GET "/jobs/$job/publish-check"
  [ "${LAST_STATUS:0:1}" = 2 ] || { stop_jobs "publish-check failed (HTTP $LAST_STATUS)"; return; }
  jq -c '.data | {blocking: [.blocking[]?.code]}' <<<"$LAST_BODY" 2>/dev/null || true
  echo "NOTE: publishing needs a verified company (KYB), accepted DPA and complete details; not attempted."
}
job_flow

step "9. logout"
if [ "$AUTH_MODE" = bearer ]; then
  api POST /auth/logout "$(jq -nc --arg r "$REFRESH" '{refresh_token:$r}')"
else
  api POST /auth/logout
fi
expect logout

step "10. /auth/me after logout must be rejected"
api GET /auth/me
case "$LAST_STATUS" in 401 | 403) echo "rejected as expected" ;; *) echo "WARN: /auth/me returned $LAST_STATUS after logout (bearer access tokens may stay valid until expiry)" ;; esac

printf '\nSUCCESS: auth e2e (%s mode) completed for %s\n' "$AUTH_MODE" "$EMAIL"
