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

                ; Bootable build (NETBOOT_STREAM, no NETBOOT_HOSTTEST): own the
                ; org so the boot entry is the first instruction at &8000 — the
                ; address the netboot AUTO BASIC CALLs (CALL 32768). tcp_conn.asm
                ; suppresses its own org under NETBOOT_STREAM so this is the single
                ; org. The host-test build keeps tcp_conn as the org provider (this
                ; block is skipped), so its layout is unchanged.
                if defined(NETBOOT_HOSTTEST)==0
                org     &8000
                jp      http_main
                endif

                include "netboot_http.asm"      ; org &8000 + the fetch machine +
                                                ; tcp_conn (+ streaming/verify) +
                                                ; http_get + build_tcp_segment +
                                                ; encdrv + sha256
                include "fw_source.asm"         ; FW_MANIFEST + fw_plan_path
                                                ; (NETBOOT_STANDALONE off -> no org)
                include "body_sink.asm"         ; body_sink_write (header skip);
                                                ; recording doubles (BODY_IN/BODY_OUT)
                                                ; guarded behind NETBOOT_STANDALONE so
                                                ; the composed binary fits the &8000-
                                                ; &10000 window (Brick 3).
                include "enc_link.asm"          ; drv_wait_link (PHY link-up, i127/i128)

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
                endif  ; NETBOOT_HOSTTEST (host store double)
                ; The bootable build's real store_begin / store_end /
                ; storage_sink_leaf (the fw_span + B-DOS HSAVE-per-record leaf) are
                ; in the "Brick 7" bootable section at the foot of this file.

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

; ===========================================================================
; Brick 7 — the real-hardware bootable entry + the B-DOS HSAVE store leaf.
;
; This is the non-host-verifiable half (CLAUDE.md §5): the Trinity EEPROM
; identity read, the ENC28J60 silicon init, and the per-record HSAVE to Trinity
; storage cannot run in the flat-memory koron-go/z80 harness (no ROM / SAMDOS /
; RST 8). It ships UNVERIFIED until exercised on real Trinity hardware. The wire +
; fetch + verify path it drives (prov_first / prov_onframe / prov_next + the
; streaming SHA-256) IS host-verified by the TestProvision*Z80 oracle tests over
; the i80 emulation. Emulation-verified is not hardware-verified.
;
; The store leaf streams each fetched firmware file to Trinity as a sequence of
; bounded HSAVE records (the i99/q16 record-spanning store): conn_accumulate's
; streaming sink flushes one FW_RECORD_CAP-byte window at a time to
; storage_sink_leaf, which hashes the window into the running verify and HSAVEs it
; under the record name <prefix><NNN> (fw_span_record_name). Every record (incl.
; a single-record small file) takes the suffixed <prefix><NNN> form — the serve
; side (i101) reads the same convention from the manifest-known size; both are
; hardware-gated, so the persist/serve naming is consistent rather than matching
; bdos.SpanPlan's plain-name-when-single optimisation.
; ===========================================================================
                if defined(NETBOOT_HOSTTEST)==0

                include "fw_span.asm"          ; fw_span_record_name (<prefix><NNN>)

; The per-record HSAVE capacity (q22 — PROVISIONAL; confirm on Trinity). It is
; the RAM budget for one HSAVE source; the record-split arithmetic is correct for
; any cap, so the value is decoupled (a one-line change). It must be large enough
; that the largest firmware file fits the 1000-record (3-digit index) bound:
; start.elf is ~2.98 MB, so cap >= 2983; 4096 leaves headroom (727 records) and,
; with CONN_FLUSH_BUF = one window + one max inbound payload, keeps the bootable
; inside the &10000 budget.
FW_RECORD_CAP:    equ 4096

