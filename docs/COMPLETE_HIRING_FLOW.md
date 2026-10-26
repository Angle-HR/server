# Complete Hiring Flow

Implementation guide for the hiring API in this repository. Reviewed against the handlers and domain services on 22 October 2026. This describes implemented behavior; database catalogs and deployment configuration can change the available markets and capabilities.

## 1. Scope and recommended approach

The implemented flow is:

```text
Authenticated company user
  → load permissions and catalogs
  → satisfy company publishing requirements
  → create draft
  → save job details and hiring markets
  → configure application form
  → configure team, screening and compliance
  → preview and run publish-check
  → publish (or submit for approval)
  → share public job
Candidate, without signing in
  → read public job and privacy notice
  → upload required files
  → submit answers
Hiring team
  → list and open applications
  → review flagged applications
  → move candidate into review
  → hire, reject or withdraw
  → retention cleanup
```

The wizard steps are `details`, `application_form`, `permissions`, and `share`. Job status and wizard step are separate fields. A complete wizard does not bypass permission checks or publishing requirements.

This module ends at an application's `hired` status. It does not provide dedicated interview scheduling, offer negotiation, offer acceptance, or employee onboarding endpoints. Marking an application hired should not be treated as evidence that those separate processes ran.

Use the endpoint reference below in this order. All reference paths are relative to `/api/v1`, except the explicitly identified HTML pages.

## 2. Shared API contract

Protected endpoints require `Authorization: Bearer <access_token>`. The server resolves the caller's regional database, organization and combined role permissions; the organization owner receives founder permissions. A global-region account has incomplete onboarding for this module. An account with no organization cannot use organization-scoped hiring operations. Public endpoints do not require a token.

Send JSON bodies with `Content-Type: application/json`. Hiring JSON body reads have a 1 MiB cap. Most protected builder endpoints reject unknown JSON fields. Public upload and application request handlers use ordinary JSON unmarshalling, so do not rely on identical unknown-field behavior everywhere.

Successful JSON response:

```json
{
  "data": {"received": true},
  "meta": {"request_id": "request-id"}
}
```

`meta` and its optional properties can be omitted. The endpoint reference describes the contents of `data`. CSV, HTML and XML responses are exceptions to the JSON envelope.

Validation failure shape:

```json
{
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "some fields need attention",
    "details": {
      "fields": [{"path": "title", "message": "title must be at most 70 characters"}]
    }
  },
  "meta": {"request_id": "request-id"}
}
```

### Concurrent edits: ETag and If-Match

Job reads and many writes return `ETag: "<revision>"`. Retain the latest revision and send `If-Match: "<revision>"` on endpoints documented as requiring it. A missing header returns 428; an invalid value is a validation error; a stale revision returns 409 with `error.details.current_revision`. Reload, reconcile edits, then retry. Do not automatically overwrite another user's changes.

The form response calls the revision `job_revision`. It is the same job concurrency boundary. Team, form, compliance and screening writes also use the job revision even when their response does not contain a job object; reload the job when needed to obtain its current revision. Candidate status/review endpoints use application-state checks rather than job `If-Match`.

### Common errors

| HTTP | Meaning / client action |
| --- | --- |
| 400 | Invalid JSON, field value, query or header; display field errors when present. |
| 401 | Missing or invalid authentication on a protected route. |
| 403 | Caller lacks the required permission or job access. |
| 404 | Resource unavailable in this caller's scope, unknown public job, or no organization. |
| 409 | Stale revision, invalid status transition, review conflict, job not accepting applications, or reused idempotency key with different content. |
| 410 | Archived public job/application target is gone. |
| 413 | Request body exceeds the cap. |
| 422 | Publishing is blocked; display all `blocking`, `warnings`, and `gates` from error details. |
| 428 | Required job `If-Match` is missing. |
| 429 | Rate limited; respect `Retry-After` where returned. |
| 503 | A required service such as file handling is unavailable. |

Exact error codes and additional account/onboarding errors are defined in `pkg/apperror`; handle the structured error instead of matching message text.

## 3. Company setup and permissions

Start with `GET /hiring/me`, `GET /hiring/catalog`, `GET /hiring/departments`, and `GET /organization/agreements`. Use returned permissions to show actions; the server remains authoritative. Team membership grants visibility, not blanket editing rights.

Before publishing, the company must be verified, have accepted the current terms/DPA, and have a privacy contact. Drafting is allowed before verification. The current DPA version in code is `2026-10-draft`, explicitly a placeholder; always read `dpa_version` from the server instead of hardcoding it.

Example setup bodies:

```json
{"version":"2026-10-draft"}
```

Send to `POST /organization/agreements`; only a caller with `account.agreements.accept` can accept.

```json
{"privacy_contact_email":"privacy@example.com","dpo_contact":"Data Protection Officer"}
```

Send to `PUT /organization/privacy-contact`. Email must be a plain valid email, at most 254 characters; DPO contact is at most 300 characters.

For automated screening, Legal enables `PUT /hiring/settings` with `{"automated_screening_enabled":true}` and records `PUT /organization/dpia`:

```json
{"reference":"Internal DPIA record 2026-01","summary":"Screening of role-specific eligibility answers"}
```

DPIA requires a reference or summary; limits are 500 and 2,000 characters respectively. Recording creates a new version. Screening additionally needs the job's own DPIA scope confirmation.

## 4. Create and save a job

`POST /jobs` accepts an empty body or `{}` and returns 201, a job view, `ETag`, and `Location`. Every creation field is optional. Optional `template_id` and `form_template_id` select saved job/form templates. The ordinary job patch fields are flattened into the creation body, not nested under `details`.

A new job starts with six months' retention, visible pay, career-page visibility, and the standard form. Templates/defaults can supply other values.

Example body for `PATCH /jobs/{id}` or `PUT /jobs/{id}/details`:

```json
{
  "title":"Backend Engineer",
  "department_id":"11111111-1111-4111-8111-111111111111",
  "location_mode":"specific_area",
  "location_text":"Lagos, Nigeria",
  "use_company_address":false,
  "workplace_type":"hybrid",
  "travel_frequency":"none",
  "visa_sponsorship":"no",
  "employment_type":"full_time",
  "pay":{"type":"range","currency":"NGN","period":"month","min":50000000,"max":80000000,"visible":true},
  "skills":[{"custom_label":"Go"}],
  "markets":[{"market_code":"NG","city":"Lagos"}],
  "lawful_basis":"contract",
  "retention_months":6,
  "show_on_career_page":true
}
```

Replace example UUIDs with actual company/catalog IDs. The NG example also depends on the market being open in the current catalog. Amounts are minor units: `50000000` kobo represents NGN 500,000.

`PATCH` checks formats while allowing incomplete drafts. `PUT /details` validates the merged job's required detail fields and advances the wizard. The latter is still a partial merge, not replacement of the whole job. Publishing runs stricter checks and always requires complete pay.

Omitted patch fields preserve their values. Empty strings clear text. Arrays such as `skills` and `markets` replace their previous arrays; send `[]` to clear them. Description entries merge by section key; `null` or an empty string removes that description section. Null pointer fields generally behave like omission, so do not use null as a universal clearing instruction. Clearing `pay.type` clears pay amounts; exact pay normalizes max to min.

### Job fields and validation

| Field | Meaning and validation |
| --- | --- |
| `title` | At most 70 characters; letters, digits, spaces and supported punctuation `-/&(),.+#'’`. Required on continue/publish. |
| `department_id` | Company department UUID; use the department picker or create one first. |
| `closing_date` | Optional `YYYY-MM-DD`; cannot be in the past when validating saved details. Expiry is handled by the background lifecycle. |
| `location_mode` | `anywhere`, `specific_area`, `specific_timezone`. |
| `markets` | Up to 30 `{market_code, subdivision?, city?, timezone?}` selections; catalog and mode rules apply. Closed markets are rejected. Anywhere uses no explicit selections; effective markets are resolved by the server. |
| `location_text` | Display location, at most 200 characters. |
| `use_company_address` | Use the registered company address for location. |
| `workplace_type` | `onsite`, `hybrid`, `remote`. |
| `travel_frequency` | `none`, `occasional`, `frequent`. |
| `visa_sponsorship` | `yes`, `no`, `case_by_case`. |
| `employment_type` | `full_time`, `part_time`, `contract`, `internship`. |
| `description_sections` | JSON editor documents under `about_company`, `role`, `responsibilities`, `requirements`, `benefits`, `hiring_process`; total at most 200 KiB. |
| `industry_id`, `custom_industry` | Catalog UUID or custom text, not both; custom text at most 80 characters. |
| `seniority_level_id`, `experience_range_id` | Valid reference UUIDs from the catalog. |
| `pay` | Type `exact`/`range`, currency from catalog, period `hour`/`month`/`year`, nonnegative minor-unit amounts; max must not be below min. Requires `job.salary_range.set` to change. |
| `skills` | Up to 30 entries, each using either catalog `skill_id` or `custom_label` (at most 60 characters). `name` is output-only. |
| `lawful_basis` | `contract`, `legitimate_interests`, `consent`, `legal_obligation`. Required for publish. |
| `lia_reference` | Required for publishing on legitimate interests; at most 500 bytes. |
| `retention_months` | 3, 6 or 12; changing requires `job.retention.set`. Market minimums can raise the selected value. |
| `assessment_url` | Optional HTTPS URL, at most 500 characters; never exposed as a public URL by the job JSON. |
| `show_on_career_page` | Career-page visibility setting. |

