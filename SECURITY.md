# Security Policy

## Supported Versions

Only the versions listed below actively receive security patches. If you are running an unsupported version, please upgrade before reporting a vulnerability.

| Version | Supported          |
|---------|--------------------|
| latest  | ✅ Yes             |
| < 1.0   | ❌ No              |

<!-- Update this table as you cut new releases. -->

---

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

If you discover a security vulnerability, report it responsibly by emailing **[security@your-domain.com]**. Include as much of the following as possible:

- A description of the vulnerability and its potential impact.
- The affected version(s).
- Step-by-step instructions to reproduce the issue.
- Any proof-of-concept code or screenshots.

You can optionally encrypt your report using our PGP key: **[link or fingerprint]**.

---

## What to Expect

| Step                           | Timeframe              |
|--------------------------------|------------------------|
| Acknowledgement of report      | Within **48 hours**    |
| Initial assessment             | Within **5 business days** |
| Resolution or mitigation patch | Depends on severity    |
| Public disclosure              | After patch is released |

We follow [coordinated disclosure](https://en.wikipedia.org/wiki/Coordinated_vulnerability_disclosure): we will work with you to understand the issue and release a fix before any public disclosure. We will credit you in the release notes unless you prefer to remain anonymous.

---

## Severity Classification

We use the [CVSS v3.1](https://www.first.org/cvss/) scoring system to assess severity:

| Severity | CVSS Score | Response Target   |
|----------|------------|-------------------|
| Critical | 9.0 – 10.0 | Patch ASAP        |
| High     | 7.0 – 8.9  | Within 7 days     |
| Medium   | 4.0 – 6.9  | Within 30 days    |
| Low      | 0.1 – 3.9  | Next minor release|

---

## Out of Scope

The following are generally **not** considered vulnerabilities for this project:

- Issues in third-party dependencies (report those upstream).
- Bugs that require physical access to a machine.
- Social engineering attacks.
- Issues in unsupported versions.

---

## Security Best Practices for Users

- Always run the latest stable release.
- Review your environment variables and secrets — never commit `.env` files.
- Enable dependency auditing in your CI pipeline (e.g. `npm audit`, `go mod verify`, `pip-audit`).
- Subscribe to [GitHub Security Advisories](https://github.com/Angle-HR/server/security/advisories) for this repo to get notified of published CVEs.
