;Include demo square2. Includes file "square I.S"
;works in MODE 3

               ORG  30000
               DUMP $

               XOR  A              ;signal cls entire screen
               CALL &014E          ;Jp table CLs BlocK
               LD   A,-2           ;channel 'S'
               CALL &0112          ;open channel
               LD   A,1
               LD   (&5A4D),A      ;set fatpix
               LD   BC,5*256+2     ;B=y=5,C=x=6
repeat:        PUSH BC
               LD   HL,13*256+23   ;H=depth=13,L=width=23
               CALL drawsquare
               LD   A,22           ;print AT:
               RST  16
               LD   A,B            ;line INT (b/8)+1
               RRCA
               RRCA
               RRCA
               AND  31
               INC  A
               RST  16
               LD   A,C            ;column int (C/4)+1
               RRCA
               RRCA
               AND  63
               INC  A
               RST  16
               LD   DE,message
               LD   BC,messend-message
               CALL &0013          ;print string 'message'
               POP  BC
               LD   A,C            ;next column (7 chars)
               ADD  28
               LD   C,A
               CP   254            ;line full ?
               JR   NZ,repeat      ;jp if not
               LD   C,2            ;left column
               LD   A,B            ;next line (2 chars)
               ADD  16
               LD   B,A
               CP   181            ;out of lines ?
               JR   C,repeat       ;jp if not
               LD   HL,&5C3B       ;FLAGS
wait:          BIT  5,(HL)         ;key pressed ?
               JR   Z,wait         ;wait for key
               RES  5,(HL)         ;reset key
               RET
               INC  "square I.S"   ;include.
                                   ;CALL routines: drawsquare
                                                  ;drawline
message:       DEFM "COMET"
messend:
