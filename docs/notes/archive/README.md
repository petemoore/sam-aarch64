# Archived investigation notes

These docs describe theories explored during the M0 boot-crash
investigation that **did not pan out**. Kept for forensic interest only
— do not rely on their conclusions.

The actual M0 root cause turned out to be three bugs in
`tools/build-disk.sh`, all empirically verified and now fixed:

1. Body header bytes 5-6 = `0x00 0x00` (should be `0xFF 0xFF`) on
   stub/IN, causing ROM `LOAD CODE` to take the auto-exec path and JP
   to garbage.
2. Spurious `0x20` bytes after each BASIC keyword token, which SAM
   BASIC stores without trailing spaces (LIST adds them at render
   time).
3. **The big one**: BASIC `auto` body had no vars/gap allocation, so
   the dir-entry triplets at `0xDD/0xE0/0xE3` all pointed to PROG+52
   (zero-sized vars area), and CLEAR walked off the end of the
   program into junk. Canonical SAM SAVE allocates `vars + gap = 604`
   bytes after PROG (94% of disks).

See current authoritative references:
- `../sam-basic-save-format.md` — vars/gap invariant, ROM citations
- `../test-mgt-byte-layout.md` — byte-by-byte map of `build/test.mgt`
- `../../tools/build-disk.sh` — emitter

## Files in this archive

| File                         | Reason archived                                                            |
|------------------------------|----------------------------------------------------------------------------|
| `clear-investigation.md`     | MCLS-page-collision hypothesis. Plausible but unconfirmed; not the cause.  |
| `clear-mechanism.md`         | Off-by-one fix at build-disk.sh:240. Empirically refuted.                  |
| `clear-actual-mechanism.md`  | Agent's CLRSR trace. §2-§4 verified inert; §5 mechanism refuted.           |
| `clear-remaining-diff.md`    | 0xDC/0xE6-0xE7 byte differences. §2-§4 facts verified inert.               |
| `simcoupe-batch.md`          | M0 Task-1 spike. Superseded by `../headless-simcoupe.md`.                  |
| `sam-file-io.md`             | HOFLE/SBYT/CFSM claims wrong per `sam-stub-audit.md` audit.                |

If a fact from any of these docs proves load-bearing for future work,
extract it into a current reference doc with proper citations rather
than reviving the archived file.