The current currency set is GBP, EUR, USD, NGN, KES and INR. Prefer catalog responses over duplicated frontend constants. Fetch time zones rather than hardcoding current UTC offsets.

A job view flattens the job details alongside `id`, `job_number`, `job_code`, optional `public_id`, `created_by`, `status`, `current_step`, `completed_sections`, `revision`, timestamps, optional department name, `warnings`, optional `duplicates`, and optional `members`. `job_code` uses the per-company `JB-<number>` format. Warnings advise; they do not automatically block saves.

## 5. Application form, team and screening

Read `GET /jobs/{id}/application-form` first. Return the existing IDs when editing questions. `PUT` replaces the whole form, so preserve every question you intend to keep, especially the locked name/email questions.

Form request shape:

```json
{
  "questions":[
    {"id":"existing-name-id","section":"personal_information","position":0,"type":"short_text","label":"Full name","required":true,"config":{},"system_key":"full_name","locked":true},
    {"id":"existing-email-id","section":"personal_information","position":1,"type":"email","label":"Email","required":true,"config":{},"system_key":"email","locked":true},
    {"id":"existing-cv-id","section":"profile","position":0,"type":"file_upload","label":"CV","required":true,"config":{"max_mb":5,"accept":["pdf"]},"system_key":"cv","locked":false}
  ],
  "declarations":[]
}
```

IDs above are placeholders for IDs returned by the server. For new questions omit `id`; the builder generates IDs. `locked` is server-controlled. The default form also includes optional phone, location and resume-autofill fields. The example intentionally shows a reduced replacement form.

The response contains `job_id`, `job_revision`, `questions`, `declarations`, `automated_screening_enabled`, and `warnings`.

Form limits include 50 questions, and 2–50 options for choice fields. Supported types include short/long text, number, email, phone, link, single/multiple choice, dropdown, checkbox, linear scale, file upload, resume autofill, date and time. Config and answer schemas appear in the source contracts appendix below. Choice answers use stable option IDs, not display labels. CV uploads are PDF only. Pay-history questions are refused. Special-category questions need declarations with `category`, `legal_condition`, and `purpose`, plus a lawful basis on the job; special-category answers are not returned in ordinary candidate detail.

Replace the team using `PUT /jobs/{id}/members`:

```json
{"members":[{"user_id":"22222222-2222-4222-8222-222222222222","role":"hiring_manager"}]}
```

Roles: `hiring_manager`, `recruiter`, `interviewer`, `viewer`. Members must belong to the company; duplicates and the creator are rejected because the creator already has access. A limited collaborator permission permits only viewer additions/removals on eligible own jobs; preserve existing non-viewers when replacing the list.

Example screening body for `PUT /jobs/{id}/disqualification-rules`:

```json
{
  "rules":[{"question_id":"existing-choice-question-id","operator":"in","value":["no"],"reason":"Requires human review of eligibility"}],
  "dpia_scope_confirmed":true
}
```

`no` must be an actual option ID from that question. At most 20 rules, with a required reason of at most 300 characters. Choice operators are `in`/`not_in`; numeric/scale operators are `lt`, `lte`, `gt`, `gte`, `eq`, `neq`. Rules must match compatible existing questions. Saving rules needs company screening enabled. Changing rules without an explicit scope confirmation clears the old confirmation. Knockout and screening rules flag candidates for a human; neither automatically rejects them.

## 6. Publish, approval and sharing

1. Read the preview and compliance gates.
2. For each applicable manual gate, use `PUT /jobs/{id}/compliance/{gate_id}` with `{"confirmed":true}` when authorized. Gate IDs and wording come from the server. Automatic gates are evaluated from details. Confirmations against old gate versions become stale.
3. Run `GET /jobs/{id}/publish-check`. Its `data` is `{blocking:[], warnings:[], gates:[]}`; each issue has `code`, `severity`, `message`, and optional `path`/`gate_id`.
4. Fix all blockers, then `POST /jobs/{id}/publish` with the latest `If-Match` and no required body.
5. Inspect `data.job.status`. An eligible editor without external-publish permission submits the draft to `pending_approval`; an authorized publisher can publish the pending job through the same endpoint.
6. Once published, fetch `GET /jobs/{id}/share-kit`.

Publish checks run with the status change in the transactional update. A prior successful dry run does not guarantee the later write. Checks include verified company, current DPA, privacy contact, required details/pay, saved form, available hiring markets, lawful basis/LIA, retention minimums, special-category declarations, screening switch/DPIA/scope, valid rules, wording flags, and applicable compliance gates. A 422 includes the full report.

Publish and lifecycle response `data` is `{"job": <job view>, "warnings": [<issue>]}`. A share kit is `{"job_url":"https://<site>/j/<public_id>","links":[{"channel":"linkedin","label":"LinkedIn","url":"<share URL>"}]}`. Channels are WhatsApp, X, Facebook, LinkedIn, Telegram and email. Carry the link's `src` into application `source`.

### Job lifecycle

Every action below is `POST /jobs/{id}/{action}` with `If-Match` and no required body.

| Action | Allowed source | Result |
| --- | --- | --- |
| `publish` | draft / pending_approval | published, or approval submission for a caller without publishing permission |
| `withdraw` | pending_approval | draft |
| `pause` | published | paused |
| `resume` | paused | published; reruns publish checks |
| `close` | published / paused | closed |
| `reopen` | closed / expired | published; reruns publish checks |
| `to-draft` | published / paused | draft |
| `archive` | closed / expired | archived |

Expiry is system-driven from published status. Only drafts can be deleted. Pause, close and moving to draft trigger removal from boards through the lifecycle/outbox machinery. External synchronization is asynchronous; a successful API response is not proof every external channel has already updated.

## 7. Candidate flow and uploads

`GET /public/jobs/{public_id}` returns `public_id`, `state`, `accepting`, `job`, `markets`, optional `privacy_contact_email`, `privacy_notice_url`, `privacy_notice_version`, `consent_required`, and `url` inside `data`.

The public job contains display details, public pay when visible, and the published question schema. It omits knockout rules, internal lawful basis/retention, and the assessment URL. Always build the candidate form from these returned questions, not from a separately cached draft form. Submission validates against the form snapshot frozen at publication.

For each required file:

1. Request `POST /public/jobs/{public_id}/uploads`:

```json
{"question_id":"published-cv-question-id","filename":"candidate-cv.pdf","content_type":"application/pdf","size_bytes":120000}
```

2. A 201 returns `token`, `method`, `url`, `headers`, `expires_at`, and `max_bytes` inside `data`.
3. Upload raw file bytes to that returned URL using its method and exact returned headers. This storage request is separate from the API and does not use the JSON success envelope.
4. Use the returned token as that file question's answer. Uploading alone does not submit an application.

Limits depend on question config and plan: CV PDF caps are 5 MB free / 10 MB paid. File type, actual content/size, expiry and malware checks apply. A failed file is discarded and must be uploaded again. Storage/scanner configuration is required; file-dependent flows can return 503 when unavailable.

Submit `POST /public/jobs/{public_id}/applications`:

```json
{
  "candidate_country":"NG",
  "answers":{
    "published-name-question-id":"Ada Example",
    "published-email-question-id":"ada@example.com",
    "published-cv-question-id":"upload-token"
  },
  "privacy_notice_acknowledged":true,
  "consent":false,
  "source":"linkedin"
}
```

Replace keys with published question IDs and include every required answer. Country must be supported and eligible for the role's effective markets. Privacy acknowledgement is required; consent must be true when the public response requires it. Invalid/unknown source values normalize to `direct`. Total answers are capped at 200,000 encoded bytes.

Success is always 201 with `{"data":{"received":true}}` (plus optional metadata); no candidate ID, internal screening decision or duplicate indicator is disclosed. The same normalized email applying to the same job does not overwrite the original. A candidate blocked from reapplying also receives the same generic success, without disclosing the employer's prior decision.

Send an optional `Idempotency-Key` matching `[A-Za-z0-9_.:-]{8,64}`. Retry the same payload with the same key. Reusing it for different content returns 409. A recognized identical retry is answered before normal validation, including when its upload token has already been consumed or the job has since closed.

Application rate limits: 120 requests per IP per 15 minutes for flood protection, 30 validated submissions per IP per hour, and 5 per email/job per hour. Limiter failures are logged and allowed through. Upload presigning has its own limiter; do not assume the submission quota describes upload quotas.

HTML pages are `/j/{public_id}` and `/j/{public_id}/privacy` at the site root. `GET /api/v1/public/jobs/sitemap.xml` serves XML, not JSON.

## 8. Review candidates and complete hiring

List `GET /jobs/{id}/applications` with optional `status`, `new`, `q`, `cursor`, `limit` as specified in the endpoint reference. Status supports comma-separated/repeated values; `new=true` means nobody has opened it. Default page size is 25, maximum 100. Response `data` is `{items:[], next_cursor?:"...", counts:{...}}`. Counts cover the whole job, independent of filters. This pagination differs from the job list, whose cursor is in `meta`.

Rows contain `id`, `name`, `email`, `country`, `market`, `status`, `source`, `flag_count`, `opened`, `has_files`, and `submitted_at`; list rows contain no answers.

Opening `GET /jobs/{id}/applications/{app_id}` returns those fields plus `job_id`, `form_version`, `flags`, `answers`, `files`, optional `review`, optional `status_note`/`decided_at`/`consent_given_at`, `privacy_notice_version`, `retention_until`, and `can_decide`. Answers carry question ID, section, type, label, value or file reference. Special-category answers are never included.