; --- the bootable firmware-fetch driver --------------------------------------
; http_main — read the SAM's MAC + IP from the Trinity EEPROM, fill the connection
; config + the per-file base port/ISS, init the ENC28J60, then drive the
; multi-file provisioning loop: fetch each manifest file end to end, streaming its
; body through the SHA-256 verify into bounded HSAVE records on Trinity storage.
; On a bring-up failure it sets a distinctive border colour and halts; on success
; (all files fetched) it sets a green border and halts. CALL 32768 (the boot AUTO
; BASIC) reaches this via the `jp http_main` at &8000.
http_main:
                di
                ; --- locate + read the "Trinity Network " flash chunk ---
                ld      a, 1
                ld      (part), a
                ld      (total), a
                ld      hl, ht_chunk_name
                ld      de, name
                ld      bc, 16
                ldir
                call    find_index
                ld      a, (value)
                and     a
                jp      z, ht_fail_cfg
                call    read_chunk
                ld      a, (value)
                and     a
                jp      z, ht_fail_cfg

                ; copy sam_mac (chunk+0) / sam_ip (chunk+6) into the conn config.
                ld      hl, chunk + 0
                ld      de, CONN_CLIENT_MAC
                ld      bc, 6
                ldir
                ld      hl, chunk + 6
                ld      de, CONN_CLIENT_IP
                ld      bc, 4
                ldir

                ; --- the rest of the connection config (edit for your network) --
                ; per-file base client port (an ephemeral high port), big-endian;
                ; prov_start derives each file's port as BASE_PORT + file index.
                ld      a, &C0
                ld      (BASE_PORT), a
                ld      a, &00
                ld      (BASE_PORT + 1), a
                ; the firmware HTTP server IP (the cdn.githubraw.com proxy / mirror).
                ld      hl, ht_server_ip
                ld      de, CONN_SERVER_IP
                ld      bc, 4
                ldir
                ; the server port = 80 (HTTP), big-endian.
                ld      a, 80 >> 8
                ld      (CONN_SERVER_PORT), a
                ld      a, 80 & &ff
                ld      (CONN_SERVER_PORT + 1), a
                ; per-file base initial send sequence (big-endian); prov_start
                ; derives each file's ISS as BASE_ISS + index*0x10000.
                ld      hl, ht_iss
                ld      de, BASE_ISS
                ld      bc, 4
                ldir

                ; --- arm the streaming sink: each window is one HSAVE record ---
                ld      a, 1
                ld      (CONN_SINK_ENABLED), a
                ld      hl, FW_RECORD_CAP
                ld      (CONN_FLUSH_WINDOW), hl

                ; --- init the ENC28J60 with the SAM's real MAC ----------
                ld      hl, CONN_CLIENT_MAC
                call    drv_init
                ld      a, b
                or      c
                jp      z, ht_fail_init

                ; --- wait for the PHY link before the first (proactive) TX -
                ; prov_first broadcasts an ARP request immediately; a frame sent
                ; before the 10BASE-T link is up is silently dropped, and drv_init
                ; does not wait for link. A reactive server gets this for free (its
                ; first send is a reply); a proactive transmitter (this, like the
                ; TFTP client) must wait. (i127/i128 — root-caused via the i124
                ; boot-path emulation; mirrors netboot_client.asm.)
                call    drv_wait_link
                ld      a, b
                or      c
                jp      z, ht_fail_link

                ; --- drive the multi-file provisioning loop --------------
                call    prov_first              ; file 0's ARP
ht_prov_loop:
                call    prov_onframe            ; one rx frame -> reply; sets PROV_STATUS
                ld      a, (PROV_STATUS)
                cp      PROV_ALL_DONE
                jr      z, ht_done
                cp      PROV_FILE_DONE
                jr      nz, ht_prov_loop        ; PROV_CONTINUE: keep fetching this file
                call    prov_next               ; file complete: next file's ARP
                jr      ht_prov_loop
ht_done:
                ; success: all files fetched + stored. green border, halt.
                ld      a, 4
                out     (&fe), a
                di
                halt

ht_fail_cfg:
                ld      a, 2                    ; red border: no/bad network settings
                out     (&fe), a
                di
                halt
ht_fail_init:
                ld      a, 1                    ; blue border: ENC28J60 init failed
                out     (&fe), a
                di
                halt
ht_fail_link:
                ld      a, 6                    ; yellow border: PHY link never came up
                out     (&fe), a                ; (cable unplugged, or no link partner)
                di
                halt

ht_chunk_name:    defm "Trinity Network "      ; the flash chunk holding MAC+IP
; The fixed firmware-fetch target — edit for your network. Each file's HTTP path +
; Host come from the pinned manifest (fw_source.asm); prov_start wires them.
ht_server_ip:     defb 192, 168, 0, 1          ; the HTTP server (firmware mirror)
ht_iss:           defb 0, 0, 4, 0              ; per-file base initial send sequence (BE)

