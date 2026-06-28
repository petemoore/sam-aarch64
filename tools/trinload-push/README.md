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

TrinLoad must already be running on the SAM (it listens on `0xEDB0`). The full
hardware procedure lives in
[`docs/notes/netboot-trinity-testing.md`](../../docs/notes/netboot-trinity-testing.md).
