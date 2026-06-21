#!/usr/bin/env python3
"""PreToolUse deploy guard for the real SAM Coupé.

Registered as a Claude Code PreToolUse hook matching the Bash tool (see
.claude/settings.json). It reads the hook JSON on stdin, looks at
tool_input.command, and if the command looks like a *real-hardware SAM
deployment* it DENIES the call and injects an exhaustive pre-deploy
checklist. The agent confirms every item against the specific program,
then re-runs the same command with DEPLOY_CHECKED=1 set, which the guard
lets through.

The gate, in one line:
    no DEPLOY_CHECKED in the command  -> deny + checklist
    DEPLOY_CHECKED=1 present          -> allow (the agent has confirmed)

Detection deliberately errs toward catching real deploys: a couple of
false positives (the agent confirms and proceeds) are acceptable; a
missed deploy that strands or crashes the SAM is not. See README.md for
how to add new deploy patterns.

Pure stdlib (no jq / no third-party deps) so it runs anywhere python3 is.
Always exits 0; the decision is carried in the JSON on stdout. On any
internal error it falls through to "no decision" (exit 0, no output) so a
guard bug can never block ordinary Bash.
"""

import json
import re
import sys


# --- SAM deployment detection -------------------------------------------------
#
# Each entry is (regex-source, human-label). Matching is case-insensitive.
# A command that matches ANY of these is treated as a real-hardware deploy.
# Keep these precise: they must fire on real deploys (trinload push, a TFTP
# transfer to the SAM, a push to page 1 / &8000) but NOT on builds, greps,
# go test, git, etc.

SAM_IP = "192.168.2.75"

DEPLOY_PATTERNS = [
    # trinload push tooling (the primary deploy path)
    (r"trinload-push", "trinload-push (trinload UDP push to the SAM)"),
    (r"trinpush-serve", "trinpush-serve (push the serve program to the SAM)"),
    (r"trinpush", "trinpush (trinload UDP push wire protocol)"),

    # the SAM's IP on Pete's LAN — any command that talks to it
    (re.escape(SAM_IP), f"the SAM's IP {SAM_IP}"),

    # the trinload discovery port, in any of its written forms
    (r"0xEDB0", "the trinload discovery port (0xEDB0)"),
    (r"(?<![0-9a-fx])EDB0(?![0-9a-fx])", "the trinload discovery port (EDB0)"),
    (r"(?<![0-9a-fx])60848(?![0-9])", "the trinload discovery port (60848)"),

    # a TFTP transfer to the SAM (curl/tftp/atftp/tftp-hpa, RRQ or WRQ).
    # tftp:// is unambiguous; a bare `tftp ...` invocation is also a deploy.
    (r"tftp://", "a TFTP transfer (tftp:// URL)"),
    (r"\btftp\b\s", "a TFTP client invocation"),
    (r"\batftp\b", "an atftp client invocation"),

    # pushing to the trinload load address / page used for SAM RAM delivery
    (r"0x8000", "a push to &8000 (the trinload load address)"),
    (r"page[\s_-]*1\b", "a push to page 1 (SAM RAM page-1 delivery)"),
    (r"(?<![0-9a-fx])8000(?![0-9a-fx])", "a push to &8000 / page-1 load addr"),
]

# Patterns whose bare numeric form (8000, page 1) is ambiguous and only counts
# as a deploy when the command also carries push context (a push tool, &8000,
# the SAM IP). The explicit 0x8000 form is NOT in here — it always counts.
_NEEDS_PUSH_CONTEXT = {
    "a push to page 1 (SAM RAM page-1 delivery)",
    "a push to &8000 / page-1 load addr",
}

_PUSH_CONTEXT_RE = re.compile(
    r"trinload|trinpush|push|0x8000|page|" + re.escape(SAM_IP), re.IGNORECASE
)

# A command that merely SEARCHES FOR or PRINTS these tokens (grep for the
# string "trinload-push", cat a script, git log) is not a deploy. If the
# command's leading word is a read-only inspection tool, suppress the guard:
# a real deploy never starts with grep/cat/less/etc. This is precision, not
# safety relaxation — none of these tools can push to the SAM.
_INSPECTION_LEADERS = re.compile(
    r"^\s*(?:sudo\s+)?(?:grep|rg|ag|ack|cat|less|more|head|tail|bat|"
    r"find|fd|ls|wc|sed|awk|git\s+grep|git\s+log|git\s+show|git\s+diff)\b",
    re.IGNORECASE,
)


_HAS_SEPARATOR = re.compile(r"&&|\|\||[;|]")


