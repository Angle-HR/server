#!/usr/bin/env bash
# Admin TOTP end to end through the admin BFF (curl + jq + `go run ./cmd/tools/totpcode` as the authenticator app).
#
#   Needs: IDENTITY_REMOTE=on on the monolith, UPSTREAM_IDENTITY_URL=http://identity:8080 on the admin-bff, the
#   identity service, mailhog. Works with ADMIN_TOTP_REQUIRED unset/false or true (the forced-enrolment gate is only
#   asserted when the login response says totp_enrollment_required).
#
#   1  bootstrap admin logs in (password only), enrols: wrong confirm code refused, right one enables TOTP
#   2  logout; password-only login yields totp_required and NO session (edge and service refuse the admin routes)
#   3  wrong code (attempts do not issue a session), right code completes login; pre-auth is single use
#   4  edge behaviour: BFF refuses protected admin paths without a cookie; product paths are 404 on the admin BFF
#   5  lockout: five wrong codes, then even the right code is refused (429)
#   6  superadmin reset: a second admin (invite) enrols, the superadmin resets them, they sign in with a password only
#   7  audit log has enrol / verify / fail / reset entries
#
# Env: ADMIN_BASE (admin-bff API base, default http://localhost:8082/api/v1), MAILHOG, ADMIN_EMAIL, ADMIN_PASSWORD.
set -euo pipefail
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/e2e-common.sh"
e2e_url ADMIN_BASE ADMIN_BFF_PORT 8082 /api/v1
e2e_url MAILHOG MAILHOG_UI_PORT 8025
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@anglehr.local}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-changeme123}"
STAFF="totp-staff-$(date +%s)-$RANDOM@example.com"
STAFF_PASS="staff-password-123"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
J1="$(mktemp)"; J2="$(mktemp)"; J3="$(mktemp)"
trap 'rm -f "$J1" "$J2" "$J3"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
step() { printf '\n=== %s ===\n' "$1"; }
j() { jq -nc "$@"; }
TOTPBIN="$(mktemp)"; (cd "$ROOT" && go build -o "$TOTPBIN" ./cmd/tools/totpcode) || fail "build totpcode"
trap 'rm -f "$J1" "$J2" "$J3" "$TOTPBIN"' EXIT
totp() { "$TOTPBIN" "$@"; }

# req JAR METHOD PATH [BODY] [CSRF] -> LAST_STATUS, LAST_BODY, LAST_HDRS (file)
LAST_HDRS="$(mktemp)"
req() {
  local jar="$1" method="$2" path="$3" body="${4:-}" csrf="${5:-}" args=(-sS -D "$LAST_HDRS" -o - -b "$1" -c "$1" -X "$2")
  args+=(-H 'Content-Type: application/json')
  [ -n "$csrf" ] && args+=(-H "X-CSRF-Token: $csrf")
  [ -n "$body" ] && args+=(-d "$body")
  LAST_BODY="$(curl "${args[@]}" "$ADMIN_BASE$path")"
  LAST_STATUS="$(head -1 "$LAST_HDRS" | awk '{print $2}')"
  printf '%s %s -> %s %s\n' "$method" "$path" "$LAST_STATUS" "$(printf '%s' "$LAST_BODY" | jq -c 'if .error then {error:.error.code,details:.error.details} else (.data|if type=="object" then del(.csrf_token,.secret,.otpauth_url) else . end) end' 2>/dev/null | cut -c1-200)"
}
want() { [ "$LAST_STATUS" = "$1" ] || fail "$2: expected HTTP $1, got $LAST_STATUS: $LAST_BODY"; }
has_cookie() { grep -q "$2" "$1"; }

step "0. preflight"
curl -fsS "${ADMIN_BASE%/api/v1}/healthz" >/dev/null || fail "admin-bff not healthy"
curl -fsS "$MAILHOG/api/v2/messages?limit=1" >/dev/null || fail "mailhog not reachable"

