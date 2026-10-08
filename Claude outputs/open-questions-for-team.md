# Hiring module: open questions for the team

Compiled 5 Oct 2026 from the Job Creation Server Plan, the Sprint v2 docs (Product & Legal, Backend v2), KYB V2, the GDPR doc, the Roles and permissions doc and the T&C. Each question says who can answer it, what it blocks, and where it comes from. Questions marked **Counsel** need a lawyer's answer.

**Answers added 7 Oct 2026** (from the team's "Response to Back-end questions"). Each question below now has its answer underneath. **Proposed** means it is the team's call but not yet confirmed. **Counsel** means a lawyer still has to confirm. Card IDs (PL-, BE-, DS-, FE-) point to the 8-week Hiring Sprint v2 docs. Role names are the seven in the Roles PRD: Founder, HR 1, HR 2, Line manager, Payroll, Employee, Legal.

Numbering now follows the answers document (Q1 to Q48). Six questions (33 to 35, 36, 46, 47) came from the answers and were not in the 5 Oct list; they are marked **New**.

**One rule for every published job:** any edit that touches markets, salary, screening rules, workflows or special-category fields re-runs the publish gates (BE-12). Resuming a paused job re-runs them too. Without this, someone could publish a plain job and add knockouts or a workflow afterwards, skipping the DPIA and checklist.

## 1. Roles, invites and ownership (Jerry, Product, Legal)

Source: PL-02, Roles and permissions TODO. Blocks: the invite, role-change and ownership-transfer endpoints, and the Founder-as-Legal setup at signup.

**1. How does a member get invited? Which roles can send an invite, and which roles can the inviter hand out?**

Answer: Founder and HR 1 can invite.

- Who can assign what: Founder can assign HR 1, HR 2, Line manager, Payroll, Employee and Legal. HR 1 can assign HR 1, HR 2, Line manager and Employee.
- Payroll is Founder-only. It carries `job.budget.approve`, which HR 1 doesn't have. If HR 1 could assign Payroll, it could invite a second email of its own and approve its own budgets.
- Nobody can change their own roles.
- An invite link only works for the invited email address: the invitee signs up or signs in with that address, so a forwarded link doesn't let someone else in.
- Invites expire after 7 days and can be resent (V2 Onboarding §11.1). Founder or HR 1 can cancel a pending invite. Pending invites are cancelled automatically when the inviter is removed.
- The invite email links the privacy notice (§11.2). Invitees don't go through identity checks (KYB V2).
- New permission rows: add `member.invite`, `member.role.assign`, `member.remove` and `account.ownership.transfer` so BE-04 loads them and BE-T1 tests them.

**2. How are roles changed or removed? Does removing someone end their sessions immediately (V2 Onboarding §11.3 says yes)?**

Answer (Proposed):

- Each company has exactly one Founder, the account owner. Founder can't be given by invite or role change; it moves only through the transfer in Q3. Otherwise a Founder could invite a second Founder (no identity check) who then removes the first, skipping Q3's verification. Co-founders get HR 1, plus Legal or Payroll if they need them.
- You can remove a user only if you could assign every role they hold. So HR 1 can't remove someone who also holds Legal or Payroll.
- Removing a user, or taking away one of their roles, ends their sessions and revokes their API and MCP tokens immediately. The change is logged (V2 Onboarding §11.3, BE-07).
- Removing the only Legal user triggers the Founder-as-Legal prompt (Q4).

**3. When ownership of an account transfers, does the old owner lose all roles at once, does the new owner have to accept, and is the new owner re-verified?**

Answer:

- The Founder starts the transfer. The new owner must accept it and pass identity verification (KYB V2) before anything changes. If they fail verification, the transfer is cancelled.
- When the transfer starts, the Founder chooses whether the old owner stays on as HR 1 or is removed at completion.
- Same company only: if the business itself changes hands, the new company completes KYB and accepts the DPA as a new customer. Otherwise a buyer would publish under the old company's verified name.
- The DPA acceptance belongs to the company, so it stays valid through a transfer within the same company.
- If the old owner held Legal through the Q4 fallback, Legal doesn't transfer. The new Founder is asked to take it on or assign it.
- Founder unreachable (left, lost access): ownership is recovered through support, using KYB evidence such as the registry's list of officers. It isn't done in-app.
- The T&C and DPA need a short section on invitations, role changes and transfers (Roles PRD TODO).

**4. Does the Founder hold the Legal role by explicit assignment at signup when the company has no legal or data-protection person? Is it shown in the UI, and how does it get handed over later?**

Answer: Yes, by explicit assignment at signup, never inherited silently.

- The T&C explain the fallback, but they can't make the assignment. A clause every customer accepts would be exactly the silent inheritance the Roles PRD warns against. The assignment is a signup checkbox: "I'm also responsible for legal and data protection."
- The team page shows it as a badge. Hand it over through a normal role change, and when someone else gets Legal, prompt the Founder to drop it.
- When the Founder holds Legal, opening a candidate's health detail asks for a reason and is logged, because the same person also makes hiring decisions. EDI stays aggregate-only for everyone.
- With no Legal user, the special-category toggles stay locked. Jobs with screening can't be published either, because they need a DPIA (Q27).
- **Needs design** (fits DS-04, W2). Owen to talk with the team.

**5. Can HR roles see health data? The GDPR doc says yes for adjustments, and the Roles doc says never. Counsel**

Answer (Counsel): No. HR sees only the operational need (for example "step-free access required"), never the condition.

- Founder, HR 1, HR 2, Line manager and Legal hold `form.adjustments.view_operational`. Only Legal holds `form.adjustments.view_health`.
- Two separate fields in two stores, not one field behind a permission check (BE-09, DS-05).
- Free text can still carry health detail. The apply form asks "What do you need?" with a hint that candidates don't have to name a condition. An optional detail field goes to the restricted store. Note guidance tells interviewers not to write health detail in notes.
- The Roles doc is right, so update the GDPR doc's access matrix to match.

**6. What is HR 2's `job.collaborator.add_restricted`? It is "-" in the matrix today.**

Answer: It lets someone add a collaborator with view and comment access only.

- HR 2 gets it for the jobs it manages, so the cell becomes "✓ (own jobs)". BE-04's scopes already support this.
- Rename it `job.collaborator.add_limited`, because "restricted" means special-category data everywhere else in the docs.
- Only existing members of the company can be added.
- It opens that job's candidates only. It shows on the job's access list (DS-04), so candidates' notices stay accurate.
- It ends when the job is closed or archived, or when someone removes it.
- It never opens restricted or EDI data, which stays with Legal.

**7. Who sets a job's jurisdiction, retention period and residency region, given that routing is automatic?**

Answer:

- Jurisdiction is set automatically from the job's location(s) (BE-03). Founder, HR 1 and Legal can add markets (`job.jurisdiction.set`). Nobody can remove a market that comes from the job's location: a Berlin job always carries the EU, so its salary rule can't be dodged by relabelling it UK. Only Legal can remove one, with a logged reason. Any change re-runs the gates.
- Retention defaults come from the PL-04 table for each market. Only Legal can override it on a job (`job.retention.set`), and only within that market's minimum and maximum. In small companies that is the Founder holding Legal (Q4).
- Residency region is not a per-job setting. It follows the residency key (Q21) and is fixed when the company is set up. Make `job.residency.region.set` ✗ for every role, as `job.delete.hard` is. Moving a company to another region is a data migration done by our team, not a button.

**8. Do the Job Screening PRD's Super Admin and Co-Reviewer map onto the seven roles, or do we add them?**

Answer: Map them onto the seven roles. Don't add new ones.

| Source term | Maps to | Can do |
|---|---|---|
| Super Admin (Job Screening PRD) | Founder | Everything the Founder can. Per-user screen-level access is not built (BE-04: no permission builder). |
| Co-Reviewer | Job collaborator (Q6) | Review candidates on the jobs they're added to, nothing else |
| Tier 2 | HR 2 | Create jobs and edit their own drafts. Can't publish, edit a published job, archive or export. |
| Tier 3 / limited permission | Line manager | View assigned jobs, review their candidates, raise requisitions. Can't create, publish or export. |

- Acting on jobs you don't manage: only Founder and HR 1 (`job.view.all` + `job.edit.published`).
- Pausing and closing: the matrix has no row for this. Add `job.status.change` for Founder and HR 1, and for HR 2 on its own jobs.
- HR 2 can pause and close its own jobs, but not resume or reopen them. Resuming or reopening goes through publish, so it needs `job.publish.external` and the gates run again.
- Figma: rename the tiers to these role names.

**9. When a creator is deactivated, do their jobs go to their manager or to the account admin?**

Answer:

- The person deactivating the user picks the new owner. The default is the Founder, and only Founder, HR 1 or HR 2 can be picked. Don't hand them to "their manager": the data model has no reporting line, and Line managers can't edit jobs.
- Deactivation also disconnects their calendars and removes them from booking slots. Their upcoming interviews move to the new owner, so candidates can't book into a calendar nobody is watching.
- Notify the new owner.
- The job still shows the original creator, marked inactive (FE-02).

**10. Who may use `jobs.export` and `jobs.manage_any`? They are in the server plan but not in the roles matrix.**

Answer: Rename them `job.export` and `job.manage_any` to match the matrix, and add them as rows.

- Founder and HR 1 hold both.
- Every export is logged (BE-07) and leaves out restricted and EDI fields. A bulk export of candidates notifies the Founder.
- Legal doesn't need it: subject access requests go through BE-24, which exports one person's data across every store with a deadline clock.

## 2. Legal documents (Jerry, Legal, Founder)

Source: PL-03, T&C §3 and §9, GDPR doc §3. Blocks: the DPA gate, publish.

**11. Is there a DPA document and version ready? The T&C say customers must sign it before uploading personal data, while the 5 Oct decision folds the DPA into the T&C. Which is it, and does acceptance need a named signatory?**

Answer: The text isn't written yet. Jerry and counsel will have it ready by **23 Oct**, before the DPA gate is tested in W4 (PL-03).

- The DPA stays a separate, versioned document. It is incorporated into the T&C and accepted with the same click at signup (T&C §3 already says this). "Folded into the T&C" means the same click, not the same text. That keeps T&C §24's order of precedence working.
- No signature needed: click-wrap is valid (GDPR Art. 28(9) allows electronic form).
- Only the Founder or Legal can accept the DPA, including later versions. T&C §3 makes them confirm they can bind the company.
- We store the company, user ID, name, email, role, DPA version and timestamp (BE-07).
- New versions take effect after 30 days' notice (T&C §18). If it isn't accepted by then, the company can't publish new jobs, but live jobs keep running. The gate checks acceptance of the current version.
- The DPA must cover: candidates as data subjects (the current T&C §4 and §11 only mention employee data); the 24-hour breach notice (BE-29); the sub-processor objection process; how we forward requests that candidates send to us instead of to the employer.
- Domain: the T&C use openhr.io (openhr.io/legal/dpa, legal@openhr.io). Pick one domain and update the T&C, the privacy policy and the emails. Keep the URL in config, not in code.

**12. Who is the legal entity named in notices and the DPA?**

Answer: Angle Open Source Ltd, company number 17066367, registered office 71–75 Shelton Street, London WC2H 9JQ (T&C §1).

- The customer is the controller, so BE-13 fills the controller block from the company's own details. Angle Open Source Ltd appears only as the platform provider and processor.
- The notice also needs the customer's privacy contact email, and their DPO if they have one. Company setup collects only name, industry, team size and country, so add a privacy contact field and require it before publish.
- EU notices also name our EU representative (Q16).
- Still open: whether notices say "Open HR" or "Angle" (plan review Q24).

**13. When will the applicant privacy notice text be ready for each launch market?**

Answer: Ready by **23 Oct** (end of W3) for every market we switch on at launch (PL-03). Any market without notice text stays switched off, and BE-12 blocks publishing there anyway.

**14. Where is the public sub-processor list published?**

Answer: At /legal/subprocessors (on the domain from Q11), linked from the DPA and the privacy notice.

- It lists each sub-processor's purpose, the data it gets, its region and the transfer mechanism.
- Changes need 30 days' notice, with a right to object (Art. 28(2)).
- Live by the week of **26 Oct**, because BE-13 pre-fills notices from it.

## 3. Markets and platform prerequisites (Founder, Counsel)

Source: PL-01, PL-03, BE-01a, BE-02. Blocks: opening EU, NG, KE, IN. (Singapore and the UAE are out of scope, see Q20.)

**15. Which markets are on at launch? Who signs the decision record (PL-01)?**

Answer: We build all six: UK, EU, US, India, Nigeria and Kenya. Each has its own switch, and a market goes live only when its prerequisites are done. Keep the server as it is (UK and US on, the rest off) and turn markets on as they clear. The Founders sign PL-01.

| Market | Switches on when |
|---|---|
| UK | DPA and UK notice ready |
| US | DPA, CCPA notice at collection, US pay rules (Q23) |
| EU | EU representative appointed and named in the privacy policy (Q16), EU notice, SCCs with non-EEA sub-processors |
| Nigeria | Transfer basis (CBDTI), our NDPC decision (Q18), cookie opt-in on job pages, notice |
| Kenya | s.50 opinion (Q17), ODPC registration (Q18), notice |
| India | Counsel on legitimate use vs consent (Q30), notice |

A switch doesn't stop that country's residents applying. A Kenyan can apply to a remote UK job while Kenya is off.

- The apply form asks the candidate's country and accepts only the job's markets ("This role is open to candidates in: UK").
- It can't stop EU and Kenyan residents reaching us through sign-ups and remote jobs. **Counsel** should say whether we need VeraSafe and Kenya's ODPC registration from launch day, whatever the switches say (Q16, Q18).

**16. EU: Estonian or Irish entity, or a service such as VeraSafe, and by what date?**

Answer: We're appointing VeraSafe as our Art. 27 representative.

- Contract signed by **[date to add]**. Their details go in the privacy policy and EU notices.
- The EU stays switched off until it's signed.
- The `eu-rep` gate becomes required, not advisory (JCF V2 §7).
- **Counsel:** whether we need the representative from launch anyway (see the note under Q15).

**17. Nigeria and Kenya: when does the legal consultant report?**

Answer: Not in yet. Kenya's s.50 opinion is due by **23 Oct** (end of W3, BE-02), and Kenya stays off until it arrives. Nigeria's report is due **[date to add]**.

**18. Does Angle itself register with NDPC and ODPC, and who confirms each prerequisite, from which admin screen?**

Answer: With counsel now (PL-03).

- Kenya: plan on registering, because ODPC registration covers foreign processors of Kenyan data (BE-02).
- Nigeria: depends on the NDPC's registration thresholds, which counsel confirms. **Counsel**
- Proposed admin screen: market switches and their prerequisite records live in an internal Angle admin screen, not in customer settings. The Founder turns a market on after counsel signs off, and each change is logged with a link to the evidence.
- Customers see only their own duties (BE-14).

**19. India: what has to happen before it can open?**

Answer: India opens when counsel answers Q30, the India notice text is ready, and the region is set up (`ap-south-1` Mumbai, BE-01a).

- KYB for India is manual (GSTIN/CIN, KYB V2).
- The DPDP Rules were notified on 13 Nov 2025, and their core duties apply from 13 May 2027 (FE-09).

**20. Singapore and UAE: planned for launch?**

Answer: Neither is a launch market. Leave them out of the market picker and the server. There's no UAE market, so no UAE data and no Middle East region question.

**21. Residency: is the key the tenant, the job market or the candidate's location (BE-01a)? Which region serves Nigeria and Kenya?**

Answer (Proposed; the Tech lead and Legal sign it in BE-01a. This is a Gate 1 item.)

- **Key: the company.** The company's country at setup (V2 Onboarding §6.2) picks the region. All of that company's jobs, candidates, files and backups live there.
- The country and region lock once the company holds any personal data. V2 Onboarding says setup fields are editable from Settings; this one can't be, or data would sit in the wrong region. Check it against the KYB country of registration.
- Why not the candidate's location (JCF V2 §6): the employer reads every CV from wherever they sit. Storing a Nigerian candidate in an African region doesn't avoid a transfer when a London recruiter opens the CV. It only splits one job's applicants across regions and multiplies backups and purges.
- A US company hiring in the EU stores EU candidates in `us-east-1`. The DPA needs SCCs (or the EU–US Data Privacy Framework) for that. **Counsel**
- Exception: where a law requires an in-country copy. That's Kenya, if counsel says s.50 applies (BE-02).
- Regions: UK `eu-west-2`, EU `eu-west-1`, US `us-east-1`, India `ap-south-1`.
- Nigeria and Kenya: no hyperscaler has a region in either country. Proposed: store their data in `eu-west-2` with a documented transfer mechanism (Nigeria: CBDTI; Kenya: s.48–49 safeguards). Kenya changes if the s.50 opinion requires local hosting. **Counsel**
- Shared services: list what personal data each holds: sign-in, the audit log, email sending, error logs and the malware scanner (C14). Error logs must not capture request bodies.
- If we use R2: use jurisdiction-enforced buckets, not location hints (JCF V2 §6 gap).

> **Conflicts with the current Server Plan**, which stores applications in the candidate's region and has `markets.AE.storage_region = asia`. The plan needs updating to a company-keyed region.

## 4. Gates, retention and screening (Counsel, Legal, Product)

Source: PL-04, PL-05, PL-07, BE-12, BE-27. Blocks: the gate catalog, retention values, knockout behaviour.

**22. Where is the full gate catalog?**

Answer: It's JCF V2 §4. Add the severity, because the server needs it:

| Market | Gate | Severity | Fix before loading |
|---|---|---|---|
| UK | `uk-lawful` | Required | |
| UK | `uk-privacy` | Required | |
| UK | `uk-retention` | Required | Drop "max 6 months per ICO". The ICO sets no fixed number (BE-11), so the gate becomes "retention set and stated in the notice". |
| UK | `uk-scd` | Required if special-category flags are set | Criminal records need Art. 10, not Art. 9 (Q35) |
| EU | `eu-pay-trans` | Required | Checks the range is shown, not just stored (Q29) |
| EU | `eu-lawful` | Required | |
| EU | `eu-rep` | Market switch | Our obligation, so it moves to the switch (Q16) |
| EU | `eu-works` | Advisory | |
| EU | `eu-scc` | Market switch | Our obligation: a customer can't confirm our SCCs |
| NG | `ng-lawful`, `ng-cookie` | Required | |
| NG | `ng-cbdt` | Market switch | Our transfer instrument, not the customer's |
| NG | `ng-dcmi` | Advisory | The NDPC term is DCPMI (BE-14) |
| KE | `ke-lawful`, `ke-odpc` | Required | `ke-odpc` is the customer's own registration (BE-14) |
| KE | `ke-loc` | Market switch | Our hosting question, settled by the s.50 opinion |
| KE | `ke-retention` | Required | Value pending counsel (Q24) |
| IN | `in-dpdp` | Informational | Update the text: Rules notified 13 Nov 2025, core duties from 13 May 2027 |
| IN | `in-consent` | Required only if Q30 says consent | |
| IN | `in-fiduciary` | Advisory | |
| US | Server's US set | Per Q23 | JCF V2 has no US section |

- Customer checklist items are the customer's own confirmations, logged against their user. The product never says it makes them compliant (BE-14).
- Gates that are our obligations (`eu-rep`, `eu-scc`, `ng-cbdt`, `ke-loc`) are prerequisites for the market switch (Q15). They never appear as customer checkboxes.
- System gates for every market (BE-12): DPA accepted; a notice for the job's market; an EU salary range; a DPIA when screening is automated; a lawful basis for special-category data; KYB `verified`; the market switched on.
- Pay-history questions are blocked outright.
- Drop JCF V2 §4.6 (SG), §4.7 (PH) and §4.9 (AE).

**23. Can counsel confirm the gate wording and the proposed US gates? Counsel**

Answer (Counsel): Our proposal, for counsel to confirm by **16 Oct**:

- Pay transparency: required for jobs in states with pay-range laws (for example CA, CO, WA, NY, IL; PL-07 holds the full list). US jobs therefore need a state on the location, plus the city for NYC. Several of these laws also reach remote roles that could be done in that state, so a remote US job follows the strictest rule and needs a range.
- Salary history: already blocked for every market by BE-10, so it doesn't need a US gate.
- CCPA: the notice at collection is required for California applicants (BE-13).
- Fair chance: flag any criminal-history question on US jobs before an offer. California and NYC ban it at application.
- EEO statement: advisory, and required for federal contractors.
- NYC AEDT: a flag, not a gate. It applies only if an automated tool substantially assists the hiring decision. With AI scoring out and knockouts flagging for review (Q26), it shouldn't trigger at launch.

**24. Retention: what are the minimum, maximum and default per category and market? Is Kenya 12 months or employment plus six years? Counsel**

Answer (Counsel). Proposed values for BE-11, needed by Gate 2 (**16 Oct**):

| Category | UK, EU, NG, KE, IN | US |
|---|---|---|
| Unsuccessful applicants (CV, answers, interview notes) | Default 6 months, max 12 | Min and default 1 year (EEOC); 2-year min for federal contractors with 150+ staff; 4-year min in California |
| Talent pool | 12 months from joining, renewable when the candidate confirms (PL-08) | Same |
| Criminal-record checks | Keep the decision, not the certificate; UK 6 months max | Same |
| Right-to-work documents (hires) | UK: employment + 2 years | I-9: 3 years from hire or 1 year after leaving, whichever is later |
| EDI responses | Aggregate only; individual answers deleted with the application | Same |

- The clock starts, for unsuccessful applicants, at the rejection date. For anyone never reviewed, it starts when the job closes.
- Jobs with several markets: retention follows the candidate's own country, which the apply form now collects (Q15). A UK maximum of 12 months and a California minimum of 4 years can't both apply to one record.
- US figures are minimums. They aren't a 2-year maximum.
- Pricing's 12 months is the maximum, not the default.
- Kenya: "employment plus 6 years" only makes sense for hires. For rejected applicants we propose the same 6/12 as the other markets, until counsel says otherwise.

**25. Who can change a retention period, and can a job override the tenant default?**

Answer: Only Legal, for the company default and per job (`job.retention.set`). In small companies that's the Founder holding Legal (Q4).

- A change must stay within the market's minimum and maximum, needs a written justification (BE-11) and is logged.
- A shorter period can be applied to existing records. A longer period applies to new records only, because existing candidates were told the old period in their notice.
- Nothing is ever deleted before its minimum.

**26. Do failed knockouts flag a candidate for human review or reject automatically? Which markets, if any, allow automatic rejection? Counsel**

Answer (Counsel): At launch there is no automatic rejection in any market, including through workflows.

- A failed knockout flags the candidate for human review.
- A workflow Reject step creates a review task instead of rejecting (BE-21, BE-31).
- There's no bulk "reject all flagged" action. Each flagged candidate must be opened before they can be rejected, and the review is logged with who and when. Otherwise the review is a rubber stamp, and Art. 22 requires meaningful human involvement.
- The candidate's data is kept for that review under normal retention.
- Later, automatic rejection can be turned on per market once counsel confirms it (PL-05), and only with the safeguards: the candidate is told, can respond, can get human review and can contest the decision.

**27. Where is a DPIA recorded, who records it, and what counts as "automated screening"? Counsel to confirm.**

Answer:

- The customer's Legal user records the DPIA in-app from our template. It must be recorded before the first job with screening is published. Each job then confirms its rules are within the DPIA's scope.
- "Automated screening" means any rule or AI that rejects, scores, ranks, filters or hides candidates, even if a person makes the final call.
- So any job with knockouts, auto-disqualification rules, an assessment with a pass mark, or a workflow condition on answers sets the DPIA requirement (BE-27). That holds when they're added after publish too (see the rule at the top).
- Turning it on is Legal-only (`form.automated_screening.enable`).

**28. Is the discriminatory-wording check a fixed word list per market or an AI classifier? Who writes the lists?**

Answer: A fixed, versioned list of flagged questions and phrases per market, not an AI classifier. A list can be explained and tested, and AI for reviewing candidates is out.

- Legal and Product write it, in PL-07 (W2–W3).
- It checks the job title and description as well as the questions. "Young", "recent graduate" and "native speaker" sit in the description far more often than in a question.
- A flagged item can still be published with a logged override reason (BE-27). Only Founder, HR 1 or Legal can override, so HR 2 can't approve its own flagged question.
- The gender-neutral title check warns but never blocks (BE-06).

**29. EU pay transparency: is blocking publish without a range acceptable? What are the rules per member state and US state? Counsel**

Answer (Counsel): For launch, keep the block: an EU job can't publish without a salary range, and the range shows in the ad.

- The gate checks the range is set **and** `show_publicly` is on. A stored range hidden from the ad would pass a "range exists" check and still break the rule. The same applies in US pay-range states (Q23).
- Sanity warning when the minimum is 0 or the maximum is more than double the minimum.
- Why keep the block: the Directive also allows giving the range before the first interview, but some member states may require it in the ad. The block is simple and always compliant. We record this as a deliberate choice (BE-06).
- Later we can offer "send the range before the first interview", where the booking link (BE-18) sends the range automatically.

**30. India: legitimate use or consent? Does the 22-language notice option apply? Counsel**

Answer (Counsel, due **23 Oct**): BE-13 reads employment as a legitimate use under DPDP s.7, so no consent notice is needed. The open question is whether that covers applicants as well as employees.

- If counsel says legitimate use: no consent notice, and we drop FE-09.
- If counsel says consent: a consent notice, plus the option to read it in English or any of the 22 Eighth Schedule languages (s.5(3)), and we build FE-09.
- India stays off until the answer arrives.

**31. Does the reapply block have a lawful basis, and what is kept for it?**

Answer: Decided 30 Sep: the block is optional, off by default, and lasts 6 months when an employer turns it on.

- Lawful basis: the employer's legitimate interests, so not re-processing a recent applicant for the same role. The employer needs a short legitimate-interests assessment, and counsel checks the notice wording before launch (non-blocking).
- What we keep: a keyed hash of the email (HMAC with a per-company secret), the job ID, the rejection date and the block end date. No CV or answers.
- Scope: the same job, or a job created from it by duplicating or reopening. Not a fuzzy match on similar titles, which would let an employer turn the block into a company-wide blacklist.
- The block record is deleted when the block ends, separately from the application's own retention. An erasure request or an objection from the candidate deletes it too.
- The notice mentions the block when it's on. Blocked candidates see a neutral message (BE-28).

**32. How should the data classes be placed: right-to-work answers, vetting results, adjustment needs, EDI? (PL-06 asks for a signed table.)**

Answer (Proposed for PL-06, signed by Legal and the backend lead by **16 Oct**):

| Data | Store | Who sees it |
|---|---|---|
| Right-to-work yes/no answer | Standard | Hiring roles on the job |
| Right-to-work documents, visa or immigration detail | Highly restricted | Legal. HR sees "verified on [date]". |
| Vetting and background-check reports (criminal data, Art. 10) | Highly restricted | Legal. HR sees clear or not clear. |
| Adjustment need ("step-free access") | Standard | Roles with `form.adjustments.view_operational` |
| Health detail behind an adjustment, post-offer health answers | Restricted | Legal only |
| EDI responses | EDI schema | Aggregate only, never joined to a named candidate |

- India uses the same classes, at the strictest level (BE-08).
- US right-to-work wording: ask only "Are you authorised to work in the US?" and "Will you need sponsorship?". Asking about citizenship can be discriminatory, so PL-07 adds it to the flagged list.

**33. (New) How do "Anywhere" and time-zone jobs work for markets and gates?**

Answer:

- "Anywhere" selects every switched-on market when the job is published. The employer sees every market's must-dos, and the strictest salary rule applies (an EU range).
- Markets switched on later are not added to live jobs. The employer re-publishes to include them, which runs their gates.
- Time-zone jobs: the employer picks the countries.
- This also answers C7: a job can target several markets, so BE-03 must support that.

**34. (New) Should the sexual orientation question stay in self-ID? Counsel**

Answer (Counsel): Remove it at launch.

- It's special-category data in the UK and EU, US EEO forms don't ask it, and no launch customer needs it.
- If a customer asks later, it comes back inside the EDI module, which only Legal can turn on (`form.edi_module.enable`).
- Figma: take it off the screen.

> **Reverses the 5 Oct product decision** to keep sexual orientation in self-ID (see the Server Plan).

**35. (New) Should the criminal records wording change to Art. 10? Counsel**

Answer (Counsel to confirm the wording): Yes, change the copy.

- Criminal records need an Art. 10 basis. In the UK they also need a DPA 2018 Schedule 1 condition and an appropriate policy document.
- Health, ethnicity, religion and biometrics stay under Art. 9.
- JCF V2 Step 6: split "Criminal records or DBS" out of the Art. 9 list.

## 5. Product and engineering decisions

Source: server plan, AI Build Plan. Blocks: schema choices and some endpoints.

**36. (New) What is the status name for a live job, and what is the full set of states?**

Answer: `published`. The full set is draft, published, paused, closed and archived (BE-05). Figma will be updated to match.

**37. Description storage: sanitised HTML or the editor's JSON?**

Answer: Store the editor's JSON as the source.

- Public pages and job boards: when a job is published, render sanitised HTML with an allow-list sanitiser.
- Emails: plain text.
- Never store HTML from the user.

**38. Public job pages: one shared domain or per-org subdomains?**

Answer: One shared domain with an org path (jobs.<domain>/org-slug) at launch.

- Slugs are unique per company. An old slug redirects after a rename.
- Claiming a slug only after KYB is verified, checked against the verified company name, so nobody can post jobs on our domain under another company's name.
- Custom subdomains come later on paid plans.
- Job pages show the cookie banner from PL-08.

**39. Assessments: build our own or link to a third-party tool?**

Answer: At launch we link to a third-party assessment tool.

- We store the link and whether the candidate completed it. We don't import scores.
- The tool is the employer's own processor, not our sub-processor, as long as we don't send it candidate data. Use the same link for every candidate, with no names or emails in it.
- An assessment with a pass mark counts as automated screening (Q27).

**40. Where does the AI co-writer get company information, given LinkedIn has no open API for company pages?**

Answer: The AI co-writer sits outside the 8-week sprint unless PL-01 brings it in (AI Build Plan). When we build it:

- Ask for the company website URL and skip LinkedIn.
- Fetch the site on the server through the URL safety check (https only, no internal addresses; AI-BE-06).
- Summarise it into the company profile, which the user edits and saves.
- Every job reuses that profile (`get_company_profile`, AI-BE-04).
- Only company details go into prompts.

**41. Duplicate-job detection: what counts as a duplicate (JCHF §7)?**

Answer: This changes JCHF §7 from "block" to "warn". A job counts as a duplicate when:

- it's in the same company;
- it has the same normalised title and the same set of locations;
- the other job is a draft, published or paused, or was closed in the last 30 days.

Show a warning with a link to the other job, but only for jobs the user can see (BE-T5). Continuing anyway takes one click ("This is a different role") and is logged.

**42. Is a streaming method decided for AI chat?**

Answer: Server-sent events: chat only streams one way, and SSE is simpler than websockets.

- The ~25-second heartbeat (AI-BE-02) also keeps the connection alive.
- Reconnects resume from `Last-Event-ID`.
- This only matters once AI work starts.

## 6. Design

Source: server plan, Figma notes. Blocks: frontend work, not the server.

**43. When will the Permissions and Share screens be designed?**

Answer: Permissions and access list: DS-04 in W2 (**12–16 Oct**), which feeds FE-06 and FE-07 in W4. It needs Q1–Q10 settled first.

**44. When will the knockout switches, legal reason and retention fields, and the country checklist on Share be added?**

Answer: Knockout switches, legal reason and retention fields: DS-03 in W2 (**12–16 Oct**). The country checklist on Share and Publish: DS-12 in W3 (**19–23 Oct**). Until counsel answers Q23–Q26, knockouts are designed as flag-for-review.

**45. How does a Checkbox question differ from Multiple choice?**

Answer: A checkbox question allows several answers; multiple choice allows one.

- Make them look different in the preview, with square boxes for checkboxes and round radio buttons for multiple choice.
- Consider the names "Multiple answers" and "Single answer".
- Backend: they're separate field types, and knockout rules differ (any versus all of the selected answers).

**46. (New) What is the difference between "Save as template" and "Keep this setup for my future jobs"?**

Answer: They're different things.

- "Save as template" creates a named job template (job details and form) in the Templates tab. Anyone with `job.template.use` can reuse it. Creating one needs `job.template.create` (Founder, HR 1).
- "Keep this setup for my future jobs" saves the form setup as that user's default for new jobs. It doesn't appear in Templates and needs no template permission.

**47. (New) CV upload: which formats and size limits?**

Answer: PDF only at launch (BE-28, JCF V2). DOCX comes later, converted to PDF on upload so the CV viewer (FE-20) can show it.

- Size: 5 MB on the free plan and 10 MB on paid plans. On free, 100 applicants × 5 MB = 500 MB, exactly the free storage cap (Pricing §9), and Pricing modelled paid plans at 10 MB.
- The limit comes from the employer's plan, read at intake. The form shows the limit before upload and enforces it in the browser and on the server (FE-16).
- Malware scanning: every file is scanned in our own region, for example with ClamAV. Never send CVs to a public multi-scanner such as VirusTotal, which shares uploaded files.
- The PDF viewer shows PDFs in a sandboxed viewer with scripts off.

> **Changes the Server Plan**, which followed the PRD's 10 MB for everyone (and earlier PDF/DOCX/ODT at 5 MB).

**48. Job board integrations at launch: which ones does the launch page show?**

Answer: At launch, the jobs page shows Google for Jobs and share links. LinkedIn, Indeed, ZipRecruiter and TargetJobs show as "Coming soon".

- Google for Jobs needs no partner approval: it reads structured data on our public job pages (BE-28). So it isn't a push integration or a new sub-processor.
- Closed jobs: their page marks the job as expired, so Google drops it.
- Pushing to job boards is still an in-or-out decision in PL-01. Only Founder and HR 1 can do it (`job.share.job_board`).

## Still waiting on

Dates are from the answers above.

- **16 Oct:** counsel on the US gates (Q23); retention values for Gate 2 (Q24); the data-class table signed by Legal and the backend lead (Q32).
- **23 Oct:** DPA text (Q11); applicant notice text for launch markets (Q13); Kenya s.50 opinion (Q17); India legitimate use vs consent (Q30).
- **Week of 26 Oct:** sub-processor list live (Q14).
- **Dates still to add:** VeraSafe contract signed (Q16); Nigeria legal report (Q17).
- **Counsel answers still open:** Q5 (HR and health data), Q15/16/18 (whether VeraSafe and the ODPC registration are needed from launch day, NDPC thresholds), Q21 (SCCs for US-hosted EU data; Nigeria and Kenya hosting), Q26 (automatic rejection), Q27 (DPIA), Q29 (EU pay transparency), Q34, Q35.
- **Proposed, awaiting sign-off:** Q2 (one Founder per company), Q21 (company-keyed residency, signed in BE-01a), Q18 (internal market-switch admin screen).
- **Design:** the Founder-as-Legal flow (Q4, DS-04, W2) and the screens in Q43 and Q44.
- **Open from the answers:** whether notices say "Open HR" or "Angle" (Q12); one domain to use across the T&C, privacy policy and emails (Q11).
