#!/usr/bin/env bash
# Shared helpers for local Kubernetes dev (kind or Docker Desktop).
# Source this file from deploy scripts; do not execute directly.

k8s_current_context() {
	kubectl config current-context 2>/dev/null || true
}

k8s_context_exists() {
	local ctx="$1"
	kubectl config get-contexts -o name 2>/dev/null | grep -Fxq "$ctx"
}

k8s_use_context() {
	local ctx="$1"
	kubectl config use-context "$ctx" >/dev/null
	echo "Using kubectl context: $ctx"
}

k8s_kind_cluster_config() {
	if [[ -n "${KIND_CLUSTER_CONFIG:-}" ]]; then
		echo "$KIND_CLUSTER_CONFIG"
		return
	fi
	local lib_dir
	lib_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
	echo "${lib_dir}/../../k8s/kind/cluster.yaml"
}

k8s_kind_has_ingress_ports() {
	local cluster_name="$1"
	local container="${cluster_name}-control-plane"
	if ! docker inspect "$container" >/dev/null 2>&1; then
		return 1
	fi
	docker port "$container" 2>/dev/null | grep -q '80/tcp'
}

k8s_recreate_kind_cluster() {
	local cluster_name="$1"
	local kind_config="$2"
	echo "Deleting kind cluster $cluster_name..."
	kind delete cluster --name "$cluster_name"
	echo "Creating kind cluster $cluster_name (ports 80/443 → localhost)..."
	kind create cluster --name "$cluster_name" --config "$kind_config"
}

k8s_can_manage_namespaces() {
	kubectl auth can-i create namespaces >/dev/null 2>&1
}

k8s_select_local_context() {
	local mode="${DEV_CLUSTER:-auto}"
	local ctx=""

	if [[ -n "${KUBECTL_CONTEXT:-}" ]]; then
		if ! k8s_context_exists "$KUBECTL_CONTEXT"; then
			echo "KUBECTL_CONTEXT=$KUBECTL_CONTEXT was not found in kubeconfig." >&2
			exit 1
		fi
		k8s_use_context "$KUBECTL_CONTEXT"
		return
	fi

	case "$mode" in
	auto)
		if command -v kind >/dev/null 2>&1; then
			mode=kind
		elif k8s_context_exists docker-desktop; then
			mode=docker-desktop
		elif k8s_context_exists docker-for-desktop; then
			mode=docker-desktop
			KUBECTL_CONTEXT=docker-for-desktop
		else
			mode=""
		fi
		;;
	esac

	case "$mode" in
	kind)
		if ! command -v kind >/dev/null 2>&1; then
			echo "kind is not installed." >&2
			echo "Install: brew install kind" >&2
			echo "Or enable Kubernetes in Docker Desktop and run: DEV_CLUSTER=docker-desktop bash deploy/scripts/dev-up.sh" >&2
			exit 1
		fi
		local cluster_name="${CLUSTER_NAME:-anglehr-dev}"
		local kind_ctx="kind-${cluster_name}"
		local kind_config
		kind_config="$(k8s_kind_cluster_config)"
		if [[ ! -f "$kind_config" ]]; then
			echo "Kind cluster config not found: $kind_config" >&2
			exit 1
		fi

		if kind get clusters 2>/dev/null | grep -qx "$cluster_name"; then
			if ! k8s_kind_has_ingress_ports "$cluster_name"; then
				echo "Kind cluster '$cluster_name' exists but localhost port 80 is not mapped." >&2
				echo "Ingress URLs (api.anglehr.local, etc.) will not work until the cluster is recreated." >&2
				if [[ "${KIND_RECREATE:-}" == "1" ]]; then
					k8s_recreate_kind_cluster "$cluster_name" "$kind_config"
				else
					echo "" >&2
					echo "Recreate with:" >&2
					echo "  kind delete cluster --name $cluster_name" >&2
					echo "  bash deploy/scripts/dev-up.sh" >&2
					echo "" >&2
					echo "Or non-interactively:" >&2
					echo "  KIND_RECREATE=1 bash deploy/scripts/dev-up.sh" >&2
					exit 1
				fi
			fi
		else
			echo "Creating kind cluster $cluster_name (ports 80/443 → localhost)..."
			kind create cluster --name "$cluster_name" --config "$kind_config"
		fi
		k8s_use_context "$kind_ctx"
		;;
	docker-desktop)
		ctx="${KUBECTL_CONTEXT:-docker-desktop}"
		if ! k8s_context_exists "$ctx" && k8s_context_exists docker-for-desktop; then
			ctx=docker-for-desktop
		fi
		if ! k8s_context_exists "$ctx"; then
			echo "Docker Desktop Kubernetes context not found." >&2
			echo "Enable Kubernetes in Docker Desktop (Settings → Kubernetes → Enable)." >&2
			exit 1
		fi
		k8s_use_context "$ctx"
		;;
	"")
		local current
		current="$(k8s_current_context)"
		echo "No local Kubernetes cluster detected (kind or Docker Desktop)." >&2
		if [[ -n "$current" ]]; then
			echo "Current kubectl context is: $current" >&2
			if [[ "$current" == gke_* ]] || [[ "$current" == arn:aws:eks:* ]]; then
				echo "That looks like a remote cloud cluster — dev-up.sh will not deploy there by default." >&2
			fi
		fi
		echo "" >&2
		echo "Options:" >&2
		echo "  brew install kind && bash deploy/scripts/dev-up.sh" >&2
		echo "  DEV_CLUSTER=docker-desktop bash deploy/scripts/dev-up.sh  (Docker Desktop Kubernetes)" >&2
		echo "  KUBECTL_CONTEXT=<local-context> bash deploy/scripts/dev-up.sh" >&2
		exit 1
		;;
	*)
		echo "Unknown DEV_CLUSTER value: $mode (use auto, kind, or docker-desktop)" >&2
		exit 1
		;;
	esac
}

