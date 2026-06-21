# `sampage` — SAM Coupé memory paging model

A tiny, self-contained Go model of SAM Coupé memory: 32 × 16 KB RAM pages, a
32 KB system ROM (ROM0 + ROM1), and the LMPR/HMPR section-paging registers
(ports `&FA`/`&FB`). Reads honour the live page mapping; ROM is read-only (writes
into a ROM-mapped section drop, the `&C000` wall).

It is a faithful port of the assembler harness's inline pager
(`tools/z80-test-harness-go/harness.go` `resolveRead`/`resolveWritePage`),
extracted so the netboot harness (`..`) gains real paging from **one** memory
model rather than a flat model with a paged one bolted beside it (CLAUDE.md §7).

This is the **down-payment on i190** (fold the two Go SAM Coupé emulators onto one
memory core): i190 promotes this package to the shared core both harnesses import.

What/how/why deep doc: the package doc comment in `sampage.go` (memory-map table,
the flat-equivalent default config). Caller: the parent `z80` harness's `mem`.
