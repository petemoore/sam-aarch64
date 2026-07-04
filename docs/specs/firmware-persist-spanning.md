# Firmware persist spanning — reconciling record-spanning with B-DOS directory capacity

**Item:** i70c · **Type:** design reconciliation · **Status:** design proposed,
awaiting Pete sign-off (spec gate) — see **q74 / §7**.

This spec reconciles the firmware record-spanning convention
([`phase3-delivery-design.md` §8](phase3-delivery-design.md)) with the physical
capacity of a Trinity SD record's B-DOS directory, so that a multi-MB firmware
blob (a Raspberry Pi `start.elf` ≈ 3 MB) can be persisted to the shared SD card
without exhausting records or the directory. It is the design gate the i70c item
names ("a persist design reconciliation FIRST") before any multi-MB persist is
built. Everything here is grounded in the two authorities cited inline; nothing is
guessed.

## 1. The two "record" concepts (do not conflate)

A Trinity SD card holds two nested structures, both loosely called "records":

- **Trinity SD record** — one whole ~800 KB SAM floppy image (1600 sectors × 512 B
  = 819,200 B; `sd_csd.asm:384`, `storage.go:27`). A card holds up to 65,536 of
  them, counted from the CSD into `BD_RECORDS` (`sd_csd.asm:1172-1231`). Records
  are enumerated and allocated through the **central record LIST** (16-byte
  entries, 32 per sector, in card sectors `1..base-1`;
  `trinity-record-detection-design.md:152-196`) by `bdos_find_free_record` /
  `bdos_select_record` (HRECORD) / `bdos_claim_record`
  (`bdos_seam.asm:382-390,639-681,779-834`).

- **B-DOS directory slot (a file)** — inside a record's own 800 KB body, the first
  4 tracks (tracks 0–3 = 40 sectors, 2 × 256-byte entries per sector) form a
  directory that holds **at most 80 files**
  (`sam-coupe_tech-man_v3-0.txt:4340-4359`; `http_main.asm:713-734`). Filenames
  are 10 chars (`bdos.go:40`). File data allocates from track 4 onward, leaving
  ~780 KB usable per record. HSAVE (RST 8 hook 132) creates one directory entry +
  its data in the currently HRECORD-selected record (`bdos_seam.asm:409-418`).

A record is accepted by HRECORD **iff** its own first directory sector carries the
4-byte `"BDOS"` stamp at offset **+232** (`http_main.asm:711-785`,
`bdos_seam.asm:250-280`) — a body stamp, *not* a LIST field. A genuinely free
record lacks it, which is why the store leaf formats a record before saving into
it (i357).

## 2. What the code does today, and why it does not scale

The streaming HTTP fetch cannot hold a multi-MB body in RAM: the bootable is a
32 KB overlay (`&8000..&10000`), so the flush buffer spares only ~8 KB and the
per-window cap is `FW_RECORD_CAP = 6144` (`http_main.asm:305-330`). Each flushed
window is one HSAVE. That part is forced by RAM and is not in question.

The problem is **placement**. `storage_sink_leaf` HSAVEs window *N* into a
**separate Trinity SD record** `FW_BASE_RECORD + FW_REC_IDX`
(`http_main.asm:683-692`), one file per record:

```
                ld      hl, (FW_BASE_RECORD)   ; first free record, per file
                ld      de, (FW_REC_IDX)       ; per-window index, 0-based
                add     hl, de                 ; record = base + window index
                ld      a, l
                call    bdos_select_record     ; HRECORD-select that whole record
                call    bdos_save_hook         ; HSAVE the 6144-byte window into it
```

So `start.elf` (fixture size 2,979,296 B, `span_test.go:28`) → `ceil(2979296 /
6144) = 485` windows → **485 whole 800 KB records**, each holding a single
6144-byte file. Two failures follow:

1. **Space blow-up.** 485 records × 800 KB = ~388 MB of card consumed for 2.98 MB
   of data (≈ 0.8 % efficiency). The full 6-file firmware set (~10–15 MB) would
   need ~1,700+ records — exhausting small cards outright, and a single file's
   485-record band is close to the 3-digit-index 1000-record ceiling
   (`span.go:32-33`).
2. **Incomplete bookkeeping.** `store_format_record` runs only on window 0
   (`http_main.asm:632-635`) and `store_end` claims only `FW_BASE_RECORD`
   (`http_main.asm:586-599`) — both explicitly flagged as "the i70c
   record-spanning reconciliation" (`http_main.asm:569-571,626-629`). Windows
   1..N land in unformatted, unclaimed records.

