# pagepool

The host-side correctness authority for the on-SAM IDE **page allocator**
(item i2a) — the `page_owner[]` ownership table and the `alloc_page`/`free_page`
discipline that lets the editor's document and the assembler's IN/OUT/scratch
buffers share one dynamic pool of physical RAM pages.

**Design authority:** [`docs/specs/ide-memory-model-design.md`](../../../docs/specs/ide-memory-model-design.md) §4.1

`Pool` holds one ownership byte per physical page: `Reserved` (never handed out —
ROM/BASIC/DOS/screen/resident-code/absent), `Free`, or an owner tag (`OwnerDoc`,
`OwnerIn`, `OwnerOut`, …). `Init` sizes the pool to the machine's page count;
`Reserve` removes the system pages; `Alloc` hands out the lowest-numbered `Free`
page (a deterministic first-FREE policy); `Free` returns a page only when it
carries the expected owner tag, so a double-free or a wrong-owner free is caught.

This package is the *policy* reference; the SAM byte layout and Z80 register
interface live in [`src/pagepool.asm`](../../../src/pagepool.asm). The
koron-go/z80 harness drives the Z80 routines against this `Pool` as an oracle in
[`tools/netboot-oracle/z80/pagepool_test.go`](../../netboot-oracle/z80/pagepool_test.go).

This brick is the allocator mechanism only: boot-time sizing from the SAM
sysvars (PRAMTP/VMPR/…) and wiring the assembler buffers to the pool are later
i2 bricks.
