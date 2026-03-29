# Angle HR — Server

Backend service for [Angle HR](https://github.com/Angle-HR/server), written in Go. This repository holds the HTTP API and related server-side code, quality tooling, and automation.

## Requirements

- [Go](https://go.dev/dl/) **1.22** or newer (matches [CI](.github/workflows/code-quality.yml))
- Optional for local parity with CI: [golangci-lint](https://golangci-lint.run/), `goimports`, [gosec](https://github.com/securego/gosec), [govulncheck](https://go.dev/security/vuln/)

## Quick start

```bash
git clone https://github.com/Angle-HR/server.git
cd server
go version   # expect 1.22+
```

When a `go.mod` is present at the repo root:

```bash
go mod download
go build ./...
go test ./...
```

Install dev tools used by the Makefile (once per machine):

```bash
make tools
```

Run the full local quality gate (format check, lint, tests, coverage threshold, security scans):

```bash
make check
```

Individual targets: `make fmt`, `make lint`, `make test`, `make cover`, `make security`, `make tidy`.

## Documentation

- **[CONTRIBUTING.md](./CONTRIBUTING.md)** — how to contribute, commits, branches, reviews
- **[SUPPORT.md](./SUPPORT.md)** — where to ask questions and report issues
- **[SECURITY.md](./SECURITY.md)** — responsible disclosure (do not use public issues for vulnerabilities)
- **[ROADMAP.md](./ROADMAP.md)** — planned direction
- **[CHANGELOG.md](./CHANGELOG.md)** — release history

Extended guides and API reference can live under [`docs/`](./docs/) as the project grows.

## Project layout

Typical Go layout (create these as you add code):

| Path        | Purpose                          |
|------------|-----------------------------------|
| `cmd/`     | `main` packages (binaries)        |
| `internal/`| private application code          |
| `pkg/`     | libraries safe for external use   |

CI runs on every push and pull request ([code quality](.github/workflows/code-quality.yml)); merges to `main` run an additional [merge gate](.github/workflows/merge-gate.yml).

## Contributing

Issues and pull requests are welcome. Please read [CONTRIBUTING.md](./CONTRIBUTING.md) and the [Code of Conduct](./CODE_OF_CONDUCT.md) before contributing.

## License

This project is licensed under the **GNU Affero General Public License v3.0** — see [LICENSE](./LICENSE).
