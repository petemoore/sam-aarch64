; eeprom.asm — Quazar Trinity EEPROM (flash) functions (vendored, verbatim).
;
; PROVENANCE: this file is Colin Piggot's Trinity EEPROM library from
; simonowen/trinload (https://github.com/simonowen/trinload, commit a4b7af7),
; reproduced verbatim below this header. trinload carries a BSD-style "do what
; you like" intent (its ReadMe.txt). It reads the SAM's network settings (MAC +
; IP) from the Trinity flash "Trinity Network " chunk so the netboot program
; uses the SAM's real identity on hardware — we do NOT reimplement it (memory
; feedback_go_is_encoding_authority — port / reuse the authority).
;
; The bring-up smoke test (smoke_test.asm) and the netboot server use
; find_index + read_chunk to load the "Trinity Network " chunk: sam_mac =
; chunk+0 (6 bytes), sam_ip = chunk+6 (4 bytes), matching trinload.asm:414-415.
;
; VERIFICATION: NOT host-verifiable — the koron-go/z80 harness has no Trinity
; EEPROM behind port &DD, and the exact chunk layout / sam_ip offset is a flagged
; hardware unknown (netboot impl plan §7.5 #4: trinload's own sanity check reads
; sam_ip+6, internally inconsistent with the 4-byte IP). The EEPROM read is
; exercised only on real Trinity hardware — the smoke test's bootable path reads
; it, but the host harness drives the smoke logic from a fixed CONFIG block
; instead. Emulation-verified is not hardware-verified (CLAUDE.md §5).
;
; This file is data + routines with no `org`; a caller (smoke_test.asm) includes
; it after its own code so its `chunk`/`name`/`part`/`total`/`value` storage and
; the find_index/read_chunk routines share the program's address space.
;
; --- begin verbatim simonowen/trinload eeprom.asm @ a4b7af7 ---

; --------------------------------------------------------------
;
; Trinity EEPROM functions by Colin Piggot
;
; --------------------------------------------------------------

;               ORG  32768
;               DUMP 32768

; BASIC jump table.
;
; The combined-bootblock build (SAMBOOT_BOOTBLOCK, src/netboot/samboot_bootblock.asm)
; reuses only the read-only closure of this file and must fit the bootblock's 674
; free bytes, so it gates out the BASIC entry table (it has no BASIC caller) and
; the write/delete/find-empty/read-index paths (auto-boot reads only). Every guard
; below changes ZERO bytes for the existing includers (none define
; SAMBOOT_BOOTBLOCK): the gated-out spans are kept under `defined(...)==0`.
               if defined(SAMBOOT_BOOTBLOCK)==0
               JP   count_empty          ; 32768
               JP   find_empty           ; 32771
               JP   find_index           ; 32774
               JP   delete_index         ; 32777
               JP   read_index           ; 32780
               JP   read_chunk           ; 32783
               JP   write_index          ; 32786
               JP   write_chunk          ; 32789
               endif

; Input and return value + the index/chunk scratch. In the bootblock build these
; relocate to a RAM scratch home (SAMBOOT_SCRATCH, defined by the includer) so the
; 1 KB chunk buffer + the index/name storage stay OUT of the flashed image. The EQU
; layout mirrors the verbatim DEFB/DEFS sizes byte-for-byte; index_store (below)
; continues that layout at SAMBOOT_SCRATCH+1089. RAM scratch is emulation-valid (flat
; RAM in the harness); the hardware-safe address is confirmed at i230 (Pete present).
               if defined(SAMBOOT_BOOTBLOCK)
value:         equ SAMBOOT_SCRATCH+0      ; 1 byte
part:          equ SAMBOOT_SCRATCH+1      ; 1 byte
total:         equ SAMBOOT_SCRATCH+2      ; 1 byte
name:          equ SAMBOOT_SCRATCH+3      ; 16 bytes
description:   equ SAMBOOT_SCRATCH+19     ; 46 bytes
chunk:         equ SAMBOOT_SCRATCH+65     ; 1024 bytes (ends SAMBOOT_SCRATCH+1089)
               else
value:         DEFB 0                    ; 32792

; 64 byte index header

part:          DEFB 0                    ; 32793
total:         DEFB 0
name:          DEFS 16                   ; 32795
description:   DEFS 46

; 1024 byte data chunk

chunk:         DEFS 1024                 ; 32857
               endif


               if defined(SAMBOOT_BOOTBLOCK)==0
; --------------------------------------------------------------
;
; count_empty - count the number of free chunks in the EEPROM
;               and return the number in 'value'

