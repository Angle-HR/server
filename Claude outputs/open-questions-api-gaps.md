# Hiring API gaps: open questions

Compiled 9 Oct 2026 from the API gap report ("not-done") and the server changes made on 8 Oct. Questions are split into **product** (the team decides what the product should do) and **technical** (the engineers decide how). Each question says what it blocks and carries a suggested default, so a quick "yes, go with the default" is a valid answer.

**Already built on 8 Oct, not yet compiled:** list row data (workplace type, published date, closing date, managers), list filters and sort, JSON and selected-ids export, `GET /hiring/me`, `GET /hiring/people`, and the lifecycle paths in the OpenAPI file. The questions below cover what is left, plus a few choices made in that code that someone should confirm.

How to answer: write under **Answer:**. Leave it blank if undecided.

---

## Part 1. Product questions

### A. Jobs list

**P1. The list has an applicant count column, but candidates cannot apply yet. What should the column show until they can?**

Blocks: the four table columns and the card that read applicant data.

Suggested default: hide the column until the applications module ships, rather than show a misleading 0.

Answer:

**P2. Which of the new list filters does the UI expose at launch, and what is the default sort?**

The API now filters by creator, assignee, employment type, workplace type, location mode, market and created date, and sorts by updated or created date, newest first by default. Sorting by title or applicant count is not supported.

Blocks: nothing in the server. It decides which controls the frontend builds.

Suggested default: expose assignee, employment type, workplace type and market; keep "recently updated" as the default sort.

Answer:

**P3. Who should appear in the "assign people" picker?**

`GET /hiring/people` lists every member of the company with their roles. The Roles PRD says only existing members can be added and that adding a collaborator with limited access opens that job's candidates only.

Question: should the picker hide some roles (for example Payroll or Employee), or show everyone?

Suggested default: show everyone, and let the server refuse roles that cannot be added.

Answer:

### B. Status changes and bulk actions

**P4. Should bulk reopen and bulk resume exist?**

The lifecycle code says resume and reopen are one job at a time because each re-runs the publish checks and writes its own compliance log. The frontend toolbar currently offers bulk reopen.

Blocks: the bulk toolbar. If yes, we also need to decide what the user sees when some jobs pass the checks and others do not.

Suggested default: keep them one at a time, and remove bulk reopen from the toolbar.

Answer:

**P5. Which other bulk actions are needed at launch: change closing date, assign people, duplicate?**

Changing the closing date or the team of a published job re-runs the publish gates (rule BE-12), so those two carry the same concern as P4. Bulk duplicate is simple because a copy starts with no team and no screening rules.

Suggested default: ship bulk duplicate only.

Answer:

**P6. Can any job be deleted, or only drafts?**

Today only drafts can be deleted. The UI's delete dialog shows the applicant count and a US notice for any job. Deleting a job that has applicants touches retention and the GDPR notice. **Counsel**

Blocks: the delete dialog on published, paused and closed jobs.

Suggested default: drafts only. Everything else is archived, and deletion of jobs with applicants goes through the retention schedule.

Answer:

**P7. What does "expired" mean?**

The database allows an `expired` status and the frontend filters on it, but nothing sets it.

Questions: does a job expire automatically when its closing date passes? Can it be reopened? Is the owner told?

Suggested default: yes to auto-expire on the closing date, reopen allowed (it re-runs the publish checks), owner notified by email.

Answer:

**P8. What can a limited role do on the toolbar?**

`GET /hiring/me` now tells the client each person's roles and permissions. HR 2 can change status only on jobs it created and cannot export. Line manager, Payroll and Employee see jobs only if assigned or published internally.

Question: please confirm the toolbar action list per role, or point us to the Roles PRD table so we can derive it.

Answer:

### C. Job form

**P9. Structured description sections or one rich-text box?**

The API stores `description_sections` (structured). The frontend has one rich-text editor. Switching changes the editor and the public job page.

Question: which wins, and if sections, which are required?

Suggested default: keep sections (the public page and AI generation both work better with them) and change the editor.

Answer:

**P10. Keep the Google Places location chips and the free-form timezone offset?**

The API takes `markets` (market code, city, subdivision, timezone) plus a location text. A Places result does not map onto that cleanly.

Question: is it acceptable to ask for market, city and timezone in separate fields, with Places only filling the city?

Answer:

**P11. What does "copy link" copy?**

Jobs get a `public_id` when published. Questions: is there a link for a draft (a preview link), and what does the link show once the job is paused, closed or expired?

Answer:

### D. Templates and export

**P12. Which template features ship at launch?**

