package format

import "testing"

func TestSymbolTableInternFirstEncounter(t *testing.T) {
	st := NewSymbolTable()
	id := st.Intern("loop")
	if id != 0 {
		t.Errorf("first intern() = %d, want 0", id)
	}
	if got := st.Intern("loop"); got != 0 {
		t.Errorf("second intern of same name = %d, want 0", got)
	}
	id2 := st.Intern("exit")
	if id2 != 1 {
		t.Errorf("intern second name = %d, want 1", id2)
	}
}

func TestSymbolTableName(t *testing.T) {
	st := NewSymbolTable()
	st.Intern("alpha")
	st.Intern("beta")
	if st.Name(0) != "alpha" || st.Name(1) != "beta" {
		t.Errorf("Name lookup failed")
	}
	if st.Name(99) != "" {
		t.Errorf("out-of-range Name should return \"\"")
	}
}

func TestSymbolTableLen(t *testing.T) {
	st := NewSymbolTable()
	if st.Len() != 0 {
		t.Errorf("empty table Len() = %d, want 0", st.Len())
	}
	st.Intern("a")
	st.Intern("b")
	st.Intern("a") // duplicate
	if st.Len() != 2 {
		t.Errorf("Len() = %d after two distinct interns, want 2", st.Len())
	}
}

func TestSymbolTableNames(t *testing.T) {
	st := NewSymbolTable()
	st.Intern("a")
	st.Intern("b")
	names := st.Names()
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Errorf("Names() = %v, want [a b]", names)
	}
}