count_empty:
               XOR  A
               LD   (value),A

               LD   HL,0
               LD   B,120
               LD   DE,64
               LD   C,&DD
empty_loop:
               CALL eeprom_enable
               LD   A,&03
               OUT  (C),A
               CALL wait_ready
               XOR  A
               OUT  (C),A
               CALL wait_ready
               OUT  (C),H
               CALL wait_ready
               OUT  (C),L
               CALL wait_ready
               OUT  (C),A
               CALL wait_ready
               IN   A,(C)
               CP   0
               JR   Z,empty_yes
               CP   255
               JR   NZ,empty_skip
empty_yes:
               LD   A,(value)
               INC  A
               LD   (value),A
empty_skip:
               CALL eeprom_disable
               ADD  HL,DE
               DJNZ empty_loop
               RET



; --------------------------------------------------------------
;
; find_empty - find the first free chunk in the EEPROM and
;              return the number in 'value'. 0 is returned if
;              no empty space

find_empty:
               LD   A,1
               LD   (value),A
               LD   HL,0
               LD   B,120
               LD   DE,64
               LD   C,&DD
find_loop:
               CALL eeprom_enable
               LD   A,&03
               OUT  (C),A
               CALL wait_ready
               XOR  A
               OUT  (C),A
               CALL wait_ready
               OUT  (C),H
               CALL wait_ready
               OUT  (C),L
               CALL wait_ready
               OUT  (C),A
               CALL wait_ready
               IN   A,(C)
               CP   0
               JP   Z,exit
               CP   255
               JP   Z,exit

               CALL eeprom_disable
               LD   A,(value)
               INC  A
               LD   (value),A
               ADD  HL,DE
               DJNZ find_loop
               XOR  A
               LD   (value),A
               RET

               endif                     ; count_empty / find_empty (unused by the bootblock)



; --------------------------------------------------------------
;
; find_index - search the index table to match the part number,
;              total number and name and return the number.
;              0 is returned if not found.

find_index:
               LD   A,1
               LD   (value),A
               LD   HL,0
               LD   B,120
               LD   DE,64
               LD   C,&DD
index_loop:
               CALL eeprom_enable
               LD   A,&03
               OUT  (C),A
               CALL wait_ready
               XOR  A
               OUT  (C),A
               CALL wait_ready
               OUT  (C),H
               CALL wait_ready
               OUT  (C),L
               CALL wait_ready

               PUSH BC
               PUSH HL

               LD   HL,index_store
               LD   B,18

index_loop2:   OUT  (C),D
               CALL wait_ready
               INI
               LD   A,B
               JR   NZ,index_loop2

               POP  HL
               POP  BC

               CALL eeprom_disable
               JP   check_index

index_back:
               LD   A,(value)
               INC  A
               LD   (value),A
               ADD  HL,DE
               DJNZ index_loop
               XOR  A
               LD   (value),A
               RET

check_index:
               PUSH BC
               PUSH HL
               PUSH DE

               LD   DE,index_store
               LD   HL,part
               LD   B,18
check_loop:
               LD   A,(DE)
               CP   (HL)
               JR   NZ,check_return

               INC  HL
               INC  DE
               DJNZ check_loop

               POP  DE
               POP  HL
               POP  BC
               RET

check_return:
               POP  DE
               POP  HL
               POP  BC
               JP   index_back

               if defined(SAMBOOT_BOOTBLOCK)
index_store:   equ SAMBOOT_SCRATCH+1089   ; 18 bytes, continuing the relocated layout
               else
index_store:   DEFS 18
               endif

               if defined(SAMBOOT_BOOTBLOCK)==0
; --------------------------------------------------------------
;
; delete_index - delete a chunk entry from the index table

delete_index:
               LD   A,(value)
               CALL get_index

               CALL write_enable
               CALL eeprom_enable

               LD   C,&DD
               LD   A,&02
               OUT  (C),A
               CALL wait_ready
               XOR  0
               OUT  (C),A
               CALL wait_ready
               OUT  (C),H
               CALL wait_ready
               OUT  (C),L
               CALL wait_ready
               XOR  A
               OUT  (C),A
               CALL wait_ready
               OUT  (C),A
               CALL wait_ready

               CALL eeprom_disable
               CALL write_delay
               RET

; --------------------------------------------------------------
;
; read_index - read the part, total, name and description for
;              for the chunk number in 'value'

read_index:
               LD   A,(value)
               CALL get_index
               CALL eeprom_enable

               LD   BC,&40DD
               LD   E,0

               LD   A,&03
               OUT  (C),A
               CALL wait_ready
               XOR  A
               OUT  (C),A
               CALL wait_ready
               OUT  (C),H
               CALL wait_ready
               OUT  (C),L
               CALL wait_ready

               LD   HL,part
