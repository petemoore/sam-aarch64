; http_main.asm — the SAM-side multi-file firmware-fetch orchestration loop: the
; Z80 port of the netboot-oracle Go authority tools/netboot-oracle/http/
; provision.go::Provisioner. It walks a download plan and fetches each selected
; firmware file end to end — one TCP connection per file — streaming each body
; through the HTTP header-skip + a SHA-256 verify into a per-file store, recording
; whether the streamed bytes matched the file's pinned hash, then advancing.
;
; It composes the already-host-verified pieces and adds only the sequencing:
;   - netboot_http.asm  : the single-file fetch phase machine (http_fetch_first /
;     http_fetch_onframe) + its whole include chain (http_get -> tcp_conn ->
;     build_tcp_segment -> encdrv, plus the streaming sink + SHA-256 verify),
;   - fw_source.asm     : the pinned manifest (FW_MANIFEST) + the per-file path
;     builder (fw_plan_path / fw_manifest_entry),
;   - body_sink.asm     : the HTTP-response header skip (body_sink_write) — joined
;     in Brick 3, where its standalone recording doubles (BODY_IN/BODY_OUT, ~12 KB)
;     are guarded behind NETBOOT_STANDALONE so the composed binary stays inside the
;     &8000-&10000 host-test window (the doubles are unused once body_sink forwards
;     to storage_sink_leaf).
;
; The orchestration driver (prov_first / prov_onframe / prov_next) is the rx-
; driven port of Provisioner.First / OnFrame / Next; it lives OUTSIDE the
; NETBOOT_HOSTTEST guard so the host harness drives it exactly like
; http_fetch_onframe. The real-hardware bootable http_main (EEPROM read, the
; B-DOS HSAVE write) is the only non-host-verifiable part (CLAUDE.md §5).
;
; This file is built incrementally per docs/plans/z80-http-main-port-plan.md.
; Brick 1 (this commit) establishes the composition: it pulls the three include
; trees into one binary and proves every symbol the later bricks need resolves,
; with no label/org collisions and no behaviour change. The prov_* routines and
; the per-file store land in the following bricks.

                include "netboot_http.asm"      ; org &8000 + the fetch machine +
                                                ; tcp_conn (+ streaming/verify under
                                                ; NETBOOT_HOSTTEST) + http_get +
                                                ; build_tcp_segment + encdrv + sha256
                include "fw_source.asm"         ; FW_MANIFEST + fw_plan_path
                                                ; (NETBOOT_STANDALONE off -> no org)
                                                ; body_sink.asm joins in Brick 3.

; prov_skeleton — the Brick 1 placeholder entry so the composed binary has a
; public label to assemble. Superseded by prov_first/prov_onframe/prov_next in
; Brick 6.
prov_skeleton:  ret
