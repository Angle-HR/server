# Local Docker dev/test stack (project name `openhr-local`, set in docker-compose.yml).
# Included from the main Makefile. See docs/microservices/local-dev.md.
#
#   make local-up                          base stack + nats
#   make local-up PROFILES="identity org"  also build/run those service profiles (or `split`, `web`, `clamav`)

LOCAL_PROFILES ?= $(PROFILES)
LOCAL_COMPOSE  := docker compose -f docker-compose.yml -f deploy/local/compose.split.yml $(foreach p,$(LOCAL_PROFILES),--profile $(p))

.PHONY: local-grpc-certs local-up local-down local-reset local-logs local-smoke local-e2e local-e2e-all

local-grpc-certs: ## Generate a throwaway dev CA and per-service certificates for GRPC_AUTH=mtls (deploy/local/certs, gitignored)
	./scripts/gen-grpc-certs.sh

local-up: ## Build and start the local stack, wait until healthy
	$(LOCAL_COMPOSE) up -d --build --wait --wait-timeout 300

local-down: ## Stop the local stack (keeps volumes)
	$(LOCAL_COMPOSE) --profile '*' down --remove-orphans

local-reset: ## Stop the local stack and delete its volumes (databases, minio, nats)
	$(LOCAL_COMPOSE) --profile '*' down --volumes --remove-orphans

local-logs: ## Follow logs (SERVICE=server to filter)
	$(LOCAL_COMPOSE) logs -f --tail=100 $(SERVICE)

local-smoke: ## Health-check every running service
	./scripts/smoke-split.sh

local-e2e: ## Auth e2e (AUTH_MODE=cookie|bearer, default cookie) plus the onboarding e2e
	AUTH_MODE=$(or $(AUTH_MODE),cookie) ./scripts/e2e-auth.sh
	./scripts/e2e-onboarding.sh >/dev/null && echo "onboarding e2e OK"

# All six e2e scripts on ONE FRESH stack, in this order (scripts/lib/e2e-common.sh has the port rules and the admin TOTP helper):
#   e2e-auth        no admin needed
#   e2e-apply       first admin login: enrols the bootstrap admin's TOTP when ADMIN_TOTP_REQUIRED=true (secret kept in a temp file),
#                   writes the company it made to $(E2E_STATE_FILE)
#   e2e-identity    admin login completes TOTP
#   e2e-individual  an individual account hires end to end (own personal organization; deletes it at the end)
#   e2e-couplings   DELETES the company made by e2e-apply (so a full run needs a fresh stack: `make local-reset` first)
#   e2e-admin-totp  last: its lockout step blocks the bootstrap admin's TOTP checks for 15 minutes
# BASE/BASE_APPS/ports etc. pass through the environment; with COMPOSE_PROJECT_NAME set the *_PORT variables are required.
E2E_PROJECT    := $(or $(COMPOSE_PROJECT_NAME),openhr-local)
E2E_STATE_FILE ?= $(or $(TMPDIR),/tmp)/openhr-e2e-$(E2E_PROJECT)-state.env
E2E_TOTP_FILE  ?= $(or $(TMPDIR),/tmp)/openhr-e2e-$(E2E_PROJECT)-admin-totp-secret
local-e2e-all: ## Run e2e-auth, e2e-apply, e2e-identity, e2e-individual, e2e-couplings, e2e-admin-totp in order (fresh stack required)
	@rm -f $(E2E_STATE_FILE) $(E2E_TOTP_FILE)
	@for s in e2e-auth e2e-apply e2e-identity e2e-individual e2e-couplings e2e-admin-totp; do \
	  echo "== $$s"; \
	  AUTH_MODE=$(or $(AUTH_MODE),cookie) STATE_FILE=$(E2E_STATE_FILE) ADMIN_TOTP_SECRET_FILE=$(E2E_TOTP_FILE) ./scripts/$$s.sh || exit 1; \
	done
