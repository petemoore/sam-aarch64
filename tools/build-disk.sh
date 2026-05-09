#!/usr/bin/env bash
# Build the M0 round-trip disk image. Layout, semantics, and citations:
#   docs/notes/test-mgt-byte-layout.md   ← byte-by-byte reference
#   docs/notes/sam-basic-save-format.md  ← BASIC vars/gap invariant
#
# Slots:
#   0  samdos2  T4S1..T5S10  (20 sectors; ROM BOOT reads T4S1 raw)
#   1  auto     T6S1..T6S2   (BASIC AUTO: CLEAR + LOAD + CALL)
#   2  stub     T6S3         (the assembler stub)
#   3  IN       T6S4         (assembly source fixture)
#
# We hand-roll the directory rather than calling `samfile add` because
# samfile's SAMMask has an operator-precedence bug at samfile.go:564
# (`1 << bitOffset & 0x07` parses as `(1 << bitOffset) & 0x07`) that
# zeroes the sector bitmap for bitOffset ≥ 3. (Tracked in samfile;
# unrelated to this script.)
#
# Usage: build-disk.sh <input.s> <output.mgt>

set -euo pipefail

input="$1"
output="$2"

cd "$(dirname "$0")/.."

if [ ! -f build/stub.bin ]; then
    echo "ERROR: build/stub.bin not found — run 'make stub' first" >&2
    exit 1
fi

samdos2="reference/samdos/samdos2.bin"
if [ ! -f "$samdos2" ]; then
    echo "ERROR: $samdos2 not found (vendored from ~/git/samdos/res/)" >&2
    exit 1
fi

rm -f "$output"
dd if=/dev/zero of="$output" bs=1024 count=800 2>/dev/null

python3 - "$output" "$samdos2" build/stub.bin "$input" <<'EOF'
import sys

output_path, samdos2_path, stub_path, input_path = sys.argv[1:5]

# --- MGT format helpers -------------------------------------------------
# Cylinder-interleaved: each cylinder = side0 (5120 B) + side1 (5120 B).
# Track byte's bit 7 selects side. Tech Manual v3.0 §disk format;
# samfile.go::Sector.Offset matches.

def sector_offset(track: int, sector: int) -> int:
    return ((track >> 7) * 5120) + ((sector - 1) * 512) + ((track & 0x7f) * 10240)

def sector_bit(track: int, sector: int) -> int:
    return (track & 0x7f) * 10 + (sector - 1) + ((track & 0x80) >> 7) * 800 - 40

