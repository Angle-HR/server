.PHONY: fmt lint test cover security check tidy pr-description hooks hooks-uninstall build build-worker run run-worker migrate migrate-up migrate-down sqlc sqlc-check swagger swagger-check

PR_TEMPLATE := .github/pull_request_template.md
PR_OUT_DIR := pr_template
BIN := bin/server
BIN_WORKER := bin/worker
SQLC_VERSION := v1.29.0
SWAG_VERSION := v1.16.4

# ─────────────────────────────────────────────
# Application
# ─────────────────────────────────────────────

migrate-up:
	@echo "→ Running migrations..."
	@if [ -f .env ]; then set -a && . ./.env && set +a; fi; \
	if [ -z "$$DB_URL" ]; then echo "❌ DB_URL is required (set it or add .env)"; exit 1; fi; \
	./db/migrations/migrate.sh up
	@echo "✅ Migrations applied."

migrate-down:
	@echo "→ Rolling back migration..."
	@if [ -f .env ]; then set -a && . ./.env && set +a; fi; \
	if [ -z "$$DB_URL" ]; then echo "❌ DB_URL is required (set it or add .env)"; exit 1; fi; \
	./db/migrations/migrate.sh down
	@echo "✅ Migration rolled back."

sqlc:
	@echo "→ Generating sqlc code..."
	@go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate
	@echo "✅ sqlc generation complete."

sqlc-check:
	@echo "→ Checking sqlc generated code..."
	@go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate
	@git diff --exit-code internal/db/sqlc || \
		(echo "❌ sqlc output is out of date. Run 'make sqlc' and commit the result." && exit 1)
	@echo "✅ sqlc output is up to date."

swagger:
	@echo "→ Generating OpenAPI docs..."
	@go run github.com/swaggo/swag/cmd/swag@$(SWAG_VERSION) init \
		-g cmd/server/main.go \
		-o internal/docs/spec \
		--parseDependency \
		--parseInternal
	@echo "✅ OpenAPI docs generated."

swagger-check:
	@echo "→ Checking OpenAPI docs..."
	@go run github.com/swaggo/swag/cmd/swag@$(SWAG_VERSION) init \
		-g cmd/server/main.go \
		-o internal/docs/spec \
		--parseDependency \
		--parseInternal
	@git diff --exit-code internal/docs/spec || \
		(echo "❌ OpenAPI docs are out of date. Run 'make swagger' and commit the result." && exit 1)
	@echo "✅ OpenAPI docs are up to date."


# ─────────────────────────────────────────────
# Formatting
# ─────────────────────────────────────────────

fmt:
	@echo "→ Running gofmt..."
	@gofmt -w .
	@echo "→ Running goimports..."
	@goimports -w .
	@echo "✅ Formatting done."

# ─────────────────────────────────────────────
# Linting
# ─────────────────────────────────────────────

lint:
	@echo "→ Running golangci-lint..."
	@golangci-lint run --timeout=5m
	@echo "✅ Lint passed."

lint-fix:
	@echo "→ Running golangci-lint with auto-fix..."
	@golangci-lint run --fix --timeout=5m

# ─────────────────────────────────────────────
# Testing
# ─────────────────────────────────────────────

test:
	@echo "→ Running tests..."
	@go test -race -timeout=120s ./...
	@echo "✅ Tests passed."

cover:
	@echo "→ Running tests with coverage..."
	@go test -race -coverprofile=coverage.out -covermode=atomic -coverpkg=./... ./...
	@go tool cover -func=coverage.out | grep total
	@go tool cover -html=coverage.out -o coverage.html
	@echo "✅ Coverage report generated at coverage.html"

cover-threshold:
	@COVERAGE=$$(go tool cover -func=coverage.out | grep total | awk '{print $$3}' | tr -d '%'); \
	THRESHOLD=70; \
	echo "Coverage: $${COVERAGE}%  Threshold: $${THRESHOLD}%"; \
	if [ $$(echo "$$COVERAGE < $$THRESHOLD" | bc -l) -eq 1 ]; then \
		echo "❌ Coverage below threshold."; exit 1; \
	fi; \
	echo "✅ Coverage threshold met."

# ─────────────────────────────────────────────
# Security
# ─────────────────────────────────────────────

security:
	@echo "→ Running gosec..."
	@gosec -exclude-generated ./...
	@echo "→ Running govulncheck..."
	@govulncheck ./...
	@echo "✅ Security scan passed."

# ─────────────────────────────────────────────
# Dependencies
# ─────────────────────────────────────────────

tidy:
	@echo "→ Running go mod tidy..."
	@go mod tidy
	@go mod verify
	@echo "✅ Dependencies tidied and verified."

# ─────────────────────────────────────────────
# Install tools
# ─────────────────────────────────────────────

tools:
	@echo "→ Installing development tools..."
	@go install golang.org/x/tools/cmd/goimports@latest
	@go install golang.org/x/vuln/cmd/govulncheck@latest
	@go install github.com/securego/gosec/v2/cmd/gosec@latest
	@curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin latest
	@echo "✅ Tools installed."

# ─────────────────────────────────────────────
# Run everything (mirrors CI)
# ─────────────────────────────────────────────

check: tidy lint security
	@echo ""
	@echo "✅ All quality checks passed."

# ─────────────────────────────────────────────
# Git hooks
# ─────────────────────────────────────────────

hooks:
	@test -f .githooks/pre-commit || (echo "❌ Missing .githooks/pre-commit"; exit 1)
	@mkdir -p .git/hooks
	@cp .githooks/pre-commit .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@echo "✅ Installed git pre-commit hook (runs: make check)"


# ─────────────────────────────────────────────
# Pull request draft (active branch)
# ─────────────────────────────────────────────

# Scaffolds a PR body from the repo template into pr_template/ (gitignored).
pr-description:
	@test -f $(PR_TEMPLATE) || (echo "❌ Missing $(PR_TEMPLATE)"; exit 1)
	@mkdir -p $(PR_OUT_DIR)
	@branch=$$(git branch --show-current); \
	safe=$$(printf '%s' "$$branch" | sed 's/[^A-Za-z0-9._-]/-/g'); \
	out="$(PR_OUT_DIR)/pr-$$safe.md"; \
	cp $(PR_TEMPLATE) "$$out"; \
	{ \
		echo ""; \
		echo "---"; \
		echo ""; \
		echo "## Draft context (auto-generated)"; \
		echo ""; \
		echo "**Branch:** \`$$branch\`"; \
		echo ""; \
		base=""; \
		for candidate in main origin/main master origin/master; do \
			if git rev-parse --verify "$$candidate" >/dev/null 2>&1; then \
				mb=$$(git merge-base HEAD "$$candidate" 2>/dev/null); \
				if [ -n "$$mb" ]; then \
					base="$$candidate"; \
					break; \
				fi; \
			fi; \
		done; \
		if [ -n "$$base" ]; then \
			echo "**Commits (vs \`$$base\`):**"; \
			echo ""; \
			echo '```'; \
			git log "$$base"..HEAD --oneline || true; \
			echo '```'; \
		else \
			echo "**Recent commits (no \`main\`/\`master\` base found):**"; \
			echo ""; \
			echo '```'; \
			git log -20 --oneline || true; \
			echo '```'; \
		fi; \
	} >> "$$out"; \
	echo "✅ Wrote $$out"