# `tools/deploy-guard` — pre-deployment safety gate for the real SAM

A Claude Code **PreToolUse** hook that makes it impossible to push a program to
the real SAM Coupé + Quazar Trinity without first confirming an exhaustive
hardware-readiness checklist. ~50 hardware deployments have failed because a
program was pushed without all known hardware fixes (most recently `serve_main`
crashed the SAM for lack of the SD-before-ENC `drv_init` fix); this gate makes
that class of accident hard to repeat.

## How it fires

`pre-deploy-check.sh` (python3, stdlib-only — no `jq`) is registered in
`.claude/settings.json` under `hooks.PreToolUse` with a `Bash` matcher. On each
Bash call it reads the hook JSON on stdin and inspects `tool_input.command`:

- **Not a SAM deploy** → exits 0 silently (normal permission flow).
- **A SAM deploy** → `deny`, with the full checklist in `permissionDecisionReason`.
- **A SAM deploy prefixed `DEPLOY_CHECKED=1`** → allowed through.

The escape hatch: confirm every checklist item against the specific program, then
re-run the exact same command prefixed with `DEPLOY_CHECKED=1`. The guard always
exits 0 (falls through to "no decision" on any error), so it can never block
ordinary Bash.

## Detected patterns (case-insensitive)

`trinload-push`, `trinpush-serve`, `trinpush`; the SAM IP `192.168.2.75`; the
discovery port (`0xEDB0`/`EDB0`/`60848`); a TFTP transfer (`tftp://`,
`tftp`/`atftp`); a push to `0x8000`/`&8000` or `page 1`. A read-only inspection
of these tokens (`grep`/`cat`/`git log`…) does not fire unless it chains a real
deploy after `&&`/`;`/`|`.

## Adding a pattern

Edit `DEPLOY_PATTERNS` in `pre-deploy-check.sh` (a `(regex, label)` pair). Err
toward catching deploys — a false positive just means confirm-and-proceed; a
missed deploy can crash the SAM. Test per the matrix in that file's docstring.
