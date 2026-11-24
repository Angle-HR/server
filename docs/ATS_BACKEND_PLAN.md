# Job application flow: backend plan

Scope: backend only. Source: Figma pages "1/2---Job creation Flow Ready for Dev" and the ATS / Job Screening Flow page.
Special-category defaults are out of scope and stay as they are.

Services stay as they are: applications owns applications, org owns companies, hiring owns jobs, notifications sends mail. Everything moves over the existing outbox and NATS events. No new service is needed.

## What the design needs that the backend doesn't have

- **Application statuses are final.** `rejected`, `hired` and `withdrawn` are terminal (migration 000016). The design lets you move a candidate between stages, including out of Disqualified or Hired.
- **No stages, notes, tags, follows, activity feed, email history or interviews.** Only `app_audit_events` and the candidate-facing `email_outbox` exist.
- **No offers or e-sign.**
- **No company public profile.** `organizations` has `legal_name` but no slug, about text or logo. Public jobs only expose a sitemap list, so the client's company careers page runs on fixtures and `/api/v1/public/companies/<slug>` returns 404.

## Phase 1: public careers API (size M)

- **Org:** add `slug`, `about`, `website` and `logo` to organizations, with `GET/PUT /organization/profile`. Slug uniqueness checks and a reserved-word list.
- **Global registry:** a `company_slugs` table (slug to company and region), kept in sync by org events. The public URL carries no region, so the lookup must be global.
- **Hiring:** a read model of the public profile, fed by an event such as `org.company.profile_updated.v1`. Add `GET /public/companies/{slug}` returning the profile and open roles, with filters (location, team, employment type, remote) and cursor paging. Only jobs with "show on career page" on are included; the indexing flag is respected.
- **BFF:** route `/public/companies/*` to hiring, rate-limited as public. The client then drops its fixtures.

## Phase 2: pipeline, notes, tags, activity (size L)

- **Stages:** `job_stages` (job, key, name, position, kind). Each job is seeded with Applied, Shortlisted, Interview, Offer, Hired and Disqualified, plus custom stages.
  - Add `stage_id` to applications. Keep `status` as the coarse lifecycle.
  - Backfill: received, in_review and flagged become Applied; hired becomes Hired; rejected becomes Disqualified.
  - Endpoints: stage CRUD and reorder, move to stage, bulk move, per-stage counts in the list.
- **Disqualify and reinstate:** reasons catalog, optional note, reinstate action. This replaces the "rejected is final" rule (product decision, see below).
- **Notes, tags, follow/star:** `application_notes`, a company tag catalog with a join table, `application_follows`, with endpoints for each.
- **Activity feed:** an `application_events` timeline written in the same transaction as every change. `GET /applications/{id}/activity` with cursor paging. Actor names come from the identity user directory.
- **Permissions:** new permissions for notes, stage management and email, checked through the existing job-access check.
- **Events:** `application.stage_changed.v1` and similar on the outbox.

## Phase 3: email (size L)

- **Templates:** per-company templates with placeholders (candidate name, job title, company); validation and preview.
- **Send:** `POST /applications/{id}/emails` stores the message and queues it through notifications. Recipients are restricted to that application's candidate so it cannot be used as an open relay. Per-company quotas.
- **Scheduled send:** a new scheduled job, like the existing hiring ones.
- **Attachments and signatures:** attachments reuse file storage and the malware scan.
- **History:** a per-candidate list, with Brevo delivery-status webhooks updating status.
- **Disqualify or archive with email:** reason plus template in one action.
- **Inbound replies:** deferred.

## Phase 4: interviews and compare (size M)

- **Interviews:** an `interviews` table (time, timezone, location or link, interviewers, status), CRUD, calendar invites by ICS email, and a per-user schedule list. Calendar sync comes later.
- **Compare:** `GET …/compare?ids=a,b` returns answers, documents and links side by side. The design's "Role fit" needs CV parsing, so it is left out for now.

## Phase 5: offers and signing (size L)

- **Offer templates and offers:** fields for signature, full name and date; status flow draft, sent, viewed, signed, declined, expired.
- **Public signing:** token-based endpoints with single-use tokens and expiry.
- **Audit and output:** audit trail (document hash, consent, IP and user agent, timestamps) and a signed PDF in the regional bucket.
- **Event:** `offer.signed` moves the candidate to Hired.

## Cross-cutting

- **Migrations:** applications regional migrations from 000025 on, plus one global-registry migration for slugs. Staging holds real legacy data, so every migration needs a backfill.
- **GDPR:** extend erasure and retention to notes, emails, events and interviews; keep special-category answers out of compare and exports; keep a human in every rejection (knockout review tasks already exist).
- **Residency:** all new data stays in the regional DB and bucket.
- **API:** idempotency keys and optimistic concurrency on mutations, swagger updated, BFF route predicates extended.
- **Tests:** an e2e script per phase.

## Decisions (settled)

1. **Disqualified and Hired are final.** No reinstate action. Stage moves only happen between open stages; Disqualified, Hired and Withdrawn are terminal. The existing status rules in migration 000016 stay, so Phase 2 only adds stages on top of them. Disqualify keeps a reason and optional note.
2. **Stages are per job**, seeded from a default set when the job is created.
3. **Company slug is chosen by the owner** (uniqueness and reserved-word checks).
4. **E-sign starts with our own** signature and audit trail; no external provider.
5. **Deleting a candidate anonymises** the record. Notes, emails, events and interviews are scrubbed of personal data and kept as counts and audit rows.
6. **Inbound email and CV parsing are out of scope** for now.

All decisions are made, so Phase 1 and Phase 2 are unblocked.
