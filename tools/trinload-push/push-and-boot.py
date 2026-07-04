#!/usr/bin/env python3
"""Push a .mgt into a free Trinity SD record and BOOT it — one command (i284).

The clean, repeatable one-command demo of the now-working network SD write: it
chains the two hardware-proven primitives so the record number never has to be
copied by hand.

  1. sd-push.py's push_mgt streams the .mgt into the FIRST FREE record via
     sd_push (own-CMD24 write, the record claimed + its "BDOS" stamp written) and
     REPORTS which record it claimed (the finalize 'D'+record reply, i308).
  2. boot-record.py boots exactly that record (HRECORD-select + ALHK fire) so the
     SAM loads and runs the disk's AUTO* file.

Both legs are hardware-proven independently — sd_push wrote a cj.mgt record that
booted CJ's Elephant on the real SAM (i295/#784), and boot-record booted a stored
record that TFTP-self-confirmed on the wire (i319b campaign). This wrapper only
adds the orchestration: thread the claimed record from the push into the boot.

BOOTABILITY PRE-CHECK (fail fast): before spending the ~2-minute push, the .mgt's
directory is parsed for the AUTO* file B-DOS's record boot will run, and the push
is REFUSED (exit 1; --force overrides) on the two known will-not-boot shapes
boot-record.py guards — the i331 stack overlap and the i332 BASIC-auto livelock —
so a disk that cannot boot never claims a record. The boot leg re-applies the same
guard as the authority.

DATA SAFETY (the Trinity SD card is a SHARED user resource,
trinity_storage_shared_resource): sd_push writes ONLY the first free record and
never targets a used one. Still, treat every push with care.

Usage:
    push-and-boot.py <sam-ip> <mgt-path> [options]

Options:
    --no-boot            push only; print the claimed record and stop (boot later
                         with boot-record.py <sam-ip> <record>)
    --force              push/boot even when the pre-check reports a will-not-boot
                         shape
    --sd-push-bin PATH   sd_push program (default build/sd_push.bin)
    --boot-bin PATH      boot_record program (default build/boot_record.bin)
    --boot-map PATH      boot_record pyz80 mapfile (default: --boot-bin with .map)
    --page N             TrinLoad target page for both programs (default 1)

Exit codes: 0 = pushed and booted (or pushed, with --no-boot); 1 = failure or a
refused will-not-boot disk; 3 = the push very likely completed but its finalize
reply was lost, so the claimed record is unknown and the boot could not be driven
automatically — verify with list-records.py and boot with boot-record.py (i335).
"""
import argparse
import importlib.util
import os
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
# The sibling scripts do `from trinpush import …`; make that resolve no matter the
# caller's cwd (running by path puts _HERE on sys.path[0], but an importlib load
# from elsewhere would not).
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)


def _load(filename, module_name):
    """Import a dash-named sibling script (not a legal `import` name) as a module."""
    path = os.path.join(_HERE, filename)
    spec = importlib.util.spec_from_file_location(module_name, path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


sd_push = _load("sd-push.py", "sd_push_launcher")
boot_record = _load("boot-record.py", "boot_record_launcher")


def run(sam, mgt_path, *, no_boot=False, force=False,
        sd_push_bin="build/sd_push.bin", boot_bin="build/boot_record.bin",
        boot_map=None, page=1):
    """Push `mgt_path` to a free record and boot it. Returns a process exit code."""
    # Fail-fast bootability pre-check — do not spend a ~2-minute push on a disk
    # whose AUTO* file cannot boot (i331/i332). boot-record.py is the authority;
    # we reuse its parser + hazard classifier.
    hazard = boot_record.boot_hazard(
        boot_record.parse_mgt_auto_entry(open(mgt_path, "rb").read()))
    if hazard is not None and not no_boot:
        if not force:
            print(f"REFUSED: {hazard} (--force to push/boot anyway)")
            return 1
        print(f"WARNING (--force): {hazard}")

    code, record = sd_push.push_mgt(sam, mgt_path, sd_push_bin)
    if code == 1:
        print("push failed — not booting")
        return 1
    if record is None:
        # code 0 (older binary) or 3 (lost finalize reply): the push likely
        # landed but we don't know the record, so we cannot auto-boot.
        print("push completed but sd_push did not report the claimed record — "
              "cannot auto-boot; run list-records.py to find it, then "
              f"boot-record.py {sam} <record>")
        return code or 3

    print(f"pushed to record {record}")
    if no_boot:
        print(f"--no-boot: stopping (boot later with boot-record.py {sam} {record})")
        return 0

    # Boot exactly the record just written. boot-record.py's main() re-applies the
    # i331/i332 guard via --image (the authority) and drives the boot; a
    # power-cycle is NOT needed between push and boot because boot_record is a
    # fresh trinload-push, and sd_push has RET'd back to trinload after finalize.
    boot_argv = [sam, str(record), "--image", mgt_path, "--bin", boot_bin,
                 "--page", str(page)]
    if boot_map is not None:
        boot_argv += ["--map", boot_map]
    if force:
        boot_argv.append("--force")
    return boot_record.main(boot_argv)


def main(argv):
    ap = argparse.ArgumentParser(
        description="push a .mgt to a free Trinity SD record and boot it (one command)")
    ap.add_argument("sam", help="the SAM's IP address (TrinLoad must be running)")
    ap.add_argument("mgt", help="the .mgt disk image to push (819200 B = a full record)")
    ap.add_argument("--no-boot", action="store_true",
                    help="push only; print the claimed record and stop")
    ap.add_argument("--force", action="store_true",
                    help="push/boot even on a known will-not-boot shape (i331/i332)")
    ap.add_argument("--sd-push-bin", default="build/sd_push.bin",
                    help="the sd_push program (default build/sd_push.bin)")
    ap.add_argument("--boot-bin", default="build/boot_record.bin",
                    help="the boot_record program (default build/boot_record.bin)")
    ap.add_argument("--boot-map", default=None,
                    help="the boot_record pyz80 mapfile (default: --boot-bin with .map)")
    ap.add_argument("--page", type=int, default=1,
                    help="TrinLoad target page for both programs (default 1)")
    args = ap.parse_args(argv)
    return run(args.sam, args.mgt, no_boot=args.no_boot, force=args.force,
               sd_push_bin=args.sd_push_bin, boot_bin=args.boot_bin,
               boot_map=args.boot_map, page=args.page)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
