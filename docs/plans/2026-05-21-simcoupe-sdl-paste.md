# SimCoupé macOS SDL Paste — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an Edit > Paste menu item to the macOS SDL build of SimCoupé, bound to Cmd+V, that injects the system clipboard into the SAM keyboard buffer.

**Architecture:** Move the existing Win32-only `Action::Paste` handler into cross-platform `Base/Actions.cpp` so all platforms can dispatch it. Add a UE_PASTE user-event code, a tiny C-callable bridge function (`sim_can_paste`), and the Cocoa Edit > Paste menu item with `validateMenuItem:` greyout. Single thread throughout; no synchronisation needed.

**Tech Stack:** C++17 / Objective-C, SDL2, CMake. macOS build only (Linux and Win32 unchanged code-wise; Win32 handler relocated).

**Target repo:** `~/git/simcoupe` (currently at `a65a16e Add -exitonhalt option to aid automation`).

**Iteration knob — defer to user if uncertain:** If at any point the cross-platform handler move (Task 2) feels messier than expected, alternative is to revert Task 2 and instead duplicate the 3-line handler into `SDL/UI.cpp::DoAction`. Pete's criterion: pick whichever results in fewer net lines.

**Per-task workflow:** Each task ends with `cmake --build build` to confirm no build break, then a commit. The user (Pete) will exercise the actual paste behaviour at the explicitly-marked verification gates in Tasks 5, 6, and 7.

**Git tooling:** Use `g` instead of `git` for all git operations (Pete's wrapper preserves timestamps that plain `git` doesn't).

---

## Task 1: Create feature branch in simcoupe

**Files:** None (git operation only).

- [ ] **Step 1: Confirm clean working tree**

```bash
cd ~/git/simcoupe && g status
```

Expected: `nothing to commit, working tree clean` on `main`.

If unclean, STOP and ask the user before proceeding.

- [ ] **Step 2: Create feature branch**

```bash
cd ~/git/simcoupe && g checkout -b sdl-paste-clipboard
```

Expected: `Switched to a new branch 'sdl-paste-clipboard'`.

- [ ] **Step 3: Verify the existing build still works**

```bash
cd ~/git/simcoupe && cmake --build build 2>&1 | tail -5
```

Expected: build completes without errors (the `build/` dir was last configured 2026-05-11 per memory file `simcoupe_install_requirements`). If cmake reports missing build dir, run `cmake -S . -B build -DBUILD_BACKEND=sdl` first.

---

## Task 2: Move `Action::Paste` handler to `Base/Actions.cpp`

**Files:**
- Modify: `~/git/simcoupe/Base/Actions.cpp`
- Modify: `~/git/simcoupe/Win32/UI.cpp:955-968` (delete `case Action::Paste:` block)

This makes the Paste action work on every platform that calls `Actions::Do(Action::Paste)`. Win32 behaviour is unchanged because the dispatch falls through from `UI::DoAction` (which no longer handles Paste, returns false) into the new `Base` case.

- [ ] **Step 1: Add the include and handler to `Base/Actions.cpp`**

In `Base/Actions.cpp`, add one `#include` near the existing block at lines 21-39 (the alphabetical-ish section under `#include "SimCoupe.h"`). Add it in alphabetical position, between `Input.h` and `Options.h`:

```cpp
#include "Keyin.h"
```

(Note: `OSD.h` is already transitively available — `Base/SimCoupe.h:96` includes `"OSD.h"` and `Base/Actions.cpp:21` includes `"SimCoupe.h"`. Do not add an explicit `#include "OSD.h"`.)

Then add the new case to the switch statement inside `bool Do(...)`. Insert it just before the `// Not processed` `default:` label at line 378-380. Find this block:

```cpp
        case Action::SpeedNormal:
            SetOption(speed, 100);
            Frame::SetStatus("100% Speed");
            break;

            // Not processed
        default:
            return false;
```

Add the new case immediately before `// Not processed`:

```cpp
        case Action::SpeedNormal:
            SetOption(speed, 100);
            Frame::SetStatus("100% Speed");
            break;

        case Action::Paste:
            Keyin::String(OSD::GetClipboardText());
            break;

            // Not processed
        default:
            return false;
```

- [ ] **Step 2: Remove the Paste case from `Win32/UI.cpp`**

