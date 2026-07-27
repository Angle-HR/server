#!/usr/bin/env bash
set -euo pipefail

# Creates or updates the anglehr-secrets Secret in the anglehr namespace from a .env file.
# Builds Kubernetes-internal DSNs from POSTGRES_PASSWORD_* and passes through R2 credentials.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=lib/k8s-local.sh
source "${ROOT_DIR}/deploy/scripts/lib/k8s-local.sh"

ENV_FILE="${1:-$ROOT_DIR/.env}"
NAMESPACE="${NAMESPACE:-anglehr}"

if [[ "${SKIP_K8S_CONTEXT_SELECT:-}" != "1" ]]; then
	k8s_select_local_context
fi
k8s_require_namespace_access

if [[ ! -f "$ENV_FILE" ]]; then
	echo "Missing env file: $ENV_FILE" >&2
	echo "Copy .env.example to .env and fill in values." >&2
	exit 1
fi

set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

required=(
	POSTGRES_PASSWORD_UK POSTGRES_PASSWORD_US POSTGRES_PASSWORD_AFRICA POSTGRES_PASSWORD_EU POSTGRES_PASSWORD_ASIA POSTGRES_PASSWORD_GLOBAL
	R2_ACCESS_KEY R2_SECRET_KEY
	ANGLEHR_UK_R2_BUCKET ANGLEHR_US_R2_BUCKET ANGLEHR_AFRICA_R2_BUCKET ANGLEHR_EU_R2_BUCKET ANGLEHR_ASIA_R2_BUCKET
)
for key in "${required[@]}"; do
	if [[ -z "${!key:-}" ]]; then
		echo "$key is required in $ENV_FILE" >&2
		exit 1
	fi
done

if [[ -z "${R2_ENDPOINT:-}" ]]; then
	echo "R2_ENDPOINT is required in $ENV_FILE" >&2
	exit 1
fi

SMTP_PORT="${SMTP_PORT:-587}"
SMTP_HOST="${SMTP_HOST:-}"
SMTP_USER="${SMTP_USER:-}"
SMTP_PASSWORD="${SMTP_PASSWORD:-}"
SMTP_FROM="${SMTP_FROM:-}"
SMTP_FROM_NAME="${SMTP_FROM_NAME:-}"
APP_URL="${APP_URL:-http://app.anglehr.local}"
JWT_SECRET="${JWT_SECRET:-dev-insecure-jwt-secret-change-me}"

R2_ENDPOINT="${R2_ENDPOINT:-}"

REDIS_DB="${REDIS_DB:-0}"
REDIS_URL="redis://redis:6379/${REDIS_DB}"

kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

kubectl create secret generic anglehr-secrets \
	--namespace="$NAMESPACE" \
	--dry-run=client -o yaml \
	--from-literal=POSTGRES_PASSWORD_UK="$POSTGRES_PASSWORD_UK" \
	--from-literal=POSTGRES_PASSWORD_US="$POSTGRES_PASSWORD_US" \
	--from-literal=POSTGRES_PASSWORD_AFRICA="$POSTGRES_PASSWORD_AFRICA" \
	--from-literal=POSTGRES_PASSWORD_EU="$POSTGRES_PASSWORD_EU" \
	--from-literal=POSTGRES_PASSWORD_ASIA="$POSTGRES_PASSWORD_ASIA" \
	--from-literal=POSTGRES_PASSWORD_GLOBAL="$POSTGRES_PASSWORD_GLOBAL" \
	--from-literal=DB_URL_UK="postgres://anglehr:${POSTGRES_PASSWORD_UK}@postgres-uk:5432/anglehr_uk?sslmode=disable" \
	--from-literal=DB_URL_US="postgres://anglehr:${POSTGRES_PASSWORD_US}@postgres-us:5432/anglehr_us?sslmode=disable" \
	--from-literal=DB_URL_AFRICA="postgres://anglehr:${POSTGRES_PASSWORD_AFRICA}@postgres-africa:5432/anglehr_africa?sslmode=disable" \
	--from-literal=DB_URL_EU="postgres://anglehr:${POSTGRES_PASSWORD_EU}@postgres-eu:5432/anglehr_eu?sslmode=disable" \
	--from-literal=DB_URL_ASIA="postgres://anglehr:${POSTGRES_PASSWORD_ASIA}@postgres-asia:5432/anglehr_asia?sslmode=disable" \
	--from-literal=DB_URL_GLOBAL="postgres://anglehr:${POSTGRES_PASSWORD_GLOBAL}@postgres-global:5432/anglehr_global?sslmode=disable" \
	--from-literal=R2_ACCESS_KEY="$R2_ACCESS_KEY" \
	--from-literal=R2_SECRET_KEY="$R2_SECRET_KEY" \
	--from-literal=R2_ENDPOINT="$R2_ENDPOINT" \
	--from-literal=REDIS_URL="$REDIS_URL" \
	--from-literal=JWT_SECRET="$JWT_SECRET" \
	--from-literal=ANGLEHR_UK_POSTGRES_DSN="postgres://anglehr:${POSTGRES_PASSWORD_UK}@postgres-uk:5432/anglehr_uk?sslmode=disable" \
	--from-literal=ANGLEHR_UK_R2_BUCKET="$ANGLEHR_UK_R2_BUCKET" \
	--from-literal=ANGLEHR_US_POSTGRES_DSN="postgres://anglehr:${POSTGRES_PASSWORD_US}@postgres-us:5432/anglehr_us?sslmode=disable" \
	--from-literal=ANGLEHR_US_R2_BUCKET="$ANGLEHR_US_R2_BUCKET" \
	--from-literal=ANGLEHR_AFRICA_POSTGRES_DSN="postgres://anglehr:${POSTGRES_PASSWORD_AFRICA}@postgres-africa:5432/anglehr_africa?sslmode=disable" \
	--from-literal=ANGLEHR_AFRICA_R2_BUCKET="$ANGLEHR_AFRICA_R2_BUCKET" \
	--from-literal=ANGLEHR_EU_POSTGRES_DSN="postgres://anglehr:${POSTGRES_PASSWORD_EU}@postgres-eu:5432/anglehr_eu?sslmode=disable" \
	--from-literal=ANGLEHR_EU_R2_BUCKET="$ANGLEHR_EU_R2_BUCKET" \
	--from-literal=ANGLEHR_ASIA_POSTGRES_DSN="postgres://anglehr:${POSTGRES_PASSWORD_ASIA}@postgres-asia:5432/anglehr_asia?sslmode=disable" \
	--from-literal=ANGLEHR_ASIA_R2_BUCKET="$ANGLEHR_ASIA_R2_BUCKET" \
	--from-literal=SMTP_HOST="$SMTP_HOST" \
	--from-literal=SMTP_PORT="$SMTP_PORT" \
	--from-literal=SMTP_USER="$SMTP_USER" \
	--from-literal=SMTP_PASSWORD="$SMTP_PASSWORD" \
	--from-literal=SMTP_FROM="$SMTP_FROM" \
	--from-literal=SMTP_FROM_NAME="$SMTP_FROM_NAME" \
	--from-literal=APP_URL="$APP_URL" \
	| kubectl apply -f -

echo "Secret anglehr-secrets applied in namespace $NAMESPACE"
