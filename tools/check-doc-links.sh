#!/usr/bin/env bash
# tools/check-doc-links.sh — verify every relative markdown link resolves.
#
# Scans the entry docs (README.md, CLAUDE.md, src/README.md) and every
# markdown file under docs/, tools/, tests/ for inline link targets
# `[text](target)`. A target that is a path into the repo — either
# repo-root-relative (docs/, src/, tools/, tests/, reference/, scripts/, …)
# or relative to the containing file's directory — must exist. URLs
# (including immutable blob/<sha> pins), mailto: links, and in-page
# anchors are skipped.
#
# Exit 0 when every link resolves; exit 1 listing each dangling link.
set -u
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

fail=0

files="$( { ls README.md CLAUDE.md src/README.md 2>/dev/null;
            find docs tools tests -name '*.md' 2>/dev/null; } | sort -u)"

for f in $files; do
  dir="$(dirname "$f")"
  # Inline-link targets: the `](target` span, with any #anchor and the
  # closing paren excluded by the character class.
  while IFS= read -r target; do
    [ -n "$target" ] || continue
    case "$target" in
      http://*|https://*|mailto:*) continue ;;  # URLs incl. blob/<sha> pins
    esac
    [ -e "$ROOT/$target" ] && continue          # repo-root-relative
    [ -e "$dir/$target" ] && continue           # file-relative
    echo "DANGLING: $f -> $target"
    fail=1
  done < <(grep -oE '\]\([^)#[:space:]]+' "$f" 2>/dev/null | sed 's/^](//')
done

if [ "$fail" -ne 0 ]; then
  echo "check-doc-links: dangling links found" >&2
  exit 1
fi
echo "check-doc-links: OK"
