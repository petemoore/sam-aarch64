
;fast arrow

               ORG  16384
               DUMP $

               DI
               IN   A,(252)
               AND  31
               OUT  (251),A
               CALL initmouse
               CALL getback
               EI

repeat:        HALT
               CALL putback
               LD   HL,(pos)
               LD   (opos),HL
               CALL getback
               CALL putarrow
               LD   A,(butt.stat)
               CP   7
               JR   NZ,repeat
               XOR  A
               LD   (&5AFD),A
               EI
               RET

opos:          DEFW 0
pos:           DEFW 0
butt.stat:     DEFB 0
buffer:        DEFS 3*8


getback:       LD   HL,(pos)
               SCF
               RR   H
               RR   L
               LD   DE,buffer
               LD   BC,&08FF
getback.1:     LDI
               LDI
               LDI
               LD   A,L
               ADD  125
               LD   L,A
               JR   NC,getback.2
               INC  H
               LD   A,H
               CP   224
               JR   NC,getback.3
getback.2:     DJNZ getback.1
getback.3:     RET

putback:       LD   DE,(opos)
               SCF
               RR   D
               RR   E
               LD   HL,buffer
               LD   BC,&08FF
putback.1:     LDI
               LDI
               LDI
               LD   A,E
               ADD  125
               LD   E,A
               JR   NC,putback.2
               INC  D
               LD   A,D
               CP   224
               JR   NC,putback.3
putback.2:     DJNZ putback.1
putback.3:     RET

putarrow:      LD   HL,(pos)
               SCF
               RR   H
               RR   L
               JP   C,arrowo

arrowe:        LD   A,(HL)
               AND  15
               OR   240
               LD   (HL),A
               LD   A,L
               ADD  128
               LD   L,A
               JR   NC,$+7
               INC  H
               LD   A,H
               CP   224
               RET  NC
               LD   (HL),255
               LD   A,L
               ADD  128
               LD   L,A
               JR   NC,$+7
               INC  H
               LD   A,H
               CP   224
               RET  NC
               LD   (HL),255
               INC  HL
               LD   A,L
               AND  127
               JR   Z,arrowe.1
               LD   A,(HL)
               AND  15
               OR   240
               LD   (HL),A
arrowe.1:      LD   A,L
               ADD  127
               LD   L,A
               JR   NC,$+8
               INC  H
               LD   A,H
               CP   224
               JR   NC,arrowe.end
               LD   (HL),255
               INC  HL
               LD   A,L
               AND  127
               JR   Z,arrowe.2
               LD   (HL),255
arrowe.2:      LD   A,L
               ADD  127
               LD   L,A
               JR   NC,$+8
               INC  H
               LD   A,H
               CP   224
               JR   NC,arrowe.end
               LD   (HL),255
               INC  HL
               LD   A,L
               AND  127
               SCF
               JR   Z,arrowe.3
               LD   (HL),255
               INC  HL
               LD   A,L
               AND  127
               JR   Z,arrowe.3
               LD   A,(HL)
               AND  15
               OR   240
               LD   (HL),A
arrowe.3:      LD   A,L
               ADC  126
               LD   L,A
               JR   NC,$+8
               INC  H
               LD   A,H
               CP   224
               JR   NC,arrowe.end
               LD   (HL),255
               INC  HL
               LD   A,L
               AND  127
               JR   Z,arrowe.4
               LD   A,(HL)
               AND  15
               OR   240
               LD   (HL),A
arrowe.4:      LD   A,L
               ADD  127
               LD   L,A
               JR   NC,$+8
               INC  H
               LD   A,H
               CP   224
               JR   NC,arrowe.end
               LD   A,(HL)
               AND  15
               OR   240
               LD   (HL),A
               INC  HL
               LD   A,L
               AND  127
               JR   Z,arrowe.5
               LD   (HL),255
arrowe.5:      LD   A,L
               ADD  128
               LD   L,A
               JR   NC,$+8
               INC  H
               LD   A,H
               CP   224
               JR   NC,arrowe.end
               LD   A,L
               AND  127
               JR   Z,arrowe.end
               LD   (HL),255
arrowe.end:    RET

arrowo:        LD   A,(HL)
               AND  240
               OR   15
               LD   (HL),A
               LD   A,L
               ADD  128
               LD   L,A
               JR   NC,$+7
               INC  H
               LD   A,H
               CP   224
               RET  NC
               LD   A,(HL)
               AND  240
               OR   15
               LD   (HL),A
               INC  HL
               LD   A,L
               AND  127
               JR   Z,arrowo.1
               LD   A,(HL)
               AND  15
               OR   240
               LD   (HL),A