def set_sector_in_map(sam_map: bytearray, track: int, sector: int) -> None:
    b = sector_bit(track, sector)
    sam_map[b // 8] |= 1 << (b % 8)

def page_form_3byte(value: int) -> bytes:
    """Encode a value < 64K as a 3-byte page-form: [page, offset_lo, offset_hi]
    with offset's bit 15 set (8000H REL PAGE FORM). Used for dir-entry
    BASIC triplets at 0xDD/0xE0/0xE3."""
    page = value // 16384
    offset = (value % 16384) | 0x8000
    return bytes([page, offset & 0xff, (offset >> 8) & 0xff])

def write_directory_entry(img: bytearray, slot: int, *, type_byte: int,
                          name: bytes, chain: list, length: int,
                          body_header: bytes = b"",
                          exec_addr_div_16k: int = 0xff,
                          exec_addr_mod_16k: int = 0xffff,
                          start_line: int = -1) -> None:
    """Write a 256-byte directory entry to slot N (0-79).

    `body_header` (9 bytes) is mirrored into dir bytes 0xD3-0xDB
    (SAMDOS body-header cache, samdos/src/c.s:1376) and its
    StartPage/Offset/Pages fields into 0xEC-0xEF (samfile.go:248-256).

    Auto-exec is gated by ROM at rom-disasm:22471-22484. To suppress
    auto-exec, set BOTH dir byte 0xF2 = 0xFF (default here) AND body
    header byte 6 = 0xFF (callers' responsibility).
    """
    assert len(name) == 10, name
    if body_header:
        assert len(body_header) == 9, len(body_header)
    e = bytearray(256)
    e[0x00] = type_byte
    e[0x01:0x0b] = name
    e[0x0b] = (len(chain) >> 8) & 0xff
    e[0x0c] = len(chain) & 0xff
    e[0x0d] = chain[0][0]
    e[0x0e] = chain[0][1]
    sam_map = bytearray(195)
    for t, s in chain:
        set_sector_in_map(sam_map, t, s)
    e[0x0f:0x0f + 195] = sam_map
    if body_header:
        e[0xd3:0xd3 + 9] = body_header
        e[0xec] = body_header[8]
        e[0xed] = body_header[3]
        e[0xee] = body_header[4]
        e[0xef] = body_header[7]
    e[0xf0] = length & 0xff
    e[0xf1] = (length >> 8) & 0xff
    if start_line >= 0:
        # BASIC auto-RUN: dir byte 0xF2 = 0 marks "auto-RUN", 0xF3-0xF4 = line
        e[0xf2] = 0
        e[0xf3] = start_line & 0xff
        e[0xf4] = (start_line >> 8) & 0xff
    else:
        e[0xf2] = exec_addr_div_16k
        e[0xf3] = exec_addr_mod_16k & 0xff
        e[0xf4] = (exec_addr_mod_16k >> 8) & 0xff
    img[slot * 256:(slot + 1) * 256] = e

def write_file_chain(img: bytearray, chain: list, file_bytes: bytes) -> None:
    """Write file_bytes across `chain` sectors with next-sector links at
    bytes 510-511 of each sector. Last sector's link is (0,0) = end."""
    chunks = [file_bytes[i:i + 510] for i in range(0, len(file_bytes), 510)]
    assert len(chunks) <= len(chain), \
        f"file needs {len(chunks)} sectors but allocated {len(chain)}"
    for i, chunk in enumerate(chunks):
        track, sec = chain[i]
        off = sector_offset(track, sec)
        sd = bytearray(512)
        sd[0:len(chunk)] = chunk
        if i + 1 < len(chunks):
            nt, ns = chain[i + 1]
            sd[510] = nt
            sd[511] = ns
        img[off:off + 512] = sd

# --- Open image ---------------------------------------------------------
with open(output_path, "r+b") as f:
    img = bytearray(f.read())
    assert len(img) == 819200, f"Expected 819200-byte image, got {len(img)}"

    # === Slot 0: samdos2 ===============================================
    # 10000 bytes + 9-byte header = 10009 bytes = 20 sectors (T4-T5).
    # ROM BOOT (D8CD-D97D) reads T4S1 raw to 0x8000 and checks for the
    # literal "BOOT" at body offset 247 (sector offset 256). The
    # samdos2 binary places "BOOT" so this matches.
    #
    # Body header bytes are taken verbatim from the canonical FRED 02
    # / Defender install: type 0x13, length 10000, PageOffset 0x8009,
    # 0xff 0xff (no auto-exec — ROM BOOT bypasses this), Pages 0,
    # StartPage 0x7d (decorative bits set; samfile masks to & 0x1f = 29
    # → samfile-reported Start = 491529).
    samdos2_chain = [(t, s) for t in (4, 5) for s in range(1, 11)]
    samdos2_body = open(samdos2_path, "rb").read()
    assert len(samdos2_body) == 10000, len(samdos2_body)
    samdos2_header = bytes.fromhex("13102709 80ffff00 7d".replace(" ", ""))
    write_directory_entry(
        img, slot=0, type_byte=0x13, name=b"samdos2   ",
        chain=samdos2_chain, length=10000,
        body_header=samdos2_header,
    )
    write_file_chain(img, samdos2_chain, samdos2_header + samdos2_body)

    # === Slot 1: auto BASIC ============================================
    # `10 CLEAR 32767: LOAD "stub" CODE 32768: CALL 32768`
    #
    # Tokens (single-byte unless noted): CLEAR=0xb3, LOAD=0x95,
    # CODE=0xff,0x6c (two-byte SAM-extended token), CALL=0xe4. SAM BASIC
    # stores tokens WITHOUT trailing 0x20 — the LIST renderer adds the
    # display space at view time. Each numeric literal is followed by a
    # 6-byte binary form: 0x0e marker + 5-byte value (small-int form is
    # [0x0e, 0x00, sign, lo, hi, 0x00]).
    def num(n: int) -> bytes:
        return bytes([0x0e, 0x00, 0x00, n & 0xff, (n >> 8) & 0xff, 0x00])

    LOAD_ADDR = 32768
    stmt_clear = bytes([0xb3]) + str(LOAD_ADDR - 1).encode() + num(LOAD_ADDR - 1)
    stmt_load = (bytes([0x95, 0x22]) + b"stub"
                 + bytes([0x22, 0xff, 0x6c])
                 + str(LOAD_ADDR).encode() + num(LOAD_ADDR))
    stmt_call = bytes([0xe4]) + str(LOAD_ADDR).encode() + num(LOAD_ADDR)
    line_body = stmt_clear + b"\x3a" + stmt_load + b"\x3a" + stmt_call + b"\x0d"
    PROG_SECTION = (bytes([0x00, 0x0a, len(line_body) & 0xff, (len(line_body) >> 8) & 0xff])
                    + line_body + b"\xff")    # line + end-of-program sentinel

    # Trailer: 92-byte vars area + 512-byte gap. See
    # docs/notes/sam-basic-save-format.md for the canonical recipe and
    # ROM citations. CLEAR re-initialises this region on AUTO-RUN, so
    # all-zeros works; for byte-perfect canonical fidelity, fill with
    # the CLRSR pattern (rom-disasm:13215-13228).
    EMPTY_VARS_SIZE = 92
    EMPTY_GAP_SIZE = 512
    BASIC_BODY = (PROG_SECTION
                  + b"\x00" * EMPTY_VARS_SIZE
                  + b"\x00" * EMPTY_GAP_SIZE)
    NVARS_OFFSET = len(PROG_SECTION)
    NUMEND_OFFSET = NVARS_OFFSET + EMPTY_VARS_SIZE
    SAVARS_OFFSET = NUMEND_OFFSET + EMPTY_GAP_SIZE
    assert SAVARS_OFFSET == len(BASIC_BODY)

    auto_chain = [(6, 1), (6, 2)]    # 656 + 9 hdr = 665 B; needs 2 sectors
    PROG_PAGE_OFFSET = 0x9CD5        # PROG = 0x5CD5 in 8000H REL PAGE FORM
    PROG_START_PAGE = 0
    auto_header = bytes([
        0x10,                                                # Type = SAM BASIC
        len(BASIC_BODY) & 0xff, (len(BASIC_BODY) >> 8) & 0xff,
        PROG_PAGE_OFFSET & 0xff, (PROG_PAGE_OFFSET >> 8) & 0xff,
        0xff, 0xff,                                          # body-header exec marker
        0,
        PROG_START_PAGE,
    ])
    write_directory_entry(
        img, slot=1, type_byte=0x10, name=b"auto      ",
        chain=auto_chain, length=len(BASIC_BODY),
        body_header=auto_header,
        start_line=10,
    )
    auto_e = 1 * 256
    img[auto_e + 0xDD:auto_e + 0xE0] = page_form_3byte(NVARS_OFFSET)
    img[auto_e + 0xE0:auto_e + 0xE3] = page_form_3byte(NUMEND_OFFSET)
    img[auto_e + 0xE3:auto_e + 0xE6] = page_form_3byte(SAVARS_OFFSET)
    img[auto_e + 0xDC] = 0x20    # MGTFlags — canonical real-SAVE convention
    write_file_chain(img, auto_chain, auto_header + BASIC_BODY)

    # === Slot 2: stub ===================================================
    # CODE file at LOAD_ADDR. Body header bytes 5-6 = 0xFF FF (LOADED
    # auto-exec marker = "no") so ROM rom-disasm:22471-22484 returns
    # cleanly to BASIC after LOAD, letting the AUTO line's `: CALL`
    # invoke the stub.
    stub_body = open(stub_path, "rb").read()
    stub_chain = [(6, 3)]
    assert len(stub_body) + 9 <= 510 * len(stub_chain), \
        f"stub too large for {len(stub_chain)} sector(s)"
    stub_header = bytes([0x13, len(stub_body) & 0xff, (len(stub_body) >> 8) & 0xff,
                         LOAD_ADDR & 0xff, (LOAD_ADDR >> 8) & 0xff,
                         0xff, 0xff, 0, (LOAD_ADDR >> 14) - 1])
    write_directory_entry(
        img, slot=2, type_byte=0x13, name=b"stub      ",
        chain=stub_chain, length=len(stub_body),
        body_header=stub_header,
    )
    write_file_chain(img, stub_chain, stub_header + stub_body)

    # === Slot 3: IN ====================================================
    # Data file. Same body-header convention as stub (no auto-exec).
    in_body = open(input_path, "rb").read()
    in_chain = [(6, 4)]
    assert len(in_body) + 9 <= 510 * len(in_chain), \
        f"IN too large for {len(in_chain)} sector(s)"
    in_header = bytes([0x13, len(in_body) & 0xff, (len(in_body) >> 8) & 0xff,
                       LOAD_ADDR & 0xff, (LOAD_ADDR >> 8) & 0xff,
                       0xff, 0xff, 0, (LOAD_ADDR >> 14) - 1])
    write_directory_entry(
        img, slot=3, type_byte=0x13, name=b"IN        ",
        chain=in_chain, length=len(in_body),
        body_header=in_header,
    )
    write_file_chain(img, in_chain, in_header + in_body)

    f.seek(0)
    f.write(img)

print(f"samdos2: {len(samdos2_body)} bytes  T4S1-T5S10")
print(f"auto:    {len(BASIC_BODY)} bytes   T6S1-T6S2  (PROG={NVARS_OFFSET}, +VARS={EMPTY_VARS_SIZE}, +GAP={EMPTY_GAP_SIZE})")
print(f"stub:    {len(stub_body)} bytes     T6S3")
print(f"IN:      {len(in_body)} bytes     T6S4")
EOF

echo "Built $output"
samfile ls -i "$output"
