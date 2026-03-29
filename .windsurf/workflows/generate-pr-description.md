---
description: Drafts a pull request description for the active Git branch using the repository PR template. Use when the user asks for a PR description, pull request body, GitHub PR text for the active branch, or when preparing to open a PR.
---

# Generate PR description (active branch)

## Goal

Produce a Markdown PR body that follows `.github/pull_request_template.md`, scoped to the **current branch**, and save it under `pr_template/` (ignored by git).

## Workflow (agent runs this — do not ask the user to run commands)

1. **Run** from the repository root:

   ```bash
   make pr-description
   ```

   This creates `pr_template/pr-<sanitized-branch-name>.md` by copying the official template and appending auto-generated context (branch name and commits vs `main` when available).

2. **Read** the generated file path from the command output (`✅ Wrote …`).

3. **Edit that file in place**: fill **Summary**, **Related Issue(s)** (or remove the placeholder), check the right **Type of Change** boxes, complete **Changes Made** and **How to Test** from `git diff` / commit messages / the Draft context section. Remove or replace HTML comments where you have real content.

4. **Update the Checklist** (required): replace every `- [ ]` in **## Checklist** with `- [x]` or `- [ ]` based on verification—do not leave the section as all empty boxes by default. Use the table below; run commands when you can (e.g. tests, git status).

   | Item | How to verify |
   |------|----------------|
   | Up to date with `main` | After `git fetch` if needed: compare to `main` / `origin/main` (e.g. not behind, or note if merge/rebase still needed). |
   | [Commit convention](../CONTRIBUTING.md#commit-convention) | Skim `git log main..HEAD` (or equivalent range) for message style. |
   | Tests added or updated | Inspect diff: test files or new test cases changed. Use `[ ]` with **N/A** in prose only if the change truly cannot need tests (e.g. typo-only docs)—and say so briefly in your report. |
   | All existing tests pass locally | Run `make test` or `go test ./...` from repo root; mark `[x]` only on success. |
   | Documentation updated where needed | Mark `[x]` if README/docs/comments in the diff satisfy the change; `[ ]` if follow-up docs are still required. |
   | Merge conflicts resolved | `git status` clean for merge state; or merge/rebase with `main` locally if you run it. |
   | No secrets / credentials / debug noise | Quick pass over `git diff main...` (and staged) for keys, tokens, passwords, stray `console`/`fmt` debug. |

   If you cannot verify an item (e.g. no network for `git fetch`), leave it `[ ]` and mention that in your report.

5. **Report** the path to the finished file, what you filled in, and checklist items left unchecked (if any) with reason.

## If `make pr-description` fails

1. Read `.github/pull_request_template.md`.
2. Create `pr_template/` if needed.
3. Write `pr_template/pr-<branch>.md` (sanitize `/` and unsafe characters in the filename).
4. Mirror the template sections; use `git log` vs `main` / `origin/main` / `master` as needed for **Summary** and **Changes Made**. Apply the **Checklist** verification table from the main workflow above.

## Constraints

- Do not commit files under `pr_template/`; the directory is gitignored.
- Always base the structure on `.github/pull_request_template.md`, not a free-form outline.