Opening records an audit event and removes a first-opened application from the new count. A permitted decision maker opening a flagged application becomes its reviewer. This GET therefore has meaningful side effects; avoid prefetching it just to render a list.

### Application transitions

| Current state | Allowed operation | Next state |
| --- | --- | --- |
| received | status endpoint | in_review / withdrawn |
| in_review | status endpoint | rejected / hired / withdrawn |
| flagged | status endpoint | withdrawn only |
| flagged with open review | review decision `proceed` | in_review |
| flagged with open review | review decision `reject` | rejected |
| rejected / hired / withdrawn | none | final |

Example `POST /jobs/{id}/applications/{app_id}/status`:

```json
{"status":"in_review","note":"Recruiter has started reviewing"}
```

After review, send `{"status":"hired","note":"Hiring decision confirmed"}` or `{"status":"rejected","note":"Decision recorded"}` from `in_review`. Status response data contains `status` and optional `decided_at`. Notes are optional, at most 2,000 characters.

For a flagged application, first open it yourself, then `POST /jobs/{id}/applications/{app_id}/review`:

```json
{"decision":"proceed","note":"Reviewed eligibility evidence"}
```

Only `proceed` or `reject` is accepted. The decision maker must be the last eligible person to have opened the application; another reviewer opening it can require reopening before deciding. Conflicts include `review_required`, `no_open_review`, `not_opened`, `changed`, and `invalid_transition` in `error.details.reason`. There is no bulk candidate decision endpoint.

Decision rights belong to the creator, a job hiring manager/recruiter, or a caller with `job.manage_any`; viewing alone is insufficient. Use returned `can_decide` and `review.opened_by_me` for UI state.

`GET /jobs/{id}/applications/{app_id}/files/{file_id}` returns `{url, filename, expires_at}`. Links last five minutes and requests are audited. Obtain a fresh link when expired. Candidate handlers use `Cache-Control: no-store`.

Company candidate settings are read/written at `/hiring/applicant-settings`:

```json
{"reapply_block_months":6,"send_status_emails":true}
```

Only 0 or 6 months is allowed. Writes require `job.retention.set`; omitted fields preserve values. Six-month blocking requires the server's reapplication hashing key and can be checked through `reapply_block_available`. Rejection/hire notification emails use asynchronous delivery when enabled. Final decisions start the retention clock; retention workers remove eligible data and associated files according to the implemented privacy machinery.

## 9. Listing, reuse, export and operational details

- Jobs: `GET /jobs` supports status, department, search, creator, assignee, employment/workplace type, location mode, market and creation-date filters. Default sorting is updated-at descending; created-at and ascending are supported. Use opaque returned cursors, not offsets. Page size defaults to 25 and caps at 100.
- Tab counts: `GET /jobs/counts` returns `{all, by_status}` with every status, including zero counts. It shares list visibility and filters but ignores status/pagination/sort.
- Departments: names are unique per company ignoring case; creating an existing name returns the existing department.
- Templates: save from an existing job using `{kind, name, from_job_id, as_my_default}`. Kinds are `job_details` and `application_form`. Personal defaults support the caller's future drafts. Title and closing date are omitted from reusable setup; restricted pay/retention follows permissions.
- Duplicating a job creates a new draft owned by the caller with copied details/form and no team, screening rules, confirmations or public ID. Review copied closing dates and any form-level screening before publishing.
- `POST /jobs/bulk` accepts `{action, ids}` for up to 100 selected jobs. Actions use domain values such as `to_draft` (the single-job URL uses `to-draft`). Results are `{action, done:[{id,before,after}], skipped:[{id,reason}]}`. Each job is processed independently; inspect skips and do not assume an atomic batch or guaranteed undo.
- `GET /jobs/export` supports CSV by default or JSON with `format=json`, optional selected IDs and status. Requires `job.export`, respects visibility, and caps at 5,000 rows. CSV is a downloadable response; JSON returns list-row data in the envelope.
- Duplicate-warning dismissal records acknowledgement in audit; it does not permanently hide the warning.

The implementation separates HTTP handling (`hiringhttp`), domain rules (`jobs`, `questions`, `gates`, `lifecycle`, `screening`, `applications`, `apply`) and PostgreSQL persistence (`hiringstore`). The global registry resolves a public ID to the company's region; employer and candidate data live in regional stores. Publishing freezes a public form snapshot. Candidate submission validates answers, claims/scans file uploads, evaluates flags, seals sensitive answers where configured, persists the application and queues confirmation delivery. Durable outbox workers handle later delivery/synchronization. Do not perform those asynchronous effects again in the frontend.

## 10. Endpoint-by-endpoint reference

The following inventory is taken from the handler contracts. Each entry gives purpose, payload/query/header requirements, success status and response type. `data` is the listed envelope's `Data` field; exact request/response fields are provided in the source-contract appendix. A missing body parameter means no required JSON body. Paths below need the `/api/v1` prefix.

### Job form pick lists

- `GET /hiring/catalog`

Fixed lists (employment type, pay, travel, visa, lawful basis, currencies, retention options), seniority, experience, industries and every market with its warnings and whether it is open.

Success: `200 {object} hiringhttp.CatalogEnvelope`.

### Time zones

- `GET /hiring/timezones`

Time zones with their current UTC offset, for jobs that are open to a time zone rather than a place.

Success: `200 {object} hiringhttp.TimezonesEnvelope`.

### Search skills

- `GET /hiring/skills`

Request parameters (name, location, type, required, description):

- `q query string false "Start of the skill name"`
- `limit query int false "Most results (default 20, max 50)"`

Success: `200 {object} hiringhttp.SkillsEnvelope`.

### My hiring roles and permissions

- `GET /hiring/me`

The signed-in person's company roles and the permissions they add up to, such as job.export. Use it to show or hide actions; the server still checks every request.

Success: `200 {object} hiringhttp.MeEnvelope`.

### List the company's people

- `GET /hiring/people`

Members of the caller's company, with their roles, for the "add people to this job" picker. Search matches the name or email. Returns at most 50 people. Needs job.collaborator.add or job.collaborator.add_limited.

Request parameters (name, location, type, required, description):

- `q query string false "Part of a name or email"`

Success: `200 {object} hiringhttp.PeopleEnvelope`.

### List departments

- `GET /hiring/departments`

Success: `200 {object} hiringhttp.DepartmentsEnvelope`.

### Add a department

- `POST /hiring/departments`

Names are unique per company ignoring case; adding an existing name returns the existing department.

Request parameters (name, location, type, required, description):

- `body body hiringhttp.DepartmentRequest true "Department"`

Success: `200 {object} hiringhttp.DepartmentEnvelope`.

### List templates

- `GET /hiring/templates`

Company templates plus the caller's own default setup.

Request parameters (name, location, type, required, description):

- `kind query string false "job_details or application_form" Enums(job_details, application_form)`

Success: `200 {object} hiringhttp.TemplatesEnvelope`.

### Save a job's setup as a template

- `POST /hiring/templates`

The template is made from an existing job, so it always matches what the builders produce. Set as_my_default to keep the setup for the caller's future jobs ("Keep this setup"); otherwise name it to share it with the company. Titles and closing dates are never saved, and pay and retention only when the caller may set them.

Request parameters (name, location, type, required, description):

- `body body draft.TemplateInput true "Template"`

Success: `201 {object} hiringhttp.TemplateEnvelope`.

### Delete a template

- `DELETE /hiring/templates/{id}`

Request parameters (name, location, type, required, description):

- `id path string true "Template id"`

Success: `200 {object} map[string]bool`.

### Rename or pin a template

- `PATCH /hiring/templates/{id}`

Send name, pinned or both. Only company templates can be changed; a personal default cannot. Pinned templates list first for the whole company.

Request parameters (name, location, type, required, description):

- `id path string true "Template id"`
- `body body hiringhttp.TemplatePatchRequest true "Changes"`

Success: `200 {object} hiringhttp.TemplateEnvelope`.

### Duplicate a template

- `POST /hiring/templates/{id}/duplicate`

Copies a company template. Without a name the copy is called "<name> (copy)".

Request parameters (name, location, type, required, description):

- `id path string true "Template id"`
- `body body hiringhttp.TemplateDuplicateRequest false "Copy name"`

Success: `201 {object} hiringhttp.TemplateEnvelope`.

### Export a template

- `GET /hiring/templates/{id}/export`

Returns a company template's kind, name and payload in a portable form.

Request parameters (name, location, type, required, description):

- `id path string true "Template id"`

Success: `200 {object} hiringhttp.TemplateExportEnvelope`.

### Hiring settings

- `GET /hiring/settings`

Company-level switches. Automated screening (knockout questions) is off until the company's legal contact turns it on.

Success: `200 {object} hiringhttp.SettingsEnvelope`.

### Change hiring settings

- `PUT /hiring/settings`

Needs form.automated_screening.enable (Legal).

Request parameters (name, location, type, required, description):

- `body body hiringhttp.SettingsRequest true "Settings"`

Success: `200 {object} hiringhttp.SettingsEnvelope`.

### List jobs

- `GET /jobs`

Newest updated first unless sort and order say otherwise. People who can only see assigned jobs get the jobs they created or were added to. Each row carries the workplace type, published and closing dates and the hiring managers. applicant_count is the number of applications received and new_applicant_count the number nobody has opened yet.

Request parameters (name, location, type, required, description):