The Go authority does not settle this: `SpanPlan` / `SpanRecordName`
(`span.go:52-97`) split **purely by byte cap** with **no directory-slot model**
(only the 3-digit index caps a blob at 1000 records). Placement is therefore a
genuinely new design choice, not a mechanical port (CLAUDE.md §6 does not apply).

## 3. The option space (as the i70c item framed it)

The reconciliation is fundamentally: *many small windows must be persisted; where
do they go so a multi-MB blob fits the card sanely?*

### Option A — separate record per window (today). Rejected.
The current code (§2). Simplest to write, catastrophic on space, and pushes the
1000-record ceiling. Not viable for multi-MB.

### Option B — pack up to 80 windows per record; advance records as directories fill. **Recommended.**
Keep the 6144 RAM window. HSAVE consecutive windows as files
`<hash6>000`, `<hash6>001`, … into the **same** HRECORD-selected record until its
80-slot directory is full, then advance to the next first-free record and format
it. A record holds 80 × 6144 = 480 KB (directory-slot-bound; well inside the
~780 KB data area). `start.elf` → 485 windows → `ceil(485/80) = 7` records
(6 full + 1 partial); the whole firmware set → ~25–30 records. Efficiency ≈ 60 %
per record — irrelevant when a card holds thousands of records.

- **Cost:** placement + bookkeeping only (record-advance, format-per-record,
  claim-per-record). No change to the hot receive path, no paging, no push on the
  65535 HSAVE ceiling.
- **Risk:** low. Sequential HSAVEs into one record are the ordinary SAMDOS/MGT
  model — each HSAVE allocates the next free sectors from the directory's existing
  entries (there is no separate free bitmap; allocation hard-starts at track 4,
  i357), so 80 back-to-back saves into one formatted record Just Work.

### Option C — raise the per-file window by paging the flush buffer. Rejected (for now).
Move the flush buffer out of the 32 KB overlay into a dedicated physical page so
the window can grow toward the 65535 HSAVE ceiling. At a ~64 KB window `start.elf`
→ 46 files → ~4 records (≈ 93 % efficiency). But it adds paging churn to the
**hottest, most timing-sensitive code** (the ENC receive + per-window SHA-256
loop — the exact code whose fragility drove the ENC-RX-re-establish saga), and a
64 KB HSAVE source spans 4 physical pages that must all be resident. The
efficiency win is real but buys nothing the card needs, at material correctness
risk. Not worth it while records are abundant. (Kept on record as the lever to
pull if a future target is genuinely record-starved.)

**Recommendation: Option B.** Lowest risk, adequate on every realistic card,
leaves the delicate receive path untouched, and is a self-contained change to
placement/bookkeeping on both the persist and serve sides.

## 4. Option B — the design in detail

### 4.1 Go authority first (encode, then port — CLAUDE.md rule 6)
`SpanPlan` stays byte-cap-only (it is correct as the byte-range plan). Add a thin
**placement** layer over it — proposed `bdos.PlacementPlan(baseRecord, size, cap,
filesPerRecord)` — that maps each span index *i* to `(record, name)`:

```
record = baseRecord + i / filesPerRecord      // integer division
name   = SpanRecordName(hash, i)              // unchanged <hash6><NNN>
```

with `filesPerRecord = 80` (a named constant, not a magic literal — the physical
directory bound; a conservative value ≤ 80 is allowed). Host-verify it the same
way `span_test.go` verifies the arithmetic: assert the index→record mapping,
the record count `ceil(spanCount / filesPerRecord)`, and that names stay
`SpanRecordName(hash, i)`.

### 4.2 Z80 `storage_sink_leaf` change
The filename stays `<hash6><NNN>` with `NNN = FW_REC_IDX` (globally unique per
file; 000..484 for `start.elf`, still under 1000). Only the **record selection**
changes from `base + FW_REC_IDX` to `base + FW_REC_IDX / FILES_PER_RECORD`:

- On the **first window of each fresh record** (`FW_REC_IDX mod FILES_PER_RECORD
  == 0`), `store_format_record` that record (blank tracks 0–3 + stamp "BDOS" via
  the proven raw-CMD24 write) — extending today's format-only-window-0 to
  format-per-record.
- HRECORD-select `base + FW_REC_IDX / FILES_PER_RECORD`, then HSAVE — the file
  lands in that record's directory (slot `FW_REC_IDX mod FILES_PER_RECORD`).
- `FW_REC_IDX` still increments per window.

`FILES_PER_RECORD` (80) is a build-time constant paired with `FW_RECORD_CAP`.