; --- the real B-DOS HSAVE store leaf (hardware-gated, q16/q22) ----------------
; FW_REC_IDX — the record index within the current file (0-based); store_begin
; resets it, storage_sink_leaf increments it per flushed window.
FW_REC_IDX:        defs 2
; FW_STORE_NAME_PTR — the current file's logical name pointer (the manifest name
; ptr prov_start passes to store_begin); fw_span_record_name builds each record
; name from it.
FW_STORE_NAME_PTR: defs 2

; store_begin(HL = the file's logical name ptr) — open a file in the store:
; remember its name for record naming and reset the record index. No HSAVE yet;
; the body is written window-by-window by storage_sink_leaf as it streams.
; Clobbers HL.
store_begin:
                ld      (FW_STORE_NAME_PTR), hl
                ld      hl, 0
                ld      (FW_REC_IDX), hl
                ret

; store_end — close the current file: finish the streamed-body SHA-256 and compare
; it against the pinned hash (conn_verify_final sets CONN_HASH_MATCH). The fetched
; bytes are already persisted (storage_sink_leaf HSAVE'd each window); this only
; finalises the verify verdict. Acting on a mismatch — reject / retry / surface it
; — is the picker UX (i100c). Clobbers A, BC, DE, HL, IX.
store_end:
                jp      conn_verify_final

; storage_sink_leaf(HL = window ptr, BC = window length) — the per-window store
; leaf the streaming flush reaches (through the body-skip filter). It (1) hashes
; the window into the running verify, then (2) HSAVEs it to Trinity as the next
; bounded record <prefix><NNN>. NOT host-verifiable (no RST 8 / SAMDOS in the
; harness) — unverified until Pete's Trinity test (CLAUDE.md §5).
;
; The HSAVE source-paging (page = ptr>>14, offset = ptr&0x3FFF | &8000) follows
; the assembler's own save sites' convention; the exact physical-page mapping for
; the loaded image is the q16 hardware detail confirmed on Trinity. Clobbers A,
; BC, DE, HL, IX.
storage_sink_leaf:
                ; --- (1) hash this window into the running SHA-256 verify ---
                push    hl                      ; save window ptr across the hash
                push    bc                      ; save window length across the hash
                call    sha256_update           ; hash BC bytes at HL
                pop     bc                      ; restore length
                pop     hl                      ; restore ptr

                ; --- (2) HSAVE this window as the next record ---
                push    hl                      ; save window ptr (HSAVE source addr)
                push    bc                      ; save window length (HSAVE size)
                ; build the record name <prefix><NNN> from the logical name + index.
                ld      hl, (FW_STORE_NAME_PTR) ; HL = logical name ptr
                ld      bc, (FW_REC_IDX)        ; BC = record index
                call    fw_span_record_name     ; FW_SPAN_NAME = <prefix><NNN>
                ld      hl, FW_SPAN_NAME
                ld      (BD_NAME_PTR), hl
                pop     bc                      ; BC = window length (HSAVE size)
                pop     hl                      ; HL = window ptr (HSAVE source addr)
                ld      (BD_SAVE_SIZE), bc      ; HSAVE byte count = window length
                ; page = ptr >> 14 (top 2 bits of H); bdos masks to the low 5 bits.
                ld      a, h
                rlca
                rlca
                and     3
                ld      (BD_SAVE_PAGE), a
                ; section-C source offset = (ptr & 0x3FFF) | &8000.
                ld      a, h
                and     &3F
                or      &80
                ld      h, a
                ld      (BD_SAVE_ADDR), hl
                call    bdos_fill_save_uifa     ; build the HSAVE UIFA
                xor     a
                call    bdos_select_record      ; record 0 = the Trinity flat store
                call    bdos_save_hook          ; HSAVE (real B-DOS only)

                ; --- advance the record index for the next window ---
                ld      hl, (FW_REC_IDX)
                inc     hl
                ld      (FW_REC_IDX), hl
                ret

                endif  ; !NETBOOT_HOSTTEST (bootable entry + store leaf)