- `status query string false "Comma-separated statuses, e.g. draft,published"`
- `department_id query string false "Only this department"`
- `q query string false "Search the title or department name"`
- `created_by query string false "Only jobs created by this user id"`
- `assignee query string false "Only jobs this user id is on the hiring team of"`
- `employment_type query string false "full_time, part_time, contract or internship"`
- `workplace_type query string false "onsite, hybrid or remote"`
- `location_mode query string false "anywhere, specific_area or specific_timezone"`
- `market query string false "Two-letter market code, e.g. UK"`
- `created_from query string false "Created on or after this date (YYYY-MM-DD)"`
- `created_to query string false "Created on or before this date (YYYY-MM-DD)"`
- `sort query string false "updated_at (default) or created_at"`
- `order query string false "desc (default) or asc"`
- `cursor query string false "next_cursor from the previous page"`
- `limit query int false "Page size (default 25, max 100)"`

Success: `200 {object} hiringhttp.JobListEnvelope`.

### Count jobs per status

- `GET /jobs/counts`

The numbers for the status tabs, in one call instead of loading every job. Takes the same filters as the jobs list (search, department, creator, assignee, employment and workplace type, location mode, market, created dates); status, cursor, limit and sort are ignored. Every status is present, zero when there are none, and all is the total. People who can only see assigned jobs are counted on those jobs only.

Request parameters (name, location, type, required, description):

- `q query string false "Search title or department"`
- `department_id query string false "Only this department"`
- `created_by query string false "Only jobs created by this user id"`
- `assignee query string false "Only jobs this user id is on the hiring team of"`
- `employment_type query string false "full_time, part_time, contract or internship"`
- `workplace_type query string false "onsite, hybrid or remote"`
- `location_mode query string false "anywhere, specific_area or specific_timezone"`
- `market query string false "Two-letter market code, e.g. UK"`
- `created_from query string false "Created on or after this date (YYYY-MM-DD)"`
- `created_to query string false "Created on or before this date (YYYY-MM-DD)"`

Success: `200 {object} hiringhttp.JobCountsEnvelope`.

### Create a draft job

- `POST /jobs`

All fields are optional, so the page can create a draft the first time the user saves. The draft starts from the caller's saved default setup and the standard application form unless template_id or form_template_id say otherwise.

Request parameters (name, location, type, required, description):

- `body body draft.CreateRequest false "Job fields"`

Success: `201 {object} hiringhttp.JobEnvelope`.

### Get a job

- `GET /jobs/{id}`

Returns the job with warnings (market rules, pay advice, possible duplicates). The ETag header carries the revision to send back in If-Match.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`

Success: `200 {object} hiringhttp.JobEnvelope`.

### Save a draft

- `PATCH /jobs/{id}`

"Save as draft": send only the fields that changed. Formats are checked, required fields are not. Pay needs job.salary_range.set and retention needs job.retention.set.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`
- `If-Match header string true "Revision the client loaded"`
- `body body jobs.Patch true "Fields to change"`

Success: `200 {object} hiringhttp.JobEnvelope`.

### Save and continue on the job details step

- `PUT /jobs/{id}/details`

Same body as PATCH, but the required fields (title, department, hiring option, travel, visa, employment type and, for people who can set pay, pay) must be present once applied. Moves the draft to the application form step.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`
- `If-Match header string true "Revision the client loaded"`
- `body body jobs.Patch true "Fields to change"`

Success: `200 {object} hiringhttp.JobEnvelope`.

### Set the hiring option and markets

- `PUT /jobs/{id}/markets`

Replaces the places a job is open to. Closed markets are rejected, an anywhere job takes no places, and the retention period is raised to the strictest minimum of the chosen markets. The response carries the market warnings.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`
- `If-Match header string true "Revision the client loaded"`
- `body body hiringhttp.MarketsRequest true "Markets"`

Success: `200 {object} hiringhttp.JobEnvelope`.

### Delete a draft

- `DELETE /jobs/{id}`

Only drafts can be deleted. Everything else is closed or archived instead.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`
- `If-Match header string true "Revision the client loaded"`

Success: `200 {object} map[string]bool`.

### Get the application form

- `GET /jobs/{id}/application-form`

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`

Success: `200 {object} hiringhttp.FormEnvelope`.

### Save the application form

- `PUT /jobs/{id}/application-form`

Replaces the whole form. Full name and email are locked, the CV accepts PDF only, questions about current or past pay are refused, knockout rules need automated screening enabled for the company, and special category questions need a declaration and a lawful basis on the job. A knockout flags a candidate for a person to review; it never rejects anyone.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`
- `If-Match header string true "Revision the client loaded"`
- `body body draft.FormInput true "Form"`

Success: `200 {object} hiringhttp.FormEnvelope`.

### Preview the posting

- `GET /jobs/{id}/preview`

The candidate-facing view, built the same way as the live page. It never contains knockout rules, the lawful basis, retention or the assessment link.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`

Success: `200 {object} hiringhttp.PreviewEnvelope`.

### Acknowledge the duplicate-job warning

- `POST /jobs/{id}/duplicates/dismiss`

Records in the audit log that the user saw the warning and kept going. The warning still shows if the job is opened again.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`

Success: `200 {object} map[string]bool`.

### Check whether a job can publish

- `GET /jobs/{id}/publish-check`

The dry run of publishing: the same checks, nothing written. Blocking issues stop a publish; warnings do not. Also lists the compliance gates that apply and where each stands.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`

Success: `200 {object} hiringhttp.ReportEnvelope`.

### Publish a job

- `POST /jobs/{id}/publish`

Runs every publish check inside the same transaction as the status change: a verified company, an accepted DPA, a privacy contact, complete details, an open market, a lawful basis, the screening prerequisites and every required compliance gate. People who may not publish submit the job for approval instead. A blocked publish returns 422 with every issue.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`
- `If-Match header string true "Revision the client loaded"`

Success: `200 {object} hiringhttp.TransitionEnvelope`.

### Change a job's status

- `POST /jobs/{id}/pause`
- `POST /jobs/{id}/resume`
- `POST /jobs/{id}/close`
- `POST /jobs/{id}/reopen`
- `POST /jobs/{id}/archive`
- `POST /jobs/{id}/to-draft`
- `POST /jobs/{id}/withdraw`

One path per action: pause, resume, close, reopen, archive, to-draft and withdraw. Pause, close and move-to-draft take a job off every board. Resume and reopen run the publish checks again. HR 2 can change only the jobs it created. Which statuses allow which action is in the status rules of the lifecycle package.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`
- `If-Match header string true "Revision the client loaded"`

Success: `200 {object} hiringhttp.TransitionEnvelope`.

### Duplicate a job

- `POST /jobs/{id}/duplicate`

Copies the details and application form into a new draft owned by the caller. The copy has no team, no screening rules, no confirmations and no public id.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`

Success: `201 {object} hiringhttp.JobEnvelope`.

### List a job's compliance gates

- `GET /jobs/{id}/compliance`

The gates that apply to the job's markets, special categories and screening, with where each stands. Gates marked auto are answered from the job's own details.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`

Success: `200 {object} hiringhttp.GatesEnvelope`.

### Confirm or withdraw a compliance gate

- `PUT /jobs/{id}/compliance/{gate_id}`

The confirmation is the company's own and is written to the append-only compliance log against the caller. A confirmation stops counting when the gate's wording changes.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`
- `gate_id path string true "Gate id, e.g. uk-lawful"`
- `If-Match header string true "Revision the client loaded"`
- `body body hiringhttp.ConfirmRequest true "Confirmation"`

Success: `200 {object} hiringhttp.GatesEnvelope`.

### List a job's hiring team

- `GET /jobs/{id}/members`

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`

Success: `200 {object} hiringhttp.MembersEnvelope`.

### Set a job's hiring team

- `PUT /jobs/{id}/members`

Replaces the team (the Permissions step). The creator already has access and is not listed. Team members see the job and its candidates but gain no edit rights. HR 2 can add and remove viewers on its own jobs only.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`
- `If-Match header string true "Revision the client loaded"`
- `body body hiringhttp.MembersRequest true "The whole team"`

Success: `200 {object} hiringhttp.MembersEnvelope`.

### Get a job's screening rules

- `GET /jobs/{id}/disqualification-rules`

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`

Success: `200 {object} hiringhttp.RulesEnvelope`.

### Set a draft's screening rules

- `PUT /jobs/{id}/disqualification-rules`

Replaces the rules. A rule flags a candidate for a person to review; it never rejects anyone. Rules count as automated screening, so they need Legal to have enabled screening for the company and, before publishing, the company DPIA and this job's DPIA scope confirmation.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`
- `If-Match header string true "Revision the client loaded"`
- `body body draft.RulesInput true "Rules"`

Success: `200 {object} hiringhttp.RulesEnvelope`.

### Change several jobs at once

- `POST /jobs/bulk`

Pause, resume, close, reopen, archive or move to draft, for up to 100 jobs. Resume and reopen run the publish checks for each job; a job that fails them is skipped with the reasons. Not all-or-nothing: each job is changed on its own, and the ones that could not be are listed with the reason. The result carries each job's earlier status so the client can offer undo.

Request parameters (name, location, type, required, description):

- `body body hiringhttp.BulkRequest true "Action and job ids"`

Success: `200 {object} hiringhttp.BulkEnvelope`.

### Export jobs as CSV or JSON

- `GET /jobs/export`

Every job the caller may see, newest first, or only the jobs in ids. CSV is the default; format=json returns the same rows as on the jobs list. Needs job.export.

Request parameters (name, location, type, required, description):

