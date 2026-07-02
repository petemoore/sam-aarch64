#!/usr/bin/env python3
"""Unit tests for the SAM deploy guard's execution-only detection.

The guard lives in ``pre-deploy-check.sh`` (python3 despite the .sh name, so
that Claude Code's Bash PreToolUse hook can exec it). Because the filename is
not an importable module name, we load it via importlib.

Run:  python3 -m unittest    (from this directory)
  or: python3 pre_deploy_check_test.py
"""

import importlib.util
import io
import json
import os
import unittest
from contextlib import redirect_stdout
from importlib.machinery import SourceFileLoader

_HERE = os.path.dirname(os.path.abspath(__file__))
_GUARD_PATH = os.path.join(_HERE, "pre-deploy-check.sh")

# The guard is a python3 file with a .sh extension (so the Bash hook can exec
# it). spec_from_file_location can't infer a loader from .sh, so pass one
# explicitly that reads it as Python source.
_loader = SourceFileLoader("deploy_guard", _GUARD_PATH)
_spec = importlib.util.spec_from_loader("deploy_guard", _loader)
guard = importlib.util.module_from_spec(_spec)
_loader.exec_module(guard)


def run_main(command):
    """Drive a command through main() via JSON-on-stdin, return parsed stdout.

    Returns None when main() emits no decision (allowed / not-a-deploy), or the
    parsed hook-output dict when it denies.
    """
    payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": command}})
    out = io.StringIO()
    old_stdin = guard.sys.stdin
    guard.sys.stdin = io.StringIO(payload)
    try:
        with redirect_stdout(out):
            rc = guard.main()
    finally:
        guard.sys.stdin = old_stdin
    assert rc == 0, "guard must always exit 0"
    text = out.getvalue().strip()
    return json.loads(text) if text else None


class MustNotFire(unittest.TestCase):
    """Read-only commands that merely MENTION deploy tokens — never a deploy."""

    cases = [
        "cd ~/git/sam-aarch64 && echo x && git ls-tree -r --name-only main | grep trinload",
        "git grep -n 'trinload-push' main -- tools/",
        "cat > ~/retro.md <<EOF\n... trinload-push ...\nEOF",
        "ping 192.168.2.75",
        "grep 192.168.2.75 file",
        "git ls-tree -r --name-only main | grep trinpush",
        # extra read-only mentions that previously false-fired
        "echo 0x8000 page 1",
        "git show HEAD -- tools/trinload-push/trinload-push.py",
        "rg 'tftp' tools/",
        # i268: a tftp:// URL merely NAMED by a non-transfer verb (go test, grep,
        # a heredoc path) is a mention, not an execution — must not false-fire.
        "go test ./tools/netboot-oracle/z80/ -run TestTFTPClient -v",
        "grep -rn 'tftp://192.168.2.75' tools/",
        "cat > x.md <<EOF\nfetch via tftp://192.168.2.75/x\nEOF",
        "go test ./... -run TestTFTP <<EOF\ntftp://192.168.2.75/boot.bin\nEOF",
        # i336: statement separators INSIDE quotes are data, not boundaries —
        # a multi-line commit message whose wrapped line begins with a pusher
        # filename false-fired as "a pusher script executed directly".
        'g commit -m "trinload-push: retry finalize (i335)\n\n'
        'covered by pure verdict tests in\ntest_trinpush.py."',
        'echo "a; b; tools/trinload-push/sd-push.py"',
        "git commit -m 'ran tools/trinload-push/sd-push.py earlier; see notes'",
        # i337: filenames that merely EMBED a pusher name are not pushers —
        # the guard's own test module is the everyday case (run constantly).
        "cd tools/trinload-push && python3 -m unittest test_trinpush -v",
        "python3 test_trinpush.py",
        'cat > x.md <<EOF\n"see test_trinpush.py for the loopback tests"\nEOF',
        # i340: `python3 -m py_compile <pusher>` COMPILES the pusher, it does
        # not run it — this exact shape false-fired in three retro-corpus
        # sessions (2026-07-01/-02), each time via the DIRECTORY name
        # "trinload-push" matching the old un-anchored arg regex.
        "python3 -m py_compile tools/trinload-push/sd-push.py",
        "python3 -m py_compile tools/trinload-push/boot-record.py",
        "python3 -m compileall tools/trinload-push/",
        # i340: a grep whose PATTERN or target names pushers / deploy tokens
        # (the 2026-07-01e registry-view grep shape).
        "grep -iE 'record|tftp|push' docs/notes/item-registry-open.md",
        "grep -n 'sd-push.py' tools/trinload-push/sd-push.py",
        "git show HEAD -- tools/trinload-push/sd-push.py",
        # i340: a non-pusher helper that merely LIVES in the pusher directory
        # must not read as a pusher (the directory-name hole, closed by the
        # basename anchor).
        "python3 tools/trinload-push/some_helper.py",
    ]

    def test_must_not_fire(self):
        for cmd in self.cases:
            with self.subTest(cmd=cmd):
                self.assertIsNone(guard.is_deploy(cmd), f"falsely flagged: {cmd!r}")
                self.assertIsNone(run_main(cmd), f"main() denied: {cmd!r}")


