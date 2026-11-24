# Individual accounts can hire

Product decision (owner): an individual account hires exactly like a company. The model is deliberately small:
finishing individual onboarding creates one implicit **personal organization** (`kind = individual`) owned by the
person. Everything that works on an organization (hiring, applications, agreements, privacy contact, publish
readiness, gRPC membership, company deletion, events) works unchanged through the existing org paths. There is no
second code path for "individual hiring".

Status: implemented on branch `split/individual-hiring` and proven live by `scripts/e2e-individual.sh` (signup, onboarding,
review, job, publish, application, deletion) on the all-remote stack with mTLS between services.

## The personal organization

| Field | Value |
|---|---|
| `accounts.organizations.kind` | `individual` (new column, `business` for every existing row) |
| `legal_name` | the person's name: `first_name last_name` from the profile step |
| region / `country_id` | the user's own (set at the profile step) |
| `owner_user_id` | the user. Owner, so membership resolves with the `founder` role and the full founder permission set, as for a business owner (no member row is written; neither does a business owner have one) |
| `company_role_id` | the catalog entry `others` (`60000000-0000-4000-8000-000000000008`); the column is NOT NULL |
| `industry_id` | from the compliance step |
| `business_type_id`, `employee_count` | not asked of individuals; stored only if an older client still sends them |
| `kyb_status` | starts `pending` (see verification below) |

One organization per owner (existing unique index), so an individual has exactly one. It is created idempotently by
`POST /onboarding/complete` (and by `cmd/org-backfill-individuals` for accounts that finished before this change).

## What the client changes

Nothing is renamed. Business accounts are untouched.

### Onboarding steps for an individual

Required steps stay `verify_email` -> `profile` -> `compliance` -> `complete`. There is no identification/address
step for individuals (unchanged).

1. `PUT /onboarding/profile` with `account_type: "individual"`, `first_name`, `last_name`, `country_id`. Unchanged.
   Choosing individual no longer deletes an organization that already is a personal organization.
2. `PUT /onboarding/compliance`, **changed for individuals**: the business questions are gone.

   | Field | Individuals | Business |
   |---|---|---|
   | `industry_id` (uuid) | required: the field you mostly hire in | required |
   | `business_type_id` | **not asked**; ignored if sent | required |
   | `employee_count` | **not asked**; ignored if sent | required |

   Request: `{"industry_id": "62000000-..."}`. Response data echoes `industry_id` and the usual `onboarding`
   summary (`business_type_id` is omitted, `employee_count` is 0 when not given). A business account sending only
   `industry_id` still gets 400.
3. `POST /onboarding/complete` creates the personal organization and, in the same call, submits it for review (below).
   Response `201` data gains **`organization`** for individuals (absent for business accounts, which have none yet):

   ```json
   {"status": "completed", "workspace": {"id": "...", "slug": "..."}, "redirect_url": "/dashboard",
    "organization": {"id": "<uuid>", "kind": "individual", "verification_status": "pending"}}
   ```

   Retrying after a failed attempt is safe (it only creates what is missing); calling it again after it succeeded answers
   `409`, as for every account.

Wrong order or an unfinished step still answers `400 onboarding_incomplete` as today.

### Verification (the "KYB" of an individual)

Individuals never see company registry questions. `POST /organization/verification` (company registration form) is
not available to them.

* Path: **manual identity review** through the same admin queue as company verification. The queue item carries
  `case_type: "individual"`; the reviewer approves or rejects exactly like a company (`POST
  /admin/verification/{organizationID}/review`). No registry lookup, no registration number.
