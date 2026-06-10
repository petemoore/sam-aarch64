# basic-detokeniser-spike — design + findings

Symmetric counterpart to the forward (text → tokenised BASIC) emulator spike.
Takes a tokenised SAM BASIC file (`.mgt` + filename) and recovers
the source text by running the SAM ROM under koron-go/z80 and driving
the editor's EDIT key per line, capturing ELINE post-EDIT.

The spike code lives in git history:
- Forward spike: <https://github.com/petemoore/sam-aarch64/blob/c0f62fa/tools/basic-emulator-spike/>
- Detokeniser spike: <https://github.com/petemoore/sam-aarch64/blob/c0f62fa/tools/basic-detokeniser-spike/>
- Corpus validator: <https://github.com/petemoore/sam-aarch64/blob/c0f62fa/tools/basic-detokeniser-sweep/>

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
| Programs > ~24KB fail | PROG starts at 0x9CD5 in section C; the spike's loadProgViaPoke clears LMPR bit 6 temporarily so writes can reach into section D (RAM page (HMPR+1)&0x1F). Restores LMPR before EDIT so the ROM sees the standard layout. Hard ceiling is 0xFFFF — programs that genuinely need HMPR-paged extended addressing still fail. | Skip with clear error; full HMPR-paged support not implemented (low remainder: ~0.8% of corpus) |
| Lines with embedded 0x0D | OUTLINE itself truncates at first 0x0D byte (rom-disasm:F38D-F38F), matching SAM's actual LIST/EDIT behaviour. Spike inherits this faithfully. samfile basic-to-text faithful mode reads full line per length header and includes embedded 0x0D as content. | One DIFFER in 500-job sample; treated as known ambiguity |
| "Pretty listing" leading spaces | EDKY sets LISTFLG=0; SAM's leading-space-before-keyword logic (SPACES at rom-disasm:F36E) is gated by that flag and disabled in our path. samfile-faithful inserts them for readability. | Normalised away in sweep comparator via `stripSpacesOutsideStrings` |

## Validation strategy

The corpus validator compares spike output against
`samfile basic-to-text` (faithful mode, no `--lossy`). Both are
un-wrapped per-line decoders, so direct byte comparison works after
normalisation:

- Render control bytes as `{N}` in spike output (matches samfile).
- Track string-literal state to handle 0x0A correctly: inside
  strings it's `{10}`, outside it's the line separator.
- Strip whitespace outside string literals to ignore samfile's
  "leading space" insertions.
- Drop line-0 entries from both sides.

### Full corpus run — 7017 (disk, file) jobs across 800 .mgt disks

| Status | Count | % |
|---|---:|---:|
| MATCH | 5920 | 84.4 |
| READ-ERROR (corpus disk read / parse) | 694 | 9.9 |
| B2T-ERROR (samfile failure) | 277 | 3.9 |
| SPIKE-ERROR | 76 | 1.1 |
| DIFFER | 48 | 0.7 |

Of attemptable jobs (MATCH + DIFFER + SPIKE-ERROR = 6044):
**MATCH rate 97.95%**.
Excluding overflow (MATCH + DIFFER = 5968): **MATCH rate 99.20%**.

The 76 SPIKE-ERROR cases split as:
- 71 true >24KB overflow (need HMPR-paged extended addressing)
- 3 lines longer than their ELINE buffer ("no 0x0D found within 1255
  bytes of ELINE (line 840)" — large unterminated lines, real bug)
- 2 empty filenames in the corpus (input-validation edge case)

The 48 DIFFER cases cluster into two patterns based on first-divergence offset analysis:
- **Embedded 0x0D in string literals** — OUTLINE truncates at first
  0x0D (rom-disasm:F38D-F38F), matching SAM's actual LIST/EDIT
  behaviour. samfile basic-to-text reads the full line per the PROG
  length header and includes embedded 0x0D as content. Spike is
  arguably *more* faithful to SAM ROM here.
- **Token rendering: samfile keeps `\xF9` / `\xFC` raw; spike renders
  as `-`** — these are SAM keyword/operator tokens (e.g. variants of
  minus) that the ROM expands when OUTLINE prints. samfile faithful
  mode preserves the raw byte for round-trip-faithfulness;
  spike-via-ROM expands. Again, spike matches the actual SAM behaviour.

In both DIFFER categories, the spike's output is what real SAM would
emit; samfile's faithful-mode output is a Go-side approximation that
diverges on these specific cases.

## Next steps (not yet done)

- Investigate the embedded-0x0D-in-string DIFFER on a real example
  to confirm root cause matches the hypothesis.
- Section-D RAM paging for >9KB programs. ~10% of corpus needs this
  for full coverage.
- Optional: replace subprocess-per-file with in-process library
  (move spike's emulation core into a `package detokeniser`,
  amortise ROM boot across all files in a sweep run).
- Optional: wrapToLLIST + direct spike-vs-LLIST comparison (covers wrap
  rules + cursor `>` rendering). Currently we validate against
  samfile basic-to-text faithful as a proxy.
