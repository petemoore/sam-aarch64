; encdrv_harness.asm — org wrapper that makes the vendored ENC28J60 driver
; (encdrv.asm) loadable by the flat-memory koron-go/z80 harness for the i80
; Trinity emulation.
;
; encdrv.asm has no `org` and opens with data storage (DEFS/DEFW), so it cannot
; be assembled and loaded on its own. This wrapper orgs it at &8000 (the
; standalone-payload address every netboot routine uses) and exposes a couple of
; harness parameter blocks the Go test fills in before each call:
;
;   PARAM_MAC  — 6 bytes, the MAC address drv_init reads (entry: HL -> MAC).
;   PACKET     — the caller frame buffer drv_read fills / drv_write sends
;                (entry: HL -> buffer; for write, BC = length).
;
; The harness sets HL (and BC for write) to point at these before CALLing
; drv_init / drv_read / drv_write, exactly as a real caller would. The driver's
; own labels (drv_init/drv_read/drv_write/read_ptr/tx_flags/...) are exported via
; the pyz80 mapfile from the include below.
;
; VERIFICATION: host-verifiable under the emulated Trinity (i80). See
; tools/netboot-oracle/z80/enc28j60.go and enc28j60_test.go. Emulation-verified
; is not hardware-verified; real Trinity remains the integration gate.

                org     &8000

                include "encdrv.asm"

; ---------------------------------------------------------------------------
; Harness parameter blocks (after the driver, so they share its address space
; but don't perturb its layout). PACKET is generously sized for the ENC's
; 1518-byte max frame plus slack.
; ---------------------------------------------------------------------------
PARAM_MAC:      defs    6                       ; MAC drv_init reads (HL -> here)
PACKET:         defs    2048                    ; frame buffer for read/write