- `status query string false "Comma-separated statuses"`
- `ids query string false "Comma-separated job ids; only these jobs are exported"`
- `format query string false "csv (default) or json"`

Success: `200 {string} string "CSV"`.

### Show the company's publishing requirements

- `GET /organization/agreements`

Verification status, the DPA version to accept and whether it is accepted, the privacy contact and the DPIA.

Success: `200 {object} hiringhttp.CompanySetupEnvelope`.

### Accept the terms and data processing agreement

- `POST /organization/agreements`

Only a Founder or Legal can. The version must be the current one shown by GET.

Request parameters (name, location, type, required, description):

- `body body hiringhttp.AcceptRequest true "Version"`

Success: `200 {object} hiringhttp.CompanySetupEnvelope`.

### Set the privacy contact

- `PUT /organization/privacy-contact`

The address applicants are told to write to, and an optional DPO contact. Needed before publishing.

Request parameters (name, location, type, required, description):

- `body body draft.PrivacyContactInput true "Contact"`

Success: `200 {object} hiringhttp.CompanySetupEnvelope`.

### Record the company's DPIA

- `PUT /organization/dpia`

Legal records the data protection impact assessment before the first screening job publishes. Each call stores a new version.

Request parameters (name, location, type, required, description):

- `body body draft.DPIAInput true "Where the assessment is kept and what it covers"`

Success: `200 {object} hiringhttp.CompanySetupEnvelope`.

### Share kit

- `GET /jobs/{id}/share-kit`

The job's public link and ready-made share links for WhatsApp, X, Facebook, LinkedIn, Telegram and email. Each link carries its channel as src, so applications show which link brought the candidate. Available once the job has been published.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`

Success: `200 {object} hiringhttp.ShareKitEnvelope`.

### Public job

- `GET /public/jobs/{public_id}`

The job and its application form as a candidate sees it. No sign-in. Knockout rules, the lawful basis and the assessment link are never included. Closed, paused and archived jobs say so in state.

Request parameters (name, location, type, required, description):

- `public_id path string true "Public job id"`

Success: `200 {object} hiringhttp.PublicJobEnvelope`.

### Apply for a job

- `POST /public/jobs/{public_id}/applications`

Sends an application. No sign-in. The answers are checked against the form that was frozen when the job was published. A failed knockout or screening rule flags the application for a person to review; it never rejects it. Applying again with the same email returns the same success and changes nothing. Rate limited.

Request parameters (name, location, type, required, description):

- `public_id path string true "Public job id"`
- `Idempotency-Key header string false "A random 8 to 64 character key; a retry with the same key never creates a second application"`
- `body body hiringhttp.ApplicationRequest true "Application"`

Success: `201 {object} hiringhttp.ApplicationReceiptEnvelope`.

### Get an upload URL

- `POST /public/jobs/{public_id}/uploads`

Returns a one-time URL to PUT a file to (a CV, for example). Only the file types and size the question allows are accepted: a CV is PDF only, 5 MB on the free plan and 10 MB on paid plans. Send the file with PUT and exactly the returned headers within the time shown, then give the token as the answer to the question when applying. The file is scanned for malware when the application is submitted; a file that fails is discarded. No sign-in. Rate limited.

Request parameters (name, location, type, required, description):

- `public_id path string true "Public job id"`
- `body body hiringhttp.UploadRequest true "File to upload"`

Success: `201 {object} hiringhttp.UploadTargetEnvelope`.

### List a job's applications

- `GET /jobs/{id}/applications`

Newest first, 25 per page by default (100 at most). Rows carry no answers. `status` can be repeated or comma separated; `new=true` lists applications nobody has opened. `counts` are for the whole job, whatever the filters. Seen by people who can see the job.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`
- `status query []string false "received, in_review, flagged, rejected, hired or withdrawn"`
- `source query string false "Where the candidate came from"`
- `q query string false "Search name or email"`
- `new query bool false "Only applications nobody has opened"`
- `cursor query string false "next_cursor of the previous page"`
- `limit query int false "Page size"`

Success: `200 {object} hiringhttp.ApplicationPageEnvelope`.

### Open an application

- `GET /jobs/{id}/applications/{app_id}`

Returns the answers (special category answers are never returned), the files, the flags and the review. Opening is recorded in the job's audit log; the first opening takes the application out of the job's new count. Whoever may decide a flagged application and opens it becomes its reviewer.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`
- `app_id path string true "Application id"`

Success: `200 {object} hiringhttp.ApplicationEnvelope`.

### Change an application's status

- `POST /jobs/{id}/applications/{app_id}/status`

received to in_review or withdrawn; in_review to rejected, hired or withdrawn; flagged to withdrawn only. A flagged application is decided through its review. rejected, hired and withdrawn are final and start the retention clock. A rejection or hire emails the candidate unless the company turned that off. Needs to be the job's creator, a hiring manager or recruiter on it, or job.manage_any.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`
- `app_id path string true "Application id"`
- `body body hiringhttp.ApplicationStatusRequest true "New status"`

Success: `200 {object} hiringhttp.ApplicationResultEnvelope`.

### Decide a flagged application

- `POST /jobs/{id}/applications/{app_id}/review`

`proceed` moves it to in_review; `reject` rejects it. The person deciding must be the last to have opened the application (GET it first), so every flagged candidate is looked at by a person before they are decided. There is no bulk version.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`
- `app_id path string true "Application id"`
- `body body hiringhttp.ApplicationReviewRequest true "Decision"`

Success: `200 {object} hiringhttp.ApplicationResultEnvelope`.

### Get a download link for an application's file

- `GET /jobs/{id}/applications/{app_id}/files/{file_id}`

Returns a link that works for 5 minutes and downloads the file under its original name. The request is recorded in the job's audit log. Ask again for a new link.

Request parameters (name, location, type, required, description):

- `id path string true "Job id"`
- `app_id path string true "Application id"`
- `file_id path string true "File id"`

Success: `200 {object} hiringhttp.FileLinkEnvelope`.

### Candidate settings

- `GET /hiring/applicant-settings`

Whether rejected candidates are blocked from reapplying (0 or 6 months) and whether candidates are emailed when rejected or hired.

Success: `200 {object} hiringhttp.ApplicantSettingsEnvelope`.

### Change candidate settings

- `PUT /hiring/applicant-settings`

Needs job.retention.set. Blocking reapplications (6 months) cannot be turned on while the server has no key for it.

Request parameters (name, location, type, required, description):

- `body body hiringhttp.ApplicantSettingsRequest true "Settings"`

Success: `200 {object} hiringhttp.ApplicantSettingsEnvelope`.

### Public pages and sitemap

- `GET /j/{public_id}`: HTML job page, social previews and job structured data. Root path, no API prefix.
- `GET /j/{public_id}/privacy`: HTML applicant privacy notice. Root path, no API prefix.
- `GET /api/v1/public/jobs/sitemap.xml`: XML sitemap of eligible public jobs.

## 11. Exact source contracts

The Go contracts below are included to make all payload and response field names reviewable without duplicating an approximate JSON schema. `json` tags are wire names; `omitempty` means a field may be absent; pointers in patch/settings input generally distinguish omission from a supplied value. Embedded structs flatten their JSON fields. `json.RawMessage` is a JSON value, not a JSON-encoded string. These excerpts are a snapshot; follow the linked source when implementation changes.

### [hiringhttp/catalog.go](../internal/hiring/hiringhttp/catalog.go)

```go
type CatalogEnvelope struct {
	Data draft.Catalog `json:"data"`
	Meta *apidoc.Meta  `json:"meta,omitempty"`
}

type TimezonesEnvelope struct {
	Data []jobs.Timezone `json:"data"`
	Meta *apidoc.Meta    `json:"meta,omitempty"`
}

type SkillsEnvelope struct {
	Data []hiringtypes.CatalogItem `json:"data"`
	Meta *apidoc.Meta              `json:"meta,omitempty"`
}

type MeEnvelope struct {
	Data draft.MeView `json:"data"`
	Meta *apidoc.Meta `json:"meta,omitempty"`
}

type PeopleEnvelope struct {
	Data []hiringtypes.Person `json:"data"`
	Meta *apidoc.Meta         `json:"meta,omitempty"`
}

type DepartmentsEnvelope struct {
	Data []hiringtypes.Department `json:"data"`
	Meta *apidoc.Meta             `json:"meta,omitempty"`
}

type DepartmentEnvelope struct {
	Data hiringtypes.Department `json:"data"`
	Meta *apidoc.Meta           `json:"meta,omitempty"`
}

type TemplatesEnvelope struct {
	Data []hiringtypes.Template `json:"data"`
	Meta *apidoc.Meta           `json:"meta,omitempty"`
}

type TemplateEnvelope struct {
	Data hiringtypes.Template `json:"data"`
	Meta *apidoc.Meta         `json:"meta,omitempty"`
}

type SettingsEnvelope struct {
	Data hiringtypes.Settings `json:"data"`
	Meta *apidoc.Meta         `json:"meta,omitempty"`
}

type DepartmentRequest struct {
	Name string `json:"name" example:"Customer Support"`
}

type SettingsRequest struct {
	AutomatedScreeningEnabled bool `json:"automated_screening_enabled"`
}

type TemplatePatchRequest struct {
	Name   *string `json:"name" example:"Support roles"`
	Pinned *bool   `json:"pinned" example:"true"`
}

type TemplateDuplicateRequest struct {
	Name string `json:"name" example:"Support roles (copy)"`
}

