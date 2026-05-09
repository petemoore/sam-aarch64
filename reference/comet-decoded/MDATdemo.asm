; MDAT demomstration.

;To demonstrate the MDAT (Merge DATa) instruction, this source
;will merge a screen into the objectcode.
;When this code is executed the screen will be displayed untill
;a key is pressed.
;Before you assemble this source insert a disk with a screen on
;it and enter the file name of a screen in the MDAT instruction.

               ORG  33000          ;code must be above 32767
               DUMP $

start:
               IN   A,(250)        ;save LOMEM
               EX   AF,AF'         ;press SYMBOL E for this one
               LD   HL,screen_start+24576 ;point to palette data
               LD   DE,&55D8       ;palette table
               LD   BC,40          ;40 palettes
               LDIR                 ;move the palette colours
               HALT
               DI
               IN   A,(252)        ;get screen page
               PUSH AF
               AND  31             ;keep the page only
               OR   32             ;RAM at 0 to 16383
               OUT  (250),A        ;select screen page at LOMEM
               OR   64             ;MODE 4
               OUT  (252),A        ;set VPAGE
               LD   HL,screen_start
               LD   DE,0
               LD   BC,24576
               LDIR                 ;move screen data into screen
               EX   AF,AF'         ;restore LOMEM
               OUT  (250),A
               EI
               LD   HL,&5C3B       ;FLAGS
waitkey:       BIT  5,(HL)         ;has a key been pressed ?
               JR   Z,waitkey      ;repeat if not
               RES  5,(HL)         ;reset key
               POP  AF             ;get VPAGE back
               OUT  (252),A        ;and restore VPAGE
               RET

screen_start:  MDAT "screen name"   ;Enter a screen name here
length:        EQU  $-start