class MustFire(unittest.TestCase):
    """Statements that actually EXECUTE a push to the SAM."""

    cases = [
        "python3 tools/trinload-push/trinload-push.py 192.168.2.75 foo.bin 1 0x8000",
        "cd ~/git/sam && python tools/trinload-push/trinpush-serve.py --strategy highest",
        "./trinpush.py 192.168.2.75 x",
        "tftp 192.168.2.75",
        "curl -T foo.bin tftp://192.168.2.75/x",
        "nc 192.168.2.75 60848 < payload",
        # a few more execution shapes
        "atftp --put --local-file foo.bin 192.168.2.75",
        "/abs/path/to/trinload-push.py 192.168.2.75 foo.bin",
        "echo hi && python3 tools/trinload-push/trinload-push.py 192.168.2.75 foo.bin",
        "wget --post-file=foo tftp://192.168.2.75/x",  # wget + --post-file upload flag -> rule 3
        # i336: quote-aware splitting must not HIDE a deploy inside a quoted
        # interpreter argument — the shlex'd arg search still reaches it,
        # even with a newline inside the quotes.
        'bash -c "python3 tools/trinload-push/sd-push.py 192.168.2.75 x.mgt"',
        'sh -c "echo hi\npython3 tools/trinload-push/trinload-push.py 192.168.2.75 f.bin"',
        # i337: an UNCLOSED quote must not swallow a chained deploy — bash still
        # executes statements after a heredoc body's stray apostrophe.
        "cat > /tmp/x <<EOF\ndon't worry\nEOF\n"
        "python3 tools/trinload-push/trinload-push.py 192.168.2.75 f.bin",
        'echo "oops\npython3 tools/trinload-push/trinload-push.py 192.168.2.75 f.bin',
        # i340: the push-and-run launchers that were previously caught only by
        # accident (via their directory name) — each stage-1-pushes a program
        # to the SAM and must fire by BASENAME, from any path.
        "python3 tools/trinload-push/sd-push.py 192.168.2.75 junk.mgt build/sd_push.bin",
        "python3 tools/trinload-push/boot-record.py 192.168.2.75 186",
        "python3 tools/trinload-push/delete-record.py 192.168.2.75 187",
        "python3 tools/trinload-push/list-records.py 192.168.2.75",
        "python3 /tmp/scratch/sd-push-timing.py 192.168.2.75 junk.mgt sd_push_meter.bin",
        "./sd-push.py 192.168.2.75 x.mgt",
        # i340: -m runs the MODULE — a pusher module fires, a stdlib one doesn't.
        "cd tools/trinload-push && python3 -m trinpush 192.168.2.75 f.bin 1 0x8000",
    ]

    def test_must_fire(self):
        for cmd in self.cases:
            with self.subTest(cmd=cmd):
                self.assertIsNotNone(guard.is_deploy(cmd), f"missed deploy: {cmd!r}")
                decision = run_main(cmd)
                self.assertIsNotNone(decision, f"main() allowed deploy: {cmd!r}")
                self.assertEqual(
                    decision["hookSpecificOutput"]["permissionDecision"], "deny"
                )


class MustAllowBypass(unittest.TestCase):
    """DEPLOY_CHECKED=1 makes a real deploy pass main() even though is_deploy fires."""

    cmd = (
        "DEPLOY_CHECKED=1 python3 tools/trinload-push/trinload-push.py "
        "192.168.2.75 foo.bin"
    )

    def test_bypass(self):
        # is_deploy still recognises it as a deploy (the env prefix is stripped)...
        self.assertIsNotNone(guard.is_deploy(self.cmd))
        self.assertTrue(guard.already_confirmed(self.cmd))
        # ...but main() lets it through (no decision emitted).
        self.assertIsNone(run_main(self.cmd))


class NonBashUntouched(unittest.TestCase):
    def test_non_bash_tool_ignored(self):
        payload = json.dumps(
            {"tool_name": "Write", "tool_input": {"command": "tftp 192.168.2.75"}}
        )
        out = io.StringIO()
        old = guard.sys.stdin
        guard.sys.stdin = io.StringIO(payload)
        try:
            with redirect_stdout(out):
                rc = guard.main()
        finally:
            guard.sys.stdin = old
        self.assertEqual(rc, 0)
        self.assertEqual(out.getvalue().strip(), "")


if __name__ == "__main__":
    unittest.main()