def is_deploy(command: str):
    """Return the human label of the first matching deploy pattern, or None."""
    # Suppress on a pure read-only inspection of the deploy tokens — but only
    # when the command is a SINGLE statement (no &&/;/| chaining a real deploy
    # after the inspection), so e.g. `grep x f && curl -T x tftp://...` still
    # fires.
    if _INSPECTION_LEADERS.match(command) and not _HAS_SEPARATOR.search(command):
        return None
    has_push_context = bool(_PUSH_CONTEXT_RE.search(command))
    for src, label in DEPLOY_PATTERNS:
        if re.search(src, command, re.IGNORECASE):
            if label in _NEEDS_PUSH_CONTEXT and not has_push_context:
                continue
            return label
    return None


def already_confirmed(command: str) -> bool:
    """True if the command sets DEPLOY_CHECKED=1 (the confirm-and-proceed escape).

    Accepts the env var as an inline prefix (``DEPLOY_CHECKED=1 trinload-push ...``)
    or via ``export``/``env``. Matches 1/true/yes (case-insensitive).
    """
    return bool(
        re.search(r"\bDEPLOY_CHECKED\s*=\s*[\"']?(1|true|yes)\b", command, re.IGNORECASE)
    )


CHECKLIST = """\U0001F6A8 SAM HARDWARE DEPLOYMENT DETECTED — {label}.

This is a REAL-HARDWARE deployment to Pete's SAM Coupe + Quazar Trinity.
~50 hardware deployments have failed because a program was pushed WITHOUT all
known hardware fixes applied (most recently: serve_main crashed the SAM for
lack of the SD-before-ENC drv_init fix). DO NOT skip this.

Confirm EVERY item below OUT LOUD against the SPECIFIC program you are
deploying, then re-run the EXACT SAME command with DEPLOY_CHECKED=1 prefixed,
e.g.:  {example}

─────────────────────────────────────────────────────────────────
1. ALL known hardware fixes are present in THIS program
   (cross-check docs/notes/hardware-readiness-audit.md if it exists):
   [ ] Leading &FF flush before every SD command (i145g)
   [ ] drv_init runs BEFORE any SD transaction (i242) — NOT SD-then-drv_init
       (that ordering is the serve_main crash)
   [ ] enc_rx_reestablish after every SD transaction that precedes ENC
       serving (i242/i245)
   [ ] Clean exit via tr_terminate on BOTH success AND failure paths —
       no bare di;halt that strands the SAM (i243)
   [ ] Bounded SD busy-wait (i241) — a stuck card cannot hang the SAM
   [ ] Colin's 4-step deselect tail (&30 -> dummy &DF -> &30 -> &04)
2. This EXACT program passed the FULL emulation path (not a flat harness):
   go test green, AND the emulator MODELS the hardware behaviours this path
   exercises (no known emulation gap for this path).
3. The program has a tested, recoverable EXIT back to trinload (recover it
   remotely, or it auto-RETs) — it will NOT strand the SAM.
4. CAPTURE-READINESS: every listener/recorder you need to capture the result
   is installed, running, and LOOPBACK-VERIFIED before the shot.
5. trinload is CONFIRMED RUNNING on the SAM right now (probed, not assumed).
6. You have reviewed THIS program against the hardware-readiness audit and it
   is in the HARDWARE-READY list.
─────────────────────────────────────────────────────────────────

After confirming every item, re-run with DEPLOY_CHECKED=1 prefixed to proceed."""


def deny(label: str, command: str) -> None:
    first_line = command.strip().splitlines()[0] if command.strip() else command
    example = "DEPLOY_CHECKED=1 " + first_line
    reason = CHECKLIST.format(label=label, example=example)
    out = {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "deny",
            "permissionDecisionReason": reason,
        }
    }
    json.dump(out, sys.stdout)
    sys.stdout.write("\n")


def main() -> int:
    try:
        raw = sys.stdin.read()
        data = json.loads(raw) if raw.strip() else {}
    except Exception:
        # Malformed input: never block ordinary Bash on a guard bug.
        return 0

    if data.get("tool_name") != "Bash":
        return 0

    command = (data.get("tool_input") or {}).get("command", "")
    if not isinstance(command, str) or not command:
        return 0

    label = is_deploy(command)
    if not label:
        return 0  # not a deploy: no decision, normal permission flow

    if already_confirmed(command):
        return 0  # confirmed: let it through via the normal flow

    deny(label, command)
    return 0


if __name__ == "__main__":
    sys.exit(main())
