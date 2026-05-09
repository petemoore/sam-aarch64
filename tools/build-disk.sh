#!/usr/bin/env bash
# Constructs a self-bootable .mgt disk image containing:
#   - samdos2 as the FIRST file (slot 0, T4S1, 20 sectors covering T4-T5)
#     so that SAM ROM's BOOT command (F9) can load it without simcoupe's
#     -dosboot 1 hack — making the image bootable on real SAM hardware too.
#   - a tokenised BASIC 'auto' file that auto-runs on boot (slot 1, T6S1)
#     (CLEAR 24575 : LOAD "stub" CODE 24576 : CALL 24576)
#   - the stub binary as a code file 'stub' (slot 2, T6S2; loads at 0x6000)
#   - the input fixture as 'IN' (slot 3, T6S3)
#
# 0x6000 is the standard SAMDOS-coexisting code address — SAMDOS itself
# occupies 0x8000-0xBFFF, so user code must live below 0x8000.
#
# Why samdos2 must be at T4S1:
#   SAM ROM's BOOT routine (D8CD–D97D in ROM v3.0) reads track 4 sector 1
#   raw to &8000, then checks bytes 256–259 for the literal "BOOT"
#   (case-insensitive, bit-7 ignored). If matched, JP &8009. Otherwise it
#   issues error 53 ("NO DOS"). The samdos2 binary is engineered so that
#   the magic "BOOT" string lands at body offset 247 — which becomes
#   sector offset 256 once the standard 9-byte file header is prepended.
#
# Why we hand-roll everything:
#   samfile (Pete's tool) has an operator-precedence bug at samfile.go:564
#   in SAMMask: `1 << bitOffset & 0x07` parses as `(1 << bitOffset) & 0x07`
#   which produces wrong (often zero) masks for bit_offsets ≥ 3. As a
#   result, samfile add leaves sector address maps mostly zeroed and
#   subsequent adds allocate the same sectors. Hand-rolling all four
#   directory entries here avoids the bug. (TODO: upstream fix for
#   samfile.)
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

# Create a blank 819200-byte (800 KiB) MGT image (all zero bytes).
dd if=/dev/zero of="$output" bs=1024 count=800 2>/dev/null

# ---------------------------------------------------------------------------
# All disk construction is done in this single Python block so that the
# sector-allocation invariants are visible end-to-end.
# ---------------------------------------------------------------------------
python3 - "$output" "$samdos2" build/stub.bin "$input" <<'EOF'
import sys

output_path, samdos2_path, stub_path, input_path = sys.argv[1:5]

# --- MGT format helpers -------------------------------------------------
# Cylinder-interleaved layout: each cylinder = side0-track (5120 B) +
# side1-track (5120 B). Track byte's bit 7 selects side. (SAM tech manual
# v3.0 pp.78–81; samfile.go::Sector.Offset matches.)

def sector_offset(track: int, sector: int) -> int:
    return ((track >> 7) * 5120) + ((sector - 1) * 512) + ((track & 0x7f) * 10240)

def sector_bit(track: int, sector: int) -> int:
    return (track & 0x7f) * 10 + (sector - 1) + ((track & 0x80) >> 7) * 800 - 40

