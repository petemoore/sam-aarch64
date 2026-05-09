# FRED 56 — disk-format inspection and headless SimCoupé smoke test

Date: 2026-05-10
Goal: Use a known-good SAM Coupé magazine disk (FRED 56) to (a) figure out how
real SAM disks bundle SAMDOS, and (b) confirm that a clean, vanilla SimCoupé
running headlessly under Xvfb can boot a real disk image. This decouples our
"handcrafted MGT image won't boot" question into the disk-format vs the
emulator-environment axes.

## File obtained

Directory listing of `https://ftp.nvg.ntnu.no/pub/sam-coupe/disks/magazines/Fred/`
shows only one FRED 56 archive: `fred56.zip`.

```
$ curl -sO https://ftp.nvg.ntnu.no/pub/sam-coupe/disks/magazines/Fred/fred56.zip
$ unzip -o fred56.zip
  inflating: FRED56.DSK
$ ls -la FRED56.DSK
-rw-rw-rw-  1 pmoore  wheel  819200 30 Mar  2000 FRED56.DSK
```

819200 bytes = the canonical raw MGT image size (80 cyl x 2 sides x 10 sec x 512).
The `.DSK` extension here is a misnomer for `.mgt` (renamed copy used as `fred56.mgt`).

## Step 1 — `samfile ls` directory listing

