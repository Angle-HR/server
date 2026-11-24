# Microservices

Where the split stands and how to run it. Start with BOUNDARIES for the why, local-dev for the how.

| Doc | What it covers |
| --- | --- |
| [BOUNDARIES.md](BOUNDARIES.md) | package-to-service map, coupling analysis, extraction order, progress notes |
| [events.md](events.md) | event envelope, outbox, NATS subjects and the catalog of events |
| [notifications.md](notifications.md) | the notifications service: events transport, templates, inbox |
| [applications.md](applications.md) | the applications service, applicant counts, audit trail |
| [applications-jobmodel.md](applications-jobmodel.md) | how applications learns about jobs (db vs events read model) |
| [hiring.md](hiring.md) | the hiring service: routes, org seams, job events |
| [org.md](org.md) | the org service: onboarding, KYB, agreements, gRPC surface, events |
| [individual-accounts.md](individual-accounts.md) | individuals can hire: personal organization, verification, API contract, backfill |
| [identity.md](identity.md) | the identity service: auth, sessions, staff admin, KYB queue |
| [hardening.md](hardening.md) | TOTP key rotation, stream retention, logging, CI |
| [bff.md](bff.md) | web-bff and admin-bff: routing, rate limits, session validation |
| [sessions.md](sessions.md) | cookie sessions, CSRF, namespaces |
| [local-dev.md](local-dev.md) | the Docker stack, profiles, ports, e2e scripts |
| [switches.md](switches.md) | every environment switch with default and meaning |

Quick path: `make local-up PROFILES=split`, then `make local-smoke` and `make local-e2e-all`. The all-remote combination
is listed at the end of [local-dev.md](local-dev.md).
