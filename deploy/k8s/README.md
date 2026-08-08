# Kubernetes deployment

Kustomize manifests for running Angle HR on Kubernetes. Two overlays:

| Overlay | Purpose | Data layer |
|---------|---------|------------|
| [`overlays/dev`](overlays/dev) | Local development (kind/minikube) | In-cluster Postgres (one instance, six databases) + Redis + Cloudflare R2, mirroring [`docker-compose.yml`](../../docker-compose.yml) |
| [`overlays/prod`](overlays/prod) | Production | Managed PostgreSQL (one instance, six databases) + Cloudflare R2 |

## Layout

```
deploy/k8s/
  base/                 # server, email-worker, upload-worker, fluvio-ui
  components/           # dev-only: postgres, redis, init jobs, dashboard
  overlays/dev/         # app tier + ingress for local hostnames
  overlays/dev-jobs/    # migrate with dev image tags
  overlays/prod/        # app tier + ingress TLS + HPA
deploy/scripts/
  dev-up.sh             # bootstrap local kind cluster
  create-dev-secret.sh  # .env → Kubernetes Secret
```

## Requirements (local dev)

- Docker
- [kubectl](https://kubernetes.io/docs/tasks/tools/)
- **One local cluster** (the script refuses remote GKE/EKS contexts):
  - [kind](https://kind.sigs.k8s.io/) — `brew install kind` (recommended), or
  - **Docker Desktop Kubernetes** — enable in Settings → Kubernetes, then `DEV_CLUSTER=docker-desktop bash deploy/scripts/dev-up.sh`
- `.env` at repo root (copy from [`.env.example`](../../.env.example))

Minimum cluster resources: ~2–4 GB RAM for the full dev stack (one Postgres StatefulSet + app tier).

## Quick start (local)

```bash
cp .env.example .env
# Edit passwords in .env

bash deploy/scripts/dev-up.sh
```

The script will:

1. Create a kind cluster named `anglehr-dev` with **ports 80/443 mapped to localhost** ([`k8s/kind/cluster.yaml`](kind/cluster.yaml)), then install ingress-nginx
2. Install the [Kubernetes Dashboard](https://kubernetes.io/docs/tasks/access-application-cluster/web-ui-dashboard/) at `http://dashboard.anglehr.local`
3. Build and load `server`, `email-worker`, `upload-worker`, and `migrate` images tagged `dev`
3. Create the `anglehr-secrets` Secret from `.env`
4. Deploy the Postgres StatefulSet (six databases on one instance)
5. Run the `migrate` Job
6. Deploy server, email-worker, upload-worker, fluvio-ui, and Ingress

Add to `/etc/hosts`:

```
127.0.0.1 api.anglehr.local fluvio.anglehr.local dashboard.anglehr.local
```

Verify:

```bash
kubectl get pods -n anglehr
curl http://api.anglehr.local/api/v1/countries
```

Open **http://dashboard.anglehr.local** and sign in with **Token** (Skip login is disabled — it cannot reach the API reliably through ingress).

```bash
bash deploy/scripts/dashboard-token.sh
```

Paste the token on the login screen. The `admin-user` account has cluster-admin access, so the `anglehr` namespace and all pods are visible.

Disable the dashboard install with `SKIP_DASHBOARD=1 bash deploy/scripts/dev-up.sh`.

### Manual steps

Create or refresh the dev secret only:

```bash
bash deploy/scripts/create-dev-secret.sh
```

Apply overlays individually:

```bash
kubectl apply -k deploy/k8s/components/postgres -n anglehr
kubectl apply -k deploy/k8s/overlays/dev-jobs -n anglehr
kubectl apply -k deploy/k8s/overlays/dev -n anglehr
```

See [`overlays/dev/secrets.example.yaml`](overlays/dev/secrets.example.yaml) for the full list of Secret keys.

## Service naming (compose → Kubernetes)

| Compose | Kubernetes Service | Databases |
|---------|-------------------|-----------|
| `postgres` | `postgres:5432` | `anglehr_uk`, `anglehr_us`, `anglehr_africa`, `anglehr_eu`, `anglehr_asia`, `anglehr_global` |
| `redis` | `redis:6379` | — |

`create-dev-secret.sh` builds DSNs against `postgres:5432` with the matching `dbname`. R2 credentials are passed through from `.env` (see [`.env.example`](../../.env.example)).

## Container images

Built from the repo root:

| Image | Dockerfile | Target |
|-------|------------|--------|
| `ghcr.io/angle-hr/server` | `Dockerfile` | `server` |
| `ghcr.io/angle-hr/server-email-worker` | `Dockerfile` | `email-worker` |
| `ghcr.io/angle-hr/server-upload-worker` | `Dockerfile` | `upload-worker` |
| `ghcr.io/angle-hr/server-migrate` | `Dockerfile.migrate` | — |

CI pushes images on push to `main` and version tags (see [`.github/workflows/container-images.yml`](../../.github/workflows/container-images.yml)).

Build locally:

```bash
docker build -f Dockerfile --target server -t ghcr.io/angle-hr/server:dev .
docker build -f Dockerfile --target email-worker -t ghcr.io/angle-hr/server-email-worker:dev .
docker build -f Dockerfile --target upload-worker -t ghcr.io/angle-hr/server-upload-worker:dev .
docker build -f Dockerfile.migrate -t ghcr.io/angle-hr/server-migrate:dev .
```

## Production deployment

The prod overlay deploys **only** the application tier (server, email-worker, upload-worker, fluvio-ui). Postgres is **not** included — use managed services. Object storage is Cloudflare R2 (external).

### 1. Provision infrastructure

Outside this repo (Terraform, cloud console, etc.):

- One PostgreSQL instance with databases `anglehr_uk`, `anglehr_us`, `anglehr_africa`, `anglehr_eu`, `anglehr_asia`, and `anglehr_global`
- Cloudflare R2 bucket names: `anglehr-uk`, `anglehr-us`, `anglehr-africa`, `anglehr-eu`, `anglehr-asia`
- SMTP relay
- Ingress controller + [cert-manager](https://cert-manager.io/) (for TLS annotations in prod Ingress)

### 2. Create the production Secret

Use [`overlays/prod/secrets.example.yaml`](overlays/prod/secrets.example.yaml) as a reference. Required keys match what the Go app reads from the environment (see [`.env.example`](../../.env.example)).

**PostgreSQL DSNs** — same host, different `dbname`, with `sslmode=require`:

```
DB_URL_GLOBAL=postgres://user:pass@db.example.com:5432/anglehr_global?sslmode=require
ANGLEHR_UK_POSTGRES_DSN=postgres://user:pass@db.example.com:5432/anglehr_uk?sslmode=require
```

**Redis** — managed instance URL (required by the server):

```
REDIS_URL=redis://:password@redis-host:6379/0
```

**JWT auth** — signing secret in the Secret; token TTLs and default signup region in the ConfigMap:

```
JWT_SECRET=<long-random-secret>
```

ConfigMap keys (see [`overlays/prod/configmap.yaml`](overlays/prod/configmap.yaml)): `JWT_ACCESS_TTL`, `JWT_REFRESH_TTL`, `AUTH_DEFAULT_REGION` (default `uk`).

Optional: set `FLUVIO_POLL_ONLY=true` in the ConfigMap when Postgres is behind PgBouncer transaction pooling (disables LISTEN/NOTIFY for job workers).

**Cloudflare R2** — set shared API token credentials and per-region bucket names:

```
R2_ACCESS_KEY=<r2-access-key-id>
R2_SECRET_KEY=<r2-secret-access-key>
R2_ENDPOINT=https://<account_id>.r2.cloudflarestorage.com
ANGLEHR_UK_R2_BUCKET=anglehr-uk
```

Prefer [External Secrets Operator](https://external-secrets.io/) or your cloud secret manager rather than committing secrets.

### 3. Configure Ingress and ConfigMap

Edit before applying:

- [`overlays/prod/ingress.yaml`](overlays/prod/ingress.yaml) — replace `api.example.com` and `fluvio.example.com`
- [`overlays/prod/configmap.yaml`](overlays/prod/configmap.yaml) — set `PUBLIC_API_URL`, `FLUVIO_UI_ORIGIN`, and auth settings (`JWT_ACCESS_TTL`, `JWT_REFRESH_TTL`, `AUTH_DEFAULT_REGION`) to match

Update the cert-manager `cluster-issuer` annotation if your issuer name differs.

### 4. Deploy

```bash
# Create secret first (from your secret manager or kubectl create secret generic ...)
kubectl apply -k deploy/k8s/overlays/prod
```

Pin image tags in [`overlays/prod/kustomization.yaml`](overlays/prod/kustomization.yaml) to a git SHA or release tag instead of `latest` for reproducible deploys.

### 5. Database migrations

Production SQL migrations run via the existing GitHub Actions workflow ([`.github/workflows/migrate.yml`](../../.github/workflows/migrate.yml)) using `ANGLEHR_*_POSTGRES_DSN` and `DB_URL_GLOBAL` secrets in the `staging` / `production` environments — not via an in-cluster Job.

## Fluvio UI note

The stock `ghcr.io/software78/fluvio_ui` image bakes **`http://localhost:8080/fluvio/api`** into the JS bundle, which breaks on Kubernetes (JSON parse errors, 502 on events). Pin to **`1.0.1`** (see `docker-compose.yml`) or newer.

`dev-up.sh` builds [`deploy/docker/Dockerfile.fluvio-ui`](../docker/Dockerfile.fluvio-ui) from **`v1.0.1`** with **`VITE_API_BASE_URL=http://api.anglehr.local`**. The UI at `fluvio.anglehr.local` calls the API on `api.anglehr.local/fluvio/api/*` (CORS allowed via `FLUVIO_UI_ORIGIN`). Do not call `/fluvio/api/*` on the UI host — that path serves static HTML.

Verify after deploy:

```bash
curl -s http://fluvio.anglehr.local/ | grep -o 'index-[^"]*\\.js'   # UI bundle hash
curl -s http://api.anglehr.local/fluvio/api/jobs | head -c 40       # must be JSON
```

## Troubleshooting

**`Forbidden: User ... cannot get resource "namespaces"`** — `kubectl` is pointed at a remote cloud cluster (e.g. GKE), not a local dev cluster. The script no longer falls back to the current context when kind is missing.

Fix:

```bash
# Option A: kind (recommended)
brew install kind
bash deploy/scripts/dev-up.sh

# Option B: Docker Desktop Kubernetes
# Enable Kubernetes in Docker Desktop, then:
DEV_CLUSTER=docker-desktop bash deploy/scripts/dev-up.sh
```

Check your context: `kubectl config current-context` (should be `kind-anglehr-dev` or `docker-desktop`, not `gke_...`).

**Ingress hostnames refuse connection (`curl: (7) Failed to connect`)** — the kind cluster was likely created without host port mappings. New clusters use [`k8s/kind/cluster.yaml`](kind/cluster.yaml). Recreate:

```bash
KIND_RECREATE=1 bash deploy/scripts/dev-up.sh
```

Or manually: `kind delete cluster --name anglehr-dev` then run `dev-up.sh` again.

**Pods stuck on `CreateContainerConfigError`** — the `anglehr-secrets` Secret is missing or incomplete. Run `create-dev-secret.sh` or verify prod secret keys.

**Migrate Job fails** — check logs: `kubectl logs job/migrate -n anglehr`. Ensure the Postgres pod is Ready, all six databases exist, and DSN passwords match the Secret.

**Ingress returns 404** — confirm ingress-nginx is running and `/etc/hosts` entries match [`overlays/dev/ingress.yaml`](overlays/dev/ingress.yaml).

**Re-run init jobs** (dev):

```bash
kubectl delete job migrate -n anglehr
kubectl apply -k deploy/k8s/overlays/dev-jobs -n anglehr
```

## What stays on docker-compose

[`docker-compose.yml`](../../docker-compose.yml) remains supported for developers who prefer Compose over Kubernetes.
