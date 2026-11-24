#!/usr/bin/env bash
# Identity service checks against the local stack (curl + jq), through the web-bff.
#
#   Needs: IDENTITY_REMOTE=on on the monolith, UPSTREAM_IDENTITY_URL on the BFF, SESSION_VALIDATION=on on the BFF and
#   IDENTITY_EVENTS=on on the identity service (see docs/microservices/identity.md).
#
#   1  monolith no longer serves /auth (direct request 404), the BFF reaches the service
#   2  BFF edge: protected paths without a cookie are refused by the BFF itself, public paths are not
#   3  signup + verify + login through the BFF, product cookie passes the edge, is refused on /admin
#   4  admin login (admin cookie/namespace), product cookie under the admin name is refused, /admin/auth/me works
#   5  staff invite through the service: invite mail in mailhog, identity.staff.invited.v1 on the bus
#   6  identity.user.registered / email_verified events on the bus (NATS monitoring port)
#
# Env: BASE (web-bff API base, default http://localhost:8081/api/v1), MAILHOG, NATS_MON (default http://localhost:8222), ADMIN_EMAIL, ADMIN_PASSWORD.
#      Ports default from WEB_BFF_PORT, MAILHOG_UI_PORT, NATS_MONITOR_PORT; with another COMPOSE_PROJECT_NAME they must be set.
#      With ADMIN_TOTP_REQUIRED=true the bootstrap admin is enrolled / completes TOTP (scripts/lib/e2e-common.sh).
set -euo pipefail
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/e2e-common.sh"
e2e_url BASE WEB_BFF_PORT 8081 /api/v1
e2e_url MAILHOG MAILHOG_UI_PORT 8025
e2e_url NATS_MON NATS_MONITOR_PORT 8222
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@anglehr.local}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-changeme123}"
EMAIL="e2e-identity-$(date +%s)-$RANDOM@example.com"
STAFF="staff-$(date +%s)-$RANDOM@example.com"
PASS="test-password-123"
JAR="$(mktemp)"; AJAR="$(mktemp)"
trap 'rm -f "$JAR" "$AJAR"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
step() { printf '\n=== %s ===\n' "$1"; }
stream_msgs() { curl -s "$NATS_MON/jsz?streams=true" | jq -r '[.account_details[]?.stream_detail[]? | select(.name=="IDENTITY") | .state.messages] | add // 0'; }
nsubjects() { curl -s "$NATS_MON/jsz?streams=true" | jq -r '[.account_details[]?.stream_detail[]? | select(.name=="IDENTITY") | .state.num_subjects] | add // 0'; }

step "1. the BFF reaches the identity service"
s="$(code -X POST "$BASE/auth/login" -H 'Content-Type: application/json' -d '{}')"
case "$s" in 400|401|422) echo "BFF /auth/login -> $s (service answered)";; *) fail "BFF /auth/login -> $s";; esac

step "2. BFF edge refuses protected paths without a cookie, public paths pass"
for p in /auth/me /onboarding/status /jobs /admin/users /admin/auth/me /admin/verification; do
  [ "$(code "$BASE$p")" = 401 ] || fail "$p without cookie not refused"
  echo "$p -> 401"
done
curl -s "$BASE/auth/me" | jq -e '.error.message=="authentication required"' >/dev/null || fail "401 is not the BFF's"
for p in /countries /onboarding/industries /public/jobs/nope; do
  s="$(code "$BASE$p")"; [ "$s" != 401 ] || fail "public $p refused"; echo "$p -> $s"
done
[ "$(code -X POST "$BASE/admin/auth/login" -H 'Content-Type: application/json' -d '{}')" != 401 ] || fail "admin login refused at the edge"

step "3. product signup, verify, login through the BFF"
j() { jq -nc "$@"; }
curl -s -X POST "$BASE/auth/signup" -H 'Content-Type: application/json' -d "$(j --arg e "$EMAIL" --arg p "$PASS" '{email:$e,password:$p}')" | tee /tmp/idn-signup.$$ | jq -e '.data.verification_session_id' >/dev/null || fail "signup"
SID="$(jq -r '.data.verification_session_id' /tmp/idn-signup.$$)"; rm -f /tmp/idn-signup.$$
CODE=""
for _ in $(seq 1 30); do
  CODE="$(curl -sG "$MAILHOG/api/v2/search" --data-urlencode kind=to --data-urlencode "query=$EMAIL" | jq -r '.items[0].Content.Body // empty' \
    | sed 's/<[^>]*>/ /g' | grep -oE '(^|[^0-9])[0-9]{6}([^0-9]|$)' | head -1 | grep -oE '[0-9]{6}' || true)"
  [ -n "$CODE" ] && break; sleep 1
done
[ -n "$CODE" ] || fail "no verification mail (notifications events transport)"
curl -s -c "$JAR" -X POST "$BASE/auth/verify-email" -H 'Content-Type: application/json' \
  -d "$(j --arg s "$SID" --arg c "$CODE" '{verification_session_id:$s,code:$c}')" | jq -e '.data.csrf_token' >/dev/null || fail "verify-email"
grep -q openhr_session "$JAR" || fail "no session cookie"
[ "$(code -b "$JAR" "$BASE/auth/me")" = 200 ] || fail "valid cookie did not pass the edge"
echo "product cookie passes; /auth/me 200"
[ "$(code -b "$JAR" "$BASE/admin/users")" = 401 ] || fail "product cookie opened /admin"
echo "product cookie on /admin -> 401"

step "4. admin login"
e2e_admin_login "$AJAR" "$BASE" "$ADMIN_EMAIL" "$ADMIN_PASSWORD" || fail "admin login"
ACSRF="$E2E_ADMIN_CSRF"
grep -q openhr_admin_session "$AJAR" || fail "no admin cookie"
[ "$(code -b "$AJAR" "$BASE/admin/auth/me")" = 200 ] || fail "admin cookie refused"
[ "$(code -b "$AJAR" "$BASE/auth/me")" = 401 ] || fail "admin cookie opened a product path"
echo "admin cookie: /admin/auth/me 200, /auth/me 401"
[ "$(code -b "$AJAR" "$BASE/admin/users?limit=5")" = 200 ] || fail "admin users list"

step "5. staff invite"
ROLE="$(curl -s -b "$AJAR" "$BASE/admin/roles" | jq -r '.data[0].slug')"
before="$(stream_msgs)"
curl -s -b "$AJAR" -X POST "$BASE/admin/staff" -H 'Content-Type: application/json' -H "X-CSRF-Token: $ACSRF" \
  -d "$(j --arg e "$STAFF" --arg r "$ROLE" '{email:$e,name:"E2E Staff",roles:[$r]}')" | jq -e '.data.id' >/dev/null || fail "staff invite"
for _ in $(seq 1 30); do
  n="$(curl -sG "$MAILHOG/api/v2/search" --data-urlencode kind=to --data-urlencode "query=$STAFF" | jq '.total')"; [ "$n" -ge 1 ] && break; sleep 1
done
[ "$n" -ge 1 ] || fail "no staff invite mail"
echo "invite mail delivered to $STAFF"

step "6. identity events on the bus"
sleep 3
after="$(stream_msgs)"; echo "IDENTITY stream: $before -> $after messages, $(nsubjects) distinct subjects"
[ "$after" -gt "$before" ] || fail "no identity.staff.invited event"
[ "$(nsubjects)" -ge 3 ] || fail "expected registered, email_verified and staff.invited subjects"
echo; echo "SUCCESS: identity e2e"