* `GET /organization/verification` works for individuals. Same shape as today plus **`kind`** (`"individual"` or
  `"business"`) and, for individuals, `status` in `pending` (waiting for review, the state right after completing
  onboarding), `verified`, `failed` (with `failure_reason`, the reviewer's code). `tier` is `2`. `can_publish` is
  `true` only when `verified`. `can_retry`, `light_retry`, `name_confirmable`, `address_confirmable` are always
  `false` for individuals.
* **Request a new review** after a rejection: `POST /organization/verification` with an empty JSON body `{}`. For an
  individual this re-queues the review (200, `status: "pending"`). While `pending` it answers `409` "manual review
  is in progress"; when `verified`, `409` "organization is already verified". `/retry`, `/confirm-name`,
  `/confirm-address`, `/change-country` answer `409` ("action not available for the current verification status")
  for individuals.
* Publishing needs `verified` (same gate as companies: `company_not_verified` until then). Drafting needs nothing.

### Publish readiness (unchanged rules, individual specifics)

`GET /organization/agreements` / `POST /organization/agreements` / `PUT /organization/privacy-contact` and
work as for a company, against the personal organization:

* publish still requires **verified**, **accepted terms/DPA** (`POST /organization/agreements`) and a **privacy
  contact** (`PUT /organization/privacy-contact`). The individual is the data controller. A reasonable client default
  is to prefill the contact with the account email (the server does not do it: it is a legal statement).
* Automated screening needs a company DPIA recorded by Legal; an individual has none, so screening rules stay
  blocked (`dpia_missing`). Nothing individual-specific is added.

### Other endpoints (no contract change)

| Endpoint | For an individual after completing onboarding |
|---|---|
| `GET /hiring/me` | `200` (was `404 no organization for this account`). `company_name` is the person's name, roles `["founder"]`, the full founder permission list. There is no `kind` field here: use `account_type` from `GET /auth/me` / onboarding status |
| `/hiring/jobs...`, publish, careers pages | work; the public employer name is the person's name (see legal questions) |
| `/applications...` (hiring side) | work; the person sees their job's applications |
| `GET /organization/agreements` etc. | work |
| `POST /organizations/invites` | works (invite a colleague into the personal organization); a personal organization is not a team product, nothing stops it |

Before onboarding completes an individual still has no organization and these endpoints answer 404 as before.

## Admin side

* `GET /admin/verification` items gain **`case_type`** (`"business"` | `"individual"`); individual items are to be
  labelled in the console. For an individual `country_code` holds the person's country as the catalog slug (for example
  `united-kingdom`), not an ISO code: the regional database stores the country id only.
* `GET /admin/verification/{organizationID}` gains `case_type` and, for individuals, `legal_name` is the person's
  name, `registration_number` is empty and `tier` is 2. The reviewer decides on identity evidence that is outside
  this system today (see legal questions).
* `DELETE /admin/companies/{id}` deletes a personal organization like any company (`org.company.deleted.v1`, jobs
  archived, applications data removed). The user account is untouched and does not get a new personal organization
  (the unique owner index keeps the deleted row).

## Events

All `org.*` payloads gain `company_kind` (`"individual"` | `"business"`; `kind` is already taken by the agreement kind), next to the existing ids. Still no names, no email
addresses: a personal organization's `legal_name` is a person's name and stays out of events. Consumers that ignore
unknown fields need no change. `hiring.job.*` and `applications.*` events are unchanged (they carry the company id).

## Backfill

`org-backfill-individuals -dry-run` (`go run ./cmd/org-backfill-individuals`; the binary is also in the `jobs` image) lists, per region, individual accounts that finished onboarding and
have no organization and are not members of a company (user ids are printed, never names); without `-dry-run` it creates the personal organization and the review case for each. Idempotent
(safe to rerun; accounts that already have an organization, including deleted ones, are skipped). Needs the same
database environment as `cmd/org` (`DB_URL_GLOBAL`, `DB_URL_<REGION>`), and the queue switch (`IDENTITY_QUEUE`,
`IDENTITY_GRPC_ADDR`) when the queue is not in the global database.

## Open legal questions for the owner (no legal text invented here)

1. **Controller role.** The individual is treated as the controller of applicant data, the same way a company is. Is
   the existing terms/DPA version (`CurrentDPAVersion`) valid for a natural person acting as an employer, or does it
   need a variant (consumer vs business capacity, household exemption does not apply to hiring)?
2. **Identity verification.** What evidence must a reviewer see for an individual before approving, and where is it
   collected? Today the case holds only the name and country; collecting an ID document needs its own storage and
   retention policy that does not exist.
3. **Public employer name.** Careers pages and job events show the organization name; for an individual that is
   their personal name. Is that acceptable, or must an individual choose a display name (not built)?
4. **Region/country rules.** Are there markets where individuals may not employ through the platform?
5. **Rejection.** Failure reasons are the company ones (`name_mismatch`, ...). A rejected individual can request a
   new review without limit; should that be capped?