type TemplateExportEnvelope struct {
	Data draft.TemplateExport `json:"data"`
	Meta *apidoc.Meta         `json:"meta,omitempty"`
}
```

### [hiringhttp/jobs.go](../internal/hiring/hiringhttp/jobs.go)

```go
type JobEnvelope struct {
	Data draft.JobView `json:"data"`
	Meta *apidoc.Meta  `json:"meta,omitempty"`
}

type JobListEnvelope struct {
	Data []hiringtypes.ListItem `json:"data"`
	Meta *apidoc.Meta           `json:"meta,omitempty"`
}

type FormEnvelope struct {
	Data draft.FormView `json:"data"`
	Meta *apidoc.Meta   `json:"meta,omitempty"`
}

type PreviewEnvelope struct {
	Data jobs.PublicJob `json:"data"`
	Meta *apidoc.Meta   `json:"meta,omitempty"`
}

type JobCountsEnvelope struct {
	Data draft.JobCounts `json:"data"`
	Meta *apidoc.Meta    `json:"meta,omitempty"`
}

type MarketsRequest struct {
	LocationMode string           `json:"location_mode"`
	Markets      []jobs.MarketSel `json:"markets"`
}
```

### [hiringhttp/publish.go](../internal/hiring/hiringhttp/publish.go)

```go
type ConfirmRequest struct {
	Confirmed bool `json:"confirmed"`
}

type MembersRequest struct {
	Members []draft.MemberInput `json:"members"`
}

type BulkRequest struct {
	Action string   `json:"action"`
	IDs    []string `json:"ids"`
}

type AcceptRequest struct {
	Version string `json:"version"`
}
```

### [hiringhttp/sharekit.go](../internal/hiring/hiringhttp/sharekit.go)

```go
type ShareKitEnvelope struct {
	Data public.ShareKit `json:"data"`
	Meta *apidoc.Meta    `json:"meta,omitempty"`
}
```

### [hiringhttp/public.go](../internal/hiring/hiringhttp/public.go)

```go
type PublicJobData struct {
	PublicID         string         `json:"public_id"`
	State            string         `json:"state"`
	Accepting        bool           `json:"accepting"`
	Job              jobs.PublicJob `json:"job"`
	Markets          []string       `json:"markets"`
	PrivacyContact   string         `json:"privacy_contact_email,omitempty"`
	PrivacyNoticeURL string         `json:"privacy_notice_url"`
	NoticeVersion    string         `json:"privacy_notice_version"`
	ConsentRequired  bool           `json:"consent_required"`
	URL              string         `json:"url"`
}

type PublicJobEnvelope struct {
	Data PublicJobData `json:"data"`
	Meta *apidoc.Meta  `json:"meta,omitempty"`
}

type ApplicationRequest struct {
	// CandidateCountry is the candidate's country, ISO 3166-1 alpha-2. It must belong to one of the job's markets.
	CandidateCountry string `json:"candidate_country"`
	// Answers maps a question id to the answer, in the shape that question's type expects.
	Answers                   map[string]json.RawMessage `json:"answers"`
	PrivacyNoticeAcknowledged bool                       `json:"privacy_notice_acknowledged"`
	// Consent is required only when the job's lawful basis is consent (see consent_required).
	Consent bool `json:"consent"`
	// Source is the share channel the candidate came from (the src of the link); anything else is "direct".
	Source string `json:"source"`
}

type ApplicationReceipt struct {
	Received bool `json:"received"`
}

type ApplicationReceiptEnvelope struct {
	Data ApplicationReceipt `json:"data"`
	Meta *apidoc.Meta       `json:"meta,omitempty"`
}

type UploadRequest struct {
	// QuestionID is the file question the upload answers.
	QuestionID string `json:"question_id"`
	Filename   string `json:"filename"`
	// ContentType must be one the question accepts, e.g. application/pdf for a CV.
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

type UploadTarget struct {
	Token     string            `json:"token"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
	MaxBytes  int64             `json:"max_bytes"`
}

type UploadTargetEnvelope struct {
	Data UploadTarget `json:"data"`
	Meta *apidoc.Meta `json:"meta,omitempty"`
}
```

### [hiringhttp/applications.go](../internal/hiring/hiringhttp/applications.go)

```go
type ApplicationStatusRequest struct {
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

type ApplicationReviewRequest struct {
	Decision string `json:"decision"`
	Note     string `json:"note,omitempty"`
}

type ApplicantSettingsRequest struct {
	ReapplyBlockMonths *int  `json:"reapply_block_months,omitempty"`
	SendStatusEmails   *bool `json:"send_status_emails,omitempty"`
}
```

### [jobs/model.go](../internal/hiring/jobs/model.go)

```go
type Skill struct {
	SkillID     string `json:"skill_id,omitempty"`
	CustomLabel string `json:"custom_label,omitempty"`
	Name        string `json:"name,omitempty"` // resolved label on output; ignored on input
}

type MarketSel struct {
	MarketCode  string `json:"market_code"`
	Subdivision string `json:"subdivision,omitempty"`
	City        string `json:"city,omitempty"`
	Timezone    string `json:"timezone,omitempty"`
}

type Pay struct {
	Type     string `json:"type"`
	Currency string `json:"currency"`
	Period   string `json:"period"`
	Min      *int64 `json:"min"`
	Max      *int64 `json:"max"`
	Visible  bool   `json:"visible"`
}

type Details struct {
	Title             string      `json:"title"`
	DepartmentID      string      `json:"department_id"`
	ClosingDate       string      `json:"closing_date"`
	LocationMode      string      `json:"location_mode"`
	LocationText      string      `json:"location_text"`
	UseCompanyAddress bool        `json:"use_company_address"`
	WorkplaceType     string      `json:"workplace_type"`
	TravelFrequency   string      `json:"travel_frequency"`
	VisaSponsorship   string      `json:"visa_sponsorship"`
	Description       Sections    `json:"description_sections"`
	IndustryID        string      `json:"industry_id"`
	CustomIndustry    string      `json:"custom_industry"`
	EmploymentType    string      `json:"employment_type"`
	SeniorityLevelID  string      `json:"seniority_level_id"`
	ExperienceRangeID string      `json:"experience_range_id"`
	Pay               Pay         `json:"pay"`
	ShowOnCareerPage  bool        `json:"show_on_career_page"`
	Skills            []Skill     `json:"skills"`
	Markets           []MarketSel `json:"markets"`
	LawfulBasis       string      `json:"lawful_basis"`
	LIAReference      string      `json:"lia_reference"`
	RetentionMonths   int         `json:"retention_months"`
	AssessmentURL     string      `json:"assessment_url"`
}

type Job struct {
	ID                string   `json:"id"`
	JobNumber         int      `json:"job_number"`
	JobCode           string   `json:"job_code"`
	PublicID          string   `json:"public_id,omitempty"`
	CreatedBy         string   `json:"created_by"`
	Status            string   `json:"status"`
	CurrentStep       string   `json:"current_step"`
	CompletedSections []string `json:"completed_sections"`
	Revision          int      `json:"revision"`
	DepartmentName    string   `json:"department_name,omitempty"`
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`
	PublishedAt       string   `json:"published_at,omitempty"`
	Details
}

type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type Warning struct {
	Code     string `json:"code"`
	Title    string `json:"title,omitempty"`
	Message  string `json:"message"`
	Market   string `json:"market,omitempty"`
	Severity string `json:"severity,omitempty"`
}
```

### [jobs/patch.go](../internal/hiring/jobs/patch.go)

```go
type PayPatch struct {
	Type     *string `json:"type"`
	Currency *string `json:"currency"`
	Period   *string `json:"period"`
	Min      *int64  `json:"min"`
	Max      *int64  `json:"max"`
	Visible  *bool   `json:"visible"`
}

type Patch struct {
	Title             *string                    `json:"title"`
	DepartmentID      *string                    `json:"department_id"`
	ClosingDate       *string                    `json:"closing_date"`
	LocationMode      *string                    `json:"location_mode"`
	LocationText      *string                    `json:"location_text"`
	UseCompanyAddress *bool                      `json:"use_company_address"`
	WorkplaceType     *string                    `json:"workplace_type"`
	TravelFrequency   *string                    `json:"travel_frequency"`
	VisaSponsorship   *string                    `json:"visa_sponsorship"`
	Description       map[string]json.RawMessage `json:"description_sections"`
	IndustryID        *string                    `json:"industry_id"`
	CustomIndustry    *string                    `json:"custom_industry"`
	EmploymentType    *string                    `json:"employment_type"`
	SeniorityLevelID  *string                    `json:"seniority_level_id"`
	ExperienceRangeID *string                    `json:"experience_range_id"`
	Pay               *PayPatch                  `json:"pay"`
	ShowOnCareerPage  *bool                      `json:"show_on_career_page"`
	Skills            *[]Skill                   `json:"skills"`
	Markets           *[]MarketSel               `json:"markets"`
	LawfulBasis       *string                    `json:"lawful_basis"`
	LIAReference      *string                    `json:"lia_reference"`
	RetentionMonths   *int                       `json:"retention_months"`
	AssessmentURL     *string                    `json:"assessment_url"`
}
```

### [jobs/public.go](../internal/hiring/jobs/public.go)

```go
type PublicQuestion struct {
	ID          string          `json:"id"`
	Section     string          `json:"section"`
	Type        string          `json:"type"`
	Label       string          `json:"label"`
	Description string          `json:"description,omitempty"`
	HelperText  string          `json:"helper_text,omitempty"`
	Required    bool            `json:"required"`
	Config      json.RawMessage `json:"config"`
	SystemKey   string          `json:"system_key,omitempty"`
	Sensitive   bool            `json:"sensitive,omitempty"`
}

type PublicPay struct {
	Type     string `json:"type"`
	Currency string `json:"currency"`
	Period   string `json:"period"`
	Min      int64  `json:"min"`
	Max      int64  `json:"max"`
}

type PublicLocation struct {
	MarketCode  string `json:"market_code"`
	Subdivision string `json:"subdivision,omitempty"`
	City        string `json:"city,omitempty"`
	Timezone    string `json:"timezone,omitempty"`
}

type PublicJob struct {
	JobCode           string           `json:"job_code"`
	Title             string           `json:"title"`
	Company           string           `json:"company"`
	Department        string           `json:"department,omitempty"`
	Description       Sections         `json:"description_sections"`
	DescriptionText   string           `json:"description_text"`
	LocationMode      string           `json:"location_mode"`
	LocationText      string           `json:"location_text,omitempty"`
	Locations         []PublicLocation `json:"locations"`
	WorkplaceType     string           `json:"workplace_type,omitempty"`
	EmploymentType    string           `json:"employment_type,omitempty"`
	TravelFrequency   string           `json:"travel_frequency,omitempty"`
	VisaSponsorship   string           `json:"visa_sponsorship,omitempty"`
	Industry          string           `json:"industry,omitempty"`
	Seniority         string           `json:"seniority,omitempty"`
	Experience        string           `json:"experience,omitempty"`
	Skills            []string         `json:"skills"`
	Pay               *PublicPay       `json:"pay,omitempty"`
	ClosingDate       string           `json:"closing_date,omitempty"`
	Questions         []PublicQuestion `json:"questions"`
	AssessmentLinkSet bool             `json:"assessment_link_set,omitempty"`
}
```

### [jobs/timezones.go](../internal/hiring/jobs/timezones.go)

```go
type Timezone struct {
	Name          string `json:"name"`
	Offset        string `json:"offset"`
	OffsetSeconds int    `json:"offset_seconds"`
}
```

### [jobs/markets.go](../internal/hiring/jobs/markets.go)

```go
type Market struct {
	Code               string    `json:"code"`
	Name               string    `json:"name"`
	IsOpen             bool      `json:"is_open"`
	MinRetentionMonths int       `json:"min_retention_months,omitempty"`
	Currencies         []string  `json:"currencies,omitempty"`
	Warnings           []Warning `json:"warnings"`
}
```

### [jobs/dupes.go](../internal/hiring/jobs/dupes.go)

```go
type Duplicate struct {
	ID      string `json:"id"`
	JobCode string `json:"job_code"`
	Title   string `json:"title"`
	Status  string `json:"status"`
}
```

### [draft/service.go](../internal/hiring/draft/service.go)

```go
type JobView struct {
	jobs.Job
	Warnings   []jobs.Warning   `json:"warnings"`
	Duplicates []jobs.Duplicate `json:"duplicates,omitempty"`
	// Members is the hiring team, shown to people who may see the access list.
	Members []hiringtypes.Member `json:"members,omitempty"`
}

type FormView struct {
	JobID                     string                   `json:"job_id"`
	JobRevision               int                      `json:"job_revision"`
	Questions                 []questions.FormQuestion `json:"questions"`
	Declarations              []questions.Declaration  `json:"declarations"`
	AutomatedScreeningEnabled bool                     `json:"automated_screening_enabled"`
	Warnings                  []jobs.Warning           `json:"warnings"`
}

type JobCounts struct {
	All      int            `json:"all"`
	ByStatus map[string]int `json:"by_status"`
}
```

### [draft/edit.go](../internal/hiring/draft/edit.go)

```go
type CreateRequest struct {
	jobs.Patch
	// TemplateID starts the draft from a saved job-details template instead of the caller's default.
	TemplateID *string `json:"template_id"`
	// FormTemplateID starts the application form from a saved form template.
	FormTemplateID *string `json:"form_template_id"`
}

type formPayload struct {
	Questions    []questions.FormQuestion `json:"questions"`
	Declarations []questions.Declaration  `json:"declarations"`
}

type FormInput struct {
	Questions    []questions.FormQuestion `json:"questions"`
	Declarations []questions.Declaration  `json:"declarations"`
}
```

### [draft/extras.go](../internal/hiring/draft/extras.go)

```go
type TemplateInput struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	FromJob     string `json:"from_job_id"`
	AsMyDefault bool   `json:"as_my_default"`
}

