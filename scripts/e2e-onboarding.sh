#!/usr/bin/env bash
set -euo pipefail

BASE="${BASE:-http://localhost:8080/api/v1}"
EMAIL="${EMAIL:-e2e-$(date +%s)@example.com}"
PASS="${PASS:-test-password-123}"
UK_COUNTRY="a1b2c3d4-e5f6-4789-a012-3456789abcde"
COMPANY_ROLE="60000000-0000-4000-8000-000000000001"
BUSINESS_TYPE="61000000-0000-4000-8000-000000000001"
INDUSTRY="62000000-0000-4000-8000-000000000001"

pretty() { python3 -m json.tool 2>/dev/null || cat; }
step() { echo ""; echo "=== $1 ==="; }

step "1. POST /auth/signup ($EMAIL)"
SIGNUP=$(curl -s -X POST "$BASE/auth/signup" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$PASS\"}")
echo "$SIGNUP" | pretty
SESSION_ID=$(echo "$SIGNUP" | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['verification_session_id'])")

step "2. Fetch OTP from Redis"
REDIS_RAW=$(docker compose exec -T redis redis-cli GET "verify:$SESSION_ID")
if [ -z "$REDIS_RAW" ]; then
  echo "ERROR: verification session not found in Redis"
  exit 1
fi
OTP=$(python3 -c "import json,sys; print(json.loads(sys.argv[1])['code'])" "$REDIS_RAW")
echo "OTP=$OTP"

step "3. POST /auth/verify-email"
VERIFY=$(curl -s -X POST "$BASE/auth/verify-email" -H 'Content-Type: application/json' \
  -d "{\"verification_session_id\":\"$SESSION_ID\",\"code\":\"$OTP\"}")
echo "$VERIFY" | pretty
ACCESS=$(echo "$VERIFY" | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['access_token'])")
REFRESH=$(echo "$VERIFY" | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['refresh_token'])")
AUTH="Authorization: Bearer $ACCESS"

step "4. GET /onboarding/status"
curl -s "$BASE/onboarding/status" -H "$AUTH" | pretty

step "5. PUT /onboarding/profile (business)"
curl -s -X PUT "$BASE/onboarding/profile" -H "$AUTH" -H 'Content-Type: application/json' \
  -d "{\"account_type\":\"business\",\"legal_business_name\":\"ANGLE E2E Ltd\",\"legal_full_name\":\"Jerry Oluwasegun\",\"company_role_id\":\"$COMPANY_ROLE\"}" | pretty

step "6. PUT /onboarding/address"
curl -s -X PUT "$BASE/onboarding/address" -H "$AUTH" -H 'Content-Type: application/json' \
  -d "{\"country_id\":\"$UK_COUNTRY\",\"entry_mode\":\"manual\",\"line_1\":\"10 Downing Street\",\"city\":\"London\",\"state_or_county\":\"Greater London\",\"post_code\":\"SW1A 2AA\",\"identification\":{\"registration_number\":\"12345678\"}}" | pretty

step "7. PUT /onboarding/compliance"
curl -s -X PUT "$BASE/onboarding/compliance" -H "$AUTH" -H 'Content-Type: application/json' \
  -d "{\"business_type_id\":\"$BUSINESS_TYPE\",\"industry_id\":\"$INDUSTRY\",\"employee_count\":25}" | pretty

step "8. POST /onboarding/complete"
curl -s -X POST "$BASE/onboarding/complete" -H "$AUTH" | pretty

step "9. GET /onboarding/status (completed)"
curl -s "$BASE/onboarding/status" -H "$AUTH" | pretty

step "10. POST /auth/login"
curl -s -X POST "$BASE/auth/login" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$PASS\"}" | pretty

step "11. POST /auth/refresh"
curl -s -X POST "$BASE/auth/refresh" -H 'Content-Type: application/json' \
  -d "{\"refresh_token\":\"$REFRESH\"}" | pretty

step "12. GET /auth/me"
curl -s "$BASE/auth/me" -H "$AUTH" | pretty

step "13. POST /auth/logout"
curl -s -X POST "$BASE/auth/logout" -H 'Content-Type: application/json' \
  -d "{\"refresh_token\":\"$REFRESH\"}" | pretty

echo ""
echo "SUCCESS: business onboarding E2E completed for $EMAIL"
