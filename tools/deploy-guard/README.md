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
never block ordinary Bash.

## What counts as a deploy (execution-only)

Detection is **per-statement verb analysis**, not a token match. The command is
split into statements on `&&`, `||`, `;`, newline and `|` (pipe); each
statement's leading `cd <path>` segments and `VAR=val` env prefixes are stripped
to expose the real verb; and a statement counts as a deploy only if its verb
actually **executes a push** to the SAM:

1. an interpreter (`python`/`python3`/`sh`/`bash`/`perl`) invoking a pusher
   script (`trinload-push*.py|.sh`, `trinpush-serve*`, `trinpush*`), or the
   pusher script run directly (`./trinpush.py`, an absolute path, the bare name);
2. a TFTP client — verb `tftp`/`atftp`, or any `tftp://` URL in the statement;
3. `curl`/`wget` with an upload flag (`-T`/`--upload-file`/`-d`/`--data`/
   `--data-binary`) **and** targeting the SAM (its IP or a `tftp://` URL);
4. a raw socket tool (`nc`/`ncat`/`socat`) with the SAM IP or the trinload
   discovery port (`0xEDB0`/`EDB0`/`60848`) in the statement.

A command that merely **mentions** a pusher path, the SAM IP, `&8000`, the
discovery port, etc. — a `grep`, `git ls-tree`, a heredoc writing a retro, a
`ping`/`arp` — is **not** a deploy and passes straight through, because no
statement's *verb* runs a push.

To add a **new deploy mechanism**, add a new **verb** rule in
`_statement_is_deploy` (a new interpreter, client, or socket tool) — never a
bare token "mention" pattern, or read-only commands start false-firing again.
`pre_deploy_check_test.py` (stdlib `unittest`) pins the must-fire / must-not-fire
/ bypass matrix; run it via `python3 -m unittest` (or `python3
pre_deploy_check_test.py`) and extend it alongside any detection change.