type TemplateExport struct {
	Version int             `json:"version"`
	Kind    string          `json:"kind"`
	Name    string          `json:"name"`
	Payload json.RawMessage `json:"payload"`
}

type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type Catalog struct {
	LocationModes     []Option                  `json:"location_modes"`
	WorkplaceTypes    []Option                  `json:"workplace_types"`
	EmploymentTypes   []Option                  `json:"employment_types"`
	TravelFrequencies []Option                  `json:"travel_frequencies"`
	VisaPolicies      []Option                  `json:"visa_policies"`
	PayTypes          []Option                  `json:"pay_types"`
	PayPeriods        []Option                  `json:"pay_periods"`
	LawfulBases       []Option                  `json:"lawful_bases"`
	Currencies        []string                  `json:"currencies"`
	RetentionMonths   []int                     `json:"retention_months"`
	DescriptionKeys   []string                  `json:"description_sections"`
	MaxTitleLength    int                       `json:"max_title_length"`
	SeniorityLevels   []hiringtypes.CatalogItem `json:"seniority_levels"`
	ExperienceRanges  []hiringtypes.CatalogItem `json:"experience_ranges"`
	Industries        []hiringtypes.CatalogItem `json:"industries"`
	Markets           []jobs.Market             `json:"markets"`
}
```

### [draft/team.go](../internal/hiring/draft/team.go)

```go
type MemberInput struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

type RulesInput struct {
	Rules []screening.Rule `json:"rules"`
	// DPIAScopeConfirmed is the job owner's confirmation that this job's screening is covered by the company
	// DPIA. Nil leaves it as it is; saving different rules clears it, so it has to be confirmed again.
	DPIAScopeConfirmed *bool `json:"dpia_scope_confirmed"`
}

type RulesView struct {
	Rules                     []screening.Rule `json:"rules"`
	DPIAScopeConfirmed        bool             `json:"dpia_scope_confirmed"`
	AutomatedScreeningEnabled bool             `json:"automated_screening_enabled"`
}

type BulkResult struct {
	Action  string              `json:"action"`
	Done    []BulkDone          `json:"done"`
	Skipped []lifecycle.Skipped `json:"skipped"`
}

type BulkDone struct {
	ID     string `json:"id"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type MeView struct {
	UserID      string `json:"user_id"`
	CompanyName string `json:"company_name"`
	// CompanyAddress is the company's registered address, for "Same as company address". Empty when none is set.
	CompanyAddress string   `json:"company_address"`
	Roles          []string `json:"roles"`
	Permissions    []string `json:"permissions"`
}
```

### [draft/company.go](../internal/hiring/draft/company.go)

```go
type PrivacyContactInput struct {
	Email string `json:"privacy_contact_email"`
	DPO   string `json:"dpo_contact"`
}

type DPIAInput struct {
	Reference string `json:"reference"` // where the full assessment lives
	Summary   string `json:"summary"`   // what automated screening the DPIA covers
}
```

### [draft/publish.go](../internal/hiring/draft/publish.go)

```go
type TransitionResult struct {
	Job      *JobView      `json:"job"`
	Warnings []gates.Issue `json:"warnings"`
}
```

### [questions/builder.go](../internal/hiring/questions/builder.go)

```go
type FormQuestion struct {
	ID              string          `json:"id,omitempty"`
	Section         string          `json:"section"`
	Position        int             `json:"position"`
	Type            string          `json:"type"`
	Label           string          `json:"label"`
	Description     string          `json:"description,omitempty"`
	HelperText      string          `json:"helper_text,omitempty"`
	Required        bool            `json:"required"`
	Config          json.RawMessage `json:"config"`
	SystemKey       string          `json:"system_key,omitempty"`
	Locked          bool            `json:"locked"` // set by the server: full name and email cannot be removed or made optional
	Knockout        json.RawMessage `json:"knockout,omitempty"`
	SpecialCategory string          `json:"special_category,omitempty"`
}

type Declaration struct {
	Category       string `json:"category"`
	LegalCondition string `json:"legal_condition"`
	Purpose        string `json:"purpose"`
}

type knockoutChoice struct {
	OptionIDs []string `json:"option_ids"`
	Match     string   `json:"match"`
	Reason    string   `json:"reason"`
}

type knockoutNumber struct {
	Operator string   `json:"operator"`
	Value    *float64 `json:"value"`
	Reason   string   `json:"reason"`
}
```

### [questions/registry.go](../internal/hiring/questions/registry.go)

```go
type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type textConfig struct {
	MaxLength int `json:"max_length"`
}

type numberConfig struct {
	Min  *float64 `json:"min"`
	Max  *float64 `json:"max"`
	Unit string   `json:"unit"`
}

type phoneConfig struct {
	DefaultCountry string `json:"default_country"`
}

type linkConfig struct {
	AllowedHosts []string `json:"allowed_hosts"`
}

type choiceConfig struct {
	Options     []Option `json:"options"`
	MinSelected int      `json:"min_selected"`
	MaxSelected int      `json:"max_selected"`
	Searchable  bool     `json:"searchable"`
}

type scaleConfig struct {
	Min      int    `json:"min"`
	Max      int    `json:"max"`
	MinLabel string `json:"min_label"`
	MaxLabel string `json:"max_label"`
}

type uploadConfig struct {
	MaxMB  int      `json:"max_mb"`
	Accept []string `json:"accept"`
}

type dateConfig struct {
	MinDate string `json:"min_date"`
	MaxDate string `json:"max_date"`
}
```

### [screening/screening.go](../internal/hiring/screening/screening.go)

```go
type Rule struct {
	ID         string          `json:"id,omitempty"`
	QuestionID string          `json:"question_id"`
	Operator   string          `json:"operator"`
	Value      json.RawMessage `json:"value"`
	Reason     string          `json:"reason"`
}

type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type option struct {
	ID string `json:"id"`
}

type Flag struct {
	RuleID     string `json:"rule_id,omitempty"`
	QuestionID string `json:"question_id"`
	Reason     string `json:"reason"`
}
```

### [gates/gates.go](../internal/hiring/gates/gates.go)

```go
type Gate struct {
	ID          string          `json:"id"`
	MarketCode  string          `json:"market_code,omitempty"`
	Platform    bool            `json:"platform,omitempty"`
	Severity    string          `json:"severity"`
	Requirement string          `json:"requirement"`
	LegalBasis  string          `json:"legal_basis,omitempty"`
	Trigger     json.RawMessage `json:"-"`
	Version     int             `json:"version"`
}

type trigger struct {
	Type          string   `json:"type"`
	Value         string   `json:"value"`
	Values        []string `json:"values"`
	SubdivisionIn []string `json:"subdivision_in"`
}

type State struct {
	Gate
	// Auto is true when the server answers the gate from the job's own data and nobody confirms it.
	Auto      bool   `json:"auto"`
	Confirmed bool   `json:"confirmed"`
	Stale     bool   `json:"stale,omitempty"` // confirmed against an older version of the wording
	Detail    string `json:"detail,omitempty"`
}
```

### [gates/publish.go](../internal/hiring/gates/publish.go)

```go
type Issue struct {
	Code     string `json:"code"`
	Path     string `json:"path,omitempty"`
	GateID   string `json:"gate_id,omitempty"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type Report struct {
	Blocking []Issue `json:"blocking"`
	Warnings []Issue `json:"warnings"`
	Gates    []State `json:"gates"`
}
```

### [hiringtypes/types.go](../internal/hiring/hiringtypes/types.go)

```go
type Member struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
	Name   string `json:"name,omitempty"`  // output only
	Email  string `json:"email,omitempty"` // output only
}

