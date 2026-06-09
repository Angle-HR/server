# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

### Added
### Changed
- Object storage now uses Cloudflare R2 instead of self-hosted MinIO. Environment variables are renamed from `ANGLEHR_*_MINIO_*` to `ANGLEHR_*_R2_*`. `DBRouter.MinIO()` is now `DBRouter.R2()`. R2 credentials are shared via `R2_ACCESS_KEY` and `R2_SECRET_KEY`; `R2_ENDPOINT` is the default S3 API host with optional per-region `ANGLEHR_*_R2_ENDPOINT` overrides.

### Deprecated
### Removed
- In-cluster MinIO StatefulSets, `minio_setup` docker-compose service, and `minio-setup` Kubernetes Job. Startup no longer checks or creates R2 buckets.

### Fixed
### Security

---

## [0.1.0] - YYYY-MM-DD

### Added
- Initial project scaffold.
- Core feature: [describe].
- `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, and community health files.

---

<!-- CHANGELOG GUIDE

Each release entry should follow this structure:

## [X.Y.Z] - YYYY-MM-DD

### Added
- New features or capabilities introduced in this release.

### Changed
- Changes to existing functionality that are backwards-compatible.

### Deprecated
- Features that still work but are planned for removal in a future release.

### Removed
- Features or functionality removed in this release.

### Fixed
- Bug fixes. Reference issue numbers where applicable, e.g. "Fixed crash on empty input (#42)."

### Security
- Patches for vulnerabilities. Reference CVE IDs where applicable.

VERSIONING RULES (Semantic Versioning):
  MAJOR (X) — breaking changes incompatible with previous versions.
  MINOR (Y) — new backwards-compatible functionality.
  PATCH (Z) — backwards-compatible bug fixes.

TIPS:
  - Write entries for humans, not machines. "Fixed token expiry crash" > "Patched null pointer in auth.go:147."
  - Link issue/PR numbers: "Added batch export support (#88, #91)."
  - Keep Unreleased updated as you merge PRs; cut a release by moving it to a dated version.

-->

[Unreleased]: https://github.com/Angle-HR/server/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/Angle-HR/server/releases/tag/v0.1.0
