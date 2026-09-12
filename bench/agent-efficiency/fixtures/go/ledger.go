package ledger

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// DefaultCurrency is the currency assumed for entries that do not name one.
const DefaultCurrency = "USD"

// MaxEntries bounds a ledger so an unbounded import cannot exhaust memory.
const MaxEntries = 10000

// Entry is one posted amount. Amounts are in minor units (cents).
type Entry struct {
	ID       string
	Account  string
	Amount   int64
	Currency string
	Category string
	Posted   time.Time
	Memo     string
}

// Ledger is an append-only list of entries with a per-account balance cache.
type Ledger struct {
	entries  []Entry
	balances map[string]int64
	seen     map[string]struct{}
}

// ErrDuplicate is returned when an entry ID was already applied.
var ErrDuplicate = errors.New("ledger: duplicate entry")

// ErrFull is returned when the ledger holds MaxEntries entries.
var ErrFull = errors.New("ledger: full")

// NewLedger returns an empty ledger.
func NewLedger() *Ledger {
	return &Ledger{balances: map[string]int64{}, seen: map[string]struct{}{}}
}

// ApplyEntry validates and appends one entry, updating the account balance.
func ApplyEntry(l *Ledger, e Entry) error {
	if e.ID == "" {
		return errors.New("ledger: entry needs an id")
	}
	if e.Account == "" {
		return errors.New("ledger: entry needs an account")
	}
	if _, dup := l.seen[e.ID]; dup {
		return ErrDuplicate
	}
	if len(l.entries) >= MaxEntries {
		return ErrFull
	}
	if e.Currency == "" {
		e.Currency = DefaultCurrency
	}
	if e.Posted.IsZero() {
		e.Posted = time.Now().UTC()
	}
	l.entries = append(l.entries, e)
	l.seen[e.ID] = struct{}{}
	l.balances[e.Account] += e.Amount
	return nil
}

// Balance returns the balance of one account, summed from the entries so the
// cache can be cross-checked.
func (l *Ledger) Balance(account string) int64 {
	var total int64
	for _, e := range l.entries {
		if e.Account != account {
			continue
		}
		total += e.Amount
	}
	if cached, ok := l.balances[account]; ok && cached != total {
		panic(fmt.Sprintf("ledger: balance cache drift for %s: %d != %d", account, cached, total))
	}
	return total
}

// Accounts lists the accounts that have at least one entry, sorted.
func (l *Ledger) Accounts() []string {
	names := make([]string, 0, len(l.balances))
	for name := range l.balances {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Entries returns a copy of the entries in posting order.
func (l *Ledger) Entries() []Entry {
	out := make([]Entry, len(l.entries))
	copy(out, l.entries)
	return out
}

// ByCategory groups entry amounts by category.
func (l *Ledger) ByCategory() map[string]int64 {
	out := map[string]int64{}
	for _, e := range l.entries {
		out[e.Category] += e.Amount
	}
	return out
}

// Between returns the entries posted in [from, to).
func (l *Ledger) Between(from, to time.Time) []Entry {
	var out []Entry
	for _, e := range l.entries {
		if !e.Posted.Before(from) && e.Posted.Before(to) {
			out = append(out, e)
		}
	}
	return out
}

// Transfer posts a matching pair of entries moving amount from one account to
// another. Both entries share the transfer id prefix.
func Transfer(l *Ledger, id, from, to string, amount int64) error {
	if amount <= 0 {
		return errors.New("ledger: transfer amount must be positive")
	}
	if err := ApplyEntry(l, Entry{ID: id + ":out", Account: from, Amount: -amount, Category: "transfer"}); err != nil {
		return err
	}
	if err := ApplyEntry(l, Entry{ID: id + ":in", Account: to, Amount: amount, Category: "transfer"}); err != nil {
		return err
	}
	return nil
}

// Import applies every entry and stops at the first error, reporting how many
// were applied.
func Import(l *Ledger, entries []Entry) (int, error) {
	for i, e := range entries {
		if err := ApplyEntry(l, e); err != nil {
			return i, fmt.Errorf("ledger: import entry %d: %w", i, err)
		}
	}
	return len(entries), nil
}