def set_sector_in_map(sam_map: bytearray, track: int, sector: int) -> None:
    b = sector_bit(track, sector)
    sam_map[b // 8] |= 1 << (b % 8)        # correct (cf. samfile bug above)

def write_directory_entry(img: bytearray, slot: int, *, type_byte: int,
                          name: bytes, chain: list, length: int,
                          exec_addr_div_16k: int = 0xff,
                          exec_addr_mod_16k: int = 0xffff,
                          start_line: int = -1) -> None:
    """Write a 256-byte directory entry to slot N (0-79)."""
    assert len(name) == 10, name
    e = bytearray(256)
    e[0x00] = type_byte
    e[0x01:0x0b] = name
    e[0x0b] = (len(chain) >> 8) & 0xff             # sector count BE high
    e[0x0c] = len(chain) & 0xff                    # sector count BE low
    e[0x0d] = chain[0][0]                          # first sector track
    e[0x0e] = chain[0][1]                          # first sector
    sam_map = bytearray(195)
    for t, s in chain:
        set_sector_in_map(sam_map, t, s)
    e[0x0f:0x0f + 195] = sam_map
    e[0xf0] = length & 0xff
    e[0xf1] = (length >> 8) & 0xff
    # Bytes 0xf2-0xf4 are the 3-byte execution-address / auto-run-line field.
    # For CODE files: byte 0xf2 = ExecAddrDiv16K, 0xf3-0xf4 = ExecAddrMod16K.
    # For BASIC files with auto-RUN: byte 0xf2 = 0, 0xf3-0xf4 = start line.
    # For BASIC files without auto-RUN: byte 0xf2 = 0xff. (ROM E3D9 checks
    # this byte: if 0xff after the file is loaded, no auto-RUN happens.)
    if start_line >= 0:                            # SAM BASIC auto-RUN line
        e[0xf2] = 0                                # marker: 'auto-RUN this BASIC'
        e[0xf3] = start_line & 0xff
        e[0xf4] = (start_line >> 8) & 0xff
    else:
        e[0xf2] = exec_addr_div_16k
        e[0xf3] = exec_addr_mod_16k & 0xff
        e[0xf4] = (exec_addr_mod_16k >> 8) & 0xff
    img[slot * 256:(slot + 1) * 256] = e

def write_file_chain(img: bytearray, chain: list, file_bytes: bytes) -> None:
    """Write file_bytes (header+body) split across `chain` sectors with
    next-sector links at offsets 510–511 of each sector."""
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
        # last sector: link bytes stay (0,0) = end of file
        img[off:off + 512] = sd

# --- Open image ---------------------------------------------------------
with open(output_path, "r+b") as f:
    img = bytearray(f.read())
    assert len(img) == 819200, f"Expected 819200-byte image, got {len(img)}"

    # === Slot 0: samdos2 (T4S1..T5S10, 20 sectors) =====================
    #
    # samdos2 is 10000 bytes; with the 9-byte SAMDOS file header that's
    # 10009 bytes total = ceil(10009/510) = 20 sectors. T4 has 10 sectors,
    # T5 has 10 sectors → exact fit.
    #
    # Type byte: SAMDOS internally uses type 3 for itself, but samfile
    # treats unrecognised types as "erased" and would overwrite the slot.
    # We use type 19 (Code) which (a) is irrelevant for booting — the ROM
    # reads sector data raw and doesn't look at the directory entry type;
    # (b) makes samdos2 visible in `samfile ls`.
    samdos2_chain = [(t, s) for t in (4, 5) for s in range(1, 11)]
    samdos2_body = open(samdos2_path, "rb").read()
    assert len(samdos2_body) == 10000, len(samdos2_body)
    samdos2_header = bytes([
        0x13,                                 # type 19 (Code)
        10000 & 0xff, (10000 >> 8) & 0xff,    # LengthMod16K LE = 10000
        0x00, 0x80,                           # PageOffset = &8000
        0x00, 0x00,                           # reserved
        0x00,                                 # Pages = 0 (10K < 16K)
        0x01,                                 # StartPage = 1
    ])
    write_directory_entry(
        img, slot=0, type_byte=0x13, name=b"samdos2   ",
        chain=samdos2_chain, length=10000,
        exec_addr_div_16k=0x80,               # &8000 / 16K = 2 → div=0x80? unused for BOOT
    )
    write_file_chain(img, samdos2_chain, samdos2_header + samdos2_body)

    # === Slot 1: AUTO BASIC (T6S1) =====================================
    #
    # Tokenised SAM BASIC line:
    #   10 CLEAR 24575 : LOAD "stub" CODE 24576 : CALL 24576
    #
    # Tokens: CLEAR=0xb3, LOAD=0x95, CODE=0xff,0x6c, CALL=0xe4. Numbers
    # carry a 5-byte numeric form right after their ASCII digits, prefixed
    # with 0x0e: [0x0e, 0x00, 0x00, lo, hi, 0x00] for unsigned 16-bit.
    def num(n: int) -> bytes:
        return bytes([0x0e, 0x00, 0x00, n & 0xff, (n >> 8) & 0xff, 0x00])

    LOAD_ADDR = 24576
    stmt_clear = bytes([0xb3, 0x20]) + str(LOAD_ADDR - 1).encode() + num(LOAD_ADDR - 1)
    stmt_load  = (bytes([0x95, 0x20, 0x22]) + b"stub"
                  + bytes([0x22, 0x20, 0xff, 0x6c, 0x20])
                  + str(LOAD_ADDR).encode() + num(LOAD_ADDR))
    stmt_call  = bytes([0xe4, 0x20]) + str(LOAD_ADDR).encode() + num(LOAD_ADDR)
    line_body = stmt_clear + b"\x3a" + stmt_load + b"\x3a" + stmt_call + b"\x0d"
    BASIC_BODY = (bytes([0x00, 0x0a, len(line_body) & 0xff, (len(line_body) >> 8) & 0xff])
                  + line_body + b"\xff")
    auto_chain = [(6, 1)]
    auto_header = bytes([0x10, len(BASIC_BODY) & 0xff, (len(BASIC_BODY) >> 8) & 0xff,
                        0, 0, 0, 0, 0, 0])
    write_directory_entry(
        img, slot=1, type_byte=0x10, name=b"auto      ",
        chain=auto_chain, length=len(BASIC_BODY),
        start_line=10,                          # auto-RUN from line 10
    )
    write_file_chain(img, auto_chain, auto_header + BASIC_BODY)

    # === Slot 2: stub (T6S2) ===========================================
    stub_body = open(stub_path, "rb").read()
    stub_chain = [(6, 2)]
    assert len(stub_body) + 9 <= 510 * len(stub_chain), \
        f"stub too large for {len(stub_chain)} sector(s)"
    stub_header = bytes([0x13, len(stub_body) & 0xff, (len(stub_body) >> 8) & 0xff,
                         LOAD_ADDR & 0xff, (LOAD_ADDR >> 8) & 0xff,
                         0, 0, 0, (LOAD_ADDR >> 14) - 1])
    # Code-file metadata in the directory:
    #   ExecutionAddressDiv16K = (exec_addr >> 14) - 1
    #   ExecutionAddressMod16K = (exec_addr & 0x3fff) | 0x8000  (the 0x8000 marks 'set')
    exec_addr = LOAD_ADDR
    exec_div = (exec_addr >> 14) - 1
    exec_mod = (exec_addr & 0x3fff) | 0x8000
    write_directory_entry(
        img, slot=2, type_byte=0x13, name=b"stub      ",
        chain=stub_chain, length=len(stub_body),
        exec_addr_div_16k=exec_div, exec_addr_mod_16k=exec_mod,
    )
    write_file_chain(img, stub_chain, stub_header + stub_body)

    # === Slot 3: IN (T6S3) =============================================
    in_body = open(input_path, "rb").read()
    in_chain = [(6, 3)]
    assert len(in_body) + 9 <= 510 * len(in_chain), \
        f"IN too large for {len(in_chain)} sector(s)"
    in_header = bytes([0x13, len(in_body) & 0xff, (len(in_body) >> 8) & 0xff,
                       LOAD_ADDR & 0xff, (LOAD_ADDR >> 8) & 0xff,
                       0, 0, 0, (LOAD_ADDR >> 14) - 1])
    write_directory_entry(
        img, slot=3, type_byte=0x13, name=b"IN        ",
        chain=in_chain, length=len(in_body),
        exec_addr_div_16k=0xff, exec_addr_mod_16k=0xffff,    # IN is data, not executable
    )
    write_file_chain(img, in_chain, in_header + in_body)

    f.seek(0)
    f.write(img)

print(f"samdos2 written ({len(samdos2_body)} bytes, 20 sectors T4S1-T5S10)")
print(f"BASIC 'auto' written ({len(BASIC_BODY)} bytes, 1 sector T6S1)")
print(f"stub written ({len(stub_body)} bytes, 1 sector T6S2)")
print(f"IN written ({len(in_body)} bytes, 1 sector T6S3)")
EOF

echo "Built $output"
samfile ls -i "$output"
