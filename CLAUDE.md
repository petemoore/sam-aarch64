# sam-aarch64 — project-local Claude Code instructions

This file overrides Pete's global Claude Code instructions (`~/.claude/CLAUDE.md`) where they conflict, **for this repo only**. The global rules still apply for everything not addressed here.

## Scope discipline (load-bearing)

**Anything Pete says during a sam-aarch64 session is project-scoped.** Never propagate preferences from this repo to `~/.claude/CLAUDE.md` (global) or `~/git/CLAUDE.md` (a sibling-project file that gets picked up via Claude Code's directory walk-up). Pete's work repos have very different workflows, and mixing them would cause real friction in those repos.

If a preference seems "obviously global", **ask** before propagating — the default direction is project-local.

Memory entries written from sam-aarch64 sessions live in `~/.claude/projects/-Users-pmoore-git-sam-aarch64/memory/` — they are project-scoped by their path. Use that location for any preference shared in this project.

See also: `feedback_project_scoped_preferences` memory entry.

## PR workflow overrides

These override the global workflow for this single-developer private project.

### 1. Open PRs ready-for-review by default, not draft

The global rule says "always create PRs as draft". For this repo, **default to ready-for-review** (`gh pr create` without `--draft`). The draft flag was about external-team signalling, which doesn't apply when nobody else is looking at the repo.

Pete still reviews before merge — the difference is just one fewer click for him each time. Drafts are still fine if the change genuinely isn't ready (e.g. mid-flight, CI not yet green).

See also: `feedback_pr_drafts_not_required` memory entry.

### 2. Land PRs with merge commits, never `--squash` or `--rebase`

Use `gh pr merge --merge --delete-branch`. Squash-merge rewrites commit hashes and destroys the iteration story on a branch; we got bitten by exactly this when PR #43's broken revert went undetected because the squash hid the in-PR commit sequence (the agent did `git revert` of a squash hash and the operation silently became near-no-op).

The repo settings disallow `--squash` and `--rebase` at the GitHub level — only merge commits are accepted. The branch-protection on `main` also requires the 11 CI status checks to pass before merge, and blocks force-push + deletion.

For **building up a branch locally before opening the PR**, squash / amend / rebase are still fine — that's local hygiene, separate from how the PR lands.

See also: `feedback_merge_commits` memory entry; `docs/notes/2026-05-28-test-variant-ci-regression.md` for the PR #43 incident.

## Pointers for first-session-on-this-repo

- Project overview + roadmap: `docs/ROADMAP.md`.
- Current state + milestone status: latest `docs/notes/m{6,5,4,3}-status.md` files.
- SAM Coupé paging primer: `docs/notes/sam-paging.md`.
- Memory index: `~/.claude/projects/-Users-pmoore-git-sam-aarch64/memory/MEMORY.md` (always auto-loaded).
- Pete's prime directive ("correctness over workarounds"): the first entry in the memory index — read it before anything else.
