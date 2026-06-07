package samscreen

// Key is the input abstraction (spec §2.2): the SAM's actual key surface and
// nothing more. The terminal backend maps host keys onto this model and
// discards anything the SAM cannot express (no Ctrl+Shift+arrow chords, no
// mouse events). A SAM port reads the 9×8 key matrix (TM p16) into the same
// event type.
type Key struct {
	Kind KeyKind
	// Rune is the printable byte for KeyPrintable (the prototype is byte
	// oriented, matching the SAM's 8-bit character set — no Unicode).
	Rune byte
	// Fn is 0–9 for KeyFunc (SAM function keys F0–F9, spec §2.2).
	Fn int
	// Mods are the SAM modifier keys held with this event.
	Mods Mods
}

// KeyKind enumerates the SAM-expressible key categories (spec §2.2).
type KeyKind int

const (
	KeyNone KeyKind = iota
	KeyPrintable
	KeyReturn
	KeyEsc
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyDelete
	KeyTab
	KeyEdit // the SAM EDIT key
	KeyFunc // F0–F9 (see Key.Fn)
)

// Mods is the set of SAM modifier keys (SHIFT / SYMBOL / CNTRL) held with a
// key event (spec §2.2). Host modifiers the SAM lacks are discarded by the
// terminal backend before an event reaches here.
type Mods uint8

const (
	ModShift Mods = 1 << iota
	ModSymbol
	ModCntrl
)

func (m Mods) Has(x Mods) bool { return m&x != 0 }
