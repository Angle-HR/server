# End-to-end test plan (human-style, browser driven)

Scope: only what a person can do and see in the UI. Backend internals (events, outbox, migrations, gRPC, tooling) are
out of scope here; failures of backend services are tested only through what the UI shows. The OpenHR web app (client, :3000), the admin app (admin, :3001), the public job and apply pages, and the
services behind the BFFs (identity, org, hiring, applications, notifications). Employers are UK and EU only; applicants
can apply from anywhere. Everything runs locally on the all-remote stack (`docs/microservices/local-dev.md`, "Every
service remote") with `GRPC_AUTH=mtls` and `ADMIN_TOTP_REQUIRED=true`.

Driver: the Orca browser (`orca tab create`, `snapshot --interactive`, `fill`, `click`, `upload`, `network`, `console`),
one tab per persona so sessions never mix. DB and Redis are used only to set up state a human cannot reach (marking a
KYB verified, clearing an admin lock) and are always noted in the evidence. Verification codes are read from mailhog.

## 0. How to run

**Personas** (fresh email each run, `+tag@example.com`, mailhog at :8025):

| Id | Who | Notes |
|---|---|---|
| P-IND | Individual employer, UK | owner of a personal organization |
| P-BIZ-UK | Business founder, GB company | Companies House style number |
| P-BIZ-DE | Business founder, DE company | HRB number format |
| P-BIZ-FR | Business founder, FR company | SIREN style number, no automated registry |
| P-MEMBER | Invited teammate (line manager, then legal) | tests role matrix |
| P-ADMIN | Bootstrap superadmin with TOTP | |
| P-STAFF | Invited admin staff member | |
| P-CAND-* | Candidates: UK, DE, KE, US, no-JS, mobile | |

**Order:** 1 Public shell, 2 Auth, 3 Onboarding, 4 Verification, 5 Settings, 6 Jobs, 7 Public job and apply, 8
Applicants, 9 Admin, 10 Emails, 11 Cross-cutting, 12 Resilience as seen in the UI, 14 Other screens. Each module lists cases as `ID – action →
expected`. Type tags: **H** happy path, **N** negative, **E** edge, **S** security, **C** concurrency, **R** resilience.

**Evidence per case:** pass/fail, screenshot for visual or failure cases, the failing request id from the response
meta (`request_id`), and the service log line. Defects go in a table at the end of the run: id, module, severity,
steps, expected, actual, request id.

**Exit criteria:** every H case passes; no open S or data-loss defect; every N/E case has a defined, user-readable
outcome (no blank screens, no raw errors, no stuck spinners); every defect is triaged.

## 1. Public shell and routing

1.1 Entry and redirects
- 1.1.1 H – open `/` signed out → redirected to `/dashboard` → then `/login`. Signed in → dashboard.
- 1.1.2 H – `/terms`, `/privacy-policy`, `/privacy` (redirects to policy) render, and contain no waitlist wording.
- 1.1.3 N – unknown path `/nope`, `/j/doesnotexist`, `/company/none` → friendly 404, no stack trace.
- 1.1.4 S – response headers: `X-Frame-Options: DENY`, `nosniff`, referrer policy; `/verify`, `/invite`,
  `/reset-password` send `Referrer-Policy: no-referrer`.
- 1.1.5 E – trailing slashes, uppercase paths, `?src=` and tracking params on `/j/{id}`.

1.2 Responsive and accessibility smoke (repeat on every module's main screen)
- 1.2.1 E – 375, 768, 1280, 1920 px widths: no horizontal scroll, buttons reachable.
- 1.2.2 E – keyboard only: tab order, visible focus, Enter submits, Esc closes dialogs and dropdowns.
- 1.2.3 E – zoom 200%, reduced motion, OS dark and light mode.
- 1.2.4 E – screen reader names: every input has a label, errors announced, no unlabeled buttons.

## 2. Authentication and sessions

2.1 Signup
- 2.1.1 H – email + strong password → verification screen.
- 2.1.2 N – invalid emails (`a@`, spaces, uppercase, 255+ chars, unicode, `+` aliases, trailing dot).
- 2.1.3 N – weak passwords: short, common, only digits, equals email; show/hide toggle works; paste allowed.
- 2.1.4 E – signup with an already registered email: no account enumeration (same response shape), existing user
  gets no code spam.
- 2.1.5 E – signup, close tab, come back: can resume verification or sign in; signup again with same email.
- 2.1.6 S – rate limits on signup per IP and per email (`RATE_LIMIT_SIGNUP*`): friendly message, `Retry-After`.
- 2.1.7 S – XSS and HTML in the email field; SQL metacharacters in password; both stored and handled safely.

2.2 Email verification
- 2.2.1 H – correct 6-digit code → onboarding, session cookie set (`openhr_session`, HttpOnly, SameSite, no JS access).
- 2.2.2 N – wrong code shows attempts remaining; 5th wrong → 429 `too_many_attempts` with `Retry-After`; correct code
  during lockout is still refused; resend resets the count.
- 2.2.3 E – paste the whole code into the first box; backspace across boxes; non-digits; autofill from the email.
- 2.2.4 E – expired code (wait out TTL); old code after a resend; code from another account.
- 2.2.5 E – resend cooldown timer; resend spam limit; "Edit email" returns to signup without a duplicate account.
- 2.2.6 E – double click Continue (one verification, no double error); browser back after success.

2.3 Login
- 2.3.1 H – email → password step → dashboard. Remember where you were: deep link `/dashboard/jobs` redirects back after login.
- 2.3.2 H – "Email sign-in code" path: code arrives, works once, expires.
- 2.3.3 N – wrong password: generic error, same timing as unknown email; 5 failures → throttle message.
- 2.3.4 N – unverified account tries to log in → sent to verify, not into the app.
- 2.3.5 E – email case differences, leading/trailing spaces, password manager autofill.
- 2.3.6 E – already signed in, open `/login` and `/signup` → redirected to dashboard.
- 2.3.7 S – login rate limits per IP and per email; lockout message does not reveal whether the account exists.

2.4 Session, logout, CSRF
- 2.4.1 H – sign out clears the cookie; `GET /auth/me` → 401; back button shows no cached private data.
- 2.4.2 E – two tabs: sign out in tab A, act in tab B → B is sent to login without losing a typed draft silently.
- 2.4.3 E – idle timeout and absolute lifetime (shorten via env): mid-form expiry → clear message, work not lost where autosave exists.
- 2.4.4 E – session rotation after onboarding steps: still signed in, CSRF token refreshed, old cookie value rejected.
- 2.4.5 S – unsafe request without `X-CSRF-Token` → rejected; wrong `Origin` → 403 `origin not allowed`; cookie flags
  (`Secure` only with `COOKIE_SECURE=true`, `__Host-` prefix in secure mode).
- 2.4.6 S – session fixation: pre-login cookie is not valid after login; cookie from another user rejected.

2.5 Password reset
- 2.5.1 H – forgot password → email link → set new password → old sessions revoked → login works.
- 2.5.2 N – reused, expired, tampered and already-used reset tokens; weak new password.
- 2.5.3 E – request reset for unknown email (no enumeration); several requests in a row (rate limit, only newest valid).
- 2.5.4 S – token not logged, not in Referer, not in URL history after submit.

2.6 Account TOTP (settings page `/dashboard/settings/account`)
- 2.6.1 H – enrol with authenticator code, confirm, sign out, sign in with code.
- 2.6.2 N – wrong code, reused code in the same window, code from the previous window (skew), disable without code.
- 2.6.3 E – lost authenticator: recovery path defined; re-enrol after reset.

2.7 Invitations to an organization
- 2.7.1 H – owner invites by email → invitee opens link, signs up or signs in → joins with the invited role.
- 2.7.2 N – expired, reused, revoked invite; invite for another email; invitee already a member.
- 2.7.3 E – invitee has an account already; invitee signs in with a different email than invited.

## 3. Onboarding

3.1 Account type choice
- 3.1.1 H – Individual and Business radios switch the form; state kept when toggling back and forth.
- 3.1.2 E – direct URL to `/onboarding/compliance` before earlier steps → sent to the correct step.
- 3.1.3 E – signed in user with completed onboarding opens `/onboarding` → dashboard.

3.2 Individual
- 3.2.1 H – first and last name, country UK → compliance (industry) → dashboard, "Verification in progress".
- 3.2.2 N – empty names, 1-char names, 200-char names, emoji, RTL scripts, leading spaces, only punctuation.
- 3.2.3 N – country list contains only UK and the EU option(s); other regions are absent (and a hand-crafted
  `country_id` for Kenya/US is rejected by the API).
- 3.2.4 E – browser back and forward through the steps; refresh on each step keeps entered data or restarts cleanly.
- 3.2.5 E – complete twice (double click Finish, second tab): one organization (idempotent), one queue case.
- 3.2.6 C – region migration under `REGION_MOVE=saga`: stop identity mid-step (see 12.3), retry, ends consistent.

3.3 Business
- 3.3.1 H – legal name, country, full name, role → workspace (registration number, address) → compliance → dashboard.
- 3.3.2 N – country-specific identification: GB number formats (8 digits, SC/NI prefixes), DE `HRB 123456`, FR number;
  invalid shapes rejected with a field message.
- 3.3.3 E – address: autocomplete search, "Enter address manually", missing postcode, long lines, non-Latin characters.
- 3.3.4 E – role options (founder, engineer, HR, ...) and what each is allowed to do afterwards.
- 3.3.5 E – business size, industry list loaded from org (`ORG_INDUSTRIES`), employee count 0, negative, huge, decimals.
- 3.3.6 E – switch from Business to Individual mid-way: leftover data not leaked into the other branch.
- 3.3.7 N – country outside UK/EU through the API → error with the supported-countries message.

## 4. Verification (KYB and individual review)

4.1 Business KYB submit (`/dashboard/verification`)
- 4.1.1 H – DE number `HRB 123456` → "being reviewed" (manual) state; email "review queued" arrives.
- 4.1.2 H – GB with a valid Companies House number (or its stub) → automatic result per tier.
- 4.1.3 N – failure reasons render and offer the right action: `number_not_found`, `name_mismatch`,
  `address_mismatch`, `inactive_entity`, `wrong_country`.
- 4.1.4 E – country list is UK + 27 EU states, alphabetical, no US/NG/KE; saving a country then reload keeps it.
- 4.1.5 E – retry after failure (attempt counter, caps), confirm name, confirm address, change country (identifiers cleared).
- 4.1.6 E – submit twice quickly; submit with only whitespace name; very long legal name; unicode legal names.
- 4.1.7 E – nudge emails (day-1, day-2 style) and "failed" email wording; links open the right screen.

4.2 Individual review
- 4.2.1 H – pending state visible, can still draft jobs, cannot publish until verified.
- 4.2.2 N – admin rejects → rejected state with reason, "Request a new review" path, cap on re-reviews.
- 4.2.3 E – owner changes name after submission.

4.3 Admin decision (see 9.3 for the admin UI) and its effect on the employer
- 4.3.1 H – approve → employer's banner disappears without relogin (or after refresh); publish unlocks; email "verified".
- 4.3.2 N – reject with reason → employer sees it; resubmit; approve later.
- 4.3.3 C – two admins decide the same case: one wins, the other gets a clear conflict.
- 4.3.4 E – manual DB verification (`kyb_status='verified'`) behaves the same as approval in the UI.

## 5. Company settings and team

5.1 Agreements and contacts (`/dashboard/settings/company`)
- 5.1.1 H – accept DPA (version shown), privacy contact saved, appears on the public privacy notice.
- 5.1.2 N – accept without ticking; invalid privacy email; very long DPO name; clearing the contact.
- 5.1.3 S – only founder or legal can accept; line manager sees read-only or a clear refusal.
- 5.1.4 E – DPA version bump → publish blocked again until re-accepted.

5.2 DPIA and automated screening
- 5.2.1 H – legal role records a DPIA, then enables automated screening.
- 5.2.2 N – founder without legal role cannot enable; enabling without a DPIA is refused; individuals cannot enable.

5.4 Applicant settings
- 5.4.1 H – applicant-facing settings the company can change (where the UI exposes them) save and show on the public job and apply pages.
- 5.4.2 N – invalid values and roles without permission are refused with a clear message.

5.3 Members and roles
- 5.3.1 H – invite teammate, assign roles, change a role, remove a member.
- 5.3.2 S – permission matrix per role (founder, legal, line manager, recruiter, member): create job, edit others' job,
  publish, see audit, see pay, manage members, accept DPA. Direct URL and API calls must match the UI.
- 5.3.3 E – last owner cannot remove or demote themselves; removed member's open sessions lose access.
- 5.3.4 E – invite many emails at once, duplicates, invalid addresses in the list.

## 6. Jobs (employer side)

6.1 Jobs list (`/dashboard/jobs`)
- 6.1.1 H – empty state, "Create a new job", "Create with AI", templates; tabs All, Drafts, Archived, Templates.
- 6.1.2 E – filter, sort, grid/list view, pagination with 50+ jobs, search with special characters.
- 6.1.3 E – applicant counts, "new" counts; when applications is down they show unavailable, not 0.

6.2 Job details step
- 6.2.1 H – title, team (create new), location mode (Anywhere, Specific area, Specific timezone), workplace type,
  visa, travel, description (rich text: bold, lists), industry, employment type, seniority, experience, skills.
- 6.2.2 N – title empty and 70+ chars; description empty and enormous; HTML/script in title, description, skills.
- 6.2.3 N – pay: min greater than max, negative, zero, decimals, huge numbers, exact versus range, hourly/monthly/yearly;
  currency list is GBP and EUR only; "show on career page (required in EU)" behaviour for EU markets.
- 6.2.3b E – money display: pay is stored in minor units; the same figure (for example 50,000 per year) reads identically in the form, the jobs list, the preview, the public job page and the apply page, in GBP and EUR, hourly and yearly; no 100x or 0.01x errors.
- 6.2.4 E – closing date: past date, today, far future, invalid date, timezone edge at midnight.
- 6.2.5 E – lawful basis options and the legitimate-interests assessment reference required field.
- 6.2.6 E – job ID field uniqueness; templates ("use templates", "save as template").
- 6.2.7 E – autosave and "Save as draft" leaves the flow; reopen the draft and every field is restored.
- 6.2.8 C – edit the same draft in two tabs: second save gets the revision-conflict message and can reload; no
  silent overwrite.
- 6.2.9 E – departments: duplicate team names, rename, delete a team in use.

6.3 Application form step
- 6.3.1 H – default questions (name, email, phone, location, CV); add each question type, mark required, reorder, delete.
- 6.3.2 E – locked questions cannot be removed; duplicate labels; empty label; 100 questions.
- 6.3.3 E – knockout questions need screening switch and DPIA; knockout reason shown; never auto-rejects.
- 6.3.4 E – special category question needs a declaration and legal condition; warnings render.
- 6.3.5 E – file question limits (types, max MB) saved and enforced on the apply page.

6.4 Permissions step
- 6.4.1 H – "Anyone" versus "Restricted", add members with levels, creator always has access.
- 6.4.2 S – a member without access cannot open the job by URL.

6.5 Publish step
- 6.5.1 H – compliant job publishes on the first click; share dialog shows frontend link, channel links and copy.
- 6.5.2 N – gates block with readable reasons: DPA not accepted, privacy contact missing, org not verified, market
  gates, pay transparency, applicant notice; "Fix this" links go to the right place and back.
- 6.5.3 N – publish a job whose cached revision is stale (edit in another tab first) → handled, not a dead end.
- 6.5.4 N – publish with org service down → 503 with retry message, drafting still works (see 12.1).
- 6.5.5 E – double click Publish; browser back after publish; reload on the success state.
- 6.5.6 E – option toggles (indexing, auto-publish) persist.
- 6.5.7 E – share link opens the frontend job page; `?src=whatsapp|x|facebook|linkedin|telegram|email` is carried to apply.

6.6 After publish
- 6.6.1 H – edit a published job (which fields lock), republish, change closing date, close, reopen, archive, delete a draft.
- 6.6.2 E – closing date passes → job stops accepting, public page shows closed state.
- 6.6.3 E – company with 0 verified status after publish (revoked) → behaviour defined.
- 6.6.4 E – job audit trail (`GET /jobs/audit`) has no screen yet: API only, out of scope for this UI plan.
- 3.2.6 note – the saga case needs the stack started with `REGION_MOVE=saga`.

## 7. Public job and application

7.1 Job page (`/j/{id}`) on the frontend and the server page through the BFF
- 7.1.1 H – all fields render; pay shown only when visible; privacy notice link works (API origin).
- 7.1.2 N – unknown id, archived, closed, draft id, deleted company → "not available", HTTP semantics sane.
- 7.1.3 E – very long description, many skills, missing optional fields, RTL text.
- 7.1.4 E – SEO: indexing flag respected, canonical URL, sitemap `/public/jobs/sitemap.xml` lists only open jobs.
- 7.1.5 S – no employer-only data (internal notes, members, other jobs) in the public JSON.

7.2 Apply form (`/j/{id}/apply`)
- 7.2.1 H – fill all fields, attach a PDF, pick a country, tick privacy → thank-you page; employer count +1.
- 7.2.2 N – each required field empty, invalid email, phone with letters, missing country, privacy unchecked.
- 7.2.3 N – CV: wrong type (exe renamed pdf), 0 bytes, over the max size, encrypted PDF, corrupt PDF, two files, EICAR
  (rejected, nothing stored), filename with path or unicode, very long filename.
- 7.2.4 E – remove and re-attach the file; upload then change the country; slow upload; refresh mid-upload.
- 7.2.5 E – country: any ISO country accepted for Anywhere jobs; markets-limited jobs reject others with a message.
- 7.2.6 E – consent shown only when the job needs it; special-category question flow; knockout answers flag only.
- 7.2.7 E – double submit and network retry (idempotency key): exactly one application.
- 7.2.8 E – reapply with the same email: blocked within the window, message clear, different email allowed.
- 7.2.9 S – public apply rate limit; HTML/script in answers (escaped when the employer views them); oversized body.
- 7.2.10 E – `?src=` recorded on the application; missing `src` is "direct".
- 7.2.11 E – mobile keyboard types (email, tel), autofill, file chooser on a phone viewport.

7.3 Candidate emails
- 7.3.1 H – "application received" mail with the employer name and the OpenHR footer.
- 7.3.2 E – later "rejected" and "hired" mails, links, unsubscribe or data-request wording.

## 8. Applicants (employer side)

8.1 List and detail
- 8.1.1 H – list shows new applicants, count matches the jobs list; open one, answers and CV download (presigned link
  works, expires, bytes match).
- 8.1.2 E – filter and sort by status, source, country, date; pagination; search by name and email.
- 8.1.3 E – special category answers visible only to roles allowed; knockout flag shown with its reason.

8.2 Status workflow
- 8.2.1 H – move a candidate through the stages to hired and rejected; emails go out; timeline updated.
- 8.2.2 N – illegal transitions, change after hired, two members changing the same candidate (conflict message).
- 8.2.3 E – bulk actions if present; undo; notes and tags if present.

8.3 Data lifecycle
- 8.3.1 E – retention period (3, 6, 12 months) shown; expiry job and retention notice email.
- 8.3.2 E – candidate data request or deletion path (privacy contact) and company deletion cascade (applications and
  blobs gone, tombstone written, jobs archived), then reactivation.

## 9. Admin app (:3001)

9.1 Sign-in and TOTP
- 9.1.1 H – password → TOTP step → overview; first login enrols TOTP (QR, secret, confirm).
- 9.1.2 N – wrong password; wrong codes ×5 → 15 minute lock for that admin; correct code during the lock is refused.
- 9.1.3 E – pre-auth cookie expiry (5 min), code replay in the same window, double submit of the Verify button.
- 9.1.4 E – superadmin resets another admin's TOTP; that admin re-enrols; sessions revoked.
- 9.1.5 S – product session cookie does not authenticate admin routes and vice versa; admin CSRF and origin checks.

9.2 Overview, users, jobs, catalogs
- 9.2.1 H – overview counts match the database; users list, detail (onboarding progress, owned company), paging.
- 9.2.2 E – user detail with org service down: those fields omitted, page still loads.
- 9.2.3 H – jobs list and detail (including the never-exercised detail screen); catalogs CRUD (departments, industries,
  seniority, experience, markets open or closed).
- 9.2.4 N – catalog validation, duplicate names, deleting an entry in use.

9.3 Verification queue
- 9.3.1 H – business and individual cases listed with region, country and time; open a case; approve; reject with reason.
- 9.3.2 E – paging, filters, empty queue, case already decided elsewhere (stale page).
- 9.3.3 S – reviewer without the verification permission cannot see or decide.

9.4 Staff, roles, audit
- 9.4.1 H – invite staff by email → accept invite (set password, TOTP) → role applies; deactivate → sessions revoked.
- 9.4.2 N – expired or reused invite token; password policy failures; inviting an existing email.
- 9.4.3 S – role/permission matrix across every admin route; superadmin-only actions (company delete, reactivate, TOTP reset).
- 9.4.4 H – audit log filters (actor, resource id, time range), paging, IPs shown as stored; entries exist for every
  action above.
- 9.4.5 H – delete a company (superadmin, region param) then reactivate; employer's data and public pages follow.

## 10. Emails (mailhog) and notifications

- 10.1 H – every template renders with OpenHR branding, correct sender name, working links to the right origin:
  verification, login code, password reset, org invite, admin invite, onboarding complete, KYB review queued, KYB
  failed, KYB nudges, retention notice, application received, rejected, hired.
- 10.2 E – HTML and plain-text parts, long names and unicode, links with tokens expire correctly, no duplicate sends
  on retry, no email sent for deduplicated events.
- 10.3 R – notifications service down: user-facing actions still succeed, emails delivered after recovery (events).

## 11. Cross-cutting

11.1 Security and tenancy
- 11.1.1 S – IDOR: employer A requests B's job, applicant, upload and download ids over the API → 404 or 403 every time.
- 11.1.2 S – stored XSS through every free-text field (job, team, company, application answers, filenames) in every
  place it is rendered, including emails and the admin app.
- 11.1.3 S – injection strings in search and filters; open-redirect check on `next` or return parameters.
- 11.1.4 S – CORS and origin: allowed origins only; preflight for each custom header.
- 11.1.5 S – secrets absent from URLs, logs and the client bundle; error bodies leak no stack or SQL.
- 11.1.6 S – rate-limit classes behave per endpoint and recover after the window.

11.2 Browser behaviour
- 11.2.1 E – back, forward, refresh and duplicate-tab on every multi-step flow; offline then online; slow 3G throttle.
- 11.2.2 E – date and time formats, timezone near midnight, daylight-saving jobs closing date.
- 11.2.3 E – unicode everywhere (accents, CJK, RTL, emoji) in names, titles, answers, emails.
- 11.2.4 E – cookies blocked or cleared mid-session; localStorage blocked.

11.3 Regions (as visible to users)
- 11.3.1 E – applicant from another country applies to a UK job and to an EU job: both accepted and visible to the employer.

## 12. Resilience and consistency (stop and start containers while a human flow is mid-way)

- 12.1 R – org down: publish → 503 `Retry-After: 5`, drafts still save; org back → publish works.
- 12.2 R – hiring down during save and publish: clear error, no half-saved job, retry succeeds.
- 12.3 R – identity down mid region move (saga on): request stalls and compensates, retry succeeds, one org, one event.
- 12.4 R – applications down: job list shows counts unavailable; public apply returns a retryable error, no data lost.
- 12.5 R – messaging down and up (stop `nats`): publish a job, then see the public page and the apply form appear after recovery; applicant count catches up; no duplicate applicants or emails.
- 12.6 R – redis down: sessions fail closed with a retryable error, not a logged-in bypass.
- 12.7 R – recreate a service container while using the app: the UI recovers within seconds without a manual reload of the session.
- 12.8 R – postgres restart; clamav down (uploads fail closed with a clear message); minio unavailable.
- 12.9 E – after each scenario the UI agrees with itself: jobs list, applicant counts, public page and admin overview show the same numbers.

## 13. Reporting

- Run the six API scripts once before the UI pass (`make local-e2e-all`, fresh stack, raised rate limits) so UI failures are not setup failures.
- Report per module: cases run, passed, failed, blocked, defects by severity, and a go or no-go line.

## 14. Other screens a user can reach

14.1 Job creation helpers
- 14.1.1 H – "Create with AI": prompt produces a draft job; fields filled sensibly; nothing is published.
- 14.1.2 N – empty prompt, very long prompt, prompt with HTML, offensive or off-topic text; service failure shows a retryable message.
- 14.1.3 E – description "Generate" button: output replaces or appends (as designed), undo works, repeated clicks do not stack requests.
- 14.1.4 H – "Preview" shows the job as a candidate would see it; matches the public page after publish.
- 14.1.5 H – "Use templates" fills the form; "Save as template" appears under the Templates tab; edit and delete a template.
- 14.1.6 E – template with a team that no longer exists; template used by two people.

14.2 Candidate-facing extras
- 14.2.1 H – company careers page `/company/{slug}` lists only open jobs of that company, links to each job page.
- 14.2.2 N – unknown slug, company with no open jobs, deleted or unverified company.
- 14.2.3 H – "Autofill with resume" on the apply form fills known fields from a PDF; the candidate can correct them.
- 14.2.4 N – autofill with a wrong file type, an empty PDF, a very large PDF, a scanned image PDF; failure leaves the form usable.

14.3 Navigation and account screens
- 14.3.1 H – sidebar collapse and expand, active item, user menu, sign out, "Send us feedback" opens and submits.
- 14.3.2 E – every sidebar link and every "Fix this" link lands on the right page and the back button returns correctly.
- 14.3.3 H – account settings page (profile and TOTP) and company settings page reachable from the menu; unsaved-change prompts.
- 14.3.4 E – deep links to every dashboard URL while signed out and signed in with the wrong role.

14.4 Invitations (join flow screens)
- 14.4.1 H – `/invite` and `/invite/join` screens for a new user and an existing user; success lands on the dashboard of the inviting company.
- 14.4.2 N – invalid or expired link screens show a clear message with a next step.

14.5 Removed features
- 14.5.1 N – old waitlist URLs (signup page, confirmation links) return a normal 404 page; no waitlist wording on any screen, email or the admin app.
- 14.5.2 N – the admin app has no waitlist menu item and its old URL shows a 404 page.

14.6 Brand and copy
- 14.6.1 E – product name reads "OpenHR" on every screen, tab title and email; the admin app still says "Angle" (known issue to track).
- 14.6.2 E – legal screens (terms, privacy policy, DPA text, applicant notice) are readable and complete; wording is flagged for legal review, not judged here.