step "1. bootstrap admin: password login, enrol, confirm"
req "$J1" POST /admin/auth/login "$(j --arg e "$ADMIN_EMAIL" --arg p "$ADMIN_PASSWORD" '{email:$e,password:$p}')"; want 200 login
PRE_ENROLLED=0
if [ "$(jq -r '.data.totp_required // false' <<<"$LAST_BODY")" = true ]; then
  # An earlier e2e script on this stack (e2e-apply, e2e-identity) already enrolled the bootstrap admin: reuse its secret.
  SECRET="${ADMIN_TOTP_SECRET:-}"; [ -z "$SECRET" ] && [ -f "$E2E_TOTP_FILE" ] && SECRET="$(cat "$E2E_TOTP_FILE")"
  [ -n "$SECRET" ] || fail "bootstrap admin already has TOTP and no secret is known: use a fresh stack (down -v) or set ADMIN_TOTP_SECRET"
  PRE_ENROLLED=1
  req "$J1" POST /admin/auth/login/totp "$(j --arg c "$(totp "$SECRET")" '{code:$c}')"; want 200 "login with the stored TOTP secret"
fi
CSRF="$(jq -r '.data.csrf_token' <<<"$LAST_BODY")"; [ -n "$CSRF" ] && [ "$CSRF" != null ] || fail "no csrf"
if [ "$PRE_ENROLLED" = 1 ]; then
  echo "bootstrap admin was enrolled earlier on this stack: skipping the enrolment assertions"
  req "$J1" GET "/admin/users?limit=1"; want 200 "users with TOTP session"
else
if [ "$(jq -r '.data.totp_enrollment_required // false' <<<"$LAST_BODY")" = true ]; then
  echo "ADMIN_TOTP_REQUIRED is on: password-only session may only enrol"
  req "$J1" GET "/admin/users?limit=1"; want 403 "gate on /admin/users"
  [ "$(jq -r '.error.code' <<<"$LAST_BODY")" = TOTP_ENROLLMENT_REQUIRED ] || fail "gate code: $LAST_BODY"
  req "$J1" GET /admin/auth/me; want 200 "me while gated"
  [ "$(jq -r '.data.totp_enrollment_required' <<<"$LAST_BODY")" = true ] || fail "me must flag enrolment"
  req "$J1" POST /admin/auth/totp/disable "$(j --arg p "$ADMIN_PASSWORD" '{code:"123456",password:$p}')" "$CSRF"; want 403 "disable while required"
else
  req "$J1" GET "/admin/users?limit=1"; want 200 "users before enrolment (TOTP not required)"
fi
req "$J1" POST /admin/auth/totp/enroll "" "$CSRF"; want 200 enroll
SECRET="$(jq -r '.data.secret' <<<"$LAST_BODY")"
jq -e '.data.otpauth_url|startswith("otpauth://totp/")' <<<"$LAST_BODY" >/dev/null || fail "no otpauth url"
req "$J1" POST /admin/auth/totp/enroll "" "$CSRF"; want 200 "re-enroll before confirm replaces the pending secret"
SECRET="$(jq -r '.data.secret' <<<"$LAST_BODY")"
req "$J1" POST /admin/auth/totp/confirm '{"code":"000000"}' "$CSRF"; want 400 "wrong confirm code"
req "$J1" POST /admin/auth/totp/confirm "$(j --arg c "$(totp "$SECRET")" '{code:$c}')" "$CSRF"; want 200 "confirm"
CSRF="$(jq -r '.data.csrf_token' <<<"$LAST_BODY")"
req "$J1" GET "/admin/users?limit=1"; want 200 "users after enrolment"
req "$J1" POST /admin/auth/totp/enroll "" "$CSRF"; want 409 "enrol twice"
( umask 077; printf '%s\n' "$SECRET" >"$E2E_TOTP_FILE" )
fi

