---
description: Opens a GitHub pull request for the current branch against `main`, using the GitHub CLI when available and preferring a description from `pr_template/pr-<branch>.md` when present. Use when the user wants to open a PR, create a pull request, run gh pr create, or submit the active branch for review.
---

# Open pull request (active branch)

## Goal

Create a **GitHub pull request** targeting **`main`** from the **current branch**, with a sensible title and body—without asking the user to run commands by hand unless authentication or tooling is missing.

## Prerequisites the agent checks

1. **Repository root** — Run from the clone root (where `.git` lives).
2. **Branch** — Refuse or warn if the current branch is `main` (nothing to PR). Suggest a feature branch first.
3. **Remote** — `origin` exists and the branch is **pushed**. If `git rev-parse @{u}` fails or the branch is not on the remote, run **`git push -u origin HEAD`** (after ensuring the user's changes are committed—do not push empty or unintended WIP unless they asked).

## Body content

1. **Preferred file** — `pr_template/pr-<sanitized-branch>.md`, using the **same sanitization as the Makefile**: take `git branch --show-current`, then replace any character not in `[A-Za-z0-9._-]` with `-`.

   Example: `docs/readme` → `pr_template/pr-docs-readme.md`.

2. **If that file exists** — Use it as the PR body. Optionally **omit** the trailing block from the horizontal rule before **`## Draft context (auto-generated)`** through the end of the file, so the GitHub description does not duplicate auto-generated git context (optional but preferred).

3. **If missing** — Run **`make pr-description`** (see the **generate-pr-description** workflow), fill the draft, then use the resulting file—or fall back to **`gh pr create --fill`** using commits, or a short body summarizing `git log main..HEAD` and `git diff main --stat`.

## GitHub CLI (preferred)

1. Verify **`gh`** is available: `command -v gh` or `gh --version`.

2. Verify auth: `gh auth status`. If not logged in, tell the user they must run **`gh auth login`** (or web flow); do not fabricate credentials.

3. Create the PR (after push):

   ```bash
   gh pr create --base main --head "$(git branch --show-current)" --title "<title>" --body-file "<path-to-body.md>"
   ```

   **Title**: Prefer the **first line of the latest commit** (`git log -1 --pretty=%s`), or a concise imperative summary matching the change (align with **CONTRIBUTING.md** commit convention when possible).

   If **`--body-file`** is not usable (e.g. body only in memory), use **`--body`** with a here-doc or stdin as appropriate; avoid exceeding GitHub size limits.

4. Parse **`gh`** output for the **PR URL** and return it to the user.

### Useful variants

- **Draft PR**: add **`--draft`**.
- **Reviewer / assignee**: **`--reviewer`**, **`--assignee`** only if the user asked.

## Without `gh` (fallback)

1. Resolve **`owner/repo`** from **`git remote get-url origin`** (support both `https://github.com/owner/repo.git` and `git@github.com:owner/repo.git`).

2. Open or share the compare URL:

   `https://github.com/<owner>/<repo>/compare/main...<branch>`

   Use the **raw branch name** (including `/`) in the URL path segment GitHub expects—typically **`main...<branch>`** with the branch name **URL-encoded** if needed (spaces and special characters). If unsure, **`gh`** is strongly preferred.

3. Tell the user to paste the description from **`pr_template/pr-<sanitized-branch>.md`** (or the filled template) into the GitHub UI.

## Constraints

- **Base branch** is **`main`** unless the user explicitly names another base (then pass **`--base`** accordingly).
- Do **not** commit anything under **`pr_template/`** as part of this workflow; those files stay gitignored.
- If **push** would expose secrets or the user has not committed intentional changes, **stop** and explain—do not force-push or overwrite remote history without explicit instruction.

## Coordination with generate-pr-description

If there is no suitable body file yet, run the **generate-pr-description** workflow first (or in sequence), then open the PR with **`--body-file`** pointing at the completed draft.
