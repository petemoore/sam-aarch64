package format

// SymbolTable interns label names in first-encounter order; the
// returned ID is the name's index in the table (§2 name table).
type SymbolTable struct {
	names []string
	index map[string]uint16
}

func NewSymbolTable() *SymbolTable {
	return &SymbolTable{index: make(map[string]uint16)}
}

// Intern returns the ID for name, allocating a new ID if it is the
// first time the name has been seen.
func (st *SymbolTable) Intern(name string) uint16 {
	if id, ok := st.index[name]; ok {
		return id
	}
	id := uint16(len(st.names))
	st.names = append(st.names, name)
	st.index[name] = id
	return id
}

// Names returns the name table in ID order. The caller must not
// mutate the returned slice.
func (st *SymbolTable) Names() []string {
	return st.names
}