### 4.3 Claim bookkeeping
`store_end` must **claim every record the file spans**, not just `FW_BASE_RECORD`
(the current single-record claim, `http_main.asm:586-599`). Claim records
`base .. base + (spanCount-1)/FILES_PER_RECORD`. Each claimed record's LIST name
is content-addressed from the file's pinned hash (reuse `fw_span_record_name` with
the record's first index, or a per-record LIST name — an implementation detail to
pin in i70c-b2). This is what makes the *next* file's `bdos_find_free_record`
advance past the whole band (extends i360b's single-record claim).

### 4.4 Serve side
The manifest already carries an explicit per-span-index `Locator.Record`
(`manifest.go:24-30,67-82`), and `ServePlan` zips each span byte-range with its
locator (`manifest.go:294-315`). Option B needs **no format change**: the persist
side records the actual per-index record (repeating each record number
`FILES_PER_RECORD` times), and the server HRECORD-selects that record and HLOADs
`<hash6><NNN>` by name — reading across records in ascending index order and
concatenating, exactly as `ServePlan` already specifies. Alternatively the
mapping can be made **deterministic** (serve computes `record = base + i /
filesPerRecord` from a base + count in the manifest) to shrink the manifest — a
size optimisation to decide in i70c-b2, not a blocker.

The `ServePlan` cap-agreement guard (`manifest.go:303-304`, errors if `recordCap`
implies a record count ≠ the manifest span) stays — persist and serve must pin the
**same** `FW_RECORD_CAP` **and** `FILES_PER_RECORD`.

### 4.5 `FW_RECORD_CAP` stays 6144
No change. Option B does not touch the RAM window; it only changes where windows
are placed. The two deterministic bounds on the cap (the 16-bit HSAVE ceiling and
the 32 KB overlay budget, `http_main.asm:305-329`) are unaffected.

## 5. i347 IM2 exposure for the long fetch (assessment)
The full 6-file fetch is a 10–18+ min run dominated by the Z80 SHA-256 hashing
each window in the receive loop. `http_main` runs under `di` from entry
(`http_main.asm:343`) and the ENC receive is **polled, not interrupt-driven**, so
there is no IM2 exposure *during the fetch itself* — a long `di` is fine for the
networking path. The IM2 concern tracked by **i347** is a **serve-vessel**
property (the server must service line/frame interrupts while serving), not a
persist-path one. This spec therefore does not change the fetch's interrupt
posture; i347 remains the home for the serve-side IM2 work. (Recorded here so the
i70c item's "assess the i347 exposure" is discharged: on the persist side, none.)

## 6. Storage-adequacy summary (Option B)
| blob | size | windows (÷6144) | records (÷80) |
|------|------|-----------------|----------------|
| start.elf | 2.98 MB | 485 | 7 |
| a full 6-file set | ~10–15 MB | ~1700–2500 | ~22–32 |

Trivial on any card that holds thousands of records; a single blob's record band
stays far under the 1000-record index ceiling.

## 7. The genuinely Pete-gated decision (→ q74)
Two things need Pete before the **hardware shot**, neither of which blocks the
Option-B *implementation* (Go authority + Z80 port + emulation tests — all
host/emulation-verifiable):

1. **Spec sign-off** on Option B (the spec gate — this doc).
2. **cdn reachability for the multi-MB shot.** The pinned firmware blobs are
   SHA-256-verified end-to-end, so an untrusted proxy is fine; the open choice is
   *what the SAM fetches from on Pete's network* — a **LAN mirror** of the pinned
   blobs (simple, no SAM-side internet routing; the SAM stack is link-local with
   no DNS/gateway) vs a **resolved cdn IP** (needs a gateway/NAT path from the SAM,
   which §12 of the delivery design keeps out of scope). A LAN mirror is the
   low-friction default; this is Pete's infra call.

Tracked as **q74** (raised with this spec). The Option-B implementation (i70c-b2)
`depends_on` that question; the loop proceeds to other ready work meanwhile.

## 8. Related
- [`phase3-delivery-design.md` §8](phase3-delivery-design.md) — the flat-store +
  spanning convention this reconciles (its §8 now points here for the directory
  reconciliation).
- `src/netboot/fw_span.asm` — the Z80 span arithmetic/naming (the port of
  `span.go`); Option B adds a placement layer above it.
- `src/netboot/http_main.asm` — `storage_sink_leaf` / `store_begin` / `store_end`
  / `store_format_record` (the persist leaf changed by §4.2–4.3).
- `tools/netboot-oracle/bdos/span.go` (authority), `.../tftp/manifest.go`
  (`ServePlan`, the serve authority §4.4).
- `docs/notes/trinity-capabilities.md` — record size / throughput / measured
  bands for the long-shot estimate.
