#!/usr/bin/env python3
"""PreToolUse deploy guard for the real SAM Coupé.

Registered as a Claude Code PreToolUse hook matching the Bash tool (see
.claude/settings.json). It reads the hook JSON on stdin, looks at
tool_input.command, and if the command actually EXECUTES a *real-hardware
SAM deployment* it DENIES the call and injects an exhaustive pre-deploy
checklist. The agent confirms every item against the specific program,
then re-runs the same command with DEPLOY_CHECKED=1 set, which the guard
lets through.

The gate, in one line:
    no DEPLOY_CHECKED in the command  -> deny + checklist
    DEPLOY_CHECKED=1 present          -> allow (the agent has confirmed)

Detection is EXECUTION-ONLY (per-statement verb analysis), not a token
match. A command counts as a deploy ONLY if one of its statements actually
RUNS a pusher: an interpreter invoking a pusher script, a tftp/atftp
client, an upload (curl/wget -T … to the SAM), or a raw socket tool
(nc/socat) aimed at the SAM IP or the trinload discovery port. A command
that merely MENTIONS a pusher path, the SAM IP, &8000, the discovery port,
etc. — a grep, a git ls-tree, a heredoc writing a retro, a ping — is NOT a
deploy and is allowed through. See README.md for how to add a verb for a
new deploy mechanism (a new mechanism needs a new VERB pattern, never a
bare token mention).

Pure stdlib (no jq / no third-party deps) so it runs anywhere python3 is.
Always exits 0; the decision is carried in the JSON on stdout. On any
internal error it falls through to "no decision" (exit 0, no output) so a
guard bug can never block ordinary Bash.
"""

import json
import re
import shlex
import sys


# --- SAM deployment detection (execution-only) --------------------------------
#
# A command is a deploy iff one of its STATEMENTS actually executes a push to
# the SAM. We split the command into statements on &&, ||, ;, newline and |
# (pipe), strip each statement's leading `cd <path>` segments and `VAR=val`
# env-assignment prefixes (so `DEPLOY_CHECKED=1 python …` and `cd ~/x && …`
# expose the real verb), then test the verb + args of each statement against
# the execution patterns below.
#
# To add a NEW deploy mechanism, add a new VERB rule here (a new interpreter,
# client, or socket tool) — never a bare token "mention" pattern. A grep / ping
# / git ls-tree of a deploy token must keep passing.

SAM_IP = "192.168.2.75"

# Pusher scripts run via an interpreter, or invoked directly.
_PUSHER_SCRIPT_RE = re.compile(
    r"(?:^|/)(?:trinload-push|trinpush-serve|trinpush)\S*\.(?:py|sh)$",
    re.IGNORECASE,
)
# The same, anywhere in a statement's argument list (interpreter case).
_PUSHER_SCRIPT_ARG_RE = re.compile(
    r"(?:trinload-push|trinpush-serve|trinpush)\S*\.(?:py|sh)\b",
    re.IGNORECASE,
)

_INTERPRETERS = {"python", "python3", "sh", "bash", "perl"}

# The trinload discovery port (0xEDB0), in its written forms.
_DISCOVERY_PORT_RE = re.compile(
    r"0xEDB0|(?<![0-9a-fx])EDB0(?![0-9a-fx])|(?<![0-9a-fx])60848(?![0-9])",
    re.IGNORECASE,
)

_TFTP_URL_RE = re.compile(r"tftp://", re.IGNORECASE)
_SAM_IP_RE = re.compile(re.escape(SAM_IP))

# curl/wget body-upload flags. A tftp:// URL is treated as a deploy ONLY when it
# is an argument to a transfer verb that actually UPLOADS (curl/wget here, or the
# tftp/atftp client below) — never on a bare mention. wget's body-upload flags
# (--post-file/--body-file) are included so a wget upload is caught by the
# execution-gated rule rather than by matching the URL anywhere in the statement
# (which false-fired on a grep / go-test / heredoc that merely names a tftp:// path
# — i268).
_UPLOAD_FLAGS = {
    "-T",
    "--upload-file",
    "-d",
    "--data",
    "--data-binary",
    "--post-file",
    "--body-file",
}


