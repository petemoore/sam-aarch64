# i365 demo — end-to-end runbook (assemble + render + serve, from one Trinity record)

The i365 demo is the assembler-core proof, network-verifiable: **one boot of a
single Trinity-record disk** takes `release.tbn` and, in that one run,

1. **assembles** it → `release.img` (the aarch64 binary), saved on the record;
2. **renders** it → `release.src` (the human-readable source listing), saved on the record;
3. **serves both over TFTP** so a host fetch confirms the whole thing on real silicon.

Hardware-proven 2026-07-05 (item **i365e**, PR #873). This runbook is how to
**recreate the disk, rerun the demo (emulation and hardware), and verify the
output byte-for-byte**. Design rationale lives in
[`../specs/i365-demo-architecture.md`](../specs/i365-demo-architecture.md); the
hardware-shot summary is in
[`netboot-trinity-testing.md`](netboot-trinity-testing.md) §"The demo capstone".

SAM on Pete's LAN: **`192.168.2.75`**, MAC `02:54:52:49:4e:bc`. Power is a TAPO
plug: `~/bin/tapo.sh on|off` (power-on auto-boots trinload in ~10–15 s). **Turn
it off when idle.**

---

## 0. Fastest rerun — reboot the record that is already on the card

The demo `.mgt` is already stored on the shared SD card at **record 199** (its
catalogue name is `demo_r199.mgt`), patched for record 199. So the quickest rerun
does **not** rebuild or re-push anything — just boot it and fetch:

```sh
~/bin/tapo.sh on                                             # wait ~15 s for trinload
DEPLOY_CHECKED=1 tools/trinload-push/boot-record.py 192.168.2.75 199
# the SAM now assembles + renders + comes up as a TFTP server (~2–3 min); then:
curl -m 60  --tftp-blksize 1024 -o /tmp/release.img tftp://192.168.2.75/release.img
curl -m 300 --tftp-blksize 1024 -o /tmp/release.src tftp://192.168.2.75/release.src
~/bin/tapo.sh off
```

Verify (see §5). If record 199 was later reused/deleted, do the full run (§4).

---

## 1. Artifacts — what and where

| Thing | Path | Notes |
|---|---|---|
| The demo disk (record vessel) | `build/assemble_first_serve_record.mgt` | 819,200 B = exactly one Trinity record. Built by `make netboot-assemble-first-serve-record`. |
| The input `.tbn` (DOS `IN`) | `build/release-unstripped.tbn` | 371,642 B. Built by `make release-unstripped-tbn` (`Makefile` target `release-unstripped-tbn`). |
| Expected `release.img` (authority) | `build/release-unstripped.img` | 21,752 B. GNU/Go-identical aarch64 binary. sha256 `a93f0a48…a5a8ea8`. |
| Expected `release.src` (authority) | regenerate (§5) | 417,374 B. = Go `render.Emit(release.tbn)`. sha256 `75c835ae…aefbbb9`. |
| Emulation gate | `tools/netboot-oracle/z80/assemble_first_serve_faithful_test.go` | `TestAssembleFirstServeFaithful`. |

The `.mgt` is a `boot_record`-bootable **CODE-auto** record vessel composed of:

- **`AUTOasm`** = `build/assembler-demo-chain.bin` — the assembler built with
  `-D DEMO_ASM -D DEMO_CHAIN` (`src/assembler.asm`; the chain tail is
  `DEMO_CHAIN`-gated). This is the boot file ALHK runs.
- **`render`** overlay = `build/render_chain.bin` — the `.tbn`→source renderer
  built with `-D DEMO_CHAIN` (`src/netboot/render_disk_boot.asm` + `render_disk_sink.asm`).
- **`nbsrv`** overlay = `build/netboot_server.bin` — the TFTP server
  (`src/netboot/netboot_server.asm`), serving both files disk-backed.
- **`IN`** = `build/release-unstripped.tbn` (read whole by render, prefix by the assembler).
- by-name payloads the phases HLOAD: `disasm`/`d15` (`build/disasm.bin`),
  `enctab.enc` (`build/enctab.enc`), `sd13` (`build/sysreg_data.bin`),
  `zx013` (`build/zx0.bin`).
- **`NBMANIFEST`** — maps the 10-char store names to the full TFTP names
  (`RELEASESRC→release.src`, `RELEASEIMG→release.img`).

The build recipe is `Makefile` target `$(BUILD)/assemble_first_serve_record.mgt`
(search `netboot-assemble-first-serve-record`).

---

## 2. Recreate the disk from source

```sh
make netboot-assemble-first-serve-record        # -> build/assemble_first_serve_record.mgt
```

That rebuilds every dependency (the assembler-demo-chain, render_chain,
netboot_server, the `.tbn`, the payloads) and composes the `.mgt` via
`build/build-disk`. Expect the summary to end
`Built build/assemble_first_serve_record.mgt (boot_record-bootable CODE-auto record vessel)`
and the file to be exactly **819200** bytes.

---

## 3. Verify in emulation first (the faithful gate)

Before any hardware shot, confirm the exact `.mgt` assembles + renders + serves
byte-exact on the faithful rig (real captured ROM + **B-DOS 1.5t** + the SPI SD
model). Needs the private captures under `~/sam-archive` (this host has them):

```sh
cd tools/netboot-oracle/z80
go test -run TestAssembleFirstServeFaithful -count=1 -v -timeout 600s .
```

A pass logs `release.src served byte-exact … (417374 bytes == render.Emit)` and
`release.img served byte-exact … (21752 bytes == GNU release-unstripped.img)`.
(In CI, `SKIP_PRIVATE_TESTS=true` skips it — the captures are Colin's
non-redistributable B-DOS 1.5t.)

---

## 4. Full hardware run from scratch (store on a fresh record + boot + fetch)

The one non-obvious step is **patching the boot-record number into the disk
before pushing** — see §6. `sd_push` picks the first *free* record dynamically,
so we resolve that record first, patch it in, then push and assert the push
landed where we patched.

```sh
# 1. power on + find the first free record
~/bin/tapo.sh on                                                     # trinload up ~15 s
DEPLOY_CHECKED=1 tools/trinload-push/list-records.py 192.168.2.75    # -> "first free: REC N"

# 2. patch the record number into the render/nbsrv overlays, then push to that record
cp build/assemble_first_serve_record.mgt /tmp/demo.mgt
tools/trinload-push/patch-demo-record.py /tmp/demo.mgt N             # patches RDB_CFG_RECORD + NB_BOOT_RECORD
DEPLOY_CHECKED=1 tools/trinload-push/sd-push.py 192.168.2.75 /tmp/demo.mgt build/sd_push.bin
#   ^ ~90 s for 1600 sectors; MUST report "wrote it to record N" (== the record you patched)

# 3. boot the record (fires ALHK -> assemble -> render -> serve)
DEPLOY_CHECKED=1 tools/trinload-push/boot-record.py 192.168.2.75 N

# 4. the demo takes ~2–3 min to assemble + render + come up as a server; then fetch both.
#    (poll release.img until it returns 21752 B — that means the server is up.)
curl -m 60  --tftp-blksize 1024 -o /tmp/release.img tftp://192.168.2.75/release.img
curl -m 300 --tftp-blksize 1024 -o /tmp/release.src tftp://192.168.2.75/release.src
~/bin/tapo.sh off
```

Notes:
- Every pusher (`list-records.py`, `sd-push.py`, `boot-record.py`) is gated by the
  **deploy-guard** hook — prefix `DEPLOY_CHECKED=1` after reading its
  hardware-readiness checklist. `make trinpush-help` prints the canonical invocations.
- **Data safety:** `sd_push` writes ONLY the first-free record; used records are
  never touched. The patch must match the pushed record exactly — a wrong value
  would make render's raw CMD24 write `release.src` into another record's LBA band.
- Serve throughput is ~3 KB/s per file (RAM-arena / round-trip bound), so
  `release.src` (417 KB) takes ~2 min — hence the generous `-m 300`.
- A request for a missing name returns an error and the server keeps serving
  (negative control).

---

## 5. Verify the output byte-for-byte

`release.img` compares directly to the committed authority:

```sh
cmp /tmp/release.img build/release-unstripped.img && echo "release.img OK"
sha256sum /tmp/release.img     # expect a93f0a48f5fecac36ecacf55ccc945f22698cdede01f4bebc8e78ace6a5a8ea8
```

`release.src` is the Go `render.Emit` of the `.tbn`. There is no committed
`release.src`, so regenerate the authority with a throwaway program that calls the
`render` package (from the repo root):

```sh
mkdir -p tools/sam-aarch64/cmd/emit-src-tmp
cat > tools/sam-aarch64/cmd/emit-src-tmp/main.go <<'EOF'
package main
import ( "os"; render "github.com/petemoore/sam-aarch64/tools/sam-aarch64/render" )
func main() {
	in, _ := os.ReadFile(os.Args[1])
	out, err := render.Emit(in); if err != nil { panic(err) }
	os.WriteFile(os.Args[2], out, 0644)
}
EOF
go run ./tools/sam-aarch64/cmd/emit-src-tmp build/release-unstripped.tbn /tmp/release.src.expected
rm -rf tools/sam-aarch64/cmd/emit-src-tmp

cmp /tmp/release.src /tmp/release.src.expected && echo "release.src OK"
sha256sum /tmp/release.src     # expect 75c835ae8667196d7392d3fe3501f67648f78677c9e56d6a5c2997255aefbbb9
```

(A `make release-src` target would make this a one-liner — a nice future addition.)

Both `cmp`s passing = the SAM assembled `release.img` and rendered `release.src`
on real silicon, byte-identical to the host authorities, and served both over TFTP.

---

## 6. Why the record-number patch (§4 step 2) is needed

Two overlays use **raw absolute-LBA SD** paths that must know the record they
booted from, and there is **no runtime self-discovery** (that would need a
version-specific B-DOS sysvar address, which `src/netboot/bdos_seam.asm` bans):

- `render`'s **`RDB_CFG_RECORD`** (LE16) — the raw CMD17 read of `IN` and the raw
  CMD24 write of `release.src`;
- `nbsrv`'s **`NB_BOOT_RECORD`** (byte) — the large-file disk serve of both files.

Because `sd_push` chooses the first free record at push time, the number is not
known when the `.mgt` is built — so it is patched in **after** resolving the free
record and **before** the push. The patcher
(`tools/trinload-push/patch-demo-record.py`, library `mgt_patch.py`) writes both
symbols into the overlays' payload bytes (located via each overlay's pyz80 map)
and reads them back to confirm. This mirrors exactly what the emulation gate
patches in memory. A wrong value is a **shared-card data-safety hazard**, so §4
step 2 asserts the pushed record equals the patched record.

---

## 7. Troubleshooting

- **`sd-push.py` reports a record ≠ the one you patched** — abort; do NOT boot
  (the patch and the stored record disagree). Re-run `list-records.py`, re-patch
  for the actually-claimed record, or delete the mis-pushed record
  (`tools/trinload-push/delete-record.py`) and retry.
- **The fetch times out forever** — the come-up may have wedged (the B-DOS store
  walk, the ENC/csd re-init, or the render→nbsrv HLOAD). Power-cycle
  (`~/bin/tapo.sh off; ~/bin/tapo.sh on`) and rerun; the record is unchanged.
- **`release.img` fetches but is short** — the server was still coming up; refetch
  (poll until it returns 21752 B).
- **trinload not answering** — probe it: `tools/trinload-push/*.py` do a `?`→`!`
  discovery; a bare `!` = trinload. If silent, power-cycle.

---

## 8. Remaining work

`i368` — on-screen RST `&10` progress messages ("Generating source…", etc.) — is
deferred (RST `&10` hangs at come-up under an ALHK record boot and needs a real
screen). Everything else in the demo is done and hardware-proven.