arrowo.1:      LD   A,L
               ADD  127
               LD   L,A
               JR   NC,$+7
               INC  H
               LD   A,H
               CP   224
               RET  NC
               LD   A,(HL)
               AND  240
               OR   15
               LD   (HL),A
               INC  HL
               LD   A,L
               AND  127
               JR   Z,arrowo.2
               LD   (HL),255
arrowo.2:      LD   A,L
               ADD  127
               LD   L,A
               JR   NC,$+7
               INC  H
               LD   A,H
               CP   224
               RET  NC
               LD   A,(HL)
               AND  240
               OR   15
               LD   (HL),A
               INC  HL
               LD   A,L
               AND  127
               SCF
               JR   Z,arrowo.3
               LD   (HL),255
               INC  HL
               LD   A,L
               AND  127
               JR   Z,arrowo.3
               LD   A,(HL)
               AND  15
               OR   240
               LD   (HL),A
arrowo.3:      LD   A,L
               ADC  126
               LD   L,A
               JR   NC,$+7
               INC  H
               LD   A,H
               CP   224
               RET  NC
               LD   A,(HL)
               AND  240
               OR   15
               LD   (HL),A
               INC  HL
               LD   A,L
               AND  127
               SCF
               JR   Z,arrowo.4
               LD   (HL),255
               INC  HL
               LD   A,L
               AND  127
               JR   Z,arrowo.4
               LD   (HL),255
arrowo.4:      LD   A,L
               ADC  126
               LD   L,A
               JR   NC,$+8
               INC  H
               LD   A,H
               CP   224
               JR   NC,arrowo.end
               LD   A,(HL)
               AND  240
               OR   15
               LD   (HL),A
               INC  HL
               LD   A,L
               AND  127
               JR   Z,arrowo.5
               LD   (HL),255
arrowo.5:      LD   A,L
               ADD  127
               LD   L,A
               JR   NC,$+8
               INC  H
               LD   A,H
               CP   224
               JR   NC,arrowo.end
               LD   A,(HL)
               AND  240
               OR   15
               LD   (HL),A
               INC  HL
               LD   A,L
               AND  127
               SCF
               JR   Z,arrowo.6
               LD   A,(HL)
               AND  240
               OR   15
               LD   (HL),A
               INC  HL
               LD   A,L
               AND  127
               JR   Z,arrowo.6
               LD   A,(HL)
               AND  15
               OR   240
               LD   (HL),A
arrowo.6:      LD   A,L
               ADC  127
               LD   L,A
               JR   NC,$+8
               INC  H
               LD   A,H
               CP   224
               JR   NC,arrowo.end
               LD   A,L
               AND  127
               JR   Z,arrowo.end
               LD   A,(HL)
               AND  240
               OR   15
               LD   (HL),A
               INC  HL
               LD   A,L
               AND  127
               JR   Z,arrowo.end
               LD   A,(HL)
               AND  15
               OR   240
               LD   (HL),A
arrowo.end:    RET

YSENSE:        EQU  10+1
XSENSE:        EQU  10+1

initmouse:     LD   HL,mouser
               LD   (&5AFC),HL     ;mousev
               RET

mouser:
               LD   BC,&FFFE
               IN   A,(C)
               LD   HL,butt.stat
               LD   E,15
               IN   A,(C)
               CPL
               AND  E
               RET  NZ
               IN   A,(C)
               CPL
               AND  E
               LD   (butt.stat),A
               IN   A,(C)
               IN   A,(C)
               ADD  A
               ADD  A
               ADD  A
               ADD  A
               LD   D,A
               IN   A,(C)
               AND  E
               OR   D
               LD   D,A
               LD   A,(pos+1)
               SUB  D
               CP   192
               JR   C,mouse.2
               XOR  A
               BIT  7,D
               JR   Z,mouse.2
mouse.1:       LD   A,191
mouse.2:       LD   (pos+1),A
               IN   A,(C)
               IN   A,(C)
               ADD  A
               ADD  A
               ADD  A
               ADD  A
               LD   D,A
               IN   A,(C)
               AND  E
               OR   D
               LD   D,A
               LD   A,(pos)
               ADD  D
               BIT  7,D
               JR   NC,mouse.3
               JR   NZ,mouse.4
               SBC  A
               CP   A
mouse.3:       JR   Z,mouse.4
               XOR  A
mouse.4:       LD   (pos),A
               RET
