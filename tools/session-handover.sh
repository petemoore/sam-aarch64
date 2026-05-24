#!/usr/bin/env bash
# tools/session-handover.sh — SessionStart handover context for sam-aarch64.
#
# Invoked by the project-scoped SessionStart hook in .claude/settings.json.
# Its job: make sure the agent that's taking over reads the *current* handover
# protocol, and isn't misled by a stale working copy.
#
# Design (safe-by-default — never lose work):
#   - If on a clean `main`: fetch + fast-forward to the latest origin/main, so
#     the doc below is current.
#   - Otherwise (feature branch / uncommitted changes / main can't fast-forward):
#     do NOT mutate the tree. Warn loudly that the doc may be stale and say how
#     to refresh. A blind `checkout main` could disrupt or lose in-progress work.
#   - Then print the handover protocol section from docs/ROADMAP.md (or warn if
#     its markers aren't present on this checkout).
#
# Always exits 0 — a handover banner must never fail the session.
set -u

root="${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel 2>/dev/null || echo .)}"
cd "$root" 2>/dev/null || exit 0

echo "=== sam-aarch64 session handover ==="

branch="$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo '?')"
dirty=""
{ git diff --quiet 2>/dev/null && git diff --cached --quiet 2>/dev/null; } || dirty=1

if git fetch -q origin 2>/dev/null; then :; else
  echo "(could not fetch origin — offline? the doc below may be behind remote)"
fi

if [ "$branch" = "main" ] && [ -z "$dirty" ]; then
  if git merge-base --is-ancestor origin/main HEAD 2>/dev/null; then
    echo "On main, clean, up-to-date with origin/main. ✅"
  elif git pull --ff-only -q 2>/dev/null; then
    echo "On main, clean — fast-forwarded to latest origin/main. ✅"
  else
    echo "⚠️  On main but cannot fast-forward (history diverged). The doc below may be"
    echo "    stale; resolve before relying on it (e.g. 'git status', reconcile, pull)."
  fi
else
  behind="$(git rev-list --count HEAD..origin/main 2>/dev/null || echo '?')"
  echo "⚠️  NOT on a clean main (branch=${branch}${dirty:+, uncommitted changes present})."
  echo "    origin/main is ${behind} commit(s) ahead — the handover doc below may be STALE."
  echo "    To refresh: stash/commit your work, then 'git checkout main && git pull --ff-only'."
fi

# Stray-output guard: superpowers skills natively save to docs/superpowers/,
# which is globally ignored here (the repo canon is docs/plans + docs/specs).
# If anything landed there, surface it so it's migrated rather than lost.
if [ -n "$(find docs/superpowers -type f 2>/dev/null)" ]; then
  echo "⚠️  Stray files in docs/superpowers/ (globally ignored — would be lost):"
  find docs/superpowers -type f 2>/dev/null | sed 's/^/      /'
  echo "    Migrate to docs/plans/ or docs/specs/ and delete the originals (see CLAUDE.md)."
  echo
fi

echo
if grep -q HANDOVER-PROTOCOL-START docs/ROADMAP.md 2>/dev/null; then
  sed -n '/HANDOVER-PROTOCOL-START/,/HANDOVER-PROTOCOL-END/p' docs/ROADMAP.md
else
  echo "⚠️  Handover-protocol markers not found in docs/ROADMAP.md on this checkout."
  echo "    It may predate the protocol or be on a different branch — check out main and pull."
fi

exit 0
