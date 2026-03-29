.PHONY: fmt lint test cover security check tidy pr-description

PR_TEMPLATE := .github/pull_request_template.md
PR_OUT_DIR := pr_template

# ─────────────────────────────────────────────
# Formatting
# ─────────────────────────────────────────────

fmt:
	@echo "→ Running gofmt..."
	@gofmt -w .
	@echo "→ Running goimports..."
	@goimports -w .
	@echo "✅ Formatting done."

fmt-check:
	@echo "→ Checking formatting..."
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "❌ Unformatted files:"; echo "$$unformatted"; exit 1; \
	fi
	@echo "✅ All files formatted."

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
	@go test -race -coverprofile=coverage.out -covermode=atomic ./...
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
	@gosec ./...
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

check: tidy fmt-check lint test cover cover-threshold security
	@echo ""
	@echo "✅ All quality checks passed."

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