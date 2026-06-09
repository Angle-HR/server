#!/usr/bin/env bash
set -euo pipefail

# Prints a Kubernetes Dashboard login token for the local dev admin-user account.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=lib/k8s-local.sh
source "${ROOT_DIR}/deploy/scripts/lib/k8s-local.sh"

if [[ "${SKIP_K8S_CONTEXT_SELECT:-}" != "1" ]]; then
	k8s_select_local_context
fi

if ! kubectl get namespace kubernetes-dashboard >/dev/null 2>&1; then
	echo "Kubernetes Dashboard is not installed. Run: bash deploy/scripts/dev-up.sh" >&2
	exit 1
fi

token="$(k8s_dashboard_login_token)"
if [[ -z "$token" ]]; then
	echo "Failed to create token for admin-user." >&2
	exit 1
fi

echo "Open: http://dashboard.anglehr.local"
echo "Sign in with Token and paste:"
echo "$token"
