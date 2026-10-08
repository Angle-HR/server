# Figma findings for the open questions

Read on 5 Oct 2026 from the Open HR Figma file: Job creation Flow, Job creation Flow Ready for Dev, Create job with AI, Job Screening Flow, Home Page, plus a check of the Login, Emails and Branding pages (not relevant to these questions). Rule applied: where Figma and a document disagree, the document wins. Question numbers match `open-questions-for-team.md`.

## Answered

**Templates (removed from the team list). Are "Save as template" and "Keep this setup for my future jobs" the same template?** No, two things (Figma only; no document says otherwise).
- Job details: "Save as template. Saves the job details only. Your application form isn't included." Templates can be renamed, exported, deleted, and shared: "Allow others in your organization to use this template".
- AI flow: "Since this is your first, want me to keep this application form as your default? Your next job starts with it already set up."
- Matches the plan's `hiring.templates` kinds `job_details` and `application_form`.

**Final CV rule (removed from the team list).** PDF only, 10 MB, because the PRD wins. Figma disagrees with itself: the older flow says "Upload a Resume, Size limit: 10 MB", a newer block says "DOCX, DOTX, PDF, Max 5 MB". Keep the limits in config.

## Partly answered

**Status wording (removed from the team list).** Figma's jobs list uses six statuses: Open, Paused, Draft, Closed, Archived, Expired. Backend v2 says `published`, so by your rule `published` stays the stored value and "Open" is the label the UI shows. **The server plan currently says the opposite** ("Rename `published` to `open`"), which breaks the rule: it needs correcting or a decision.

**37. AI co-writer sources.** The Figma AI flow never touches LinkedIn. It reads the user's workspace and their last posts, matches a job family, and checks pay against similar roles ("Checking pay in Lagos", "34 similar roles"). The PRD's LinkedIn source is still unresolved, so this stays open. Paste-a-link-or-text is still the likely answer.

**45. Job board integrations.** The banner lists LinkedIn, Indeed (shown twice, a design typo), Google for Jobs, ZipRecruiter and TargetJobs, with a "Connect integrations" button. The plan already ships Share Kit and Google for Jobs first, with Indeed later. Open part: what the launch page shows for the others.

## Not answered by Figma (still open)

- **40. Permissions and Share screens.** Only the breadcrumb exists: Jobs > Job details > Application form > Permissions > Share. The AI flow copy says "Publishing takes you to access and permissions first". No designed screens.
- **41. Knockout switches, legal reason, retention, country checklist.** None of these strings appear on any job page.
- **42. Checkbox vs Multiple choice.** Both sit under "Choice" with Single choice, Dropdown and Linear scale. An older mobile picker has no Checkbox. Nothing explains the difference.
- **34, 35, 38, 39.** Description format, public job domain, duplicate detection and streaming are not specified anywhere in Figma.

## Consistency notes

- Figma's special category list is Health/disability, Race/ethnicity, Religion/belief, Biometric (five with Criminal records), and the AI flow shows four. Copy still says "Art. 9 / equivalent". Product already decided on 5 Oct that criminal records cite Art. 10.
- The older template flow includes sexual orientation in self-ID (Asexual, Heterosexual, Bisexual, Pansexual). Product decided to keep it, with guardrails.
- The Home page banner says "We're reviewing your account and will follow up shortly. In the meantime, you can create and manage Jobs." This fits the decision that companies can draft but cannot publish until verified.
- The invite screen says "By clicking Accept invite, you agree to our privacy policy and data processing agreement", so acceptance is a button click with no signatory shown. The DPA frames did not render.

## Applied on 5 Oct

Templates and the CV rule are answered, and `published` stays the stored status, so those three questions are removed from `open-questions-for-team.md` and the server plan is updated to match. The question numbers above refer to the list before that removal.
