
;Tornado_assembler to COMET source converter.21 June 1991.


               ORG  &7C00
               DUMP $

linebuff:      DEFS 65           ;note at block address
lstartp:       DEFB 5
lstarto:       DEFW 32768
cstartp:       DEFB 1
cstarto:       DEFW 32768
entry:         DI
               LD   A,5           ;set start of tornado_file
               LD   HL,32768
               LD   (lstartp),A
               LD   (lstarto),HL
               LD   A,1           ;set start of COMET file
               OUT  (251),A
               LD   (HL),0
               INC  HL
               LD   (cstartp),A
               LD   (cstarto),HL
nextline:      LD   A,(lstartp)
               OUT  (251),A
               LD   HL,(lstarto)
               LD   DE,linebuff+1 ;set start in line buffer
               LD   A,(HL)        ;fetch line no.
               CP   255
               JP   Z,exit
               CALL skipspaces
               CALL testalfa      ;test for label
               JR   NC,nolabel
               LD   BC,0
               PUSH HL
nxtlabchar:    INC  HL
               INC  C
               LD   A,(HL)
               CALL vallabchar
               JR   C,nxtlabchar
               POP  HL
               LD   A,C
               LD   (DE),A
               INC  E
               LDIR
conloop:       CALL skipspaces
nolabel:       CP   128
               JR   C,notok
               CP   131
               JP   C,movechar
               JR   NZ,tok1
               LD   (DE),A
               INC  E
               LD   A,"'"
               JP   movechar
tok1:          CP   151
               JP   Z,handledefm
               CP   159
               JR   C,dec1
               JR   NZ,tok2
quest:         LD   A,"?"
               JP   movechar
tok2:          CP   170
               JR   C,dec2
               JR   Z,quest
               CP   185
               JR   C,dec3
               JR   Z,dec2
               CP   189
               JR   C,dec1
               JR   NZ,tok3
               LD   A,200       ;PO
               JR   movechar
tok3:          CP   190
               JR   Z,dec2
               CP   223
               JR   C,movechar
               CP   227
               JR   C,inc1
               JR   NZ,movechar
               LD   A,199-2     ;PE-2
inc2:          INC  A
inc1:          INC  A
               JR   movechar
dec3:          DEC  A
dec2:          DEC  A
dec1:          DEC  A
               JR   movechar
notok:         CP   ":"
               JP   Z,linedone-1
testrem:       CP   ";"
               JR   NZ,testhex
               LD   (DE),A
               INC  E
               LD   A,1
remloop:       LD   (DE),A
               INC  E
               INC  HL
               LD   A,(HL)
               CP   128
               JR   C,noexp
               PUSH HL
               LD   HL,tokentab
               SUB  128
               JR   Z,movetok
               LD   B,A
skiptok:       LD   A,(HL)
               INC  HL
               RLA
               JR   NC,skiptok
               DJNZ skiptok
movetok:       LD   A,(HL)
               RLA
               LD   A,(HL)
               JR   C,lastchar
               LD   (DE),A
               INC  E
               INC  HL
               JR   movetok
lastchar:      POP  HL
               AND  127
noexp:         CP   10
               JR   NZ,remend
               INC  HL
               LD   B,(HL)
               DEC  B
               LD   A,32
fillspace:     LD   (DE),A
               INC  E
               DJNZ fillspace
remend:        CP   13
               JR   NZ,remloop
               JR   linedone-1
testhex:       CP   "#"
               JR   NZ,testquote
               LD   A,"&"
               JR   movechar
testquote:     CP   ""
               JR   NZ,testend
               LD   (DE),A
               INC  E
               INC  HL
               LD   A,(HL)
               CP   ""
               JR   Z,movechar
               LD   (DE),A
               INC  E
               INC  HL
               LD   A,(HL)
movechar:      INC  HL
               LD   (DE),A
               INC  E
               JP   conloop
testend:       CP   13
               JR   NZ,movechar
               JR   linedone-1
endquote:      LD   A,32
               LD   (DE),A
               INC  E
               LD   A,""
               LD   (DE),A
               INC  E
               INC  HL
linedone:      BIT  6,H       ;move the line to the end of the
               JR   Z,lindon  ;new source file
               RES  6,H
               LD   A,(lstartp)
               INC  A
               LD   (lstartp),A
lindon:        LD   (lstarto),HL
               LD   HL,(cstarto)
               EX   DE,HL       ;hl>line buff de>source end
               LD   (HL),0      ;set end marker
               LD   A,L         ;increase line length
               INC  A
               LD   L,0
               LD   (HL),A      ;set line length at start
               LD   B,L         ;BC=line length
               LD   C,A
               LD   A,(cstartp) ;select comet source page
               OUT  (251),A
               LDIR              ;move line to source
               EX   DE,HL       ;hl=source end
               BIT  6,H
               JR   Z,movlin
               RES  6,H
               INC  A
               LD   (cstartp),A
movlin:        LD   (cstarto),HL
               JP   nextline

exit:          LD   A,(cstartp)   ;file is converted
               OUT  (251),A
               LD   HL,(cstarto)
               LD   (HL),0         ;set end marker
               INC  HL
               LD   (cstarto),HL  ;set end
               EI
               RET
