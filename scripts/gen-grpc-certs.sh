#!/usr/bin/env bash
# Generates a throwaway development CA and one certificate per service for GRPC_AUTH=mtls on the local compose
# stack. Output: deploy/local/certs/{ca.crt,ca.key,<service>/{tls.crt,tls.key,ca.crt}} (gitignored).
#
#   scripts/gen-grpc-certs.sh [OUT_DIR]      (or: make local-grpc-certs)
#
# Re-running replaces the CA, so restart the services afterwards. Not for production.
set -euo pipefail
cd "$(dirname "$0")/.."
OUT="${1:-${GRPC_CERT_DIR:-deploy/local/certs}}"
rm -rf "$OUT"
exec go run ./cmd/tools/grpccerts -out "$OUT" -days "${GRPC_CERT_DAYS:-30}"