def _statements(command: str):
    """Split a command string into individual statements.

    Splits on &&, ||, ;, newline and | (pipe). This is a deliberately coarse
    lexical split — it does not honour quoting around separators — which is
    safe here: over-splitting can only ever expose MORE verbs to inspect, so
    it cannot hide a real deploy.
    """
    return re.split(r"&&|\|\||;|\n|\|", command)


def _strip_prefixes(tokens):
    """Strip leading `cd <path>` segments and `VAR=val` env prefixes.

    Returns the remaining tokens, whose first element (if any) is the real
    verb of the statement. `cd X` with no following verb collapses to [].
    """
    i = 0
    n = len(tokens)
    while i < n:
        tok = tokens[i]
        # `cd <path>` — drop the `cd` and its single path argument.
        if tok == "cd":
            i += 2  # skip `cd` and the path
            continue
        # `VAR=val` env-assignment prefix (e.g. DEPLOY_CHECKED=1, FOO=bar).
        if re.match(r"^[A-Za-z_][A-Za-z0-9_]*=", tok):
            i += 1
            continue
        # `env` wrapper: drop it (and any following VAR=val it carries are
        # handled by the loop), then the next token is the verb.
        if tok == "env":
            i += 1
            continue
        break
    return tokens[i:]


def _statement_is_deploy(stmt: str):
    """Return a human label if this single statement EXECUTES a push, else None."""
    stmt = stmt.strip()
    if not stmt:
        return None
    try:
        tokens = shlex.split(stmt)
    except ValueError:
        # Unbalanced quotes etc. — fall back to a whitespace split so a real
        # deploy verb is still seen rather than silently dropped.
        tokens = stmt.split()
    tokens = _strip_prefixes(tokens)
    if not tokens:
        return None

    verb = tokens[0]
    args = tokens[1:]
    verb_base = verb.rsplit("/", 1)[-1]  # basename for path-form verbs

    # 1. Interpreter invoking a pusher script, OR the verb itself is a pusher
    #    script (./trinpush.py, /path/to/trinload-push.py, bare trinpush.py).
    if verb_base in _INTERPRETERS:
        for a in args:
            if _PUSHER_SCRIPT_ARG_RE.search(a):
                return "a pusher script run via " + verb_base
    if _PUSHER_SCRIPT_RE.search(verb) or _PUSHER_SCRIPT_ARG_RE.search(verb_base):
        return "a pusher script executed directly (" + verb_base + ")"

    # 2. A TFTP client invocation: verb tftp/atftp. A bare tftp:// URL elsewhere
    #    (a grep, a go-test name, a heredoc path) is a MENTION, not an execution,
    #    so it is NOT matched here — a tftp:// upload is caught by rule 3 below
    #    (curl/wget + an upload flag), the only verbs that actually push via it.
    if verb_base in ("tftp", "atftp"):
        return "a TFTP client invocation (" + verb_base + ")"

    # 3. curl/wget WITH an upload flag AND targeting the SAM.
    if verb_base in ("curl", "wget"):
        has_upload = any(
            a in _UPLOAD_FLAGS or a.split("=", 1)[0] in _UPLOAD_FLAGS for a in args
        )
        targets_sam = bool(_SAM_IP_RE.search(stmt) or _TFTP_URL_RE.search(stmt))
        if has_upload and targets_sam:
            return "an upload to the SAM via " + verb_base

    # 4. Raw socket tool aimed at the SAM IP or the discovery port.
    if verb_base in ("nc", "ncat", "socat"):
        if _SAM_IP_RE.search(stmt) or _DISCOVERY_PORT_RE.search(stmt):
            return "a raw socket push via " + verb_base + " to the SAM"

    return None


def is_deploy(command: str):
    """Return a human label if any statement EXECUTES a push to the SAM, else None.

    Execution-only: a command that merely mentions a pusher path, the SAM IP,
    &8000, or the discovery port (grep, ping, git ls-tree, a heredoc) is NOT a
    deploy. Only a statement whose verb actually RUNS a pusher counts.
    """
    for stmt in _statements(command):
        label = _statement_is_deploy(stmt)
        if label:
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
