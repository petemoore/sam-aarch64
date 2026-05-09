;Include demo squares. Includes file "square I.S"

               ORG  30000
               DUMP $

               XOR  A              ;signal cls entire screen
               CALL &014E          ;Jp table CLs BlocK
               LD   A,1
               LD   (&5A4D),A      ;set fatpix
               LD   (&5A55),A      ;set over 'XOR'
repeat:        LD   BC,0           ;B=y=0,C=x=0
               LD   HL,&BFFF       ;H=depth=191,L=width=255
nextsquare:    CALL drawsquare
               INC  C              ;x=x+2
               INC  C
               INC  B              ;y=y+2
               INC  B
               LD   DE,-&0404      ;depth=depth-4,width=width-4
               ADD  HL,DE
               LD   A,B            ;Halfway the screen ?
               CP   96
               JR   C,nextsquare   ;another square if not
               LD   HL,&5C3B       ;FLAGS
               BIT  5,(HL)         ;key pressed ?
               JR   Z,repeat       ;repeat sequence not
               RES  5,(HL)         ;reset key
               RET
               INC  "square I.S"   ;include.
                                   ;CALL routines: drawsquare
                                                  ;drawline
