# Contributing

Thank you for taking the time to contribute! This document outlines the process for reporting issues, proposing changes, and getting your pull requests merged.

---

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Getting Started](#getting-started)
- [How to Contribute](#how-to-contribute)
  - [Reporting Bugs](#reporting-bugs)
  - [Suggesting Features](#suggesting-features)
  - [Submitting a Pull Request](#submitting-a-pull-request)
- [Development Setup](#development-setup)
- [Commit Convention](#commit-convention)
- [Branch Naming](#branch-naming)
- [Code Style](#code-style)
- [Testing](#testing)
- [Review Process](#review-process)
- [License](#license)

---

## Code of Conduct

This project follows a [Contributor Covenant](https://www.contributor-covenant.org/)-based Code of Conduct. By participating, you agree to uphold a welcoming, respectful environment for everyone. Violations can be reported to **[maintainer email]**.

---

## Getting Started

1. **Fork** the repository and clone your fork locally.
2. Create a new branch from `main` (see [Branch Naming](#branch-naming)).
3. Make your changes, write tests, and verify everything passes.
4. Open a pull request against `main`.

If you're unsure where to start, look for issues labelled `good first issue` or `help wanted`.

---

## How to Contribute

### Reporting Bugs

Before opening a bug report, please search existing issues to avoid duplicates.

When filing a bug, include:

- A clear, descriptive title.
- Steps to reproduce the problem.
- Expected vs. actual behaviour.
- Environment details (OS, runtime version, dependency versions).
- Relevant logs, screenshots, or stack traces.

Use the **Bug Report** issue template if one is available.

### Suggesting Features

Feature requests are welcome. Please open an issue with:

- A description of the problem you're trying to solve.
- Your proposed solution (or multiple approaches if you have them).
- Any alternatives you've considered.

Avoid opening a pull request for a significant feature without first discussing it in an issue — this saves everyone time if the direction needs adjustment.

### Submitting a Pull Request

1. Ensure your branch is up to date with `main` before opening a PR.
2. Keep PRs focused — one logical change per PR.
3. Fill out the pull request template completely.
4. Link the relevant issue(s) using `Closes #issue-number` in the PR description.
5. Add or update tests to cover your changes.
6. Make sure all CI checks pass before requesting review.

---

## Development Setup

```bash
# 1. Clone your fork
git clone https://github.com/<your-username>/<repo-name>.git
cd <repo-name>

# 2. Install dependencies
# [Replace with your project's install command, e.g.:]
# npm install | pip install -r requirements.txt | go mod download

# 3. Copy environment variables
cp .env.example .env

# 4. Run the project locally
# [Replace with your start command]
```

> **Note:** See `README.md` for full environment setup details.

---

## Commit Convention

This project uses [Conventional Commits](https://www.conventionalcommits.org/).

```
<type>(optional scope): <short summary>

[optional body]

[optional footer(s)]
```

**Types:**

| Type       | When to use                                      |
|------------|--------------------------------------------------|
| `feat`     | A new feature                                    |
| `fix`      | A bug fix                                        |
| `docs`     | Documentation changes only                       |
| `style`    | Formatting, missing semicolons, etc. (no logic)  |
| `refactor` | Code change that neither fixes a bug nor adds a feature |
| `test`     | Adding or correcting tests                       |
| `chore`    | Build process, dependency updates, tooling       |
| `perf`     | Performance improvements                         |

**Examples:**

```
feat(auth): add OAuth2 login support
fix(api): handle null response from /users endpoint
docs: update setup instructions in README
```

Breaking changes must include `BREAKING CHANGE:` in the commit footer or a `!` after the type, e.g. `feat!: drop support for Node 16`.

---

## Branch Naming

```
<type>/<short-description>
```

| Prefix      | Use for                         |
|-------------|---------------------------------|
| `feat/`     | New features                    |
| `fix/`      | Bug fixes                       |
| `docs/`     | Documentation updates           |
| `refactor/` | Refactoring work                |
| `chore/`    | Tooling, CI, dependencies       |
| `test/`     | Test additions or fixes         |

**Examples:** `feat/user-notifications`, `fix/token-expiry-crash`

---

## Code Style

- Follow the existing style and conventions in the codebase.
- Run the linter and formatter before committing:
  ```bash
  # [Replace with your lint/format command, e.g.:]
  # npm run lint | golangci-lint run | ruff check .
  ```
- Do not commit commented-out code or debug statements (`console.log`, `fmt.Println`, `print`, etc.).
- Keep functions small and well-named. Prefer clarity over cleverness.

---

## Testing

- All new features and bug fixes must include tests.
- Run the full test suite before opening a PR:
  ```bash
  # [Replace with your test command, e.g.:]
  # npm test | go test ./... | pytest
  ```
- Aim to maintain or improve current code coverage — do not open PRs that significantly reduce it.
- Tests should be deterministic and isolated — avoid relying on external services or global state.

---

## Review Process

- A maintainer will review your PR within **[X business days]**.
- You may be asked to make changes before it is merged.
- Once approved, a maintainer will merge your PR. Please do not merge it yourself.
- Stale PRs (no activity for 30 days) may be closed. You're welcome to reopen them.

---

## License

By contributing, you agree that your contributions will be licensed under the same license as this project. See [`LICENSE`](./LICENSE) for details.