In `Win32/UI.cpp`, find the block at lines 963-968:

```cpp
        case Action::Paste:
        {
            auto text = OSD::GetClipboardText();
            Keyin::String(text);
            break;
        }

        // Not processed
        default:
            return false;
```

Delete only the `case Action::Paste:` block (including its braces and `break;`), leaving the `// Not processed` / `default:` lines intact:

```cpp
        // Not processed
        default:
            return false;
```

- [ ] **Step 3: Verify nothing else references the Win32 Paste handler**

```bash
cd ~/git/simcoupe && grep -rn "Action::Paste" Base/ SDL/ Win32/
```

Expected: 4 references —
- `Base/Actions.h:29` (enum)
- `Base/Actions.cpp:73` (action table entry)
- `Base/Actions.cpp:NEW` (the new case we just added)
- `Win32/UI.cpp:1428` (`IDM_TOOLS_PASTE_CLIPBOARD: Actions::Do(Action::Paste)` — the IDM-to-Action dispatch from the menu)

No references in `SDL/`. No leftover `case Action::Paste:` in `Win32/UI.cpp::DoAction`.

- [ ] **Step 4: Build to confirm no regression**

```bash
cd ~/git/simcoupe && cmake --build build 2>&1 | tail -10
```

Expected: clean build. If `OSD::GetClipboardText` is unresolved at link time on Mac, it means we need to check that `Base/Actions.cpp` is compiled into the SDL target — it is (per the CMakeLists.txt review). If genuinely unresolved, STOP and check `Base/OSD.h`.

- [ ] **Step 5: Commit**

```bash
cd ~/git/simcoupe && g add Base/Actions.cpp Win32/UI.cpp
g commit -m "Move Action::Paste handler to Base/Actions.cpp

Both OSDs now implement GetClipboardText; no reason for the dispatch
to be Win32-specific. Win32 behaviour is unchanged (falls through
from UI::DoAction returning false). Allows SDL builds to reach
the handler too."
```

---

## Task 3: Add `UE_PASTE` and `sim_can_paste` declaration to `SDL/UI.h`

**Files:** Modify `~/git/simcoupe/SDL/UI.h`.

- [ ] **Step 1: Add `UE_PASTE` to the UE_* defines**

In `SDL/UI.h`, the UE_* block ends at line 73 with `#define UE_QUEUEFILE (UE_BASE+26)`. Add the new code on the next line:

```c
#define UE_QUEUEFILE            (UE_BASE+26)
#define UE_PASTE                (UE_BASE+27)
```

- [ ] **Step 2: Add the `sim_can_paste` C-bridge declaration**

After the UE_* defines (end of file), add:

```c
// C-callable bridge: returns true if the SAM can currently accept paste.
// Called from validateMenuItem: in SDL/OSX/SDLMain.m.
#ifdef __cplusplus
extern "C" {
#endif
bool sim_can_paste(void);
#ifdef __cplusplus
}
#endif
```

The full bottom of `SDL/UI.h` should now read:

```c
#define UE_QUEUEFILE            (UE_BASE+26)
#define UE_PASTE                (UE_BASE+27)

// C-callable bridge: returns true if the SAM can currently accept paste.
// Called from validateMenuItem: in SDL/OSX/SDLMain.m.
#ifdef __cplusplus
extern "C" {
#endif
bool sim_can_paste(void);
#ifdef __cplusplus
}
#endif
```

- [ ] **Step 3: Build to confirm header still compiles**

```bash
cd ~/git/simcoupe && cmake --build build 2>&1 | tail -5
```