step "2. logout, password-only login gives no session"
req "$J1" POST /admin/auth/logout "" "$CSRF"; want 200 logout
req "$J2" POST /admin/auth/login "$(j --arg e "$ADMIN_EMAIL" --arg p "$ADMIN_PASSWORD" '{email:$e,password:$p}')"; want 200 login
[ "$(jq -r '.data.totp_required' <<<"$LAST_BODY")" = true ] || fail "expected totp_required"
[ "$(jq -r '.data.expires_in' <<<"$LAST_BODY")" = 300 ] || fail "expires_in"
jq -e '.data.csrf_token == null' <<<"$LAST_BODY" >/dev/null || fail "csrf issued before the code"
has_cookie "$J2" openhr_admin_preauth || fail "no admin pre-auth cookie"
has_cookie "$J2" openhr_admin_session && fail "a session cookie was issued before the code"
req "$J2" GET /admin/auth/me; want 401 "me with pre-auth only"
req "$J2" GET "/admin/users?limit=1"; want 401 "users with pre-auth only"
PRE="$(awk '$6=="openhr_admin_preauth"{print $7}' "$J2")"; [ -n "$PRE" ] || fail "cannot read pre-auth cookie"
# a pre-auth value replayed as the session cookie never authenticates
curl -s -o /dev/null -w '%{http_code}' -H "Cookie: openhr_admin_session=$PRE" "$ADMIN_BASE/admin/auth/me" | grep -q 401 || fail "pre-auth accepted as session"

step "3. wrong code, right code, single use"
req "$J2" POST /admin/auth/login/totp '{"code":"000000"}'; want 400 "wrong code"
has_cookie "$J2" openhr_admin_session && fail "session after wrong code"
req "$J2" POST /admin/auth/login/totp "$(j --arg c "$(totp "$SECRET")" '{code:$c}')"; want 200 "right code"
CSRF2="$(jq -r '.data.csrf_token' <<<"$LAST_BODY")"; [ -n "$CSRF2" ] && [ "$CSRF2" != null ] || fail "no csrf after totp"
has_cookie "$J2" openhr_admin_session || fail "no session cookie after totp"
req "$J2" GET /admin/auth/me; want 200 "me after totp"
[ "$(jq -r '.data.totp_enabled' <<<"$LAST_BODY")" = true ] || fail "me.totp_enabled"
req "$J2" GET "/admin/users?limit=1"; want 200 "users after totp"
curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -H "Cookie: openhr_admin_preauth=$PRE" \
  -d "$(j --arg c "$(totp "$SECRET")" '{code:$c}')" "$ADMIN_BASE/admin/auth/login/totp" | grep -q 401 || fail "pre-auth reusable"
echo "pre-auth is single use"

step "4. edge behaviour (admin BFF)"
for p in /admin/auth/me /admin/auth/totp/enroll /admin/users; do
  s="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$ADMIN_BASE$p" -H 'Content-Type: application/json' -d '{}')"
  case "$s" in 401|405) echo "$p without cookie -> $s";; *) fail "$p without cookie -> $s";; esac
done
s="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$ADMIN_BASE/admin/auth/login/totp" -H 'Content-Type: application/json' -d '{"code":"123456"}')"
[ "$s" = 401 ] || fail "login/totp without pre-auth -> $s"
s="$(curl -s -o /dev/null -w '%{http_code}' "$ADMIN_BASE/auth/me")"; [ "$s" = 404 ] || fail "product path on the admin BFF -> $s"
echo "login/totp without pre-auth 401, product paths 404 on the admin edge"

step "5. lockout after five wrong codes"
req "$J3" POST /admin/auth/login "$(j --arg e "$ADMIN_EMAIL" --arg p "$ADMIN_PASSWORD" '{email:$e,password:$p}')"; want 200 login
for _ in 1 2 3 4 5; do req "$J3" POST /admin/auth/login/totp '{"code":"000000"}'; want 400 "wrong code"; done
req "$J3" POST /admin/auth/login/totp "$(j --arg c "$(totp "$SECRET")" '{code:$c}')"; want 429 "right code while locked"
[ "$(jq -r '.error.code' <<<"$LAST_BODY")" = too_many_attempts ] || fail "lockout code"
has_cookie "$J3" openhr_admin_session && fail "session while locked"
echo "locked: 429 too_many_attempts, no session"
# The session from step 3 is unaffected (the lockout guards new code checks).
req "$J2" GET /admin/auth/me; want 200 "established session still valid"

