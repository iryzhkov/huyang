package huyangfixture

// Store is the exported interface an impact preview should notice: a change
// to it reaches every implementation and every caller.
type Store interface {
	Balance() int
}

// Memory is one implementation.
type Memory struct{ amount int }

func (m Memory) Balance() int { return m.amount }

// Ledger is the other.
type Ledger struct{}

func (Ledger) Balance() int { return Total() }

// Sum reads the interface rather than either implementation, so it is a
// caller a file-level reader would miss.
func Sum(store Store) int { return store.Balance() }
