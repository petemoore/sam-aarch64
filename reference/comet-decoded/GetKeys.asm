;Simple read key method (as used with COMET) by Edwin Blink.

;A simple test routine

               LD   A,2            ;open channel 'S'
               CALL &0112


wait.key:      CALL test.key       ;call this one to wait
               JR   Z,wait.key     ;until a key is pressed
               CALL hex.byte       ;print HEX value.
               LD   A,13           ;print CR
               RST  16
               JR   wait.key

;- test for a keypress Z no key NZ A=key -

test.key:      LD   HL,&5C3B
               BIT  5,(HL)
               RET  Z
               LD   A,(&5C08)      ;A=key pressed see table
               RES  5,(HL)
               RET

;- print HEX value in A -

hex.byte:      PUSH AF
               RRCA
               RRCA
               RRCA
               RRCA
               CALL hex.dig
               POP  AF

hex.dig:       AND  15
               ADD  "0"
               CP   "9"+1
               JR   C,$+4
               ADD  7
               RST  16
               RET

;key table:

;SPECIAL KEYS       FUNCTION KEYS

;&06 CAPS           &C0 F0
;&07 EDIT           &C1 F1
;&08 CURSOR left    &C2 F2
;&09  " "   right   &C3 F3
;&0A  " "   down    &C4 F4
;&0B  " "   up      &C5 F5
;&0C DELETE         &C6 F6
;&0D RETURN         &C7 F7
;&FC TAB            &C8 F8
;                   &C9 F9

;all other keys ASCII value
