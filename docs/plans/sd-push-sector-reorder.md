# sd_push `.mgt`→record sector reorder (i315)

Completing PR deletes this plan (doc-lifecycle). Tracks **i315**; resolves the
determination in **i294**.

## The finding (i294, resolved — do not re-derive)

A Trinity SD **record** and a **`.mgt`** file are two *container* serializations
of the same 800K SAM disk geometry. **They differ, and this is independent of the
DOS filesystem on the disk** (SAMDOS-2 / B-DOS / MasterDOS all behave identically —
it is the track/sector *layout*, not the files, that is reordered):

- **Record = side-major.** B-DOS 1.5t `conv.de` (`&A151`, the real-silicon read
  authority, rule 8): `record_LBA(track, side, sector) = side*800 + 10*track + (sector-1)`.
  Side 0 → LBA 0..799, side 1 → LBA 800..1599.
- **`.mgt` = track-major** (cylinder-interleaved, samfile):
  `mgt_offset(cyl, side, sec) = ((cyl*2 + side)*10 + (sec-1)) * 512`.

**Confirmed 3 independent ways:** (1) `conv.de` traced; (2) samdisk 3.x
`record.cpp:153-156` write loop (`for head { for cyl }` = side-major); (3) **live
samdisk 3.8.8** — writing a `.mgt` into a file-backed HDF record yields a
**1600/1600** side-major permutation, and samdisk's read-back round-trips to the
source (only the +232 stamp differs).

**The reorder is UNCONDITIONAL, DOS-agnostic, container-level.** Empirically: a
`BDOS`@232-stamped `.mgt` still reorders 1600/1600 and survives repeated
round-trips (the "+232 gates the reorder" hypothesis is FALSE). So the rule is
simple and safe: **always reorder a `.mgt`→record; never inspect the DOS; never
touch the user's file.**

**Why the current push fails to boot:** `sd_push` streams `.mgt` sectors
sequentially (track-major) to record LBAs. Track 0 / side 0 (the directory)
aligns at LBA 0..9 under *both* orders — so the record lists + selects and shows
its files — but side-1 and higher-track file data land at the wrong LBAs, so
booting the AUTO file reads scrambled sectors → **`108 End of file, 0:1`**.

## The fix (SAM-side — per Pete; the SAM owns the `.mgt`→record translation)

In `sd_push`'s write path, map each incoming `.mgt` sector index `m` (0..1599) to
its side-major record linear **before** `bd_record_write_hw`:

```
cyl  = m / 20            ; 0..79   (each cylinder = 20 sectors: side0 x10, side1 x10)
rem  = m % 20            ; 0..19
side = rem / 10          ; 0 or 1
sec1 = m % 10            ; 0..9    (== rem % 10)
record_linear = side*800 + cyl*10 + sec1     ; 0..1599, fits 16-bit
```

Set `BD_REC_WRITE_LINEAR = record_linear` (instead of `m`) before calling
`bd_record_write_hw`.

**Constraints:**
- **Do NOT** put the reorder inside the generic `bd_record_write_hw`
  (`sd_csd.asm:606`) — other callers must keep writing by literal LBA. The reorder
  is a `.mgt`-specific concern → it lives in the `sd_push` receive/write handler.
- **Do NOT** patch the user's `.mgt` host-side, and **do NOT** reorder the stream
  in `sd-push.py` for the durable fix — keep the wire protocol "stream track-major
  `.mgt` sectors in order"; the SAM performs the translation (this is the netboot
  vision: the SAM itself fetches + stores `.mgt` images, so the translation must
  live SAM-side, not in one host pusher).

**Z80 notes:**
- Delivery is windowed (`sd-push.py` `WINDOW=4`) and may retransmit, so sectors
  can arrive out of order — compute `record_linear` **from the received `m`**, not
  from an incrementing counter. This needs an unsigned 16-bit divide by 20 and by
  10 (or two `/10`s); `m ≤ 1599` so a small shift-subtract divide suffices.
- `side*800` (800 = `&320`) + `cyl*10` + `sec1`.

**Where to edit:** the `sd_push` receive handler that takes an `@`-block's
`linearSec` and drives `BD_REC_WRITE_REC`/`BD_REC_WRITE_LINEAR` →
`bd_record_write_hw` (`src/netboot/sd_push.asm`; symbols in
`src/netboot/sd_csd.asm` `bd_record_write_hw`, `bdos_seam.asm` `BD_REC_WRITE_*`).
Read those first to place the mapping precisely.

## The +232 "BDOS" stamp (separate + optional — not part of this boot fix)

samdisk `--fix` writes `"BDOS"` (`42 44 4F 53`) at offset +232 of the record
body's first sector (the B-DOS "formatted" signature). It is **not required to
boot** (a real working record, "Comet", has none) and is orthogonal to the
reorder. #773 already handles record registration + card-derived base. Whether
`sd_push` should also stamp +232 (for `RECORD`-list recognition) is a separate
decision; leave it out of the boot fix unless a concrete need appears.

## Validation

1. **Emulation (agent, CI):** extend the netboot Z80 harness — push a `.mgt`
   through `sd_push` and assert the written record body equals the side-major
   permutation of the source (mirror the samdisk `card.hdf` check: 1600/1600, +
   the LBA-0 directory sector aligned).
2. **Hardware (Pete-gated shared-card write — i295 family):** push a side-major
   record + boot it on the SAM → CJ's Elephant. The real-silicon proof.

**Optional fast proof (before the SAM-side work):** a ~3-line host-side *stream*
reorder in `sd-push.py` (send the `@`-block with `linearSec = record_linear`
instead of `m`) is tool-side (not touching the user's file) and lets us confirm
the boot on hardware immediately, decoupled from the Z80 change. Use it to
de-risk, then implement the durable SAM-side reorder.

## References

- `conv.de`: `~/sam-archive/bdos/analysis/bdos15t-beta6.annotated.dis:4657-4677`
  (traced: `conv.de(t0,s0,sec1)=0`, `(t1,s0,sec1)=10`, `(t0,s1,sec1)=800`).
- `.mgt` layout: `~/git/samfile/samfile.go:10-11` ("cylinder-interleaved").
- samdisk 3.x reorder loop: `~/git/samdisk/src/types/record.cpp:153-156`.
- Empirical (Pete's Mac, samdisk 3.8.8): `.mgt`→HDF record = 1600/1600 side-major;
  `BDOS`-stamped `.mgt` also 1600/1600; second round-trip byte-identical.
- Related: i294 (finding), i295 (on-SAM shared-card write gate), #773 (create-record base).