skipspaces:    LD   A,(HL)
               CP   32
               JR   Z,skipspace
               CP   10
               RET  NZ
               INC  HL
skipspace:     INC  HL
               JR   skipspaces
handledefm:    DEC  A
               LD   (DE),A
               INC  E
               INC  HL
               CALL skipspaces
               LD   A,""
movestring:    LD   (DE),A
               INC  E
               INC  HL
               LD   A,(HL)
               CP   ""
               JP   Z,movechar
               CP   13
               JR   NZ,movestring
               LD   A,""
               LD   (DE),A
               INC  E
               JP   linedone-1
vallabchar:    CALL testalfanum
               RET  C
seperator:     CP   33               ;nc if seperator
               CCF                     ;C if not
               RET  NC
               CP   34
               RET  C
               RET  Z
               CP   36
               RET  C
               CP   46
               CCF
               RET  NC
               RET  Z
               CP   47
               RET  Z
               CP   60
               CCF
               RET  NC
               CP   65
               RET  C
               CP   92
               RET  Z
               SCF
               RET

testalfanum:   CP   "0"
               CCF
               RET  NC
               CP   "9"+1
               RET  C
testalfa:      CP   "A"
               CCF
               RET  NC
               CP   "Z"+1
               RET  C
testlower:     CP   "a"
               CCF
               RET  NC
               CP   "z"+1
               RET
tokentab:      DEFB "A"+128
               DEFB "A","D","C"+128
               DEFB "A","D","D"+128
               DEFB "A","F","'"+128
               DEFB "A","F"+128
               DEFB "A","N","D"+128
               DEFB "B"+128
               DEFB "B","C"+128
               DEFB "B","I","T"+128
               DEFB "C"+128
               DEFB "C","A","L","L"+128
               DEFB "C","C","F"+128
               DEFB "C","P"+128
               DEFB "C","P","D"+128
               DEFB "C","P","D","R"+128
               DEFB "C","P","I"+128
               DEFB "C","P","I","R"+128
               DEFB "C","P","L"+128
               DEFB "D"+128
               DEFB "D","A","A"+128
               DEFB "D","E"+128
               DEFB "D","E","C"+128
               DEFB "D","B"+128
               DEFB "D","M"+128
               DEFB "D","S"+128
               DEFB "D","W"+128
               DEFB "D","I"+128
               DEFB "D","I","S","P"+128
               DEFB "D","J","N","Z"+128
               DEFB "E"+128
               DEFB "E","I"+128
               DEFB "E","N","T"+128
               DEFB "E","Q","U"+128
               DEFB "E","X"+128
               DEFB "E","X","X"+128
               DEFB "H"+128
               DEFB "H","A","L","T"+128
               DEFB "H","L"+128
               DEFB "I"+128
               DEFB "I","M"+128
               DEFB "I","N"+128
               DEFB "I","N","C"+128
               DEFB "I","N","D"+128
               DEFB "I","N","D","R"+128
               DEFB "I","N","I"+128
               DEFB "I","N","I","R"+128
               DEFB "I","X"+128
               DEFB "I","Y"+128
               DEFB "J","P"+128
               DEFB "J","R"+128
               DEFB "L"+128
               DEFB "L","D"+128
               DEFB "L","D","D"+128
               DEFB "L","D","D","R"+128
               DEFB "L","D","I"+128
               DEFB "L","D","I","R"+128
               DEFB "M"+128
               DEFB "N","C"+128
               DEFB "N","E","G"+128
               DEFB "N","O","P"+128
               DEFB "N","V"+128
               DEFB "N","Z"+128
               DEFB "O","R"+128
               DEFB "O","R","G"+128
               DEFB "O","T","D","R"+128
               DEFB "O","T","I","R"+128
               DEFB "O","U","T"+128
               DEFB "O","U","T","D"+128
               DEFB "O","U","T","I"+128
               DEFB "P"+128
               DEFB "P","E"+128
               DEFB "P","O"+128
               DEFB "P","O","P"+128
               DEFB "P","U","S","H"+128
               DEFB "R"+128
               DEFB "R","E","S"+128
               DEFB "R","E","T"+128
               DEFB "R","E","T","I"+128
               DEFB "R","E","T","N"+128
               DEFB "R","L"+128
               DEFB "R","L","A"+128
               DEFB "R","L","C"+128
               DEFB "R","L","C","A"+128
               DEFB "R","L","D"+128
               DEFB "R","R"+128
               DEFB "R","R","A"+128
               DEFB "R","R","C"+128
               DEFB "R","R","C","A"+128
               DEFB "R","R","D"+128
               DEFB "R","S","T"+128
               DEFB "S","B","C"+128
               DEFB "S","C","F"+128
               DEFB "S","E","T"+128
               DEFB "S","L","A"+128
               DEFB "S","P"+128
               DEFB "S","R","A"+128
               DEFB "S","R","L"+128
               DEFB "S","U","B"+128
               DEFB "V"+128
               DEFB "X","O","R"+128
               DEFB "Z"+128

length:        EQU  $-linebuff
