
;ROM version 3 auto boot 25 april 1991
;Load samrom 3.0 at 32768
;assemble this
;save rom and program a eprom with the new rom
;The disk will be booted after a reset
;F7 will be DIR for short directory
;F8 will be DIR PEEK SVAR 7 for detailed directory

               ORG  &0F7F
               DUMP $+&8000

initstrt:      RST  48
               DEFW breakpoint       ;call breakpoint
               XOR  A
               CALL &3DB0            ;Print message 0
               LD   HL,&5A34
               LD   A,&82            ;e with accent
               DEC  (HL)
               RST  16
               INC  (HL)
               LD   A,32
               RST  16
               LD   HL,(&5CB4)
               LD   H,0
               INC  L
               ADD  HL,HL
               ADD  HL,HL
               ADD  HL,HL
               ADD  HL,HL
               LD   B,H
               LD   C,L
               RST  48
               DEFW &F5AB            ;call &F5AB Print BC
               LD   A,"K"
               RST  16
wait_key:      CALL &1CB1            ;JREADKEY
               JR   Z,wait_key
               CALL &06B5            ;cls lower
re_entry:      LD   HL,&5600
               DEC  (HL)
               JP   &102F
initend:
               ORG  &D8F9
               DUMP $

bootstrt:      DI
               LD   C,&D0
               CALL sendcom+3
               LD   H,-2
               LD   E,H
               LD   B,5
indexloop:     DEC  HL
               LD   A,H
               OR   L
               JR   NZ,indexcont
               INC  E
               JR   NZ,indexcont
               RST  8
               DEFB &37              ;Missing disk
indexcont:     IN   A,(&E0)
               LD   D,A
               XOR  C
               AND  2
               JR   Z,indexloop
               LD   C,D
               DJNZ indexloop
               CALL track00
               XOR  A
retry:         EX   AF,AF'
               LD   A,1
               OUT  (&E2),A
               LD   A,4
               OUT  (&E3),A
               LD   C,&1B
               CALL track00+2
               LD   C,&80
               CALL sendcom
               LD   H,C
               LD   L,B
               LD   C,&E3
               DEFB &FE
readbyte:      INI
readwait:      IN   A,(&E0)
               BIT  1,A
               JR   NZ,readbyte
               RRCA
               JR   C,readwait
               AND  &0E
               JR   Z,testboot
               EX   AF,AF'
               INC  A
               CP   5
               PUSH AF
               CALL Z,track00
               POP  AF
               CP   10
               JR   C,retry
               RST  8
               DEFB &13              ;Loading error
testboot:      LD   H,&81            ;HL=&8100
               LD   DE,&FB94
               LD   B,4
testchar:      LD   A,(DE)
               CP   (HL)
               JR   Z,$+4
               RST  8
               DEFB &35              ;No DOS
               INC  DE
               INC  HL
               DJNZ testchar
               JP   &8009
sendcom:       CALL wait
               LD   A,C
               OUT  (&E0),A
               LD   B,20
               DJNZ $
               RET
track00:       LD   C,&0B
               CALL sendcom
wait:          IN   A,(&E0)
               RRCA
               RET  NC
               CALL &0E5D
               JR   wait
breakpoint:    LD   A,(&5BC2)
               AND  A
               RET  NZ
               POP  BC
               POP  DE
               LD   HL,re_entry
               EX   (SP),HL
               PUSH DE
               PUSH BC
               LD   HL,&5C3B
               SET  5,(HL)
               LD   L,&08
               LD   (HL),&C9
               RET
bootend:
               DUMP &FBFF

               DEFW 1                ;set F7 to DIR
               DEFB 144
               DEFB &C8              ;set F8 to DIR PEEK SVAR 07
               DEFW 7
               DEFB 144
               DEFB 255
               DEFB 97
               DEFB 255
               DEFB 100
               DEFM "07"
