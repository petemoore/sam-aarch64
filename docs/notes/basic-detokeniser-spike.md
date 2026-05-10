# basic-detokeniser-spike — design + findings

Symmetric counterpart to `tools/basic-emulator-spike/` (text → tokenised
BASIC). Takes a tokenised SAM BASIC file (`.mgt` + filename) and recovers
the source text by running the SAM ROM under koron-go/z80 and driving
the editor's EDIT key per line, capturing ELINE post-EDIT.

Lives at `tools/basic-detokeniser-spike/` (the spike) and
`tools/basic-detokeniser-sweep/` (the corpus validator).

## Mechanism

EDIT in SAM is **not** a typed keyword. It's a keyboard key (code 0x07)
handled by EDKY at rom-disasm:0x0379. EDKY:

1. Reads leading digits in ELINE via EVALLINO; if present, sets EPPC
   to that line number (rom-disasm:038F).
2. Calls FNDLINE to locate the line in PROG.
3. Sets LISTFLG=0 ("pretty listing off" — suppresses leading-space
   insertion before keywords).
4. Calls SETSTRM to route output to channel "R".
5. RST 30H → OUTLINE — emits the line to channel R.
6. Channel R writes into ELINE as detokenised source text.

The spike drives this by:
1. Booting ROM to MAINELP (same banner-skip + FLAGS/LASTK plumbing as
   the forward spike).
2. Memory-poking the tokenised body into RAM at PROG, with a
   memmove shifting canonical NumericVars + gap + savars + ELINE +
   WORKSP up by `len(progBytes) - 1`. Every BASIC-area sysvar
   pointer except PROG/DATADD is bumped by the same delta.
3. Snapshotting post-load.
4. For each line in ascending order: restore, type the line# digits,
   send key 0x07, step until idle, read ELINE up to first 0x0D.
5. Output each captured line, control bytes escaped as `{N}`.

## Known limitations

| Limitation | Cause | Status |
|---|---|---|
| Line 0 not extractable | EDKY explicitly RETs for line 0 (rom-disasm:03A1, "DON'T EDIT LINE ZERO") | Skip in spike + strip from samfile in sweep comparator |
| Programs > ~9KB fail | PROG starts at 0x9CD5 in section C; section D is ROM 1. Single-page poke can't grow PROG past 0xBF00 (~0x223B = ~8.5KB of program). SAM uses HMPR-paged extended addressing for larger programs. | Skip with clear error; section-D RAM paging not implemented |
| Lines with embedded 0x0D | OUTLINE itself truncates at first 0x0D byte (rom-disasm:F38D-F38F), matching SAM's actual LIST/EDIT behaviour. Spike inherits this faithfully. samfile basic-to-text faithful mode reads full line per length header and includes embedded 0x0D as content. | One DIFFER in 500-job sample; treated as known ambiguity |
| "Pretty listing" leading spaces | EDKY sets LISTFLG=0; SAM's leading-space-before-keyword logic (SPACES at rom-disasm:F36E) is gated by that flag and disabled in our path. samfile-faithful inserts them for readability. | Normalised away in sweep comparator via `stripSpacesOutsideStrings` (lifted from llist-sweep) |

## Validation strategy

`tools/basic-detokeniser-sweep/` compares spike output against
`samfile basic-to-text` (faithful mode, no `--lossy`). Both are
un-wrapped per-line decoders, so direct byte comparison works after
normalisation:

- Render control bytes as `{N}` in spike output (matches samfile).
- Track string-literal state to handle 0x0A correctly: inside
  strings it's `{10}`, outside it's the line separator.
- Strip whitespace outside string literals to ignore samfile's
  "leading space" insertions.
- Drop line-0 entries from both sides.

### 500-job sample run (commit `cd6404e`)

| Status | Count | % |
|---|---:|---:|
| MATCH | 429 | 85.8 |
| SPIKE-ERROR (all overflow) | 49 | 9.8 |
| READ-ERROR (corpus disk read) | 14 | 2.8 |
| B2T-ERROR (samfile failure) | 7 | 1.4 |
| DIFFER | 1 | 0.2 |

Of attemptable jobs that fit in one PROG page: **429 of 430 match
(99.8%)**. The single DIFFER is an embedded-0x0D-in-string case (see
limitations table).

## Next steps (not yet done)

- Investigate the embedded-0x0D-in-string DIFFER on a real example
  to confirm root cause matches the hypothesis.
- Section-D RAM paging for >9KB programs. ~10% of corpus needs this
  for full coverage.
- Optional: replace subprocess-per-file with in-process library
  (move spike's emulation core into a `package detokeniser`,
  amortise ROM boot across all files in a sweep run).
- Optional: wrapToLLIST + extend `tools/llist-sweep` with
  `--uut=spike` for direct spike-vs-LLIST comparison (covers wrap
  rules + cursor `>` rendering). Currently we validate against
  samfile basic-to-text faithful as a proxy.
