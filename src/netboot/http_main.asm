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
; It currently carries: the composition of the three include trees; prov_start —
; the per-file connection re-init (port/ISS/state reset, path + host + pinned-hash
; wiring, the bodySink seam armed); store_begin/store_end — the per-file store
; double (the Z80 port of the Go MemStore) that demarcates each file's slice of
; CONN_SINK_OUT and records its verify verdict; and prov_first/prov_onframe/
; prov_next — the rx-driven download loop over the manifest (the port of
; Provisioner.First/OnFrame/Next). The bootable migration to this loop is the
; remaining brick.

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

                ; --- step 7: fw_plan_path(BC=i) -> HTTP_PATH_PTR = FW_PATH;
                ;             HTTP_HOST_PTR = FW_HOST (every fetch's Host header) ---
                ; fw_plan_path clobbers BC (returns path length in BC on exit).
                ; Save i across the call so we can recover it for step 8.
                push    bc                      ; save BC=i
                call    fw_plan_path            ; BC=i in -> HL=FW_PATH, BC=path_len out
                ld      hl, FW_PATH
                ld      (HTTP_PATH_PTR), hl
                ld      hl, FW_HOST
                ld      (HTTP_HOST_PTR), hl
                pop     bc                      ; restore BC=i

                ; --- step 8: fw_manifest_entry(BC=i) -> copy rec+4 -> CONN_PINNED_HASH;
                ;             also stash the record's name ptr (rec+0) for store_begin ---
                call    fw_manifest_entry       ; BC=i -> BC = record ptr (preserved
                                                ; until the ldir below clobbers it)
                ; record layout: name ptr@0, path ptr@2, SHA-256@4 (32 bytes), size@36.
                ld      h, b
                ld      l, c                    ; HL = record ptr (rec+0)
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                 ; DE = name ptr (from rec+0)
                push    de                      ; save name ptr for step 10 (store_begin)
                ld      h, b
                ld      l, c                    ; HL = record ptr again
                ld      de, 4
                add     hl, de                  ; HL = rec+4 = SHA-256 field
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

                ; --- step 10: store_begin(HL = manifest name ptr) ---
                ; Opens this file in the per-file store double: records its name +
                ; the current sink-out offset as the file's start boundary.
                pop     hl                      ; HL = name ptr (saved in step 8)
                call    store_begin
                ret

; ===========================================================================
; Brick 5 — the per-file store double (the Z80 port of the Go MemStore).
;
; The Go authority Provisioner.start(i) does `hash = NewHashingSink(store.Begin(
; name))` per file and, on file completion, records `Verified = (hash.Sum() ==
; spec.SHA256)` then `store.End(name)`. The Z80 mirror keeps one shared append-
; only sink buffer (CONN_SINK_OUT, the recording double for the real B-DOS
; bounded write) and DEMARCATES each file's slice of it by recording the sink-out
; length at the file's open (store_begin) and close (store_end) — so file i's
; body is CONN_SINK_OUT[PROV_STORE_OFFS[i] : PROV_STORE_OFFS[i+1]]. The verdict
; per file is CONN_HASH_MATCH (conn_verify_final's body-vs-pin compare).
;
; This is the host-test recording double only (NETBOOT_HOSTTEST): it uses the
; streaming-sink state (CONN_SINK_OUT_LEN / CONN_HASH_MATCH / conn_verify_final),
; all of which exist only in the host-test build. The bootable build's real
; store_begin/store_end (the B-DOS HSAVE leaf, q16/i93) land in Brick 7; until
; then they are no-ops there.
; ===========================================================================
                if defined(NETBOOT_HOSTTEST)
; PROV_STORE_COUNT — number of files closed so far (store_end increments). It is
; the live index store_begin/store_end write into, so store_begin for file i sees
; i and store_end for file i sets it to i+1.
PROV_STORE_COUNT:   defw 0
; PROV_STORE_NAMES — one name pointer per file (2 B each), in fetch order; the
; Go MemStore.Order analogue.
PROV_STORE_NAMES:   defs 16     ; up to 8 files
; PROV_STORE_OFFS — the file boundaries within CONN_SINK_OUT (2 B each): file i's
; body is [OFFS[i], OFFS[i+1]). store_begin writes the start at OFFS[i], store_end
; the end at OFFS[i+1], so N files leave N+1 boundaries.
PROV_STORE_OFFS:    defs 18     ; up to 9 boundaries
; PROV_FILE_VERDICTS — CONN_HASH_MATCH per file (1 B each): 1 if the streamed
; body's SHA-256 matched the pinned hash, else 0 (the Go FileResult.Verified).
PROV_FILE_VERDICTS: defs 8      ; up to 8 files

; ---------------------------------------------------------------------------
; store_begin — open file (PROV_STORE_COUNT) in the store double.
; In:  HL = the file's name pointer (NUL-terminated string).
; Records the name + the current sink-out offset as the file's start boundary.
; Clobbers: A, BC, DE, HL.
; ---------------------------------------------------------------------------
store_begin:
                ex      de, hl                  ; DE = name ptr
                ld      hl, (PROV_STORE_COUNT)
                add     hl, hl                  ; HL = idx*2 (word index)
                push    hl                      ; save idx*2 for the OFFS store
                ld      bc, PROV_STORE_NAMES
                add     hl, bc                  ; HL = &PROV_STORE_NAMES[idx]
                ld      (hl), e
                inc     hl
                ld      (hl), d                 ; PROV_STORE_NAMES[idx] = name ptr
                pop     hl                      ; HL = idx*2
                ld      bc, PROV_STORE_OFFS
                add     hl, bc                  ; HL = &PROV_STORE_OFFS[idx]
                ld      bc, (CONN_SINK_OUT_LEN)
                ld      (hl), c
                inc     hl
                ld      (hl), b                 ; PROV_STORE_OFFS[idx] = start offset
                ret

; ---------------------------------------------------------------------------
; store_end — close the current file: finish + verify its hash, record the
; verdict and the end boundary, advance the file count.
; In:  CONN_PINNED_HASH filled (by prov_start), the body fully streamed.
; Clobbers: A, BC, DE, HL, IX (conn_verify_final).
; ---------------------------------------------------------------------------
store_end:
                call    conn_verify_final       ; CONN_HASH + CONN_HASH_MATCH
                ; PROV_FILE_VERDICTS[idx] = CONN_HASH_MATCH (1 byte per file).
                ld      hl, (PROV_STORE_COUNT)
                ld      de, PROV_FILE_VERDICTS
                add     hl, de                  ; HL = &PROV_FILE_VERDICTS[idx]
                ld      a, (CONN_HASH_MATCH)
                ld      (hl), a
                ; PROV_STORE_OFFS[idx+1] = current sink-out length (end boundary).
                ld      hl, (PROV_STORE_COUNT)
                inc     hl                      ; idx+1
                add     hl, hl                  ; (idx+1)*2
                ld      de, PROV_STORE_OFFS
                add     hl, de                  ; HL = &PROV_STORE_OFFS[idx+1]
                ld      bc, (CONN_SINK_OUT_LEN)
                ld      (hl), c
                inc     hl
                ld      (hl), b
                ; PROV_STORE_COUNT = idx+1.
                ld      hl, (PROV_STORE_COUNT)
                inc     hl
                ld      (PROV_STORE_COUNT), hl
                ret
                else
; Bootable build: the real B-DOS HSAVE store leaf lands in Brick 7. Until then
; these are no-ops so prov_start (which is assembled into the bootable) links.
store_begin:    ret
store_end:      ret
                endif

; ===========================================================================
; Brick 6 — the rx-driven download loop: prov_first / prov_onframe / prov_next,
; the Z80 port of Provisioner.First / OnFrame / Next.
;
; The driver walks files 0..PROV_FILE_COUNT-1 of the manifest, one TCP connection
; per file, reusing the single-file fetch phase machine (http_fetch_first /
; http_fetch_onframe) for the per-file wire work and adding only the cross-file
; sequencing:
;
;   tx := prov_first              ; file 0's ARP — the first frame on the wire
;   loop:
;     drv_write tx
;     rx := drv_read
;     call prov_onframe           ; BC = this frame's reply (or 0); PROV_STATUS set
;     PROV_CONTINUE  : keep going (BC is the fetch's reply)
;     PROV_FILE_DONE : drv_write BC (the FIN-ACK); tx := prov_next ; loop
;     PROV_ALL_DONE  : drv_write BC (the last FIN-ACK); done
;
; PROV_STATUS mirrors the Go Status enum (Continue=0 / FileDone=1 / AllDone=2).
; The done edge is FETCH_PHASE == PH_DONE (the fetch's FIN moved the connection to
; FIN_WAIT). On done, store_end closes the file (verify + record verdict) and the
; idx-vs-count compare picks FileDone or AllDone — exactly Provisioner.OnFrame.
; ===========================================================================
PROV_CONTINUE:  equ 0           ; still fetching the current file
PROV_FILE_DONE: equ 1           ; current file complete; more remain
PROV_ALL_DONE:  equ 2           ; last file complete; the plan is exhausted

; ---------------------------------------------------------------------------
; prov_first — start file 0's fetch (the Z80 port of Provisioner.First).
; Out: BC = the broadcast ARP frame length (the first frame on the wire).
; ---------------------------------------------------------------------------
prov_first:
                ld      hl, 0
                ld      (PROV_IDX), hl          ; idx = 0
                ld      bc, 0
                call    prov_start              ; prov_start(0): per-file re-init
                jp      http_fetch_first        ; ARP, BC = frame length

; ---------------------------------------------------------------------------
; prov_onframe — drive the current file's fetch with one received frame (the Z80
; port of Provisioner.OnFrame). Sets PROV_STATUS; on file completion runs
; store_end (verify + record verdict) and reports AllDone (last file) or FileDone.
; Out: BC = bytes transmitted (the fetch's reply / the FIN-ACK; 0 if none).
; ---------------------------------------------------------------------------
prov_onframe:
                call    http_fetch_onframe      ; BC = tx length (0 if none)
                ld      a, (FETCH_PHASE)
                cp      PH_DONE
                jr      z, prov_onframe_done
                ; still fetching: PROV_STATUS = Continue, BC = the fetch's reply.
                ld      a, PROV_CONTINUE
                ld      (PROV_STATUS), a
                ret
prov_onframe_done:
                ; file complete: close the store (verify + record verdict). store_end
                ; clobbers BC, so save the tx length (this file's FIN-ACK) across it.
                push    bc
                call    store_end
                pop     bc
                ; PROV_STATUS = AllDone if idx+1 == count (last file), else FileDone.
                ld      hl, (PROV_IDX)
                inc     hl                      ; idx+1
                ld      de, (PROV_FILE_COUNT)
                or      a
                sbc     hl, de                  ; Z iff idx+1 == count
                ld      a, PROV_ALL_DONE
                jr      z, prov_onframe_status
                ld      a, PROV_FILE_DONE
prov_onframe_status:
                ld      (PROV_STATUS), a
                ret                             ; BC = the FIN-ACK length

; ---------------------------------------------------------------------------
; prov_next — start the next file's fetch after a FileDone (the Z80 port of
; Provisioner.Next). The caller must have already sent the FileDone FIN-ACK.
; Out: BC = the next file's broadcast ARP frame length.
; ---------------------------------------------------------------------------
prov_next:
                ld      hl, (PROV_IDX)
                inc     hl
                ld      (PROV_IDX), hl          ; idx++
                ld      b, h
                ld      c, l                    ; BC = idx (for prov_start)
                call    prov_start              ; prov_start(idx): per-file re-init
                jp      http_fetch_first        ; ARP, BC = frame length

; Loop state (both builds — prov_* reference these unconditionally).
PROV_IDX:        defw 0                 ; index of the file currently being fetched
PROV_FILE_COUNT: defw FW_FILE_COUNT     ; number of files to fetch (host test sets it)
PROV_STATUS:     defb PROV_CONTINUE     ; last prov_onframe status (Continue/File/All)
