# `tools/netboot-oracle/z80` — flat-memory Z80 harness for the netboot port

A small Go harness (a nested module — its own `go.mod` so the parent `netboot-oracle` pure-Go job needn't pull in koron-go/z80) that runs the SAM-side netboot Z80 code (`src/netboot/*.asm`) host-side and asserts its bytes match the `netboot-oracle` golden vectors. It loads a pyz80-assembled `.bin` + symbol map into a 64 KB address space, calls a named routine under `koron-go/z80`, and exposes memory + registers for byte-comparison.

## Two host-verifiable layers

- **Protocol logic** (`harness.go` + `*_test.go`): each packet build/parse routine is pure arithmetic + memory writes, so running it and comparing its output to the golden frame proves the port faithful — the same check the Go authority (`oracle_test.go`) gets.
- **ENC28J60 wire I/O** (`enc28j60.go` + `enc28j60_test.go`, i80): emulates the Trinity Ethernet path — the microcontroller (port `&DC` select/busy + `&DD` identity probe) and the ENC28J60 SPI chip (port `&DE`) — accurately enough to run the *real* vendored driver `src/netboot/encdrv.asm`, asserting `drv_init`/`drv_write`/`drv_read` frame bytes in/out byte-exact. Grounded in the ENC28J60 datasheet (DS39662E) + the driver's port usage.

## Not host-verifiable

An end-to-end Pi netboot and real-silicon TX/RX timing stay gated on **real Trinity hardware** — the final integration gate. **Emulation-verified ≠ hardware-verified.**

Build + run: `make ci-netboot-z80` (the `netboot-z80` CI job). Authority + design: [`../README.md`](../README.md); `docs/notes/trinity-capabilities.md`; `docs/plans/phase3-netboot-implementation-plan.md`.