step "6. superadmin reset of another admin"
ROLE="$(curl -s -b "$J2" "$ADMIN_BASE/admin/roles" | jq -r '[.data[]|select(.slug=="superadmin")][0].slug // .data[0].slug')"
req "$J2" POST /admin/staff "$(j --arg e "$STAFF" --arg r "$ROLE" '{email:$e,name:"TOTP Staff",roles:[$r]}')" "$CSRF2"; want 201 "invite"
STAFF_ID="$(jq -r '.data.id // .data.staff.id' <<<"$LAST_BODY")"; [ -n "$STAFF_ID" ] && [ "$STAFF_ID" != null ] || fail "no staff id: $LAST_BODY"
TOKEN=""
for _ in $(seq 1 30); do
  body="$(curl -sG "$MAILHOG/api/v2/search" --data-urlencode kind=to --data-urlencode "query=$STAFF" | jq -r '.items[0].Content.Body // empty')"
  TOKEN="$(printf '%s' "$body" | tr -d '\r' | perl -0pe 's/=\n//g; s/=3D/=/g' | grep -oE 'token=[A-Za-z0-9_-]+' | head -1 | cut -d= -f2 || true)"
  [ -n "$TOKEN" ] && break; sleep 1
done
[ -n "$TOKEN" ] || fail "no invite token in mailhog"
SJ="$(mktemp)"; trap 'rm -f "$J1" "$J2" "$J3" "$TOTPBIN" "$SJ"' EXIT
req "$SJ" POST /admin/auth/accept-invite "$(j --arg t "$TOKEN" --arg p "$STAFF_PASS" '{token:$t,name:"TOTP Staff",password:$p}')"; want 200 "accept invite"
SCSRF="$(jq -r '.data.csrf_token' <<<"$LAST_BODY")"
req "$SJ" POST /admin/auth/totp/enroll "" "$SCSRF"; want 200 "staff enroll"
SSECRET="$(jq -r '.data.secret' <<<"$LAST_BODY")"
req "$SJ" POST /admin/auth/totp/confirm "$(j --arg c "$(totp "$SSECRET")" '{code:$c}')" "$SCSRF"; want 200 "staff confirm"
SCSRF="$(jq -r '.data.csrf_token' <<<"$LAST_BODY")"
req "$SJ" POST "/admin/staff/$STAFF_ID/totp/reset" "" "$SCSRF"; want 403 "a non-self reset needs permission/superadmin and not oneself"
req "$J2" POST "/admin/staff/$STAFF_ID/totp/reset" "" "$CSRF2"; want 200 "superadmin reset"
req "$SJ" GET /admin/auth/me; want 401 "reset admin's session was revoked"
rm -f "$SJ"; SJ="$(mktemp)"
req "$SJ" POST /admin/auth/login "$(j --arg e "$STAFF" --arg p "$STAFF_PASS" '{email:$e,password:$p}')"; want 200 "login after reset"
jq -e '.data.totp_required == null' <<<"$LAST_BODY" >/dev/null || fail "reset admin still asked for TOTP"
req "$J2" POST "/admin/staff/00000000-0000-4000-8000-000000000000/totp/reset" "" "$CSRF2"; want 404 "unknown admin"

step "7. audit log"
req "$J2" GET "/admin/audit-logs?limit=100"; want 200 audit
for a in admin.totp.enroll_start admin.totp.enroll admin.totp.verify admin.totp.fail admin.totp.reset; do
  jq -e --arg a "$a" '[.data[]?|select(.action==$a)]|length>0' <<<"$LAST_BODY" >/dev/null || fail "audit action $a missing"
  echo "audit has $a"
done
echo; echo "SUCCESS: admin TOTP e2e"