k8s_require_namespace_access() {
	if k8s_can_manage_namespaces; then
		return
	fi

	local current
	current="$(k8s_current_context)"
	echo "Cannot create namespaces on context: ${current:-<unknown>}" >&2
	echo "Your account lacks cluster-admin or namespace create permissions." >&2
	echo "" >&2
	echo "dev-up.sh is for local development only. Do not run it against production GKE/EKS." >&2
	echo "" >&2
	echo "Fix:" >&2
	echo "  brew install kind" >&2
	echo "  bash deploy/scripts/dev-up.sh" >&2
	echo "" >&2
	echo "Or enable Kubernetes in Docker Desktop, then:" >&2
	echo "  DEV_CLUSTER=docker-desktop bash deploy/scripts/dev-up.sh" >&2
	exit 1
}

k8s_install_ingress_nginx() {
	local ctx
	ctx="$(k8s_current_context)"

	if [[ "$ctx" == kind-* ]]; then
		echo "Installing ingress-nginx (kind)..."
		kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/kind/deploy.yaml
	else
		echo "Installing ingress-nginx (cloud/Docker Desktop)..."
		kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/cloud/deploy.yaml
	fi

	k8s_wait_ingress_nginx
}

k8s_wait_ingress_nginx() {
	echo "Waiting for ingress-nginx..."
	kubectl wait --namespace ingress-nginx \
		--for=condition=complete job/ingress-nginx-admission-create \
		--timeout=180s 2>/dev/null || true
	kubectl wait --namespace ingress-nginx \
		--for=condition=complete job/ingress-nginx-admission-patch \
		--timeout=180s 2>/dev/null || true
	if ! kubectl wait --namespace ingress-nginx \
		--for=condition=available deployment/ingress-nginx-controller \
		--timeout=300s 2>/dev/null; then
		echo "ingress-nginx deployment not available yet; continuing." >&2
	fi
	if ! kubectl wait --namespace ingress-nginx \
		--for=condition=ready pod \
		--selector=app.kubernetes.io/component=controller \
		--timeout=300s 2>/dev/null; then
		echo "ingress-nginx controller pods not ready yet; continuing." >&2
	fi
}

k8s_ingress_ready() {
	kubectl get namespace ingress-nginx >/dev/null 2>&1 \
		&& kubectl get deployment ingress-nginx-controller -n ingress-nginx >/dev/null 2>&1
}

k8s_dashboard_ready() {
	kubectl get namespace kubernetes-dashboard >/dev/null 2>&1 \
		&& kubectl get deployment kubernetes-dashboard -n kubernetes-dashboard >/dev/null 2>&1
}

k8s_patch_dashboard_dev_login() {
	if [[ "${DASHBOARD_REQUIRE_TOKEN:-}" == "1" ]]; then
		return
	fi
	if ! kubectl get deployment kubernetes-dashboard -n kubernetes-dashboard >/dev/null 2>&1; then
		return
	fi
	echo "Configuring dashboard for local dev (cluster-admin via token login)..."
	kubectl patch deployment kubernetes-dashboard -n kubernetes-dashboard --type=json -p='[
		{"op": "replace", "path": "/spec/template/spec/serviceAccountName", "value": "admin-user"}
	]' >/dev/null 2>&1 || true
	kubectl rollout status deployment/kubernetes-dashboard -n kubernetes-dashboard --timeout=120s >/dev/null 2>&1 || true
}

k8s_install_dashboard() {
	local dashboard_kustomize="${1:?dashboard kustomize path required}"

	if [[ "${SKIP_DASHBOARD:-}" == "1" ]]; then
		echo "Skipping Kubernetes Dashboard (SKIP_DASHBOARD=1)."
		return
	fi

	if ! k8s_dashboard_ready; then
		echo "Installing Kubernetes Dashboard..."
		kubectl apply -f https://raw.githubusercontent.com/kubernetes/dashboard/v2.7.0/aio/deploy/recommended.yaml
		if ! kubectl wait --namespace kubernetes-dashboard \
			--for=condition=available deployment/kubernetes-dashboard \
			--timeout=180s 2>/dev/null; then
			echo "Dashboard deployment not ready yet; continuing." >&2
		fi
	else
		echo "Kubernetes Dashboard already installed."
	fi

	k8s_patch_dashboard_dev_login
	k8s_wait_ingress_nginx
	kubectl apply -k "$dashboard_kustomize"
}

k8s_dashboard_login_token() {
	kubectl -n kubernetes-dashboard create token admin-user --duration=8760h 2>/dev/null || true
}

k8s_print_dashboard_access() {
	local token
	token="$(k8s_dashboard_login_token)"
	echo "  Dashboard: http://dashboard.anglehr.local"
	if [[ -n "$token" ]]; then
		echo "  Login:     choose Token, paste:"
		echo "  $token"
	else
		echo "  Login:     bash deploy/scripts/dashboard-token.sh"
	fi
}
