; key_read_test.asm — host-verifiable keyboard sysvar poll for the i138 stub.
;
; Provides two entry points that exercise the i138 LASTK/FLAGS key-injection
; mechanism (harness.go InjectKeys/PendingKeys) without interrupt support:
;
;   key_read_loop   — poll until \r (0x0D) received, storing each non-\r key into
;                     KR_BUF and its count into KR_COUNT.  RET when \r consumed.
;
;   key_poll_once   — single poll: if a key is queued (FLAGS bit 5 set), consume
;                     it and RET Z (key in A + stored at KR_BUF / count = 1);
;                     if no key, RET NZ (A = 0, count unchanged).  Used by the
;                     harness negative-control test (keyboard_test.go) to confirm
;                     that with NO injected keys FLAGS bit 5 reads clear and the
;                     routine does not fabricate a key.
;
; The poll body is a direct port of ROM KYIP2 @ 0x050A
; (docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt lines 1786-1791):
;   BIT 5,(FLAGS) -> if zero no key
;   LD  A,(LASTK) -> LASTK = head of harness queue
;   RES 5,(FLAGS) -> consume (triggers keyQueue advance in harness.go Set())
;
; KYIP2 is inlined because the netboot harness has no ROM (a CALL 0x050A would
; land on uninitialised RAM).  CLAUDE.md rule 6: Go is the encoding authority for
; new design; here the authority is the SAM ROM, and the port is mechanical.
;
; SAM sysvar addresses (SAM Tech Man p.88; ROM disasm lines 1083, 1090):
;   KB_LASTK  0x5C08  last key pressed
;   KB_FLAGS  0x5C3B  status flags; bit 5 (0x20) = key-available
; Note: the Tech Man OCR misprints FLAGS as 503BH at line 4102; 0x5C3B is
; authoritative (every ROM `LD HL,FLAGS` assembles to 21 3B 5C).

KB_LASTK:         equ &5C08        ; LASTK sysvar (SAM Tech Man p.88, ROM :1083)
KB_FLAGS:         equ &5C3B        ; FLAGS sysvar; bit 5 = key-available (ROM :1090)
KR_BUF_MAX:       equ 64          ; maximum keys before \r

; ---------------------------------------------------------------------------
; key_read_loop — poll KB_FLAGS bit 5 in a tight loop; on each key, store it
; in KR_BUF (up to KR_BUF_MAX bytes) and increment KR_COUNT.  Stop on \r.
;
; On entry: KR_BUF and KR_COUNT are zero (the harness has zeroed all RAM).
; On exit:  KR_BUF holds the keys read (excluding the terminating \r);
;           KR_COUNT holds the count of keys stored.
; Clobbers: A, BC, DE, HL.
; ---------------------------------------------------------------------------
key_read_loop:
                ld      de, KR_BUF      ; DE -> write pointer into the buffer
                ld      bc, 0           ; C = count (B stays 0)
krl_poll:
                ld      hl, KB_FLAGS    ; HL -> FLAGS sysvar
                bit     5, (hl)         ; bit 5 set? (key-available, ROM KYIP2 :1788)
                jr      z, krl_poll     ; no key yet — spin (harness step-cap guards runaway)

                ; key is available — consume it (ROM KYIP2 :1789-1791)
                ld      a, (KB_LASTK)   ; A = key (head of harness queue)
                res     5, (hl)         ; RES 5,(FLAGS) — consume: harness.Set pops queue

                cp      &0D             ; CR terminates (not stored)
                jr      z, krl_done

                ld      (de), a         ; store key into KR_BUF
                inc     de
                inc     c               ; count++
                jr      krl_poll

krl_done:
                ld      (KR_COUNT), bc  ; store count (C = count, B = 0, LE word)
                ret

; ---------------------------------------------------------------------------
; key_poll_once — single poll, no loop.  Returns Z if a key was consumed
; (key in A, stored at KR_BUF[0], KR_COUNT = 1), NZ if no key (A = 0).
; Used by keyboard_test.go's negative-control test to confirm that with an
; empty harness queue FLAGS bit 5 reads clear.
; Clobbers: A, HL.
; ---------------------------------------------------------------------------
key_poll_once:
                ld      hl, KB_FLAGS    ; HL -> FLAGS sysvar
                bit     5, (hl)         ; key-available? (ROM KYIP2 :1788)
                jr      z, kpo_none     ; no key — return NZ

                ld      a, (KB_LASTK)   ; A = key
                res     5, (hl)         ; consume (harness.Set pops queue)

                ld      (KR_BUF), a     ; store at KR_BUF[0]
                ld      hl, 1
                ld      (KR_COUNT), hl  ; KR_COUNT = 1
                xor     a               ; Z flag — key consumed
                ret

kpo_none:
                xor     a               ; A = 0
                or      1               ; NZ — no key
                ret

; ---------------------------------------------------------------------------
; Data — buffer and count output for both entry points.
; ---------------------------------------------------------------------------
KR_COUNT:         defs 2               ; number of keys stored (LE word)
KR_BUF:           defs KR_BUF_MAX     ; the stored key bytes (excl. \r)
