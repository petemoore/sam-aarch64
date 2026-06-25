# `tools/deploy-guard` — pre-deployment safety gate for the real SAM

A Claude Code **PreToolUse** hook that makes it impossible to push a program to
the real SAM Coupé + Quazar Trinity without first confirming an exhaustive
hardware-readiness checklist. ~50 hardware deployments have failed because a
program was pushed without all known hardware fixes (most recently `serve_main`
crashed the SAM for lack of the SD-before-ENC `drv_init` fix); this gate makes
that class of accident hard to repeat.

## How it fires

`pre-deploy-check.sh` (python3, stdlib-only) is registered in
`.claude/settings.json` under `hooks.PreToolUse` with a `Bash` matcher. It reads
the hook JSON on stdin and inspects `tool_input.command`:

- **Not a SAM deploy** → exits 0 silently (normal permission flow).
- **A SAM deploy** → `deny`, with the full checklist in `permissionDecisionReason`.
- **A SAM deploy prefixed `DEPLOY_CHECKED=1`** → allowed through (the escape hatch:
  confirm every checklist item against the program, then re-run prefixed).

The guard always exits 0 (falls through to "no decision" on any error), so it can
never block ordinary Bash. The detected-pattern list and the test matrix live in
the script's docstring + `DEPLOY_PATTERNS` — edit there to add a pattern (err
toward catching: a false positive is just confirm-and-proceed).
