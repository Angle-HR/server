# Hiring module: open questions for the team

Compiled 5 Oct 2026 from the Job Creation Server Plan, the Sprint v2 docs (Product & Legal, Backend v2), KYB V2, the GDPR doc, the Roles and permissions doc and the T&C. Each question says who can answer it, what it blocks, and where it comes from. Questions marked **Counsel** need a lawyer's answer.

## 1. Roles, invites and ownership (Jerry, Product, Legal)

Source: PL-02, Roles and permissions TODO. Blocks: the invite, role-change and ownership-transfer endpoints, and the Founder-as-Legal setup at signup.

1. How does a member get invited? Which roles can send an invite, and which roles can the inviter hand out?
2. How are roles changed or removed? Does removing someone end their sessions immediately (V2 Onboarding §11.3 says yes)?
3. When ownership of an account transfers, does the old owner lose all roles at once, does the new owner have to accept, and is the new owner re-verified (KYB V2 says the new owner is identity-verified)?
4. Does the Founder hold the Legal role by explicit assignment at signup when the company has no legal or data-protection person? Is it shown in the UI, and how does it get handed over later?
5. Can HR roles see health data? The GDPR doc says yes for adjustments, and the Roles doc says never (they see only the operational need). **Counsel**
6. What is HR 2's `job.collaborator.add_restricted`? It is "-" in the matrix today.
7. Who sets a job's jurisdiction, retention period and residency region, given that routing is automatic? (Roles doc: `job.jurisdiction.set`, `job.retention.set`, `job.residency.region.set`.)
8. Do the Job Screening PRD's Super Admin (per-user screen access) and Co-Reviewer map onto the seven roles, or do we add them?
9. When a creator is deactivated, do their jobs go to their manager or to the account admin?
10. Who may use `jobs.export` and `jobs.manage_any`? They are in the server plan but not in the roles matrix, so no role has them.

## 2. Legal documents (Jerry, Legal, Founder)

Source: PL-03, T&C §3 and §9, GDPR doc §3. Blocks: the DPA gate, publish.

11. Is there a DPA document and version ready? The T&C say customers must sign it before uploading personal data (openhr.io/legal/dpa), while the 5 Oct decision folds the DPA into the T&C. Which is it, and does acceptance need a named signatory?
12. Who is the legal entity named in notices and the DPA (the T&C say Angle Open Source Ltd)?
13. When will the applicant privacy notice text be ready for each launch market (PL-03 targets week 3)?
14. Where is the public sub-processor list published?

## 3. Markets and platform prerequisites (Founder, Counsel)

Source: PL-01, PL-03, BE-01a, BE-02. Blocks: opening EU, NG, KE, SG, IN and AE.

15. Which markets are on at launch? The server currently has only UK and US open. Who signs the decision record (PL-01)?
16. EU: do we appoint an Estonian or Irish entity or use a service such as VeraSafe, and by what date? The EU stays off until then.
17. Nigeria and Kenya: when does the legal consultant report? Kenya's opinion on s.50 and the in-country copy is due by end of week 3 (BE-02).
18. Does Angle itself register with NDPC and ODPC, and who confirms each prerequisite, from which admin screen?
19. India: what has to happen before it can open? We have nothing yet.
20. Singapore needs a DPO and the UAE needs a regime review. Are either planned for launch?
21. Residency: is the key the tenant, the job market or the candidate's location (BE-01a)? Which region serves Nigeria and Kenya? There is no Africa region in the current plan.

## 4. Gates, retention and screening (Counsel, Legal, Product)

Source: PL-04, PL-05, PL-07, BE-12, BE-27. Blocks: the gate catalog, retention values, knockout behaviour.

22. Where is the full gate catalog? The server only has the plan's own gates and the US set. Job Creation Flow V2 §4 should have the rest.
23. Can counsel confirm the gate wording and the proposed US gates (pay transparency, salary history, CCPA, fair chance, EEO, NYC AEDT)? **Counsel**
24. Retention: the sources conflict (3/6/12 months in the PRD, 6 to 12 in the GDPR doc, 12 in pricing, 1 year for the US EEOC). What are the minimum, maximum and default per category and market? Is Kenya 12 months or employment plus six years? **Counsel**
25. Who can change a retention period, and can a job override the tenant default?
26. Do failed knockouts flag a candidate for human review (the recommendation) or reject automatically? Which markets, if any, allow automatic rejection? **Counsel**
27. Where is a DPIA recorded, who records it, and what counts as "automated screening"?
28. Is the discriminatory-wording check a fixed word list per market or an AI classifier with human override? Who writes the lists?
29. EU pay transparency: is blocking publish without a range acceptable, given the Directive also allows disclosure before the first interview? What are the rules per member state and US state? **Counsel**
30. India: legitimate use or consent? Does the 22-language notice option apply? **Counsel**
31. Does the reapply block (optional, off by default, 6 months) have a lawful basis, and what is kept for it?
32. How should the data classes be placed: right-to-work answers, vetting results, adjustment needs, EDI? (PL-06 asks for a signed table.)

## 5. Product and engineering decisions

Source: server plan, AI Build Plan. Blocks: schema choices and some endpoints.

33. Description storage: sanitised HTML or the editor's JSON?
34. Public job pages: one shared domain or per-org subdomains?
35. Assessments: build our own or link to a third-party tool?
36. Where does the AI co-writer get company information, given LinkedIn has no open API for company pages?
37. Duplicate-job detection: what counts as a duplicate (JCHF §7)?
38. Is a streaming method decided for AI chat (server-sent events or websockets)?

## 6. Design

Source: server plan, Figma notes. Blocks: frontend work, not the server.

39. When will the Permissions and Share screens be designed?
40. When will the knockout switches, legal reason and retention fields, and the country checklist on Share be added?
41. How does a Checkbox question differ from Multiple choice? They look the same in the preview.
42. Job board integrations at launch: the Figma jobs page lists LinkedIn, Indeed, Google for Jobs, ZipRecruiter and TargetJobs, but LinkedIn is not taking new partners and Indeed needs about 6 weeks of approval. Which ones does the launch page show? Suggestion: Google for Jobs and share links, with the rest marked "coming soon".