Expected: clean build. (No callers yet; this is a pure declaration. Mac SDL build will skip `Win32/UI.cpp` so no issue if Win32 also includes UI.h — it doesn't.)

- [ ] **Step 4: Commit**

```bash
cd ~/git/simcoupe && g add SDL/UI.h
g commit -m "Add UE_PASTE event code and sim_can_paste C-bridge decl"
```

---

## Task 4: Add `UE_PASTE` handler and `sim_can_paste` definition to `SDL/UI.cpp`

**Files:** Modify `~/git/simcoupe/SDL/UI.cpp`.

- [ ] **Step 1: Add include for Keyin**

Near the top of `SDL/UI.cpp` (after the existing project includes — look for the block that already includes `"Actions.h"`), add:

```cpp
#include "Keyin.h"
```

- [ ] **Step 2: Add `UE_PASTE` case to `CheckEvents` switch**

In `SDL/UI.cpp`, find the block of `case UE_*:` statements around lines 164-180 (each one calls `Actions::Do(Action::*)`). The block currently ends with:

```cpp
                case UE_RECORDWAV:          Actions::Do(Action::RecordWav);       break;
                case UE_RECORDWAVSEGMENT:   Actions::Do(Action::RecordWavSegment); break;
```

Add `UE_PASTE` after them (alphabetical order is not strictly maintained in this block; append at the end is consistent):

```cpp
                case UE_RECORDWAV:          Actions::Do(Action::RecordWav);       break;
                case UE_RECORDWAVSEGMENT:   Actions::Do(Action::RecordWavSegment); break;
                case UE_PASTE:              Actions::Do(Action::Paste);           break;
```

- [ ] **Step 3: Add `sim_can_paste` definition at end of file**

At the bottom of `SDL/UI.cpp` (after `UI::DoAction` ends at line 243), add:

```cpp
extern "C" bool sim_can_paste(void)
{
    return Keyin::CanType();
}
```

- [ ] **Step 4: Build to confirm**

```bash
cd ~/git/simcoupe && cmake --build build 2>&1 | tail -10
```

Expected: clean build. The new symbol `_sim_can_paste` should be linker-visible.

- [ ] **Step 5: Verify the symbol is exported**

```bash
cd ~/git/simcoupe && nm -gU build/SimCoupe.app/Contents/MacOS/SimCoupe 2>&1 | grep -i "sim_can_paste" || \
  nm build/SimCoupe.app/Contents/MacOS/SimCoupe 2>&1 | grep -i "sim_can_paste"
```

Expected: at least one match showing `_sim_can_paste`. (The exact path to the binary may differ; if the `.app` bundle isn't built yet, check `build/SimCoupe` plain executable instead. If neither exists, STOP and inspect the build output.)

- [ ] **Step 6: Commit**

```bash
cd ~/git/simcoupe && g add SDL/UI.cpp
g commit -m "Wire UE_PASTE → Action::Paste and define sim_can_paste"
```

---

## Task 5: Add Edit > Paste menu + IBAction to `SDLMain.m`

**Files:** Modify `~/git/simcoupe/SDL/OSX/SDLMain.m`.

This task is intentionally atomic: the menu, the IBAction, and the wiring all land in one commit so the menu item is functional from the moment it appears.

**After this task: Cmd+V should paste. This is the first user-observable behaviour. The user will exercise Test A.**

- [ ] **Step 1: Insert the Edit menu between File and View**

In `SDL/OSX/SDLMain.m`, find the end of the File menu block at line 124:

```objc
    item = [[NSMenuItem alloc] initWithTitle:@"File" action:nil keyEquivalent:@""];
    [item setSubmenu:menu];
    [[NSApp mainMenu] addItem:item];
    [item release];
    [menu release];
```

and the start of the View menu block at line 126:

```objc
    /* Create the View menu */
    menu = [[NSMenu alloc] initWithTitle:@"View"];
```

Insert the Edit menu block between them:

```objc
    item = [[NSMenuItem alloc] initWithTitle:@"File" action:nil keyEquivalent:@""];
    [item setSubmenu:menu];
    [[NSApp mainMenu] addItem:item];
    [item release];
    [menu release];

    /* Create the Edit menu */
    menu = [[NSMenu alloc] initWithTitle:@"Edit"];

    [menu addItemWithTitle:@"Paste" action:@selector(editPaste:) keyEquivalent:@"v"];

    item = [[NSMenuItem alloc] initWithTitle:@"Edit" action:nil keyEquivalent:@""];
    [item setSubmenu:menu];
    [[NSApp mainMenu] addItem:item];
    [item release];
    [menu release];

    /* Create the View menu */
    menu = [[NSMenu alloc] initWithTitle:@"View"];
```

(The default `keyEquivalentModifierMask` on macOS is `NSEventModifierFlagCommand`, so `keyEquivalent:@"v"` binds Cmd+V without further code.)

- [ ] **Step 2: Add the `editPaste:` IBAction selector**

In the block of IBAction implementations starting at line 274, append a new line. The block currently looks like:

```objc
- (IBAction)appPreferences:(id)sender { sendUserEvent(UE_OPTIONS); }
- (IBAction)systemPause:(id)sender { sendUserEvent(UE_PAUSE); }
- (IBAction)systemNMI:(id)sender { sendUserEvent(UE_NMIBUTTON); }
- (IBAction)systemReset:(id)sender { sendUserEvent(UE_RESETBUTTON); }
- (IBAction)systemDebugger:(id)sender { sendUserEvent(UE_DEBUGGER); }
- (IBAction)viewFullscreen:(id)sender { sendUserEvent(UE_TOGGLEFULLSCREEN); }
- (IBAction)viewFrameSync:(id)sender { sendUserEvent(UE_TOGGLESYNC); }
- (IBAction)viewRatio54:(id)sender { sendUserEvent(UE_TOGGLETV); }
- (IBAction)fileImportData:(id)sender { sendUserEvent(UE_IMPORTDATA); }
- (IBAction)fileExportData:(id)sender { sendUserEvent(UE_EXPORTDATA); }
```

Add the new selector at the end of the block:

```objc
- (IBAction)fileExportData:(id)sender { sendUserEvent(UE_EXPORTDATA); }
- (IBAction)editPaste:(id)sender { sendUserEvent(UE_PASTE); }
```

- [ ] **Step 3: Build**

```bash
cd ~/git/simcoupe && cmake --build build 2>&1 | tail -10
```

Expected: clean build.

- [ ] **Step 4: USER VERIFICATION GATE — Test A (happy path)**

This step requires the user to interactively exercise the paste:

1. Launch SimCoupé: `open ~/git/simcoupe/build/SimCoupe.app` (or the equivalent path).
2. Wait for the SAM to boot to BASIC.
3. In a Mac text editor (TextEdit, VS Code, etc.) type and select-copy: `10 PRINT "hi": 20 GOTO 10`
4. Click into the SimCoupé window. Confirm the **Edit menu** is now in the menu bar between File and View, and that selecting it shows **Paste** with ⌘V.
5. Trigger Cmd+V. Observe each character of the BASIC program being typed into the SAM screen one frame at a time.
6. After the line completes, press Enter (or wait — if the clipboard included a trailing newline it's already been pressed). Type `RUN` and Enter. See the SAM produce continuous `hi hi hi…` output.

Expected: every clipboard character appears correctly, including quotes and colon. If anything is wrong (missing chars, wrong chars, no menu, crash), STOP and report the symptom to Pete.

- [ ] **Step 5: Commit**

```bash
cd ~/git/simcoupe && g add SDL/OSX/SDLMain.m
g commit -m "Add macOS Edit > Paste menu (⌘V)"
```

---

## Task 6: Add `validateMenuItem:` for greyout

**Files:** Modify `~/git/simcoupe/SDL/OSX/SDLMain.m`.

This adds the menu-item-greyout when paste is unavailable (SAM not in a key-accepting state, or clipboard empty). After this task: Tests B1, B2, B3 should all pass.

- [ ] **Step 1: Add `validateMenuItem:` method on the SDLMain controller**

In `SDLMain.m`, the IBAction selectors are methods on the `SDLMain` controller class (defined in `SDLMain.h`). Add the `validateMenuItem:` method near the IBAction selectors. Place it immediately above the block of `- (IBAction)appPreferences:` etc. at line 274:

```objc
// NSMenuValidation: grey out Paste when SAM cannot accept keys or
// the clipboard has no text.
- (BOOL)validateMenuItem:(NSMenuItem *)item
{
    if ([item action] == @selector(editPaste:))
    {
        if (!sim_can_paste())
            return NO;
        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        if (![pb availableTypeFromArray:@[NSPasteboardTypeString]])
            return NO;
        return YES;
    }
    return YES;
}


- (IBAction)appPreferences:(id)sender { sendUserEvent(UE_OPTIONS); }
```

- [ ] **Step 2: Build**

```bash
cd ~/git/simcoupe && cmake --build build 2>&1 | tail -10
```

Expected: clean build.

- [ ] **Step 3: USER VERIFICATION GATE — Tests B1, B2, B3**

This step requires user interaction:

- **B1 (empty clipboard):** Empty the clipboard via `pbcopy < /dev/null`. Launch SimCoupé. With the SAM at the BASIC prompt, click the Edit menu. Expected: **Paste menu item is greyed out**.

- **B2 (SAM not in ROM0 state):** Put text on the clipboard (`pbcopy < /etc/hosts` or copy from text editor). In SimCoupé, press F12 (Reset) and immediately hold a key so the SAM is in a transient state; or activate the debugger via System > Debugger. Click the Edit menu. Expected: **Paste menu item is greyed out**. (Press F12-Reset again or close the debugger to recover.)

- **B3 (clipboard has text + SAM ready):** Put text on the clipboard. SAM at BASIC prompt. Click the Edit menu. Expected: **Paste menu item is enabled**. Clicking it pastes as before.

If any of B1/B2/B3 behaves wrong, STOP and report to Pete.

- [ ] **Step 4: Commit**

```bash
cd ~/git/simcoupe && g add SDL/OSX/SDLMain.m
g commit -m "Grey out Paste when SAM can't accept keys or clipboard is empty"
```

---

## Task 7: End-to-end paste-while-busy test (Test C)

**Files:** None — interactive test only.

Test C exercises Keyin's queue-replace semantics: a second paste while the first is still in flight should interrupt and start typing the new text immediately, with no tail of the first.

- [ ] **Step 1: USER VERIFICATION GATE — Test C**

1. Put a long string on the clipboard. E.g. `python3 -c "print('A'*1500)" | pbcopy` (1500 chars of A).
2. In SimCoupé, at the BASIC prompt, press Cmd+V. Watch As streaming into the SAM.
3. After ~3 seconds (well before the 1500 chars are done), put a different short string on the clipboard: `echo -n "PRINT 1+1" | pbcopy`. Then immediately Cmd+V again.
4. Expected: streaming of As stops; SAM starts typing `PRINT 1+1` cleanly; once that finishes, no further As appear (the original queue was replaced, not appended).
5. Press Enter. Expected: SAM evaluates `PRINT 1+1` and shows `2`.

If you see leftover As after the `PRINT 1+1`, the queue-replace assumption in the spec is wrong and we'd need to dig into `Keyin::String`. STOP and report.

- [ ] **Step 2: No commit needed** (no code changed in this task).

---

## Task 8: Final sanity sweep

**Files:** None.

- [ ] **Step 1: Confirm the full diff is what was intended**

```bash
cd ~/git/simcoupe && g diff main..sdl-paste-clipboard --stat
```

Expected: ~5 files changed, around +40 / -6 lines total. If significantly more, something unintended slipped in.

- [ ] **Step 2: Show Pete the diff for review**

```bash
cd ~/git/simcoupe && g diff main..sdl-paste-clipboard
```

Pete reviews. If he wants changes, they're follow-up commits on the same branch. If he wants to swap to iteration-knob (B) (duplicate handler instead of moving), revert Task 2's commit and add the case to `SDL/UI.cpp::DoAction` instead.

- [ ] **Step 3: Confirm branch is local-only**

```bash
cd ~/git/simcoupe && g log --branches --not --remotes --oneline | head -20
```

Expected: the 5 new commits from this plan, plus any pre-existing unpushed commits. Per the spec, no push to origin is performed.

---

## Coverage check against spec

| Spec section | Covered by |
|---|---|
| 3.1 Architecture, `Base/Actions.cpp` change | Task 2 |
| 3.1 `Win32/UI.cpp` change | Task 2 |
| 3.1 `SDL/UI.h` change | Task 3 |
| 3.1 `SDL/UI.cpp` change | Task 4 |
| 3.1 `SDL/OSX/SDLMain.m` change | Tasks 5, 6 |
| 3.2 Iteration knob (handler location) | Task 2 + Task 8 step 2 rollback note |
| 3.3 Data flow | Task 5 verification gate (Test A) |
| 3.4 Decisions table — no `!IsTyping()`, no `!GUI::IsActive()` | Task 4 step 3 (`sim_can_paste = Keyin::CanType()` only) |
| 3.4 Greyout on empty pasteboard | Task 6 step 1 + Test B1 in Task 6 step 3 |
| 4 Test A | Task 5 step 4 |
| 4 Test B1, B2, B3 | Task 6 step 3 |
| 4 Test C | Task 7 step 1 |
| Spec ref to `git` → use `g` | Embedded in every commit step |
