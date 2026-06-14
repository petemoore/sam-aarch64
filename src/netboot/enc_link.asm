; enc_link.asm — ENC28J60 PHY link-up helpers (shared netboot layer, i127).
;
; NOT vendored: this is project code composing the vendored encdrv.asm driver
; primitives (set_bank / wr_ctl_reg / rd_m_reg). It adds the one thing trinload's
; reactive driver never needed and so never provided: a way to wait for the
; Ethernet PHY link to come up before transmitting.
;
; WHY THIS EXISTS (root-caused 2026-06-18 via the i124 boot-path emulation):
; drv_init does NOT wait for the PHY link (it has no PHSTAT2.LSTAT poll), and the
; ENC28J60 has no auto-negotiation — "link up" is 10BASE-T link-pulse detection
; that takes time after init, with no datasheet-stated duration. A transmit issued
; into a not-yet-up link is SILENTLY lost (the datasheet: TXRTS clears + TXIF sets,
; but the bytes never egress). A REACTIVE program (smoke/serve/server — first send
; is a reply to a received frame) never hits this: by the time a frame arrives to
; reply to, the link is necessarily up. A PROACTIVE transmitter (the TFTP client
; ARPs first, right after drv_init) does hit it — its first frame vanishes and,
; with no retransmit, it waits forever. The fix is to make a proactive transmitter
; wait for link, exactly what a reactive one gets for free.
;
; ANY proactive netboot transmitter must `call drv_wait_link` after a successful
; drv_init and before its first drv_write. Reactive programs may call it too
; (harmless — it returns at once once the link is up).
;
; This is the host-verifiable LSTAT-read path (enc28j60.go models the MII read of
; PHSTAT2). The actual link-up TIMING is real silicon — confirmation that gating
; the first transmit on LSTAT cures the hardware hang stays gated on real Trinity
; (CLAUDE.md §5; i127).

; rd_phy_reg — read a 16-bit ENC28J60 PHY register over the MII.
; In:  E = PHY register address (e.g. PHSTAT2 = &11)
; Out: HL = the 16-bit PHY register value (L = MIRDL, H = MIRDH)
; Clobbers: A, BC, DE, HL. Mirrors the vendored wr_phy_reg, but issues an MII READ
; (MICMD.MIIRD) and returns MIRDL/MIRDH instead of writing MIWRL/H. PHY registers
; are not SPI-accessible directly — they go through the bank-2 MII registers, and a
; MAC/MII control-register read returns a leading dummy byte (handled by rd_m_reg).
rd_phy_reg:    PUSH DE
               LD   E,&02          ; bank 2 (MII registers live here)
               CALL set_bank
               POP  DE             ; E = PHY reg address

               LD   D,&14          ; MIREGADR
               CALL wr_ctl_reg     ; select the PHY register to read

               LD   D,&12          ; MICMD
               LD   E,&01          ; MIIRD (bit 0) — start the read
               CALL wr_ctl_reg

               LD   E,&03
               CALL set_bank       ; bank 3 — also the ~10.24us MII settle delay
               LD   D,&0A          ; MISTAT (bank 3)
rphy_wait:     CALL rd_m_reg       ; MAC/MII read (double-clocked dummy)
               BIT  0,E            ; MISTAT.BUSY still set?
               JR   NZ,rphy_wait

               LD   E,&02          ; back to bank 2
               CALL set_bank
               LD   D,&12          ; MICMD
               LD   E,&00          ; clear MIIRD
               CALL wr_ctl_reg

               LD   D,&18          ; MIRDL (low byte of the result)
               CALL rd_m_reg
               LD   L,E
               LD   D,&19          ; MIRDH (high byte)
               CALL rd_m_reg
               LD   H,E
               RET

; drv_wait_link — block until the Ethernet PHY link is up (PHSTAT2.LSTAT), with a
; bounded poll so a disconnected cable can't hang forever.
; In:  drv_init has run successfully.
; Out: BC = 1 if the link came up, BC = 0 if it timed out (caller decides what to
;      do — e.g. paint a distinctive border). Clobbers A, BC, DE, HL.
; PHSTAT2 (PHY &11) bit 10 = LSTAT = "link up"; bit 10 of the 16-bit value is bit 2
; of the high byte (MIRDH), i.e. mask &04.
drv_wait_link: DI
               LD   HL,0
               LD   (dwl_budget),HL ; 65536 poll attempts (~seconds; link-up is far
                                    ; quicker, so this only bounds a no-cable hang)
dwl_loop:      LD   E,&11          ; PHSTAT2
               CALL rd_phy_reg     ; HL = PHSTAT2 value (clobbers all)
               LD   A,H
               AND  &04            ; LSTAT (bit 10 -> MIRDH bit 2)
               JR   NZ,dwl_up      ; link up

               LD   HL,(dwl_budget)
               DEC  HL
               LD   (dwl_budget),HL
               LD   A,H
               OR   L
               JR   NZ,dwl_loop
               LD   BC,0           ; timed out: link never came up
               RET
dwl_up:        LD   BC,1           ; link up
               RET
dwl_budget:    DEFW 0