type Person struct {
	UserID string   `json:"user_id"`
	Name   string   `json:"name"`
	Email  string   `json:"email"`
	Roles  []string `json:"roles"`
}

type ListItem struct {
	ID             string   `json:"id"`
	JobCode        string   `json:"job_code"`
	Status         string   `json:"status"`
	Title          string   `json:"title"`
	DepartmentName string   `json:"department_name,omitempty"`
	EmploymentType string   `json:"employment_type,omitempty"`
	LocationMode   string   `json:"location_mode,omitempty"`
	Markets        []string `json:"markets"`
	CurrentStep    string   `json:"current_step"`
	Revision       int      `json:"revision"`
	CreatedBy      string   `json:"created_by"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`

	WorkplaceType string       `json:"workplace_type,omitempty"`
	PublishedAt   string       `json:"published_at,omitempty"`
	ClosingDate   string       `json:"closing_date,omitempty"`
	Managers      []ListPerson `json:"managers"`
	// ApplicantCount is how many people have applied. NewApplicantCount is how many of them nobody on the hiring
	// team has opened yet.
	ApplicantCount    int `json:"applicant_count"`
	NewApplicantCount int `json:"new_applicant_count"`
}

type ListPerson struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
}

type Department struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Template struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Payload   json.RawMessage `json:"payload"`
	IsDefault bool            `json:"is_default"`
	CreatedBy string          `json:"created_by,omitempty"`
	UpdatedAt string          `json:"updated_at"`
	// Pinned templates sort first for the whole company.
	Pinned bool `json:"pinned"`
	// UseCount is how many drafts were started from this template by id.
	UseCount   int    `json:"use_count"`
	LastUsedAt string `json:"last_used_at,omitempty"`
}

type Settings struct {
	AutomatedScreeningEnabled bool   `json:"automated_screening_enabled"`
	EnabledAt                 string `json:"automated_screening_enabled_at,omitempty"`
}

type CatalogItem struct {
	ID   string `json:"id"`
	Slug string `json:"slug,omitempty"`
	Name string `json:"name"`
}

type CompanySetup struct {
	KYBStatus         string `json:"kyb_status"`
	DPAVersion        string `json:"dpa_version"` // the version that must be accepted
	DPAAccepted       bool   `json:"dpa_accepted"`
	DPAAcceptedAt     string `json:"dpa_accepted_at,omitempty"`
	PrivacyContact    string `json:"privacy_contact_email"`
	DPOContact        string `json:"dpo_contact"`
	DPIAVersion       int    `json:"dpia_version"` // 0 = not recorded
	DPIARecordedAt    string `json:"dpia_recorded_at,omitempty"`
	AutomatedScreenOn bool   `json:"automated_screening_enabled"`
}
```

### [applications/applications.go](../internal/hiring/applications/applications.go)

```go
type Summary struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Email       string    `json:"email"`
	Country     string    `json:"country"`
	Market      string    `json:"market"`
	Status      string    `json:"status"`
	Source      string    `json:"source"`
	FlagCount   int       `json:"flag_count"`
	Opened      bool      `json:"opened"`
	HasFiles    bool      `json:"has_files"`
	SubmittedAt time.Time `json:"submitted_at"`
}

type Page struct {
	Items      []Summary      `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
	Counts     map[string]int `json:"counts"`
}

type Answer struct {
	QuestionID string          `json:"question_id"`
	Section    string          `json:"section"`
	Type       string          `json:"type"`
	Label      string          `json:"label"`
	Value      json.RawMessage `json:"value,omitempty"`
	// FileID is set for upload questions; the file is fetched through the file endpoint.
	FileID string `json:"file_id,omitempty"`
}

type File struct {
	ID          string `json:"id"`
	QuestionID  string `json:"question_id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

type Review struct {
	Status    string          `json:"status"` // open or done
	Reasons   json.RawMessage `json:"reasons"`
	Decision  string          `json:"decision,omitempty"`
	DecidedAt *time.Time      `json:"decided_at,omitempty"`
	Note      string          `json:"note,omitempty"`
	// OpenedByMe is true when the caller opened the application since it was flagged, so they may decide it.
	OpenedByMe bool `json:"opened_by_me"`
}

type Detail struct {
	Summary
	JobID          string          `json:"job_id"`
	FormVersion    int             `json:"form_version"`
	Flags          json.RawMessage `json:"flags"`
	Answers        []Answer        `json:"answers"`
	Files          []File          `json:"files"`
	Review         *Review         `json:"review,omitempty"`
	StatusNote     string          `json:"status_note,omitempty"`
	DecidedAt      *time.Time      `json:"decided_at,omitempty"`
	ConsentGivenAt *time.Time      `json:"consent_given_at,omitempty"`
	NoticeVersion  string          `json:"privacy_notice_version"`
	RetentionUntil time.Time       `json:"retention_until"`
	// CanDecide tells the client whether to offer status changes and review decisions.
	CanDecide bool `json:"can_decide"`
}

type ApplicantSettings struct {
	// ReapplyBlockMonths is 0 (off) or 6: a rejected candidate cannot apply to the company again for that long.
	ReapplyBlockMonths int `json:"reapply_block_months"`
	// SendStatusEmails sends the candidate an email when they are rejected or hired.
	SendStatusEmails bool `json:"send_status_emails"`
	// ReapplyBlockAvailable is false when the server has no key for it, so it cannot be turned on.
	ReapplyBlockAvailable bool `json:"reapply_block_available"`
}

type FileLink struct {
	URL       string    `json:"url"`
	Filename  string    `json:"filename"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Result struct {
	Status    string     `json:"status"`
	DecidedAt *time.Time `json:"decided_at,omitempty"`
}
```

### [public/sharekit.go](../internal/hiring/public/sharekit.go)

```go
type ShareLink struct {
	Channel string `json:"channel"`
	Label   string `json:"label"`
	URL     string `json:"url"`
}

type ShareKit struct {
	JobURL string      `json:"job_url"`
	Links  []ShareLink `json:"links"`
}
```

## 12. Integration checklist

1. Read organization state and caller permissions before showing publishing/settings actions.
2. Populate selectors from server catalogs and company-owned entities.
3. Save incomplete work with PATCH; use details continuation and publish-check at their intended boundaries.
4. Keep the latest job revision for writes; handle 409 with reload/reconciliation.
5. Preserve whole collections for replacement endpoints, including locked questions and non-viewer team members.
6. Render candidates' forms from the published public schema and use actual question/option IDs.
7. Complete file upload before submitting answers, and retain one idempotency key for a single logical submission.
8. Treat generic application success as receipt only; internal candidate outcomes remain private.
9. Open flagged applications explicitly before reviewing, and display all decision conflicts.
10. Distinguish job-list pagination metadata from application-list pagination data.
11. Account for async email/board updates, expired file links, and background retention.

Primary entry points: [route mounting](../internal/app/app.go), [candidate service wiring](../internal/app/candidate.go), [response envelope](../pkg/response), [RBAC](../internal/rbac), and [hiring migrations](../db/migrations).
