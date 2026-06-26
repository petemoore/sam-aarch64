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

`Pool` (in `pagepool.go`) is the allocator mechanism only: boot-time sizing from
the SAM sysvars (PRAMTP/VMPR/…) and wiring the assembler buffers to the pool are
later i2 bricks.

## Spill manager (item i215a)

`spill.go` adds the **page-persistence (spill)** authority on top of the
allocator — the optional headroom mechanism of design **§4.5/§5/§7**. `Manager`
wraps a `Pool` and mediates every allocation so the editor's document and the
assembler's IN/OUT/scratch budget time-share RAM:

- When the FREE pool can satisfy a request, it hands out a page with **no disk
  I/O** (a 512 KB machine touches the backend zero times — observable as
  `Spills() == 0`).
- When FREE is empty, it **lazy-spills the coldest resident DOC block** (LRU) to
  a pluggable `SpillBackend` (`Store`/`Load`/`Discard`), frees that page, and
  satisfies the request — exactly one eviction per page of shortfall.
- A spilled block **reloads on demand** when next read (which may spill another
  cold block to make room).
- With a **nil backend** (a Trinity-less floppy/tape machine that declines to
  spill) the Manager degrades to the i2 baseline: allocations refuse with
  `ErrPoolExhausted` rather than spilling — the honest "document full" ceiling.

Only DOC blocks are spill victims; IN/OUT/scratch pages are the assembler's live
working set and are never evicted. The block payload is opaque `[]byte` here — on
the SAM it is the i40 `.tbn` serialization of one i41 document block; the
concrete backends (Trinity SD record / floppy-tape) and the Z80 port are later
bricks (**i215c** / **i215b**).
