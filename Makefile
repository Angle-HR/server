.PHONY: fmt lint test cover security check tidy

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