# Netboot storage: the serve manifest + record-allocation strategy

**Status:** design captured 2026-06-18 (Pete). Implementation is **hardware-gated** —
the real `RST 8` B-DOS persist/serve dispatch is the live-Trinity gate (CLAUDE.md §5,
same gate as q16/i93/i95). Four buildable sub-decisions are open (§6, tracked as **q26**).

This refines the serve-time *name → record* resolution that **i93** (the B-DOS storage
seam) left as a hand-wavy "store walk", and pairs with the **q16/i99** record-spanning
persist. It feeds **i95** (how the integrated server resolves an RRQ on real hardware),
**i70/i100** provisioning (the fetch *writes* the manifest), and the **i101** capstone.

## 1. Why a manifest at all

The TFTP server serves files *by name*, but the served names and B-DOS's storage names
cannot share one namespace:

- **B-DOS filenames are a 10-char field** (`OffName=1, 10 bytes`, plus a 4-char extension
  that CODE files leave blank), **flat — no nested directories** — and a restricted
  charset, nothing like ext4 / FAT32 / case-sensitive long names (`bdos.go`: *"longer
  names are not representable, matching the SAM directory"*).
- **Real Pi netboot names break that:** `bootcode.bin` (12), `kernel8.img` (11),
  `bcm2711-rpi-400.dtb` (19), `overlays/<x>.dtbo` (a path), and the boot ROM's
  `<serial>/start4.elf` probe (a path). Even the pinned six include names > 10 chars.
- **Multi-MB files span records** (q16), so one served name already maps to *several*
  storage records.

A **manifest** decouples the two namespaces: the served name is the manifest key (full
length, full charset, `/`-paths and all); the storage records are **internal identifiers
the provisioner chooses** (safe 10-char tokens), so B-DOS's naming limits never constrain
the served namespace. It also removes two serve-time costs — **no directory scan** to find
a file, **no size-summing** to compute the total — both are manifest fields.

## 2. Where the manifest lives — the boot-disk-local convention

The Trinity has **no auto-boot-to-a-record**: you power on (B-DOS loads from the EEPROM),
`RECORD n` to select the record (it becomes the default drive, presented as drive 2), then
`BOOT` / F9. So when the server's `AUTO` `LOAD`s and `CALL`s the netboot CODE, **the record
you selected is already the current default** — every load came from it. The server reads
its manifest as **an ordinary file on its own boot record**: no global locator, no scan.

This beats a fixed record number because users drop the server into whatever spare slot
they have, and *"the record I booted from"* is correct on every card.

- **Launch flow:** read the manifest fully into RAM once (a few KB for ~10–30 entries),
  *then* `HRECORD` freely to serve file blobs from the records it points to (switching
  records never loses the in-RAM index).
- **If no manifest is on the boot record:** prompt *"Manifest record?"* (the editor-era
  one-line status prompt) and read it from the named record — which enables a **reusable
  server disk** (server only) pointed at per-project data records.

## 3. Manifest entries — local vs remote

The firmware can't all live on the single ~800 KB boot record (`start4.elf` ≈ 2.2 MB), so
**by default the served file blobs go to their own free records** and the manifest points
outward. Each entry is one of:

- **local** — the blob is a B-DOS file *on the boot disk itself*; the entry carries just
  the B-DOS filename. (Small files — `config.txt`, the DTBs — can ride along for free.)
- **remote** — the blob spans record(s) elsewhere; the entry carries the **record
  locator(s)** + span count + total size [+ optional SHA-256].

Resolution is an **exact string match** of the RRQ filename against manifest keys → the
record list + size → straight into the existing `Resolve` / `SpanPlan` path. Paths
(`overlays/…`, `<serial>/…`) are just keys; B-DOS stays flat, and the manifest *is* the
"global directory that accepts paths."

## 4. The storage-allocation strategy (persisted in the manifest header)

Where new blobs get written is an **explicit, user-chosen policy**, configured once and
saved in a **manifest header (front section)**, with a reasonable default. This gives the
user control instead of the system silently colonising records:

- **(s1) first-free** — pack into records the server has already claimed (reuse leftover
  space: *"this file needs 10 KB and a claimed record has room"*), then the lowest free
  record.
- **(s2) fixed list** — a hand-maintained set of records; **overflow WARNS, never steals**
  an unlisted record.
- **(s3) highest-free** — like s1 but prefers high record numbers, so the user keeps low
  slots for their own favourite disks (games, etc.) rather than having TFTP storage
  colonise them.
- **(s…)** extensible.

Invariants: **overflow is an explicit warning, never a silent record-steal**; the header
records **whether the manifest's own disk is in the storage pool**; resuming a session
reuses already-claimed space (reboot-then-add-a-file packs into existing records, not a
fresh one).

## 5. Patterns this one mechanism supports

- **All-in-one** — server + manifest + small served files on one record (could even be
  drive 1, the floppy).
- **Self-contained project disk** — server + manifest on the boot record; big files span
  out to other records.
- **Reusable server disk** — server only; prompts for the manifest record (§2 fallback).
- **3-way split** — separate records for server / manifest / file storage.
- **Multi-project** — per-project manifests on per-project records: the *same* TFTP name
  (`kernel8.img`) resolves to a *different* blob per project, with **no naming gymnastics**
  (the B-DOS record names are internal). **Shared files by reference** — different
  manifests point their `start4.elf` entry at the *same* span records, so shared firmware
  isn't duplicated. (64 GB makes duplication harmless too, but reference-sharing means a
  firmware update touches one place.)

## 6. Open decisions (q26 — Pete's call; defaults proposed so the doc is buildable)

1. **Manifest encoding** — *proposed: human-editable line-based text* (small file set;
   debuggable; hand-editable; fits the editor-era ethos). Alt: packed binary (smaller,
   faster to parse, but opaque).
2. **Remote locator key** — *proposed: both* — record **number** as the primary key (the
   provisioner wrote it and knows it) + an optional record **name/label** (portable across
   card copies, and what `samdisk list` / `DIR` shows a human).
3. **Internal span-record naming** — *proposed: sequential tokens* (`fw000`, `fw001`, …)
   chosen by the provisioner. With a manifest these are never user-visible, so the existing
   7-char-prefix `SpanRecordName` truncation (and its collision risk for the broader file
   set) is no longer needed. Alt: hash-derived names (content-addressed → natural dedup of
   identical blobs).
4. **Default storage strategy** — *proposed: s3 highest-free* (respects the user's existing
   low-numbered disks, per Pete's reasoning). Alt: s1 first-free (denser packing).

## 7. Verification line (CLAUDE.md §5)

Host-verifiable now: the manifest parse + the exact-match resolve + the `SpanPlan`
arithmetic + the allocation-strategy record selection (pure logic over a modelled store).
**Hardware-gated:** the real `RST 8` `HRECORD` / `HSAVE` / `HLOAD` per record on a live
Trinity — the same gate as q16 / i93 / i95. Emulation-verified ≠ hardware-verified.

## 8. Relationship to existing work

- Refines **i93** (the seam's serve-time "store walk").
- Pairs with **q16 / i99** (record-spanning persist + streaming sink) — the spanning
  convention becomes an internal detail under the manifest.
- Feeds **i95** (how the integrated server resolves an RRQ on real hardware) and **i101**
  (the capstone's serve phase).
- **i70 / i100** provisioning **writes** the manifest as its final act — it already knows
  each file's name, size, SHA-256, and the records it wrote them to.
