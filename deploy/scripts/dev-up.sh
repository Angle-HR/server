#!/usr/bin/env bash
set -euo pipefail

# Bootstraps a local Kubernetes dev stack mirroring docker-compose.
# Requires: docker, kubectl, and either kind or Docker Desktop Kubernetes.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=lib/k8s-local.sh
source "${ROOT_DIR}/deploy/scripts/lib/k8s-local.sh"

NAMESPACE="${NAMESPACE:-anglehr}"
CLUSTER_NAME="${CLUSTER_NAME:-anglehr-dev}"
IMAGE_TAG="${IMAGE_TAG:-dev}"
KUSTOMIZE_JOBS="${ROOT_DIR}/deploy/k8s/overlays/dev-jobs"

cd "$ROOT_DIR"

if ! command -v kubectl >/dev/null 2>&1; then
	echo "kubectl is required" >&2
	exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
	echo "docker is required" >&2
	exit 1
fi

setup_local_cluster() {
	k8s_select_local_context
	k8s_require_namespace_access

	if ! k8s_ingress_ready; then
		k8s_install_ingress_nginx
	fi

	k8s_install_dashboard "${ROOT_DIR}/deploy/k8s/components/dashboard"
}

build_and_load_images() {
	echo "Building application images..."
	docker build -f Dockerfile --target server -t "ghcr.io/angle-hr/server:${IMAGE_TAG}" .
	docker build -f Dockerfile --target email-worker -t "ghcr.io/angle-hr/server-email-worker:${IMAGE_TAG}" .
	docker build -f Dockerfile --target upload-worker -t "ghcr.io/angle-hr/server-upload-worker:${IMAGE_TAG}" .
	docker build -f Dockerfile.migrate -t "ghcr.io/angle-hr/server-migrate:${IMAGE_TAG}" .
	docker build -f deploy/docker/Dockerfile.fluvio-ui \
		--build-arg VITE_API_BASE_URL="${FLUVIO_API_BASE_URL:-http://api.anglehr.local}" \
		-t "ghcr.io/angle-hr/fluvio-ui:${IMAGE_TAG}" .

	local ctx
	ctx="$(k8s_current_context)"
	if [[ "$ctx" == kind-* ]]; then
		local cluster_name="${CLUSTER_NAME}"
		cluster_name="${ctx#kind-}"
		echo "Loading images into kind cluster $cluster_name..."
		kind load docker-image "ghcr.io/angle-hr/server:${IMAGE_TAG}" --name "$cluster_name"
		kind load docker-image "ghcr.io/angle-hr/server-email-worker:${IMAGE_TAG}" --name "$cluster_name"
		kind load docker-image "ghcr.io/angle-hr/server-upload-worker:${IMAGE_TAG}" --name "$cluster_name"
		kind load docker-image "ghcr.io/angle-hr/server-migrate:${IMAGE_TAG}" --name "$cluster_name"
		kind load docker-image "ghcr.io/angle-hr/fluvio-ui:${IMAGE_TAG}" --name "$cluster_name"
	else
		echo "Using local Docker images (context $ctx — no kind load needed)."
	fi
}

wait_for_statefulsets() {
	echo "Waiting for Postgres pods..."
	kubectl wait --namespace="$NAMESPACE" \
		--for=condition=ready pod \
		--selector=app.kubernetes.io/component=database \
		--timeout=600s
}

wait_for_redis() {
	echo "Waiting for Redis..."
	kubectl wait --namespace="$NAMESPACE" \
		--for=condition=available deployment/redis \
		--timeout=120s
}

run_init_jobs() {
	echo "Running database migrations..."
	kubectl delete job migrate --namespace="$NAMESPACE" --ignore-not-found
	kubectl apply -k "$KUSTOMIZE_JOBS" --namespace="$NAMESPACE"
	kubectl wait --namespace="$NAMESPACE" --for=condition=complete job/migrate --timeout=600s
}

setup_local_cluster
build_and_load_images

SKIP_K8S_CONTEXT_SELECT=1 bash "${ROOT_DIR}/deploy/scripts/create-dev-secret.sh"

echo "Applying data layer (Postgres, Redis)..."
kubectl apply -k "${ROOT_DIR}/deploy/k8s/components/postgres" --namespace="$NAMESPACE"
kubectl apply -k "${ROOT_DIR}/deploy/k8s/components/redis" --namespace="$NAMESPACE"
wait_for_statefulsets
wait_for_redis

run_init_jobs

echo "Applying application tier..."
kubectl apply -k "${ROOT_DIR}/deploy/k8s/overlays/dev" --namespace="$NAMESPACE"

ctx="$(k8s_current_context)"
echo ""
echo "Dev stack deployed to namespace: $NAMESPACE (context: $ctx)"
echo ""
echo "Add to /etc/hosts:"
echo "  127.0.0.1 api.anglehr.local fluvio.anglehr.local dashboard.anglehr.local"
echo ""
echo "Endpoints:"
echo "  API:    http://api.anglehr.local/api/v1/countries"
echo "  Fluvio: http://fluvio.anglehr.local"
k8s_print_dashboard_access
echo ""
echo "Verify: kubectl get pods -n $NAMESPACE"
