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
                include "body_sink.asm"         ; body_sink_write (header skip);
                                                ; recording doubles (BODY_IN/BODY_OUT)
                                                ; guarded behind NETBOOT_STANDALONE so
                                                ; the composed binary fits the &8000-
                                                ; &10000 window (Brick 3).

; prov_skeleton — the Brick 1 placeholder entry so the composed binary has a
; public label to assemble. Superseded by prov_first/prov_onframe/prov_next in
; Brick 6.
prov_skeleton:  ret

; ===========================================================================
; Brick 4 — per-file re-init configuration and prov_start.
; ===========================================================================

; Config cells written by the test harness before calling prov_start.
; BASE_PORT: big-endian 16-bit base client port (test sets 0xC000 -> &C0,&00).
BASE_PORT:      defw 0
; BASE_ISS: big-endian 32-bit base ISS (test sets 0x11223344 -> &11,&22,&33,&44).
BASE_ISS:       defs 4
; PROV_WINDOW: flush window in bytes (LE16; 0 selects the connection default).
PROV_WINDOW:    defw 0

; ===========================================================================
; prov_start — per-file connection re-init (the Z80 port of Provisioner.start(i)).
; In:  BC = file index i (0..FW_FILE_COUNT-1).
; Out: (nothing useful; clobbers A, BC, DE, HL, IX).
;
; Steps (D4 order — port/ISS arithmetic with i FIRST, fw_* calls last):
;   1. CONN_CLIENT_PORT = BASE_PORT + i  (16-bit BE add)
;   2. CONN_ISS        = BASE_ISS  + i*0x10000  (32-bit BE: add i into byte 1
;                        with carry into byte 0)
;   3. CONN_STATE = 0; FETCH_PHASE = 0 (PH_ARP)
;   4. zero CONN_SERVER_MAC (6 bytes)
;   5. CONN_DATA_LEN = 0; CONN_FLUSH_LEN = 0; BODY_HDR_DONE = 0
;   6. call conn_verify_init (reset SHA-256)
;   7. fw_plan_path(BC=i) -> ld hl,FW_PATH / ld (HTTP_PATH_PTR),hl
;   8. fw_manifest_entry(BC=i) -> BC=rec ptr; copy rec+4 (32 B) -> CONN_PINNED_HASH
;   9. arm sink: CONN_SINK_FILTER_MODE=1, CONN_SINK_FILTER=body_sink_write,
;      BODY_DST_PTR=storage_sink_leaf
;  10. call store_begin (placeholder ret until Brick 5)
; ===========================================================================
prov_start:
                ; --- step 1: CONN_CLIENT_PORT = BASE_PORT + i (BE) ---
                ; BASE_PORT is big-endian: BASE_PORT+0 = high byte, BASE_PORT+1 = low byte.
                ; i is in C (BC = index, B=0 for i<256).  Add i to the low byte with
                ; carry propagated into the high byte.
                ld      a, (BASE_PORT + 1)      ; low byte of base port (BE byte 1)
                add     a, c                    ; low byte += i
                ld      (CONN_CLIENT_PORT + 1), a
                ld      a, (BASE_PORT)          ; high byte of base port (BE byte 0)
                adc     a, b                    ; high byte += carry (B=0 normally)
                ld      (CONN_CLIENT_PORT), a

                ; --- step 2: CONN_ISS = BASE_ISS + i*0x10000 (32-bit BE) ---
                ; ISSStride = 0x10000 so i*0x10000 in BE lands in byte 1 (CONN_ISS+1).
                ; Copy the 4 BASE_ISS bytes then add i to byte 1 with carry into byte 0.
                ; B=0 through the ldir (djnz-free loop so B need not be 4 at the start;
                ; we use BC=4 for the copy, which resets B to 0 on exit).
                push    bc                      ; save BC=i across the copy (B=0,C=i)
                ld      hl, BASE_ISS
                ld      de, CONN_ISS
                ld      bc, 4
                ldir
                pop     bc                      ; restore BC=i (B=0, C=i)
                ; CONN_ISS layout (BE, address order): [0]=MSB ... [3]=LSB.
                ; i*0x10000 in BE means byte 1 += i (with carry into byte 0).
                ld      a, (CONN_ISS + 1)
                add     a, c                    ; byte 1 += i
                ld      (CONN_ISS + 1), a
                ld      a, (CONN_ISS)
                adc     a, b                    ; byte 0 += carry (B=0)
                ld      (CONN_ISS), a

                ; --- step 3: CONN_STATE = 0; FETCH_PHASE = 0 (PH_ARP) ---
                xor     a
                ld      (CONN_STATE), a
                ld      (FETCH_PHASE), a

                ; --- step 4: zero CONN_SERVER_MAC (6 bytes) ---
                ld      hl, CONN_SERVER_MAC
                ld      b, 6                    ; (A=0 from xor above)
ps_mac_zero:    ld      (hl), a
                inc     hl
                djnz    ps_mac_zero             ; B = 0 after loop

                ; --- step 5: CONN_DATA_LEN = 0; CONN_FLUSH_LEN = 0; BODY_HDR_DONE = 0 ---
                ; A=0, B=0 still; C=i preserved (djnz only modifies B).
                ld      (CONN_DATA_LEN), a
                ld      (CONN_DATA_LEN + 1), a
                ld      (CONN_FLUSH_LEN), a
                ld      (CONN_FLUSH_LEN + 1), a
                ld      (BODY_HDR_DONE), a

                ; --- step 6: call conn_verify_init (reset SHA-256) ---
                ; conn_verify_init clobbers A, BC, DE, HL — save i (=C) first.
                push    bc                      ; save BC=i (B=0, C=i)
                call    conn_verify_init
                pop     bc                      ; restore BC=i

                ; --- step 7: fw_plan_path(BC=i) -> HTTP_PATH_PTR = FW_PATH ---
                ; fw_plan_path clobbers BC (returns path length in BC on exit).
                ; Save i across the call so we can recover it for step 8.
                push    bc                      ; save BC=i
                call    fw_plan_path            ; BC=i in -> HL=FW_PATH, BC=path_len out
                ld      hl, FW_PATH
                ld      (HTTP_PATH_PTR), hl
                pop     bc                      ; restore BC=i

                ; --- step 8: fw_manifest_entry(BC=i) -> copy rec+4 -> CONN_PINNED_HASH ---
                call    fw_manifest_entry       ; BC=i -> BC = record ptr
                ; record layout: name ptr@0, path ptr@2, SHA-256@4 (32 bytes), size@36.
                ld      h, b
                ld      l, c                    ; HL = record ptr
                ld      de, 4
                add     hl, de                  ; HL = record + 4 = SHA-256 field
                ld      de, CONN_PINNED_HASH
                ld      bc, 32
                ldir                            ; copy 32 bytes to CONN_PINNED_HASH

                ; --- step 9: arm the Brick 3 bodySink seam ---
                ld      a, 1
                ld      (CONN_SINK_FILTER_MODE), a
                ld      hl, body_sink_write
                ld      (CONN_SINK_FILTER), hl
                ld      hl, storage_sink_leaf
                ld      (BODY_DST_PTR), hl

                ; --- step 10: call store_begin (placeholder until Brick 5) ---
                call    store_begin
                ret

; ===========================================================================
; store_begin — placeholder for Brick 5 (the real per-file store open).
; Until Brick 5 lands, this is a no-op that makes the call site exist so the
; assembler resolves the label and the test harness can verify it compiles.
; ===========================================================================
store_begin:    ret
