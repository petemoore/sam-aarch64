# `tools/trinload-push` — push a binary to TrinLoad on the SAM (python3)

A python3 port of `simonowen/trinload`'s `test/trinload.py` — it speaks TrinLoad's
`?`/`@`/`X` UDP protocol (port `0xEDB0`) to push a pyz80-assembled `.bin` into SAM
RAM and execute it (see `src/netboot/trinload.asm` for the on-SAM side). Used to
deliver netboot programs to the real SAM over the wire (no SD-card shuffle) — e.g.
the SAMBOOT ROM/EEPROM dumper for the i87a captures.

It exists because the Pi (and other dev hosts) ship python3 only; the upstream
`trinload.py` is python2. Same protocol, byte-for-byte: discovery `?`→`!`, then
`@`-blocks (unicast, 4-byte acks, 4 outstanding), then `X` (page + execute addr).

Usage:

```sh
make netboot-dumper-trinload      # or any netboot *_trinload.bin (org &8000)
tools/trinload-push/trinload-push.py 192.168.2.75 build/netboot_dumper_trinload.bin 1 0x8000
# then pull what the program serves, e.g.:  curl -o rom1.bin tftp://192.168.2.75/rom1.bin
```

Args: `<sam-ip> <bin> [page=1] [exec-addr=0x8000]`. TrinLoad must already be running
on the SAM (it listens on `0xEDB0`). The full hardware-capture procedure lives in
[`docs/notes/netboot-trinity-testing.md`](../../docs/notes/netboot-trinity-testing.md).
