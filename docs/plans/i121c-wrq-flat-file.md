# i121c — WRQ flat-file archive store (the default storage class)

**Goal.** Implement the **default flat-file archive** WRQ store path (manifest design
§6.5 / decision 5; Pete 2026-06-18). A `tftp put NAME` whose filename has **no**
`trinity-sam-disks/` prefix is classified `FlatFile` (`bdos.Classify`) and stored as a
plain file — HSAVE'd into a free Trinity record via the i119 `bdos_save_hook` seam —
**not** validated as an 819,200-byte disk record. The `trinity-sam-disks/`-prefixed
disk-record push (i121f/g/h) is unchanged.

This closes the last gap in i121: today **both** the Go authority (`serve.go`) and the
Z80 (`netboot_serve.asm`) treat *every* WRQ as a disk-record push, so a flat file (not
exactly 819,200 bytes) is **rejected** with `ERROR(3, "invalid disk record")`. After
this item a non-prefixed push is accepted and HSAVE'd.

## The prefix becomes significant (the §6.5 default flip)
i121c isn't only additive — it realizes §6.5's classification: the **default flips to
flat-file**, and the disk-record push now **requires** the `trinity-sam-disks/` prefix
(the asm comments already anticipate exactly this: "the future i121c flat-file path uses
the prefix as the discriminator"). Consequences, all in this one cohesive change:
- The disk-record *storage* path (validate → claim → sectors) is **byte-identical** —
  only the routing-by-class changes — so the SD-write path stays as hardware-validated;
  the emulation gate (`TestServeDiskPushTrinloadDeployable`) re-runs it via a prefixed
  name.
- **Existing disk-record tests** (Go `serve_test.go` + Z80
  `netboot_serve_wrq_record_test.go`) push non-prefixed names and relied on the v1
  "every WRQ = disk-record" placeholder → they move to `trinity-sam-disks/…` names.
- **Deploy tooling** (`tools/trinload-push/trinpush.py`) prepends `trinity-sam-disks/`
  for disk-image pushes, so Pete's `trinpush <image>` workflow keeps producing disk
  records without typing the prefix; a stock `tftp put X` correctly defaults to flat.
  The hardware push uses the prefix from now on (the tool supplies it).

**Scope (single-record).** The registry row scopes i121c to a single-record HSAVE,
mirroring i119's `TestClientE2EConfirm`. The file is bounded by the `WRQ_STAGING`
window (`&C000`, the section-D 16 KB RAM staging window the dumper/i121d already page
in). Spanning a large flat file across records (manifest §3, hash-derived names) is
**out of scope** — that is i114/manifest territory. A flat file too big for the staging
window is a future concern, not this item.

## The pieces that already exist (reuse, don't reinvent)
- **Classifier:** `bdos.Classify(name)` (Go, `bdos/storage.go`) and
  `bdos_strip_disk_prefix` (Z80, `bdos_seam.asm:910`) — both detect/strip the
  `trinity-sam-disks/` prefix. `bdos_strip_disk_prefix` returns CY = prefix-present.
- **Flat receive-to-staging:** sink mode 0 (`WRQ_SINK_MODE=0`) — `handle_data`'s
  `wd_next_block` mode-0 branch accumulates into `WRQ_STAGING` and advances
  `WRQ_STAGE_OFFSET`. Compiled into **both** builds (only the mode-1 raw sink is
  bootable-gated). This is the i121b host-verified path; the bootable build just never
  selects it today (the claim always sets mode 1).
- **HSAVE-of-staged-bytes:** `client_main` (`netboot_client.asm:809-821`) — the exact
  pattern: `BD_NAME_PTR` ← filename, `BD_SAVE_PAGE` ← `STAGING>>14`, `BD_SAVE_ADDR` ←
  `STAGING&&3FFF|&8000`, `BD_SAVE_SIZE` ← `STAGE_OFFSET`, `bdos_fill_save_uifa`,
  `bdos_save_hook`. The emulator captures the save by `readPhysical(Page&0x1F,
  Addr&0x3FFF, Size)` (`bdos_store.go:370`), an identity read of `&C000` for
  `WRQ_STAGING` — so the same formula works for `WRQ_STAGING=&C000`.
- **Record claim:** `bdos_claim_record` (writes the record-list name entry so the next
  push lands elsewhere) — same call the disk-record `wd_finalize` makes.
- **Free-record find/select:** `wrq_claim_record` → `bdos_find_record_for_strategy` +
  `bdos_select_record`. Both classes need a free record; the no-free → `ERROR(3, "no
  free record")` handshake is shared.

## Go authority — `tools/netboot-oracle/serve/serve.go`
1. `startWrite`: classify `req.Filename` with `bdos.Classify`; store the class on
   `wrqReceiver` (`flat bool`). The no-free-record `ERROR(3)` check
   (`r.cfg.DiskRecordPush && nextRecordForStrategy()==0`) stays — flat files need a free
   record too.
2. `finalizePush`: branch on `rcv.flat`:
   - `flat`: **no** `ValidateDiskRecord`; record the flat store outcome (name from
     `bdos.Classify`'s internal name + the staged `rcv.data`), claim the record
     (`nextRecordForStrategy`, append a `Claim`, `removeFreeRecord`), reply final `ACK`.
   - disk-record: unchanged (validate → ACK / ERROR(3)).
3. Expose the flat outcome for the test (e.g. extend `WRQPushOutcome` or add a
   `FlatStores()` accessor) — name + bytes.

## Z80 — `src/netboot/netboot_serve.asm`
1. **State:** add `WRQ_FLAT_MODE: defs 1` (1 = flat-file push).
2. `handle_wrq` (after the `tftp.done` check, before `wrq_claim_record`): call
   `bdos_strip_disk_prefix` on `(PARSE_FILENAME)`; CY clear (no prefix) ⇒ flat. Set
   `WRQ_FLAT_MODE` accordingly. Both classes claim a free record. Refactor
   `wrq_claim_record` so it does **find + select only** (returns CY); the caller then
   arms the sink: flat ⇒ `WRQ_SINK_MODE=0` (page the RAM staging window into `&C000`
   like the dumper, then flat-accumulate); disk-record ⇒ `raw_record_sink_reset` +
   `WRQ_SINK_MODE=1`.
3. **Final block** (`handle_data`, the mode dispatch at `:825`): if `WRQ_FLAT_MODE` ⇒
   `jp wd_finalize_flat` (bootable-only); else the existing `WRQ_SINK_MODE` dispatch.
4. **`wd_finalize_flat`** (bootable-only, mirrors `client_main:809-821`):
   `BD_NAME_PTR`←`(PARSE_FILENAME)`, `BD_SAVE_PAGE`←`WRQ_STAGING>>14`,
   `BD_SAVE_ADDR`←`WRQ_STAGING&&3FFF|&8000`, `BD_SAVE_SIZE`←`(WRQ_STAGE_OFFSET)`,
   `bdos_fill_save_uifa`, `bdos_save_hook`; then `BD_CLAIM_NAME_PTR`←`(PARSE_FILENAME)`,
   `bdos_claim_record`; `serve_rearm_enc`; `ld de,(WRQ_ACKED)` → `jp wd_send_ack`.
   Note `bdos_name_to_uifa`/`bdos_build_claim_entry` already strip the prefix + dotted
   suffix, so passing the raw filename is correct.
5. **Paging:** the flat staging window at `&C000` — page the same RAM page the dumper
   uses (i188 LMPR/HMPR save/restore) before accumulating, restore before the seam
   HSAVE if needed. Mirror the dumper exactly; do not invent paging.

## Tests
- **Go** (`serve/serve_test.go`): a flat-file WRQ E2E — push a small non-prefixed file,
  assert final ACK (not ERROR), the flat store captured (name + bytes), the record
  claimed. All-full ⇒ `ERROR(3, "no free record")`, nothing stored.
- **Z80** (`z80/netboot_serve_wrq_record_test.go`): `TestServeWRQFlatFilePush` mirroring
  `TestClientE2EConfirm` — inject a non-prefixed WRQ + DATA, drive the dispatch, assert
  the `BDOSStore` HSAVE: `Selected()`==free record, `Saves()[0].Name`==filename,
  `Saves()[0]` bytes == the pushed file; final reply ACK. `TestServeWRQFlatFileNoFree`
  — all-full ⇒ `ERROR(3)` + zero HSAVEs. Compare frames vs the Go authority (`eqFrame`).

## Verification line (CLAUDE.md §5 / §7)
Host-verifiable now: the classify, the flat-accumulate, the UIFA field arithmetic, the
claim, the frame cadence (all in the Go harness + the boot-path harness test). The real
`RST 8` HSAVE persist + the `&C000` runtime paging on a live Trinity stay
**hardware-gated** — the same gate as i119. Emulation-verified ≠ hardware-verified.

## Done when
Go harness green (flat + disk-record + no-free), the boot-path harness test green,
SimCoupé boot self-tests pass, `make registry-sync-check` clean. PR; §3 review; merge.
Delete this plan in the completing PR.
