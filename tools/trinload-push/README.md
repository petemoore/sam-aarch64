# `tools/trinload-push` — push a binary to TrinLoad on the SAM (python3)

A python3 port of `simonowen/trinload`'s `test/trinload.py` — it speaks TrinLoad's
`?`/`@`/`X` UDP protocol (port `0xEDB0`) to push a pyz80-assembled `.bin` into SAM
RAM and execute it (see `src/netboot/trinload.asm` for the on-SAM side). Used to
deliver netboot programs to the real SAM over the wire (no SD-card shuffle) — e.g.
the SAMBOOT ROM/EEPROM dumper for the i87a captures.

It exists because the Pi (and other dev hosts) ship python3 only; the upstream
`trinload.py` is python2. Same protocol, byte-for-byte: discovery `?`→`!`, then
`@`-blocks (unicast, 4-byte acks, 4 outstanding), then `X` (page + execute addr). The
wire protocol lives in `trinpush.py` (a library), shared by both CLI scripts here.

## Quick reference

`make trinpush-help` prints the canonical push invocations — no looking up the path
or args. The two CLI scripts (`trinload-push.py`, `trinpush-serve.py`) are
**executable**, so run them directly (no `python3 …` prefix). For a whole hands-off
shot — power-cycle the SAM, push, capture `:9001` markers, power off — use
[`tools/hardware-shot/run-shot.sh`](../hardware-shot/run-shot.sh).

Every actual push is gated by the **deploy-guard** hook (i252): prefix
`DEPLOY_CHECKED=1` after confirming the hardware-readiness checklist it prints. The
guard fires on *executing* a pusher (so `trinpush-serve.py …` is gated whether run as
`./trinpush-serve.py` or `python3 trinpush-serve.py`), not on merely naming one.

## `trinload-push.py` — push any binary

```sh
make netboot-dumper               # or any trinload-pushable netboot .bin (org &8000)
tools/trinload-push/trinload-push.py 192.168.2.75 build/netboot_dumper.bin 1 0x8000
# then pull what the program serves, e.g.:  curl -o rom1.bin tftp://192.168.2.75/rom1.bin
```

Args: `<sam-ip> <bin> [page=1] [exec-addr=0x8000]`.

## `trinpush-serve.py` — push the combined RRQ+WRQ serve program (i121d)

Pushes the serve program (`build/netboot_serve_boot.bin` — its boot binary doubles as
the pushable block) after setting the WRQ disk-record **placement strategy** in the
`SERVE_CONFIG` block (i121h). Once running, a `put` of a `trinity-sam-disks/`-prefixed
name from any LAN machine lands a disk image in a free Trinity record per the strategy
— unattended, never overwriting a named record (write-to-free-only).