```
"\x7f FRED56 \x7f"
  Type:                              Code
  Start:                             229385
  Length:                            8078

"CrunchCode"
  Type:                              Code
  Start:                             32768
  Length:                            782

"Screens"
  Type:                              SAM BASIC
  Program length:                    33297
  Numeric variables offset:          33480
  String/array variables offset:     33901
  Start:                             23765
  Length:                            1133
  Start Line:                        0

"BITS.$"
  Type:                              Code
  Start:                             40000
  Length:                            4318

"BITS"
  Type:                              SAM BASIC
  Program length:                    35820
  Numeric variables offset:          36073
  String/array variables offset:     36424
  Start:                             23765
  Length:                            3933
  Start Line:                        10

"DocReader"
  Type:                              Code
  Start:                             32768
  Length:                            3859

"E-Code"
  Type:                              Code
  Start:                             131072
  Length:                            4819

"ADVERTS"
  Type:                              SAM BASIC
  Program length:                    33263
  Numeric variables offset:          33446
  String/array variables offset:     33867
  Start:                             23765
  Length:                            1099
  Start Line:                        0

"decruncher"
  Type:                              Code
  Start:                             16384
  Length:                            258

"MODS"
  Type:                              SAM BASIC
  Program length:                    34095
  Numeric variables offset:          34217
  String/array variables offset:     34699
  Start:                             23765
  Length:                            1976
  Start Line:                        0

"e-basic2"
  Type:                              SAM BASIC
  Program length:                    36993
  Numeric variables offset:          37215
  String/array variables offset:     37597
  Start:                             23765
  Length:                            4979
  Start Line:                        10

"e-scr.$"
  Type:                              Code
  Start:                             278528
  Length:                            7100
  Execution Address:                 278528

"s5"
  Type:                              Code
  Start:                             40000
  Length:                            14562

"Ad1"
  Type:                              Code
  Start:                             40000
  Length:                            7279

"ad2"
  Type:                              Code
  Start:                             40000
  Length:                            8445

"ad3"
  Type:                              Code
  Start:                             40000
  Length:                            4567

"AUTOFRED.."
  Type:                              SAM BASIC
  Program length:                    33842
  Numeric variables offset:          33965
  String/array variables offset:     34446
  Start:                             23765
  Length:                            1693
  Start Line:                        10

"TNTAD"
  Type:                              SAM BASIC
  Program length:                    34656
  Numeric variables offset:          34748
  String/array variables offset:     36812
  Start:                             23765
  Length:                            4044
  Start Line:                        40

"tntad1"
  Type:                              Code
  Start:                             278528
  Length:                            2408
  Execution Address:                 278528

"tntad2"
  Type:                              Code
  Start:                             278528
  Length:                            2806
  Execution Address:                 278528

"tntad3"
  Type:                              Code
  Start:                             278528
  Length:                            3528
  Execution Address:                 278528

"tntad4"
  Type:                              Code
  Start:                             278528
  Length:                            6325
  Execution Address:                 278528

"e1"
  Type:                              Code
  Start:                             49152
  Length:                            3373

"e-text"
  Type:                              Code
  Start:                             85836
  Length:                            5645

"IMPLODER"
  Type:                              SAM BASIC
  Program length:                    33353
  Numeric variables offset:          33445
  String/array variables offset:     35509
  Start:                             23765
  Length:                            3524
  Start Line:                        30

"IMPLO1.BIN"
  Type:                              Code
  Start:                             67019
  Length:                            2562

"DEBUT"
  Type:                              SAM BASIC
  Program length:                    33126
  Numeric variables offset:          33243
  String/array variables offset:     33730
  Start:                             23765
  Length:                            1089
  Start Line:                        0

"DEBUT.BAS"
  Type:                              SAM BASIC
  Program length:                    39946
  Numeric variables offset:          40196
  String/array variables offset:     40550
  Start:                             23765
  Length:                            98134
  Start Line:                        0

"MIALL-LOAD"
  Type:                              SAM BASIC
  Program length:                    33457
  Numeric variables offset:          33549
  String/array variables offset:     34061
  Start:                             23765
  Length:                            1293
  Start Line:                        0

"MIALL"
  Type:                              SAM BASIC
  Program length:                    105886
  Numeric variables offset:          106016
  String/array variables offset:     106490
  Start:                             23765
  Length:                            24570
  Start Line:                        65535

"MIALLFONT"
  Type:                              Code
  Start:                             16384
  Length:                            1024

"MIALL.$"
  Type:                              Code
  Start:                             278528
  Length:                            7136
  Execution Address:                 278528

"MIALLcode"
  Type:                              Code
  Start:                             32768
  Length:                            125

"rr1.MAG"
  Type:                              Code
  Start:                             38300
  Length:                            13404

"rr1.DCP"
  Type:                              Code
  Start:                             32768
  Length:                            84

"LINEFILL"
  Type:                              SAM BASIC
  Program length:                    33087
  Numeric variables offset:          33203
  String/array variables offset:     33691
  Start:                             23765
  Length:                            923
  Start Line:                        0

"IMPLOADER"
  Type:                              SAM BASIC
  Program length:                    33398
  Numeric variables offset:          33490
  String/array variables offset:     34002
  Start:                             23765
  Length:                            1234
  Start Line:                        0

"jstick.icn"
  Type:                              Code
  Start:                             40960
  Length:                            192

"spec.icn"
  Type:                              Code
  Start:                             40960
  Length:                            192

"music.icn"
  Type:                              Code
  Start:                             40960
  Length:                            192

"boot.icn"
  Type:                              Code
  Start:                             40960
  Length:                            192

"cards.icn"
  Type:                              Code
  Start:                             40960
  Length:                            192

"dice.icn"
  Type:                              Code
  Start:                             40960
  Length:                            192

"ICONS"
  Type:                              SAM BASIC
  Program length:                    33596
  Numeric variables offset:          33688
  String/array variables offset:     34200
  Start:                             23765
  Length:                            1432
  Start Line:                        0

"s0"
  Type:                              Code
  Start:                             40000
  Length:                            6524

"BLAST!"
  Type:                              SAM BASIC
  Program length:                    33996
  Numeric variables offset:          34088
  String/array variables offset:     34600
  Start:                             23765
  Length:                            1832
  Start Line:                        0

"BLAST.g"
  Type:                              Code
  Start:                             106496
  Length:                            117102
  Execution Address:                 106496

"fredmenu56"
  Type:                              Code
  Start:                             32768
  Length:                            24576

"fredtitle"
  Type:                              Screen
  Screen Mode:                       3
  Start:                             507904
  Length:                            24617

"qtitle.$"
  Type:                              Code
  Start:                             278528
  Length:                            7956
  Execution Address:                 278528

"reader"
  Type:                              Code
  Start:                             32768
  Length:                            2928

"alltext"
  Type:                              Code
  Start:                             80000
  Length:                            16000

"qinfoprog"
  Type:                              SAM BASIC
  Program length:                    34090
  Numeric variables offset:          34221
  String/array variables offset:     34694
  Start:                             23765
  Length:                            1993
  Start Line:                        1

"rachel.MAG"
  Type:                              Code
  Start:                             38300
  Length:                            6950

"rachel.DCP"
  Type:                              Code
  Start:                             32768
  Length:                            54

"Cdemo"
  Type:                              SAM BASIC
  Program length:                    39105
  Numeric variables offset:          39242
  String/array variables offset:     39709
  Start:                             23765
  Length:                            8619
  Start Line:                        1

"prime   .O"
  Type:                              Code
  Start:                             32768
  Length:                            2472

"xo      .O"
  Type:                              Code
  Start:                             32768
  Length:                            6067

"D2toD1alph"
  Type:                              SAM BASIC
  Program length:                    33253
  Numeric variables offset:          33345
  String/array variables offset:     33857
  Start:                             23765
  Length:                            1089
  Start Line:                        0

"pp1.MAG"
  Type:                              Code
  Start:                             38300
  Length:                            26709

"pp1.DCP"
  Type:                              Code
  Start:                             32768
  Length:                            135

"e2"
  Type:                              Code
  Start:                             49152
  Length:                            4785

"e4"
  Type:                              Code
  Start:                             49152
  Length:                            2722

"e3"
  Type:                              Code
  Start:                             49152
  Length:                            4603

"e5"
  Type:                              Code
  Start:                             49152
  Length:                            3037

"MadProf.M"
  Type:                              Code
  Start:                             69123
  Length:                            119928

"MAGICM.MOD"
  Type:                              Code
  Start:                             69123
  Length:                            50000

"s1"
  Type:                              Code
  Start:                             40000
  Length:                            20547

"s2"
  Type:                              Code
  Start:                             40000
  Length:                            14337

"s3"
  Type:                              Code
  Start:                             40000
  Length:                            16179

"s4"
  Type:                              Code
  Start:                             40000
  Length:                            13283

```

