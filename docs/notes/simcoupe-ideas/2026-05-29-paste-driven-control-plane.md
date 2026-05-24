# SimCoupé idea — paste-driven control plane

**Status:** read-only design idea. Captured 2026-05-29 from Pete's brainstorm. No code, no commits. Builds on the existing macOS paste feature (`sdl-paste-clipboard` branch in `~/git/simcoupe`, May 2026) and the longer-term sam-as-a-pipe vision.

## 1. The core observation

The paste channel into SimCoupé carries a byte stream. The SAM consumes only a subset of those bytes as keyboard input — printable ASCII, `\r`, tab, copyright. Everything else is either dropped (`Base/Keyin.cpp:35-58`) or, in some locales, mangled into BASIC token codes.

**The unused bytes are a free side-channel.** A character like an emoji or an `ESC`-prefixed sequence has no SAM-side meaning, so a parser sitting in front of `Keyin::String` can intercept it, dispatch a SimCoupé `Action` (insert disk, NMI, debugger, screen-record, …), and only pass the residue on to Keyin. The pipe becomes a control plane for the *whole* SimCoupé surface, not just the keyboard.

Combined with screen-output capture (per the investigation notes — intercept at PC `0xDC00` `PROM1` and `0xDDC4` `PRCRLCDS`), the pipe is bidirectional and lets you script the SAM end-to-end from one stdin/stdout pair.

## 2. Where the parser sits

Keyin's job today (`Base/Keyin.cpp:118`) is "type one printable byte into LAST_K per frame". That's the wrong layer to dispatch UI actions — Keyin is a register-poker. The clean separation:

- New `Drive::Feed(text)` — parses out escape sequences, calls `Actions::Do(...)` for command escapes, falls through to `Keyin::String(remaining_text)` for the rest. Lives next to or inside Keyin namespace.
- Wire `Drive::Feed` into the paste handler (replace the direct `Keyin::String` call in `Base/Actions.cpp::case Action::Paste`) and into any future `-keystdin` stdin reader.

The existing `Action::Paste` keeps its "just type the clipboard" semantics for users who never use escapes. Users who do get a richer vocabulary for free.

## 3. Existing scaffolding worth reusing

`Base/Keyin.cpp:60` already has `Keyin::EscapedString(text)` — it recognises `\n`, `\r`, `\t`, `\\`. It is a minimal escape parser. Extend it (or add a sibling) to also recognise action-dispatch escapes. The shape of the loop is already correct: byte-walk, switch on escape lead-in, dispatch.

## 4. Vocabulary — three options

**(a) Printable escapes — OSC-style.** `\e[InsertDisk1:/path/to.mgt]\e\\` and similar. Text-editable, version-control-friendly, easy to script. Survives every transport (HTTP, JSON, copy-paste). Discoverable via a `--list-actions` flag. Recommended primary API.

**(b) Single-byte control codes.** Bytes 0x01-0x07 (unused by SAM) each map to one Action. Compact but tiny namespace (~16 codes once you avoid `\b`/`\t`/`\n`/`\r`), no room for arguments. Useful as a fast path for parameterless actions (NMI, FrameStep, Pause) if profiling shows escape parsing is hot.

**(c) Emoji codes.** `🔌` = InsertDisk1, `🐛` = Debugger, `🛑` = NMI, `📸` = SavePNG, `⏸️` = Pause. Pure novelty: discoverability is poor, you need a cheat-sheet, but the codepoints don't collide with SAM bytes and they're fun for demos / typed-by-hand testing. One-line `std::map<std::string, Action>` lookup on top of (a). Worth shipping as a parallel mapping.

## 5. Atomicity gotcha

Some Actions complete instantly; others involve modal GUI flows that block on user input. Without thinking about this, a paste like `🔌 disk.mgt → RUN` races the floppy-browse dialog and the keys end up nowhere useful.

Inventory of the modal vs instant split (from `Base/Actions.cpp::Do`):

| Modal (opens `GUI::Start(...)`) | Instant |
|---|---|
| `InsertDisk1` / `InsertDisk2` (BrowseFloppy) | `EjectDisk1` / `EjectDisk2` |
| `NewDisk1` / `NewDisk2` (NewDiskDialog) | `Reset`, `Nmi`, `Pause`, `FrameStep` |
| `InsertTape` / `TapeBrowser` (BrowseTape) | `EjectTape` |
| `ImportData` / `ExportData` (Import/ExportDialog) | `SavePNG`, `SaveSSX` |
| `Options` (OptionsDialog) | `ToggleFullscreen`, `ToggleTV`, all Speed/Turbo |
| `About` (AboutDialog) | `Record{Avi,Gif,Wav}{,Half,Loop,Stop,Segment}` |
| `Debugger` (Debug::Start → Debugger window) | `TogglePrinter`, `FlushPrinter` |

The escape vocabulary should not call the existing `Action::*` for the modal cases directly. It should instead bind to **headless equivalents** that take their arguments inline:

```
\e[InsertDisk1:/path/to.mgt]\e\\   → pFloppy1->Insert("/path/to.mgt")     // not GUI::Start(BrowseFloppy)
\e[NewDisk1:/path/to.mgt]\e\\      → pFloppy1->NewDisk("/path/to.mgt")    // not GUI::Start(NewDiskDialog)
\e[OptionSet:fullscreen=1]\e\\     → SetOption + Video::OptionsChanged    // not GUI::Start(OptionsDialog)
```

The instant actions can map 1:1 onto existing `Action::*`. The modal ones need a small parallel API on `Floppy`, `Tape`, etc. — essentially exposing what the GUI dialogs do, but accepting the path/option as an argument. That's a one-time refactor that also makes the Win32 menu code simpler if it ever lands there.

For debugger control specifically (step, next, breakpoint set/clear, …), the `Debugger` window exposes a keyboard-driven command surface. Either drive that command surface from the escape parser (richer but coupled to the debugger UI), or add a parallel headless API on `Debug::` (cleaner). TBD.

## 6. Output side — the natural pair

The investigation doc identified the clean intercept point: PC `0xDC00` is `PROM1` (channel K/S/P printable-byte entry), PC `0xDDC4` is `PRCRLCDS` (control-byte entry). Both confirmed at `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:21194` and the CHANTAB at `:26992-27006`. Each is a ~5-line PC-breakpoint hook in `Base/CPU.cpp`'s step loop.

A natural framing for stdout:

- Plain SAM characters that the ROM is "printing" → emitted as-is.
- Synthetic events (screenshot saved, GIF recording started, breakpoint hit, …) → emitted as the same kind of `\e[...]\e\\` escape sequences the input parser understands, so a downstream tool consuming the stream can use the same parser both ways.

End state: `simcoupe-pipe` is a stdin-in / stdout-out filter where input is "act on the SAM" and output is "what the SAM did and printed". `cat script.sam | simcoupe-pipe | tee output.log`. The whole automation surface in one process.

## 7. Roadmap — what would come first

If pursuing this, suggested order:

1. **`-keystdin` flag** (already a logical follow-up to the paste branch). Reads stdin, feeds into `Drive::Feed` (or `Keyin::String` initially), one-line plumbing. ~80 lines per the investigation doc.
2. **Output capture at `0xDC00` + `0xDDC4`** (`-screenstdout` flag). ~20 lines.
3. **Plain `EscapedString` extension** — add the OSC-style escape syntax recogniser, but only one or two Actions wired (NMI as the canonical test — instant, no arguments, visible effect).
4. **Headless equivalents** of the modal Actions — `Floppy::Insert(path)` etc. — one PR per family.
5. **Debugger surface** — last, because it touches the most.

Items 1+2 alone give you a working `simcoupe-pipe` with no escape parsing. Item 3 adds the control plane. Items 4+5 make it cover the full UI surface.

## 8. Open questions

- **Escape lead-in choice.** ANSI ESC `\x1B` is the obvious pick, but the SAM ROM treats `\x1B` as a real character in some channels (it's the SAM-BASIC `1B` token). Need to confirm whether ESC reaches the SAM at all today, or whether `Keyin::String`'s isprint filter already drops it. If it does reach, pick a different lead-in (DLE `\x10`, RS `\x1E`, or one of the C1 controls `\x80-\x9F`).
- **Argument syntax.** `\e[ACTION:arg1=val1,arg2=val2]\e\\` reads cleanly; `\e[ACTION arg1 arg2]\e\\` is shorter. Either works; pick after seeing two or three real callers.
- **Sync barriers.** Should `\e[Sync]\e\\` (or `\e[Flush]\e\\`) wait until all preceding Keyin bytes have been consumed before the next escape fires? Probably yes, otherwise scripts race. One-liner against `Keyin::IsTyping()`.
- **Error reporting.** Unknown escape — error to stdout? Silent drop? Pass through to Keyin (so the user sees garbage and notices)?
- **Discoverability.** A `--list-actions` flag on the binary so users know the vocabulary. Auto-generated from the Action::Do switch.

## 9. Why this is worth doing

It collapses the "drive SimCoupé from a script" use case into one channel that's text, pasteable, recordable, replayable, and CI-friendly. It generalises the macOS paste feature from "type the clipboard" to "drive the emulator" without changing the user-facing surface for everyone who only wants the simple thing. And it makes the sam-as-a-pipe milestone concrete: input + output + control are all in one stream, instead of three.

The investment is bounded — a few hundred lines across `Base/Keyin.cpp`, `Base/CPU.cpp`, and small extensions to `Base/Floppy.cpp` / `Base/Tape.cpp`. Mostly mechanical once the escape format is decided.

## 10. References

- Existing macOS paste branch: `~/git/simcoupe`, branch `sdl-paste-clipboard` (preserved 2026-05-21).
- Keyin: `~/git/simcoupe/Base/Keyin.cpp:35-58` (String), `:60-89` (EscapedString), `:118-134` (Next).
- Action dispatch: `~/git/simcoupe/Base/Actions.cpp::Do`, modal cases visible at `:158-253`.
- Screen-output intercept points: `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:21194` (PROM1 at `0xDC00`) and `:26992-27006` (CHANTAB).
- Larger sam-as-a-pipe rationale: existed at `/tmp/simcoupe-vs-sam-investigation.md` from the May 21 session; ephemeral. Worth re-capturing into `docs/notes/simcoupe-ideas/` if revisiting.
