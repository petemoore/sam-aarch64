;Include file Draw square

;draw square at C,B with width L and depth H

drawsquare:
               INC  C
               PUSH BC             ;plot pixel at B,C
               PUSH HL
               CALL &0139          ;Jp table PLOT
               POP  HL
               POP  BC
               DEC  C
               LD   A,C            ;add width to obtain top-
               ADD  L              ;right corner
               LD   C,A
               CALL drawline
               LD   A,B            ;add depth to obtain Bottom-
               ADD  H              ;right corner
               LD   B,A
               CALL drawline
               LD   A,C            ;sub width to obtain Bottom-
               SUB  L              ;left corner
               LD   C,A
               CALL drawline
               LD   A,B            ;sub depth to obtain Top-left
               SUB  H              ;corner
               LD   B,A
drawline:      PUSH BC             ;save coords and depth/width
               PUSH HL
               CALL &013F          ;JP table DrawTo
               POP  HL
               POP  BC
               RET