Today: list, save and delete. Wanted: rename, pin, duplicate, export, edit, "times used" and "last used".

Questions: is pinning personal or company-wide? What counts as a "use" (creating a draft from the template)? Does "allow others" on save mean other members can use it, and can they edit it?

Suggested default: rename and personal pin now; the rest later.

Answer:

**P13. What should the export contain?**

CSV currently has job code, title, status, department, employment type, markets, created and updated. JSON returns the full list row.

Question: should CSV also carry workplace type, managers, published date and closing date? Who asked for JSON?

Answer:

### E. AI description and MCP

**P14. What should AI description do, and what may it see?**

Nothing exists in the API. Questions:

1. Generate from the job details only, or also from the company profile?
2. Edit existing text (shorter, more formal), or generate only?
3. Is it on for every company, or opt-in?
4. What is MCP connecting here, and who is allowed to connect it?

Job text and company data leave the system, so a lawful basis and a processor decision are needed first. **Counsel**

Answer:

---

## Part 2. Technical questions

### A. Verification of the 8 Oct changes

**T1. Can you run the build and tests and send me what fails?**

The sandbox could not download Go 1.25 or the modules, so the new code was only syntax-checked. Please run `go build ./...`, `go test ./internal/hiring/...` and `make swagger`, then paste any errors.

The riskiest parts are the list SQL (16 parameters, sort column filled in with `Sprintf`), the two new store queries, and the fake store used by the tests.

Answer:

**T2. Do the list and people queries need new indexes?**

The list now filters on creator, employment type, workplace type, location mode and market, and checks the hiring team for the assignee filter. Existing indexes cover tenant and status only.

Suggested default: measure first on realistic data (a few thousand jobs per company), then add indexes in migration 000012 if the list is slow.

Answer:

### B. Choices made in the new code

**T3. Is `GET /hiring/me` the right place for permissions, or should they go on `/auth/me`?**

I added `/hiring/me` because `/auth/me` is in the auth handler and does not know the company, and loading roles there adds a query to every login check. The cost is a second call from the client.

Suggested default: keep `/hiring/me`.

Answer:

**T4. Should `GET /hiring/people` return email addresses to everyone who can open it?**

HR 2 holds only the limited collaborator permission and can open the picker. The existing members endpoint already shows emails.

Suggested default: leave as is, since the picker needs something to tell two people with the same name apart.

Answer:

**T5. Is a sort limited to updated and created time acceptable?**

The cursor is a time plus an id. Sorting by title or applicant count needs a different cursor for each, which means a cursor format change.

Suggested default: yes, and add other sorts only when the product asks for them (see P2).

Answer:

### C. Remaining server work

**T6. How should applicant counts be stored once applications exist?**

Options: count with a subquery on each list call, or keep a counter on the job row. A subquery is simple and always right; a counter is faster but can drift.

Suggested default: subquery with an index on the job id, revisit if the list slows down.

Answer:

**T7. Where does the job that sets `expired` run, and how often?**

There is a `cmd/kyb-sweep` command and a worker runtime. Should the expiry sweep follow the same pattern, run daily, and write the same audit event as a manual close? Or should the status be derived when the job is read?

Suggested default: a daily sweep like kyb-sweep, so the stored status matches what filters and exports show.

Answer:

**T8. What is the source of truth for the description?**

The jobs table has `description_html`, `description_text` and `description_sections`. Which one is written when the frontend sends a rich-text body, and which does the public page read?

Answer:

**T9. Where does the public job URL come from?**

The config holds `APP_URL` and `ADMIN_APP_URL` only. For copy link we need a careers site base address, and possibly one per company if custom domains are planned.

Question: what should the setting be called, and are custom domains in scope?

Answer:

**T10. Does the server look up Google Places, or does the client send parsed fields?**

If the server does it, we need a provider key, a call budget and a mapping to market, subdivision and timezone. If the client does it, the server only validates what it receives.

Suggested default: client sends parsed fields, server validates against the markets catalogue.

Answer:

**T11. What schema changes do the template features need?**

Pin, times used and last used need columns on `hiring.templates`, and a rule for where a use is recorded (create from template in `POST /jobs`). Rename needs no schema change.

Suggested default: one migration (000012) with `pinned_by`, `times_used` and `last_used_at`, written inside the create-job transaction.

Answer:

**T12. For AI description, what are the runtime requirements?**

Questions: synchronous call or streamed? Per-company rate limit? Audit log of prompts? And since each company's data lives in a regional database, which model hosting region is allowed for UK, EU and other regions?

Answer:
