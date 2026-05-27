# SimCoupé — macOS SDL Paste Clipboard menu item

**Date:** 2026-05-21
**Target repo:** `~/git/simcoupe` (local working copy, currently at `a65a16e Add -exitonhalt option to aid automation`)
**Scope:** macOS SDL build only. Linux SDL deferred. Win32 unchanged (or moved, see iteration knob below).
**Upstream status:** Local-first. The change will land on a feature branch in the local clone; upstream-PR shape will be decided after seeing the diff.

---

## 1. Motivation

The Win32 SimCoupé build has had "Tools > Paste Clipboard" since at least 2019. The macOS SDL build does not. Pete's broader project ("sam-as-a-pipe", see `/tmp/simcoupe-vs-sam-investigation.md`) needs the ability to drive a SAM by injecting text — pasting source code, BASIC programs, or test inputs. A macOS Cmd+V paste menu is the minimum-viable first step. It also exercises every component of the keystroke-injection pipeline (system clipboard → `Keyin::String` → ROM FLAGS/LAST_K poke), giving us a fully-tested base from which to add stdin-driven and other-platform variants later.

## 2. Current state — what's already in place

The plumbing on SimCoupé's side is almost complete:

- `Action::Paste` is registered in `Base/Actions.h:29` (enum) and `Base/Actions.cpp:73` (action table). The handler currently lives only in `Win32/UI.cpp:963` (3 lines).
- `OSD::GetClipboardText()` is implemented on **both** OSDs: `SDL/OSD.cpp:167` (uses `SDL_GetClipboardText()`), `Win32/OSD.cpp:252`.
- `Keyin::String(text)` works cross-platform; filters to printable + `\n→\r` + tab + © (`Base/Keyin.cpp:35`); replaces the queue on each call (`Base/Keyin.cpp:56`).
- The macOS SDL build has full Cocoa NSMenu support via `SDL/OSX/SDLMain.m` (top-level File / View / System / Window / Help menus). Menu items dispatch via `sendUserEvent(UE_*)` → `SDL_USEREVENT` → `SDL/UI.cpp:CheckEvents` switch → `Actions::Do(Action::*)`.
- SDL is single-threaded: `SimCoupe_main` is called directly from `applicationDidFinishLaunching` (`SDL/OSX/SDLMain.m:241`) and runs the CPU loop on the AppKit main thread. `SDL_PollEvent` drains AppKit events on the same thread, so `validateMenuItem:` callbacks happen on the same stack as the emulator loop — no synchronisation needed for state reads.

What's missing on SDL:
- A `case Action::Paste:` handler reachable from SDL.
- An "Edit > Paste" menu item.
- The bridge so `validateMenuItem:` can check whether paste should be enabled.

## 3. Design

### 3.1 Architecture

Five files change. Listed by file, in expected commit order.

| File | Change | Lines (approx) |
|---|---|---|
| `Base/Actions.cpp` | Add `case Action::Paste:` to the cross-platform dispatch in `Do()`. Add `#include "OSD.h"`, `#include "Keyin.h"`. | +5 |
| `Win32/UI.cpp` | Remove the now-redundant `case Action::Paste:` block at line 963. | −6 |
| `SDL/UI.h` | Add `UE_PASTE` to the UE_* defines. Add a C-callable `sim_can_paste()` declaration guarded for both C++ and ObjC inclusion. | +6 |
| `SDL/UI.cpp` | Add `case UE_PASTE: Actions::Do(Action::Paste); break;` to the user-event switch in `CheckEvents`. Define `extern "C" bool sim_can_paste(void) { return Keyin::CanType(); }`. Add `#include "Keyin.h"`. | +5 |
| `SDL/OSX/SDLMain.m` | Add a new top-level **Edit** NSMenu between File and View, containing a single "Paste" item with `keyEquivalent:@"v"`. Implement `- (IBAction)editPaste:` posting `UE_PASTE`. Implement `- (BOOL)validateMenuItem:` that returns true iff `sim_can_paste() && [[NSPasteboard generalPasteboard] availableTypeFromArray:@[NSPasteboardTypeString]] != nil`. | +25 |

Total: roughly **+41, −6** across 5 files.

### 3.2 Iteration knob — handler location

The handler can live in either of two places:

- **(A) Cross-platform: in `Base/Actions.cpp`.** Win32's 3-line block is deleted; SDL gets paste working "for free" because `Actions::Do(Action::Paste)` reaches the cross-platform switch. Selected for v1.
- **(B) Per-platform: duplicated in `SDL/UI.cpp::DoAction`.** Win32 unchanged, SDL gets its own copy. More duplication, but smaller blast radius.

We will build (A) first. If the resulting diff is messier than expected — or if the cross-platform move would require new `#include`s in `Base/Actions.cpp` that conflict with anything (e.g. if `OSD.h` and `Keyin.h` aren't there yet) — we may swap to (B) and reassess. Pete's criterion: pick whichever results in fewer net lines once both sides are working.

### 3.3 Data flow (Cmd+V)

1. User presses Cmd+V (or selects Edit > Paste).
2. AppKit calls `validateMenuItem:` on the SDLMain delegate to decide if the item is enabled. The validator calls `sim_can_paste()` (currently `Keyin::CanType()`) AND checks the system pasteboard for string content. If either is false, the menu item is greyed out and Cmd+V is a no-op.
3. AppKit fires the `editPaste:` IBAction selector. It posts an `SDL_USEREVENT` with `code = UE_PASTE` via `sendUserEvent()`.
4. `SDL/UI.cpp::CheckEvents` (running on the same thread, inside `SDL_PollEvent`) pops the event, hits `case UE_PASTE`, calls `Actions::Do(Action::Paste)`.
5. `Actions::Do` asks `UI::DoAction` first (SDL's returns false for Paste; Win32's used to return true but no longer needs to). Falls through to the cross-platform switch. The new `case Action::Paste:` calls `Keyin::String(OSD::GetClipboardText())`.
6. `Keyin::String` filters the text and stores it in `s_input_text`.
7. Per-frame `EiHook` calls `Keyin::Next()`, which writes one char at a time to `LAST_K` and sets the FLAGS new-key bit. SAM ROM picks it up and types it.

### 3.4 Behaviour decisions

| Decision | Choice | Rationale |
|---|---|---|
| Greyout on `!Keyin::IsTyping()` | **No** — match Win32 | Pasting while busy interrupts the in-flight queue and starts fresh. Simpler, matches Win32. |
| Greyout on `!GUI::IsActive()` | **No** | Pasted keys queue while overlay open, type when it closes. Win32 doesn't block this either. |
| Greyout on empty pasteboard | **Yes** | Matches Win32's `IsClipboardFormatAvailable(CF_UNICODETEXT)` guard. Adds 1-2 lines of Cocoa. |
| Cut/Copy items in Edit menu | **No** | Lone Paste is acceptable on macOS. Cut/Copy can be added later if/when a "copy screen text" feature exists. |
| Linux SDL paste in v1 | **No** | Separate change. The cross-platform handler is reachable from Linux via `fkeys=Fxx=Paste` configuration with zero new code, so we don't even need to mention it in the UI yet. |
| Max-length cap on pasted text | **No** | YAGNI. `Keyin::String` is `std::string` — fine. |

### 3.5 Non-trivial considerations

- **C / Objective-C / C++ boundary.** `SDL/OSX/SDLMain.m` is plain Objective-C (`.m`, not `.mm`), so it cannot call C++ functions directly. The existing pattern is for `SDL/UI.h` to expose plain C macros (the UE_* defines) usable from both `.m` and `.cpp`. We extend this pattern with `extern "C" bool sim_can_paste(void)`. Avoids renaming `SDLMain.m` → `SDLMain.mm` (which would touch the build system and is the kind of mechanical change that earns review pushback).
- **Thread safety.** Already established: single-threaded. `Keyin::CanType()` reads two ints; safe.
- **Win32 regression risk.** Moving the Paste handler from `Win32/UI.cpp` to `Base/Actions.cpp` could break Win32 paste if there's a subtle ordering or include dependency. Mitigated by: the dispatch behaviour is identical (`Actions::Do(Action::Paste)` still routes via `UI::DoAction` first, which now returns false instead of true, falling through to Base). Pete doesn't have Windows; we rely on CI (if simcoupe has it) and code inspection. If we discover later that Win32 paste broke, iteration knob (B) is the rollback.

## 4. Testing plan

Pete runs all three tests on macOS post-build.

**Test A — Happy path.** Boot SAM into BASIC. `pbcopy < small.bas` (or copy from a text editor) some text like `10 PRINT "hi": 20 GOTO 10`. Open Edit menu, confirm Paste shows `⌘V`, click it (or press Cmd+V). Verify every character types into the SAM correctly. Type `RUN`, see expected output.

**Test B — Greyout.**
- B1: Empty the clipboard (`pbcopy < /dev/null` then open menu). Paste item should be greyed out.
- B2: Enter debugger. Open Edit menu. Paste should be greyed out (because `Keyin::CanType()` returns false when not in BASIC/ROM0).
- B3: Back in BASIC with text on clipboard — Paste enabled.

**Test C — Paste-while-busy.** Copy a long string (1000+ chars). Paste. While typing, copy and paste a different short string. Pass criterion: no crash, the second string types fully, no lingering tail of the first.

**Out of scope for v1 testing:** Win32 regression. Linux. We accept the risk; if Win32 paste regresses we'll catch it on next Windows test and roll back to iteration knob (B).

## 5. Out of scope (explicitly)

- Linux SDL Paste (no hotkey, no menu)
- A `-keystdin` option for headless / pipe-driven SAM (the *next* milestone in `/tmp/simcoupe-vs-sam-investigation.md`)
- Stdin streaming / EscapedString variant
- A "paste from file" action
- Max-length cap on pasted text
- Cut and Copy items in the Edit menu
- Modifying `Keyin::String`'s filtering behaviour (e.g. supporting more control characters)

## 6. Open questions to resolve during implementation

- (Likely none.) The design choices have all been made. The only thing left to verify is that the diff looks clean enough to keep the cross-platform handler move; if not, fall back to iteration knob (B).

## 7. References

- `~/git/simcoupe/Base/Actions.cpp:73,114` — action table and dispatch
- `~/git/simcoupe/Base/Keyin.cpp` — keystroke injection (filtering at line 35, queue-replace at line 56, CanType at line 102)
- `~/git/simcoupe/SDL/UI.cpp:97,217` — `CheckEvents` (UE_* dispatch) and `UI::DoAction` (currently only handles `ExitApp`)
- `~/git/simcoupe/SDL/UI.h` — UE_* defines (currently +26)
- `~/git/simcoupe/SDL/OSX/SDLMain.m:93-186` — NSMenu construction; `:274-283` — IBAction selectors
- `~/git/simcoupe/Win32/UI.cpp:865,963,1428` — current Paste plumbing on Win32
- `/tmp/simcoupe-vs-sam-investigation.md` — broader sam-as-a-pipe rationale
- Prior CLI `-exitonhalt` change: `~/git/simcoupe` commit `a65a16e` (Simon's rewrite of our PR #109)
