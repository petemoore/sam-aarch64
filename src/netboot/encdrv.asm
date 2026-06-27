; encdrv.asm — Quazar Trinity ENC28J60 Ethernet driver (vendored, verbatim).
;
; PROVENANCE: this file is Simon Owen's ENC28J60 driver from simonowen/trinload
; (https://github.com/simonowen/trinload, commit a4b7af7), reproduced verbatim
; below this header. trinload carries a BSD-style "do what you like" intent (its
; ReadMe.txt). It is the *real wire-I/O driver* the SAM-side netboot stack runs;
; we do NOT reimplement it (memory feedback_go_is_encoding_authority — port /
; reuse the authority, don't reinvent). The netboot packet builders/parsers in
; this directory sit on top of drv_init / drv_read / drv_write.
;
; VERIFICATION: host-verifiable under the i80 Trinity emulation. The Go test rig
; tools/netboot-oracle/z80/enc28j60.go emulates the Trinity microcontroller
; (port &DC select + bit-3 busy + &DD identity probe) and the ENC28J60 SPI chip
; (port &DE: RCR/WCR/RBM/WBM/BFS/BFC, the bank model, the 8 KB RX/TX buffer, the
; MAC/MII read-dummy + double-read) per the ENC28J60 datasheet (DS39662E) and
; the driver's own port usage; enc28j60_test.go assembles this file, runs
; drv_init / drv_write / drv_read against the emulation, and asserts the frame
; bytes in/out are byte-exact. Emulation-verified is NOT hardware-verified — the
; final integration gate stays real Trinity hardware (trinity-capabilities.md).
;
; Port map (trinity-capabilities.md §2): &DC = microcontroller select/status
; (bit 3 = busy), &DD = EEPROM SPI (also the identity-probe reply port),
; &DE = ENC28J60 SPI data. Auto-null &2F removes the per-byte dummy write from
; bulk reads. ENC RX ring &0000-&19FF (6.5 KB), TX &1A00-&1FFF (1.5 KB).
;
; --- begin verbatim simonowen/trinload encdrv.asm @ a4b7af7 ---

; Quazar Trinity ethernet driver for ENC28J60
;
; By Simon Owen <simon@simonowen.com>

rx_start:      EQU  &0000
rx_end:        EQU  &19FF          ; 6.5K for RX
tx_start:      EQU  rx_end+1
tx_end:        EQU  &1FFF          ; 1.5K for TX

max_pkt_len:   EQU  1518

rx_status:     DEFS 6              ; ENC RX status
tx_status:     DEFS 8              ; ENC TX status

read_ptr:      DEFW 0              ; current RX buffer pos
tx_flags:      DEFB &00            ; use &03 to append CRC

; enc_timed_out — set by wait_ready when its busy-poll budget is exhausted (a stuck
; shared-controller busy bit, e.g. the i280b ENC/SD contention after an SD transaction
; left &DC bit 3 hung). Pure reporting: serve_rearm_enc clears it before a re-arm and
; reads it after to emit a timeout marker. The load-bearing effect is that wait_ready
; RETURNS instead of spinning forever, so a stuck bus can no longer wedge the SAM — the
; ENC analogue of the i241 sdc_wait_ready bound (sd_csd.asm).
enc_timed_out: DEFB &00
ENC_BUSY_LIMIT: EQU &FFFF          ; busy-poll budget (~65535, well over any healthy &DC busy period)


; Initialise ENC
; Entry: HL points to MAC address to use
; Exit: BC=1 if successful, BC=0 if Trinity missing or ENC too old
drv_init:      DI

               PUSH HL             ; save MAC pointer
               CALL chk_trinity    ; check for Trinity board
               POP  HL
               JP   NZ,exit_failure

; enc_rx_reestablish — the body of drv_init from here on (ereset + ring/MAC/PHY
; re-arm + RX enable), MINUS chk_trinity. Call it (HL=MAC) to restore the ENC's
; persistent RX state after another peripheral (the SD card) was driven on the
; shared controller — drv_read only restores per-call select/bank, never this
; once-only RX arming, so a full SD transaction kills serving until it is re-run.
; chk_trinity is deliberately NOT re-run (it fails when the PIC is still settling
; from the &38 SD-init, the i242 finding). Falls into exit_success (BC=1, RET).
; (Added label only — the vendored instructions are unchanged.)
enc_rx_reestablish:
               CALL ereset         ; reset ENC
               CALL enulloff       ; auto-nulling off


               LD   E,&03          ; bank 3
               CALL set_bank

               CALL set_mac_addr   ; set MAC from HL


               LD   E,&00          ; bank 0
               CALL set_bank

               LD   HL,rx_start
               LD   D,&08          ; ERXSTL/H (RX start)
               CALL wr_ctl_pair

               LD   HL,rx_end
               LD   D,&0A          ; ERXNDL/H (RX end)
               CALL wr_ctl_pair

               LD   HL,rx_start
               LD   (read_ptr),HL
               LD   D,&0C          ; ERXRDPTL/H (RX buf ptr)
               CALL wr_ctl_pair


               LD   E,&02          ; bank 2
               CALL set_bank

               LD   DE,&000D       ; MACON1 (MARXEN+TX/RXPAUS)
               CALL wr_ctl_reg

               LD   DE,&0232       ; MACON3, pad to 60 + crc
               CALL wr_ctl_reg     ; not bfs on MAC regs!


               LD   HL,&0C12
               LD   D,&06          ; MAIPGL/H (!B2B pkt gap)
               CALL wr_ctl_pair

               LD   HL,&05EE
               LD   D,&0A          ; MAMXFLL/H (max frame len)
               CALL wr_ctl_pair

               LD   DE,&0412       ; MABBIPG (B2B packet gap)
               CALL wr_ctl_reg


               LD   HL,&0000
               LD   E,&00          ; PHCON1
               CALL wr_phy_reg

               LD   HL,&0100       ; HDLDIS
               LD   E,&10          ; PHCON2
               CALL wr_phy_reg

               LD   HL,&3472       ; LEDA:link LEDB:tx/rx
               LD   E,&14          ; PHLCON
               CALL wr_phy_reg


               LD   DE,&1F04       ; ECON1, RX enable
               CALL bfs_ctl_reg

               JP   exit_success

; Read next packet
; Entry: HL points to receive buffer
; Exit: BC holds length read, or zero if nothing available
drv_read:      DI

               LD   E,&01          ; bank 1
               CALL set_bank

               LD   D,&19          ; EPKTCNT (packet count)
               CALL rd_ctl_reg
               INC  E
               DEC  E              ; zero?
               JP   Z,exit_zero    ; jump if nothing to read

               PUSH HL             ; save buffer pointer

               LD   E,&00          ; bank 0
               CALL set_bank

               LD   HL,(read_ptr)  ; current packet offset
               LD   D,&00          ; ERDPTL/H
               CALL wr_ctl_pair

               LD   HL,rx_status
               LD   DE,6           ; ENC packet header
               CALL rd_buf_mem

               LD   HL,(rx_status) ; next packet offset
               LD   (read_ptr),HL  ; save for next time

               LD   HL,(rx_status+2) ; packet length
               LD   DE,-4          ; crc length
               ADD  HL,DE          ; remove from pkt len
               POP  DE             ; caller buffer pointer
               PUSH HL             ; save length
               EX   DE,HL          ; HL=ptr, DE=len
               CALL rd_buf_mem     ; read packet data

               LD   HL,(read_ptr)  ; next free space
               DEC  HL             ; errata requires odd value
               BIT  7,H            ; beyond start?
               JR   Z,no_ptr_wrap
               LD   HL,tx_end      ; wrap to end position
no_ptr_wrap:   LD   D,&0C          ; ERXRDPTL/H (bank 0)
               CALL wr_ctl_pair

               LD   DE,&1E40       ; ECON2, PKTDEC
               CALL bfs_ctl_reg    ; decrement packet count

               POP  BC             ; restore length
               LD   HL,rx_status   ; status details
               EI
               RET

; Transmit a packet, waiting until sent
; Entry: HL points to packet to sent, BC holds length
; Exit: BC=1 for success, 0 if failed (tx_status has details)
drv_write:     DI
               PUSH HL
               PUSH BC

               LD   E,&00          ; bank 0
               CALL set_bank

               LD   HL,tx_start
               LD   D,&02          ; EWRPTL/H (write ptr)
               CALL wr_ctl_pair

               LD   HL,tx_start
               LD   D,&04          ; ETXSTL/H (TX start)
               CALL wr_ctl_pair

               LD   HL,1+tx_start-1
               POP  DE             ; packet length
               PUSH DE
               ADD  HL,DE          ; point to final byte
               LD   D,&06          ; ETXNDL/H (TX end)
               CALL wr_ctl_pair

               LD   HL,tx_flags    ; TX control flags
               LD   DE,1
               CALL wr_buf_mem

               POP  DE             ; packet length
               POP  HL             ; packet to send
               LD   (tx_len_patch+1),DE ; save length for later
               CALL wr_buf_mem


               LD   HL,16          ; 16 TX attempts
tx_retry:
               ; R5 errata fix to clear stuck transmit logic
               LD   DE,&1F80       ; ECON1, TXRST bit
               CALL bfs_ctl_reg    ; reset TX logic

               LD   DE,&1F80       ; ECON1, TXRST bit
               CALL bfc_ctl_reg    ; clear TX reset

               LD   DE,&1C0A       ; EIR, TXIF+TXERIF bits
               CALL bfc_ctl_reg    ; clear TX+error ints


               LD   DE,&1F08       ; ECON1, TXRTS bit
               CALL bfs_ctl_reg    ; transmit!

               LD   A,2
               OUT  (&FE),A        ; red border during tx

               LD   D,&1C          ; EIR
wait_sent:     CALL rd_ctl_reg     ; read interrupt status
               LD   A,E
               AND  &0A            ; TXIF+TXERIF
               JR   Z,wait_sent    ; loop if still sending

               XOR  A
               OUT  (&FE),A        ; black border after tx

               LD   DE,&1F08       ; ECON1, TXRTS bit
               CALL bfc_ctl_reg    ; transmit finished


               PUSH HL             ; save retry count
               LD   HL,tx_start+1
tx_len_patch:  LD   DE,0           ; length patched above
               ADD  HL,DE          ; calc TX status
               LD   D,&00          ; ERDPTL/H
               CALL wr_ctl_pair
               LD   HL,tx_status
               LD   DE,8
               CALL rd_buf_mem     ; read status
               POP  HL             ; restore retry count

               LD   D,&1C          ; EIR
               CALL rd_ctl_reg
               BIT  1,E            ; TXERIF? (transmit error)
               JR   Z,tx_success   ; all done if no error

               LD   A,1
               OUT  (&FE),A        ; blue border for tx error

               LD   A,(tx_status+3)
               BIT  5,A            ; late TX collision?
               JR   Z,tx_success   ; all done if no collision

               LD   A,6
               OUT  (&FE),A        ; yellow border for tx collision

               DEC  L              ; retry counter
               JR   NZ,tx_retry    ; try again?

               LD   A,7
               OUT  (&FE),A        ; white border for tx failure

               JR   exit_failure   ; failed!

tx_success:    LD   HL,tx_status   ; status details
               JR   exit_success


; Close ENC link, disabling reception
drv_exit:      DI
               CALL ereset
exit_success:  LD   BC,1
               EI
               RET

exit_failure:
exit_zero:     LD   BC,0
               EI
               RET


set_bank:      PUSH DE
               LD   DE,&1F03       ; ECON1, BSEL1+BSEL0
               CALL bfc_ctl_reg    ; clear existing bank bits
               POP  DE
               LD   D,&1F          ; ECON1 (bfc changed D)
               JR   bfs_ctl_reg    ; set new bank bits

rd_ctl_pair:   CALL rd_ctl_reg
               LD   L,E
               INC  D
               CALL rd_ctl_reg
               LD   H,E
               RET

rd_ctl_reg:    CALL eon
               CALL wait_ready
               LD   BC,&00DE
               OUT  (C),D
               CALL wait_ready
               OUT  (C),B          ; dummy 0 for read
               CALL wait_ready
               IN   E,(C)
               JP   eoff

rd_m_reg:      CALL eon
               CALL wait_ready
               LD   BC,&00DE
               OUT  (C),D
               CALL wait_ready
               OUT  (C),B          ; dummy 0 for read
               CALL wait_ready
               OUT  (C),B          ; double-read for M reg
               CALL wait_ready
               IN   E,(C)
               JP   eoff

wr_ctl_pair:   LD   E,L
               CALL wr_ctl_reg
               INC  D
               LD   E,H
               ; fall through...

wr_ctl_reg:    SET  6,D            ; WCR opcode
wr_reg:        CALL eon
               LD   C,&DE
               CALL wait_ready
               OUT  (C),D
               CALL wait_ready
               OUT  (C),E
               JP   eoff

bfc_ctl_reg:   SET  5,D            ; BFC opcode (bits 5+7)
bfs_ctl_reg:   SET  7,D            ; BFS opcode
               JR   wr_reg

wr_phy_reg:    PUSH DE
               LD   E,&02          ; bank 2
               CALL set_bank
               POP  DE             ; E = PHY reg

               LD   D,&14          ; MIREGADR
               CALL wr_ctl_reg     ; PHY reg select

               LD   D,&16          ; MIWRL/H
               CALL wr_ctl_pair    ; write HL

               LD   E,&03
               CALL set_bank       ; enough to delay 10.24us

               LD   D,&0A          ; MISTAT
wr_phy_wait:   CALL rd_m_reg
               BIT  0,E
               JR   NZ,wr_phy_wait
               RET

rd_buf_mem:    CALL eon
               CALL wait_ready
               LD   BC,&3ADE       ; RBM opcode, ENC port
               OUT  (C),B
               DEC  DE             ; read all but final byte
               LD   B,E            ; Count LSB
               INC  D              ; Count MSB+1
               LD   E,0            ; dummy 0

               CALL enullon        ; auto-nulling on

               LD   A,4
               OUT  (&FE),A        ; green border during rx

rd_buf_lp:     IN   A,(&DC)
               AND  %00001000      ; busy?
               JR   NZ,rd_buf_lp   ; if so, wait

               OUT  (&FE),A        ; black border after rx

               INI                 ; read buffer byte
               JR   NZ,rd_buf_lp
               DEC  D              ; next block of 256
               JR   NZ,rd_buf_lp

               CALL enulloff       ; auto-nulling off

               CALL wait_ready
               INI                 ; read last byte
               JR   eoff

wr_buf_mem:    CALL eon
               CALL wait_ready
               LD   BC,&7ADE       ; WBM opcode, ENC port
               OUT  (C),B
               LD   B,E            ; Count LSB
               INC  D              ; Count MSB+1

wr_buf_lp:     IN   A,(&DC)
               AND  %00001000      ; busy?
               JR   NZ,wr_buf_lp   ; if so, loop

               OUTI                ; write buffer byte
               JR   NZ,wr_buf_lp
               DEC  D              ; next block of 256
               JR   NZ,wr_buf_lp
               JR   eoff

; wait_ready — busy-poll &DC bit 3 (the shared-controller busy flag) before each SPI
; byte, now BOUNDED (i280b-b2c): up to ENC_BUSY_LIMIT polls, then give up (set
; enc_timed_out) and RETURN rather than spin forever. On a healthy bus bit 3 clears in
; a poll or two so the budget is never approached; the bound only bites when the shared
; Trinity controller is wedged (the i280b ENC/SD contention shape), converting a hard
; hang into a recoverable timeout. A=0 on the ready exit (unchanged); preserves BC.
wait_ready:    PUSH BC
               LD   BC,ENC_BUSY_LIMIT
ewr_loop:      IN   A,(&DC)
               AND  %00001000
               JR   Z,ewr_ready    ; bit 3 clear -> ready (A=0)
               DEC  BC
               LD   A,B
               OR   C
               JR   NZ,ewr_loop
               LD   A,1
               LD   (enc_timed_out),A   ; budget exhausted -> report + bail (no wedge)
ewr_ready:     POP  BC
               RET

eon:           CALL wait_ready
               LD   A,%00100001    ; ENC enable
               OUT  (&DC),A
               RET

eoff:          CALL wait_ready
               LD   A,%00100000    ; ENC disable
               OUT  (&DC),A
               RET

epulse:        CALL wait_ready
               LD   A,%00100011    ; ENC disable+enable
               OUT  (&DC),A
               RET

ereset:        CALL eoff
               CALL wait_ready
               LD   A,%00101000    ; ENC reset
               OUT  (&DC),A

               LD   B,&00          ; Errata says polling
               DJNZ $              ; ESTAT.CLKRDY is unreliable
               DJNZ $              ; so wait 1ms+ after reset
               RET

enullon:       CALL wait_ready
               LD   A,&2F          ; Ethernet auto-nulling on
               OUT  (&DC),A
               RET

enulloff:      CALL wait_ready
               LD   A,&04          ; Ethernet auto-nulling off
               OUT  (&DC),A
               RET

set_mac_addr:  LD   D,&04          ; [4] (register aren't in order!)
               LD   E,(HL)
               CALL wr_ctl_reg
               INC  HL
               INC  D              ; [5]
               LD   E,(HL)
               CALL wr_ctl_reg
               INC  HL
               LD   D,&02          ; [2]
               LD   E,(HL)
               CALL wr_ctl_reg
               INC  HL
               INC  D              ; [3]
               LD   E,(HL)
               CALL wr_ctl_reg
               INC  HL
               LD   D,&00          ; [0]
               LD   E,(HL)
               CALL wr_ctl_reg
               INC  HL
               INC  D              ; [1]
               LD   E,(HL)
               JP   wr_ctl_reg

; Check if Trinity board is installed by reading the board
; signature, with fixed delays rather than checking BUSY status
chk_trinity:   LD   HL,&0809       ; locations for T and R
               LD   BC,&00DC       ; 256 counter, ENC port
               OUT  (C),H          ; select 8
               DJNZ $              ; delay
               INC  C
               IN   D,(C)          ; read T
               DJNZ $              ; delay
               DEC  C
               OUT  (C),L          ; select 9
               DJNZ $              ; delay
               INC  C
               IN   E,(C)          ; read R
               LD   HL,&5452       ; TR
               AND  A
               SBC  HL,DE          ; Z if found
               RET
