# `sampage` — SAM Coupé memory paging model

A tiny, self-contained Go model of SAM Coupé memory: 32 × 16 KB RAM pages, a
32 KB system ROM (ROM0 + ROM1), and the LMPR/HMPR section-paging registers
(ports `&FA`/`&FB`). Reads honour the live page mapping; ROM is read-only (writes
into a ROM-mapped section drop, the `&C000` wall).

Both Go Z80 harnesses import this package as their **one** memory model:

- `tools/netboot-oracle/z80` — the netboot harness; uses `sampage.New` (flat-
  equivalent default) for leaf/packet tests, paged config for trinload/dumper tests.
- `tools/z80-test-harness-go` — the assembler harness; uses a custom LMPR/HMPR
  init (`lmprDefault`/`hmprDefault`) and seeds a fake ROM (RST-8 stub) into
  `pager.ROM`.

Having one pager eliminates the class of bug where a ROM-paging fix in one
harness was not mirrored to the other (CLAUDE.md §7; the i87a dumper
ROM-paging bug reached hardware because the harnesses diverged).

What/how/why deep doc: the package doc comment in `sampage.go` (memory-map
table, the flat-equivalent default config, port constants).