read_iloop:
               OUT  (C),E
               CALL wait_ready
               INI
               CALL wait_ready
               LD   A,B
               CP   0
               JR   NZ,read_iloop

               JP   exit

               endif                     ; delete_index / read_index (unused by the bootblock)

; --------------------------------------------------------------
;
; read_chunk - read the 1K data chunk for the chunk number in
;              in 'value'

read_chunk:
               LD   A,(value)
               CALL get_chunk
               CALL eeprom_enable

               LD   BC,&00DD
               LD   DE,&0400

               LD   A,&03
               OUT  (C),A
               CALL wait_ready
               OUT  (C),H
               CALL wait_ready
               OUT  (C),L
               CALL wait_ready
               OUT  (C),E
               CALL wait_ready

               LD   HL,chunk
read_cloop:
               OUT  (C),E
               CALL wait_ready
               INI
               LD   A,B
               CP   0
               JR   NZ,read_cloop
               DEC  D
               JR   NZ,read_cloop

               JP   exit


               if defined(SAMBOOT_BOOTBLOCK)==0
; --------------------------------------------------------------
;
; write_index - write an index extry for chunk number in 'value'
;               using data from part, total, name, description

write_index:
               LD   A,(value)
               CALL get_index

               CALL write_enable
               CALL eeprom_enable

               LD   BC,&40DD

               LD   A,&02
               OUT  (C),A
               CALL wait_ready
               XOR  A
               OUT  (C),A
               CALL wait_ready
               OUT  (C),H
               CALL wait_ready
               OUT  (C),L
               CALL wait_ready

               LD   HL,part
write_iloop:
               OUTI
               CALL wait_ready
               LD   A,B
               CP   0
               JR   NZ,write_iloop

               CALL eeprom_disable
               CALL write_delay
               RET




; --------------------------------------------------------------
;
; write_chunk - write 1K data chunk, in chunk number from
;               'value'

write_chunk:
               LD   A,(value)
               CALL get_chunk

               LD   DE,chunk
               CALL write_256
               INC  L
               CALL write_256
               INC  L
               CALL write_256
               INC  L
               CALL write_256
               RET

write_256:
               CALL write_enable
               CALL eeprom_enable
               LD   BC,&00DD

               LD   A,&02
               OUT  (C),A
               CALL wait_ready
               OUT  (C),H
               CALL wait_ready
               OUT  (C),L
               CALL wait_ready
               XOR  A
               OUT  (C),A
               CALL wait_ready

               EX   DE,HL
write_cloop1:
               OUTI
               CALL wait_ready
               LD   A,B
               CP   0
               JR   NZ,write_cloop1

               CALL eeprom_disable
               CALL write_delay
               EX   DE,HL
               RET

               endif                     ; write_index / write_chunk / write_256 (unused by the bootblock)


; --------------------------------------------------------------
;
; Common sub routines used by the main functions above for
; controlling EEPROM operation on the Trinity interface

eeprom_enable:
               LD   A,&11
               OUT  (&DC),A
               JP   wait_ready

eeprom_disable:
               LD   A,&10
               OUT  (&DC),A
               JP   wait_ready

exit:
               CALL eeprom_disable
               CALL write_disable
               JP   wait_ready

;wait_ready:
;               IN   A,(&DC)
;               AND  &08
;               JR   NZ,wait_ready
;               RET

               if defined(SAMBOOT_BOOTBLOCK)==0
get_index:
               LD   HL,-64
               LD   DE,64
               LD   B,A
get_loop:
               ADD  HL,DE
               DJNZ get_loop
               RET
               endif                     ; get_index (used only by the gated index/write paths)

get_chunk:
               LD   HL,28
               LD   DE,4
               LD   B,A
chunk_loop:
               ADD  HL,DE
               DJNZ chunk_loop
               RET

               if defined(SAMBOOT_BOOTBLOCK)==0
write_delay:
               PUSH BC
               LD   BC,16
delay_loop:
               DJNZ delay_loop
               DEC  C
               JR   NZ,delay_loop
               POP  BC
               RET

write_enable:
               CALL eeprom_enable
               LD   A,&06
               OUT  (&DD),A
               CALL wait_ready
               CALL eeprom_disable
               RET
               endif                     ; write_delay / write_enable (unused by the bootblock)

write_disable:
               CALL eeprom_enable
               LD   A,&04
               OUT  (&DD),A
               CALL wait_ready
               CALL eeprom_disable
               RET
