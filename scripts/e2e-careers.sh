#!/usr/bin/env bash
# End-to-end check of the public careers page (ATS plan, phase 1) against the local stack (curl + jq).
#
#   company profile (slug, name, about, website) -> reserved and malformed values are refused -> slug check
#   -> anonymous GET /public/companies/{slug} lists the job made by e2e-apply.sh -> filters and paging
#   -> renaming the slug frees the old address
#
# Needs a company with a published job: run `STATE_FILE=state.env scripts/e2e-apply.sh` first and pass the file.
# The stack is the one of docs/microservices/local-dev.md (web-bff in front of org and hiring).
#
# Env: STATE_FILE (required), BASE (default http://localhost:8081/api/v1), SLUG (default e2e-<timestamp>).
set -euo pipefail

. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/e2e-common.sh"
e2e_url BASE WEB_BFF_PORT 8081 /api/v1
STATE_FILE="${STATE_FILE:?set STATE_FILE to the file written by e2e-apply.sh}"
# shellcheck disable=SC1090
. "$STATE_FILE"
SLUG="${SLUG:-e2e-$(date +%s)}"
JAR="$(mktemp)"; ANON_JAR="$(mktemp)"; CSRF=""
trap 'rm -f "$JAR" "$ANON_JAR"' EXIT
LAST_STATUS=""; LAST_BODY=""

step() { printf '\n=== %s ===\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }
ok() { printf 'ok: %s\n' "$1"; }
call() { # call JAR CSRF METHOD PATH [BODY]
  local jar="$1" csrf="$2" method="$3" path="$4" body="${5:-}" out
  local args=(-sS -X "$method" -c "$jar" -b "$jar" -w '\n%{http_code}' "$BASE$path")
  if [ -n "$csrf" ] && [ "$method" != GET ]; then args+=(-H "X-CSRF-Token: $csrf"); fi
  if [ -n "$body" ]; then args+=(-H 'Content-Type: application/json' -d "$body"); fi
  out="$(curl "${args[@]}")"
  LAST_STATUS="${out##*$'\n'}"; LAST_BODY="${out%$'\n'*}"
  printf '%s %s -> %s\n' "$method" "$path" "$LAST_STATUS"
  printf '%s' "$LAST_BODY" | jq -c . 2>/dev/null | cut -c1-240 || true
}
api() { call "$JAR" "$CSRF" "$@"; }
anon() { call "$ANON_JAR" "" "$@"; }
want() { [ "$LAST_STATUS" = "$1" ] || fail "$2 (want HTTP $1, got $LAST_STATUS): $LAST_BODY"; }
profile_body() { # slug
  jq -nc --arg s "$1" '{slug:$s, display_name:"E2E Careers Ltd", about:"We make things, carefully.", website:"https://example.com", logo_url:""}'
}

step "0. login as the company from the apply run"
call "$JAR" "" POST /auth/login "$(jq -nc --arg e "$EMAIL" --arg p "$PASS" '{email:$e,password:$p}')"; want 200 login
CSRF="$(jq -r '.data.csrf_token' <<<"$LAST_BODY")"

step "1. no profile yet: the page offers a suggestion"
api GET /organization/profile; want 200 "get profile"
[ "$(jq -r '.data.slug' <<<"$LAST_BODY")" = "" ] || fail "a new company has no slug"
ok "no slug chosen yet (suggested: $(jq -r '.data.suggested_slug // "none"' <<<"$LAST_BODY"))"

step "2. refused values"
api PUT /organization/profile "$(profile_body admin)"; want 400 "reserved slug"
api PUT /organization/profile "$(profile_body 'Bad Slug')"; want 400 "malformed slug"
api PUT /organization/profile "$(jq -nc --arg s "$SLUG" '{slug:$s, website:"http://insecure.example"}')"; want 400 "http website"
anon GET "/public/companies/$SLUG"; want 404 "no page before the profile is saved"
ok "reserved, malformed and insecure values are refused"

step "3. slug check and save"
api GET "/organization/profile/slug-check?slug=$SLUG"; want 200 "slug check"
[ "$(jq -r '.data.available' <<<"$LAST_BODY")" = true ] || fail "$SLUG should be available"
api PUT /organization/profile "$(profile_body "$SLUG")"; want 200 "save profile"
[ "$(jq -r '.data.slug' <<<"$LAST_BODY")" = "$SLUG" ] || fail "slug not saved"
ok "profile saved at /company/$SLUG"

step "4. the public page"
anon GET "/public/companies/$SLUG"; want 200 "public page"
[ "$(jq -r '.data.name' <<<"$LAST_BODY")" = "E2E Careers Ltd" ] || fail "display name"
[ "$(jq -r --arg id "$PUBLIC_ID" '[.data.jobs[].public_id] | index($id) != null' <<<"$LAST_BODY")" = true ] || fail "the published job is not listed"
ok "published job $PUBLIC_ID is listed"
anon GET "/public/companies/$SLUG?limit=1"; want 200 "paged"
[ "$(jq -r '.data.jobs | length' <<<"$LAST_BODY")" = 1 ] || fail "limit=1 returns one job"
anon GET "/public/companies/$SLUG?workplace_type=moon"; want 400 "unknown filter"
anon GET "/public/companies/$SLUG?cursor=garbage"; want 400 "bad cursor"
anon GET "/public/companies/$SLUG?q=zzzz-no-such-title"; want 200 "no match"
[ "$(jq -r '.data.total' <<<"$LAST_BODY")" = 0 ] || fail "a search with no match is empty"
ok "filters, paging and bad input behave"

step "5. renaming frees the old address"
NEW="$SLUG-hr"
api PUT /organization/profile "$(profile_body "$NEW")"; want 200 "rename"
anon GET "/public/companies/$NEW"; want 200 "new address"
anon GET "/public/companies/$SLUG"; want 404 "old address"
api PUT /organization/profile "$(profile_body "")"; want 400 "an address cannot be removed once chosen"
ok "old address is gone, the new one serves"

printf '\nSUCCESS: careers page e2e completed (company %s, slug %s)\n' "$EMAIL" "$NEW"