### Key observation: there is **no** SAMDOS file in the directory

Scanning the listing for any of `SAMDOS`, `SAMDOS2`, `MASTERDOS`, `BDOS`, or
similar DOS filenames: none exist as ordinary directory entries. The first
entry, however, is highly unusual:

```
"\x7f FRED56 \x7f"
  Type:                              Code
  Start:                             229385
  Length:                            8078
```

That filename — `0x7f, 0x20, "FRED56", 0x20, 0x7f` — uses the 0x7f character
(SAM Coupé's copyright glyph) as bracketing. The reported `Start: 229385`
(= 0x37FC9, which is `0x38000-55`) is a high-32K-window address, consistent
with where SAM bootstrap code typically lives once paged in.

`samfile cat -i fred56.mgt -f $'\x7f FRED56 \x7f' | xxd | head -20` shows real
Z80 code starting `21 fe 81 11 02 04 af 32 11 81 22 05 b8 7b d3 e2 ...`
(`LD HL,&81FE; LD DE,&0402; XOR A; LD (&8111),A; LD (&8105),HL; ...`),
plus the **literal ASCII "SAMDOS2"** at offset 0x133 inside the file:

```
00000130: 4413 5341 4d44 4f53 3220 2020 2020 2020  D.SAMDOS2
```

So **SAMDOS2 itself is not a directory entry**, but it is bundled inside this
boot file: the boot-file payload contains both the Z80 loader and the SAMDOS2
code/strings it sets up.

### How the boot actually fires

Inspecting the disk's directory header (offset 0 in the MGT image) for the
" FRED56 " entry's start track/sector pointer:

```
$ xxd -s 0 -l 16 fred56.mgt
00000000: 137f 2046 5245 4435 3620 7f00 1004 01ff
```

- `13`         = file type byte = 19 (Code)
- `7f 20 46 52 45 44 35 36 20 7f` = filename (with 0x7f bracketing)
- `00 10`      = sector count metadata
- `04 01`      = **start track 4 (side 0), start sector 1** -> MGT byte offset 0xa000

`xxd -s 0xa000 -l 64 fred56.mgt` shows the same Z80 bootstrap code there as in
the file payload — confirming that the SAM ROM boot routine (F9 / auto-boot)
reads track 4 sector 1 and runs that loader, which then pulls in SAMDOS and
auto-runs the BASIC entry-point (`AUTOFRED..`, type SAM BASIC, Start Line 10).

### Answer to the headline question

> Is SAMDOS bundled as a regular directory entry?

**No.** FRED 56 does **not** carry SAMDOS as a normal `SAMDOS2`-named Type-2
directory entry. Instead, the disk uses the standard SAM Coupé hidden-bootstrap
mechanism: a Code-typed entry with a 0x7f-bracketed name acts as the boot file,
its sectors live on track 4, and the SAM ROM autoboots that sector directly.
The SAMDOS2 code is embedded inside that bootstrap blob — so a SAM machine
booting this disk loads DOS off the disk transparently; you do NOT need DOS
already in memory.

This is the standard SAM commercial-disk pattern. Our handcrafted MGT image
needs to do something equivalent: either include a hidden bootstrap at
track-4-sector-1 that loads SAMDOS and runs our BASIC, OR include `SAMDOS2`
as a normal directory entry plus a CALL to enable it.

## Step 2 — vanilla SimCoupe headless smoke test

To remove instrumented SimCoupe as a confound, a **separate** Docker container
was used (NOT `sam-aarch64-ci`), with a fresh clone and clean build:

```
docker run -d --name fred-test --rm ubuntu:24.04 sleep infinity
docker exec fred-test apt-get install -y \
    git build-essential cmake libsdl2-dev libbz2-dev libpng-dev zlib1g-dev \
    xvfb imagemagick xdotool x11-utils
docker exec fred-test git clone --depth 1 https://github.com/simonowen/simcoupe.git /root/simcoupe
docker exec fred-test bash -c 'cd /root/simcoupe && mkdir build && cd build && cmake .. && make -j$(nproc)'
```

Build was clean (no patches, no instrumentation). The compiled binary is at
`/root/simcoupe/build/simcoupe` inside the `fred-test` container. The SAM ROM
and aux resources from `Resource/` were copied into
`/usr/local/share/simcoupe/` (which is where the binary looks by default,
since `make install` was not run).

### Launch sequence used

```
# Start virtual display on :151 (avoiding zombies on :99/:150)
docker exec -d fred-test bash -c 'Xvfb :151 -screen 0 1280x1024x24 >/tmp/xvfb.log 2>&1'

# Copy the FRED 56 image in
docker cp /tmp/fred56/fred56.mgt fred-test:/root/fred56.mgt

# Launch SimCoupe
docker exec -d fred-test bash -c \
  'DISPLAY=:151 SDL_VIDEODRIVER=x11 SDL_AUDIODRIVER=dummy \
   /root/simcoupe/build/simcoupe -fullscreen 0 -firstrun 0 /root/fred56.mgt \
   >/tmp/simcoupe.log 2>&1'

# Allow ~14 seconds for FRED to load through its boot loader and menu setup,
# then capture the SimCoupe SDL/OpenGL window.
sleep 14
docker exec fred-test bash -c \
  'DISPLAY=:151 import -window 0x200013 /tmp/fred-boot-window.png && \
   DISPLAY=:151 import -window root /tmp/fred-boot.png'
docker cp fred-test:/tmp/fred-boot-window.png /tmp/fred-boot-window.png
docker cp fred-test:/tmp/fred-boot.png /tmp/fred-boot.png
```

`/tmp/simcoupe.log` was empty — no warnings, no errors emitted by SimCoupé.

### What the screenshot shows

`/tmp/fred-boot-window.png` (the SimCoupe window, 1062x804) shows the
fully-rendered FRED 56 magazine **Contents** page:

- A turquoise/black checkerboard banner across the top.
- The "FRED 56" logo in stylised orange/red lettering.
- A "CONTENTS" header.
- A two-column menu of items A through N:
  - A: FRED 56
  - B: Letters
  - C: Screen$
  - D: Blast!
  - E: Imploder
  - F: MIALL
  - G: C-Demo
  - H: Quazar Information
  - I: E-tunes
  - J: MODs
  - K: Debut
  - L: Rachel 7&8
  - M: Adverts
  - N: Bits n Bobs
- Cyan horizontal rule lines and a noise-pattern animation cell at the bottom.
- Top-right corner shows "92%" — SimCoupe's built-in emulation-speed indicator
  (set by `Settings -> View -> Status` in the GUI; it draws regardless and is
  unrelated to FRED).

This is **outcome (d)** from the experiment plan: a fully working FRED-specific
menu, not a blank screen, not the bare ROM startup, and crucially **no "NO DOS"
error**.

### Did FRED boot? **YES.**

- Vanilla SimCoupé built from `master`, no patches.
- Headless Xvfb (`:151`) + `SDL_VIDEODRIVER=x11` + `SDL_AUDIODRIVER=dummy`.
- ImageMagick `import` reliably captures the SDL/OpenGL window contents.
- The disk loaded its bootstrap, brought up SAMDOS, ran AUTOFRED, and rendered
  the magazine UI.

## Conclusions

1. **SimCoupe-in-Docker-with-Xvfb is not the problem.** This exact setup
   (Xvfb :151, SDL x11 + dummy audio, vanilla simcoupe master) successfully
   boots a real SAM disk image to a custom-graphics application screen.
   Therefore, our handcrafted MGT image's failure is on the **disk-format
   side**, not the emulator-environment side.

2. **Real SAM commercial disks do NOT include SAMDOS as a directory entry.**
   They rely on a hidden bootstrap file (Code-type, 0x7f-bracketed name, sectors
   on track 4) that the SAM ROM autoboots. The bootstrap loads SAMDOS off the
   disk into RAM and then auto-runs the application's BASIC entry-point.

3. **For our handcrafted disk to autoboot like FRED**, options:
   - Add a track-4 bootstrap sector with a small loader that pages in
     SAMDOS2 and `LOAD ... LINE`s our BASIC, plus a directory entry pointing
     at it. (Faithful to commercial pattern.)
   - Or simpler: include `SAMDOS2` as a normal Type-2 directory entry and
     have the user (or boot logic) `LOAD "SAMDOS2"` first. This is the
     "homebrew" pattern — works once DOS is loaded, but the pristine SAM ROM
     boot won't pick it up automatically.

## Files produced

- `/tmp/fred56/fred56.zip`           - downloaded archive
- `/tmp/fred56/FRED56.DSK`           - extracted MGT image (819200 bytes)
- `/tmp/fred56/fred56.mgt`           - copy used by samfile
- `/tmp/fred56-ls.txt`               - raw `samfile ls` output
- `/tmp/fred-boot.png`               - full Xvfb root screenshot (1280x1024)
- `/tmp/fred-boot-window.png`        - tighter shot of just SimCoupe window (1062x804)
- `/tmp/fred-experiment.md`          - this report

The throwaway container is `fred-test` (`docker stop fred-test` to remove;
`--rm` was used so it disappears on stop).

The `sam-aarch64-ci` container was **not** touched — its instrumented SimCoupe
build is undisturbed.
