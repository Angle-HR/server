#!/usr/bin/env bash
set -euo pipefail

# Tears down the Angle HR dev stack (namespace, workloads, and PVCs).

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=lib/k8s-local.sh
source "${ROOT_DIR}/deploy/scripts/lib/k8s-local.sh"

NAMESPACE="${NAMESPACE:-anglehr}"

k8s_select_local_context

if kubectl get namespace "$NAMESPACE" >/dev/null 2>&1; then
	echo "Deleting namespace $NAMESPACE (pods, jobs, statefulsets, PVCs)..."
	kubectl delete namespace "$NAMESPACE" --wait=true
	echo "Namespace $NAMESPACE deleted."
else
	echo "Namespace $NAMESPACE does not exist; nothing to delete."
fi