The remote name's **prefix selects the storage class** (i121c, storage-manifest design
§6.5): a name **with** the `trinity-sam-disks/` prefix is stored as a validated
(819,200-byte) bootable **disk record**; any **other** name is the default **flat-file**
class (HSAVE'd as a plain file, no size validation). So a bootable disk image must be
pushed under the prefixed remote name:

```sh
make netboot-serve-trinload       # builds the pushable serve block + mapfile
tools/trinload-push/trinpush-serve.py 192.168.2.75 --strategy highest
# then push a disk image in as a bootable disk record (note the prefixed REMOTE name):
#   tftp 192.168.2.75 -m octet -c put mydisk.mgt trinity-sam-disks/mydisk.mgt
# a non-prefixed name (e.g. `put notes.dat`) is stored as a plain flat file instead.
```

`--strategy` is `highest` (default — keep the user's low record slots free; TFTP
storage grows down from the top), `lowest`, or `explicit:N` (record N if free). The
launcher reads `SERVE_CONFIG`'s offset from the mapfile, sanity-checks its `0x5A`
magic, and patches the strategy (and explicit record, LE) before pushing.
`make netboot-trinpush-test` host-tests that patch logic against the real binary.

## `sd-push.py` — push a `.mgt` into a free SD record via `sd_push` (i293)

The small `?`/`@`/`F` pusher for `src/netboot/sd_push.asm`: stage 1 uses TrinLoad to
load `build/sd_push.bin` to **page 1** and run it; stage 2 streams the `.mgt` as one
`@` block per 512-byte sector (`linearSec` ascending, windowed at 4 acks) and `F`-
finalizes. `sd_push` auto-picks the first **free** record and writes only that record.

```sh
make netboot-sd-push
tools/trinload-push/sd-push.py 192.168.2.75 mydisk.mgt build/sd_push.bin
```

Args: `<sam-ip> <mgt-path> [sd_push.bin=build/sd_push.bin]`.

**DATA-SAFETY CAVEAT (i293):** the record-DIRECTED HWSAD write **passes end-to-end in
faithful emulation** (real Colin ROM + B-DOS 1.5t: `HRECORD` redirects B-DOS's write
base to the picked free record, every CMD24 lands in that record's LBA range, byte-exact
and data-safe — `tools/netboot-oracle/z80/sd_push_faithful_test.go`), but is **not yet
hardware-confirmed** (emulation-verified is not hardware-verified, CLAUDE.md §5).
Do **not** run this against a card with data you care about until it is confirmed on
real Trinity hardware.

## `boot-record.py` — boot a Trinity SD record N via `boot_record` (i316)

The non-interactive "boot record N" primitive (the network-driven counterpart to the
i264 hold-key picker): it patches the record number into `build/boot_record.bin`'s
`BOOT_CONFIG` block, then TrinLoad-pushes the program to **page 1** / `&8000` and runs
it. `boot_record` (`src/netboot/boot_record.asm`) HRECORD-selects that record and fires
ALHK to load + run its AUTO file — so the autonomous loop can build a disk, push it into
a record (`sd-push.py`), then **boot it** and observe, hands-off. The record number is
delivered by **patching the binary** before the push (no wire loop), exactly like
`trinpush-serve.py` patches `SERVE_CONFIG`.

```sh
make netboot-boot-record
tools/trinload-push/boot-record.py 192.168.2.75 3     # boot record 3 (0 = floppy)
```

Args: `<sam-ip> <record> [--bin build/boot_record.bin] [--map …] [--page 1]`.

`boot_record`'s HRECORD-select + ALHK fire are **emulation-verified only**
(`tools/netboot-oracle/z80/boot_record_test.go` asserts both under the harness); the
real on-hardware auto-load + boot is a **separate hardware shot** (emulation-verified is
not hardware-verified, CLAUDE.md §5). The launcher boots whatever record you name — make
sure it holds a bootable disk.

## `delete-record.py` — free/delete a Trinity SD record N via `delete_record` (i317)

The store/boot/**delete** toolkit closer: it patches the record number into
`build/delete_record.bin`'s `DEL_CONFIG` block, then TrinLoad-pushes the program to
**page 1** / `&8000` and runs it. `delete_record` (`src/netboot/delete_record.asm`) clears
that record's central record-**list** name entry — a single-entry read-modify-write (real
CMD17 read, zero this record's 16 bytes, real CMD24 write-back) — so the slot reads as
free/reusable and the next `sd-push.py` lands there. This lets the autonomous loop build a
disk, push it, boot it, then **free it and re-push cleanly**, hands-off, without exhausting
records. The record number is delivered by **patching the binary** (no wire loop), exactly
like `boot-record.py`.

```sh
make netboot-delete-record
tools/trinload-push/delete-record.py 192.168.2.75 3     # free record 3 (re-pushable)
```

Args: `<sam-ip> <record> [--bin build/delete_record.bin] [--map …] [--page 1]`. Record is
1..255; record 0 (the floppy) has no list entry and is rejected.

**Data-safety** (the Trinity SD card is a SHARED user resource): `delete_record` frees
**only** the named record's 16-byte list entry — no neighbour is touched — and refuses an
out-of-range record (0, or beyond the card's record count) without writing. The list-entry
clear + neighbour-safety are **emulation-verified only**
(`tools/netboot-oracle/z80/delete_record_test.go`); the real on-hardware free is a
**separate, Pete-gated hardware shot** (CLAUDE.md §5; i295 family). The launcher frees
whatever in-range record you name — make sure it is one you own / may reuse.

TrinLoad must already be running on the SAM (it listens on `0xEDB0`). The full
hardware procedure lives in
[`docs/notes/netboot-trinity-testing.md`](../../docs/notes/netboot-trinity-testing.md).
