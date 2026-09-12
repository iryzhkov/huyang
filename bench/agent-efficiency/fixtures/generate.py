#!/usr/bin/env python3
"""Generate the benchmark fixture repositories deterministically.

Two small repositories are produced under fixtures/go and fixtures/python. Each one has
the same shape so that read and edit scenarios are comparable across languages:

- a ~150-line core module (ledger) with a function that is called from many places,
- a ~900-line report module with one well-known function in the middle,
- two small helper modules that a coordinated three-file edit touches,
- a test suite with one deliberately failing test for the V1 scenario.

Run `python3 generate.py` from this directory; the output is committed so the agent
under test sees identical files on every run. Do not hand-edit the generated files.
"""
from __future__ import annotations

import os
import textwrap

HERE = os.path.dirname(os.path.abspath(__file__))
SECTIONS = 52  # report sections per language; each section is ~14 lines
CATEGORIES = ["rent", "salary", "groceries", "transport", "utilities", "leisure"]


def write(path: str, content: str) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as handle:
        handle.write(content)


# ---------------------------------------------------------------------------
# Go fixture
# ---------------------------------------------------------------------------

GO_MOD = "module example.com/ledger\n\ngo 1.22\n"

GO_LEDGER = '''package ledger

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
'''

GO_STORE = '''package ledger

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Save writes the ledger as tab-separated lines: id, account, amount,
// currency, category, posted (RFC 3339), memo.
func Save(w io.Writer, l *Ledger) error {
	bw := bufio.NewWriter(w)
	for _, e := range l.Entries() {
		line := strings.Join([]string{
			e.ID, e.Account, strconv.FormatInt(e.Amount, 10), e.Currency, e.Category,
			e.Posted.UTC().Format(time.RFC3339), e.Memo,
		}, "\\t")
		if _, err := bw.WriteString(line + "\\n"); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// Load reads lines written by Save and applies them to a new ledger.
func Load(r io.Reader) (*Ledger, error) {
	l := NewLedger()
	scanner := bufio.NewScanner(r)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		fields := strings.Split(scanner.Text(), "\\t")
		if len(fields) != 7 {
			return nil, fmt.Errorf("store: line %d has %d fields, want 7", lineNo, len(fields))
		}
		amount, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("store: line %d amount: %w", lineNo, err)
		}
		posted, err := time.Parse(time.RFC3339, fields[5])
		if err != nil {
			return nil, fmt.Errorf("store: line %d posted: %w", lineNo, err)
		}
		e := Entry{ID: fields[0], Account: fields[1], Amount: amount, Currency: fields[3], Category: fields[4], Posted: posted, Memo: fields[6]}
		if err := ApplyEntry(l, e); err != nil {
			return nil, fmt.Errorf("store: line %d: %w", lineNo, err)
		}
	}
	return l, scanner.Err()
}
'''

GO_FORMAT = '''package ledger

import (
	"fmt"
	"strings"
)

// FormatAmount renders minor units as a decimal string with the currency code.
func FormatAmount(amount int64, currency string) string {
	sign := ""
	if amount < 0 {
		sign = "-"
		amount = -amount
	}
	return fmt.Sprintf("%s%d.%02d %s", sign, amount/100, amount%100, currency)
}

// FormatEntry renders one entry on a single line.
func FormatEntry(e Entry) string {
	memo := e.Memo
	if memo == "" {
		memo = "-"
	}
	return fmt.Sprintf("%-12s %-10s %14s %-10s %s", e.ID, e.Account, FormatAmount(e.Amount, e.Currency), e.Category, memo)
}

// FormatStatement renders every entry of an account followed by its balance.
func FormatStatement(l *Ledger, account string) string {
	var b strings.Builder
	for _, e := range l.Entries() {
		if e.Account == account {
			b.WriteString(FormatEntry(e))
			b.WriteByte('\\n')
		}
	}
	fmt.Fprintf(&b, "balance %s\\n", FormatAmount(l.Balance(account), DefaultCurrency))
	return b.String()
}
'''

GO_TEST = '''package ledger

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func sample() *Ledger {
	l := NewLedger()
	_ = ApplyEntry(l, Entry{ID: "e1", Account: "cash", Amount: 10000, Category: "salary"})
	_ = ApplyEntry(l, Entry{ID: "e2", Account: "cash", Amount: -2500, Category: "groceries"})
	_ = ApplyEntry(l, Entry{ID: "e3", Account: "savings", Amount: 5000, Category: "transfer"})
	return l
}

func TestApplyEntryRejectsDuplicates(t *testing.T) {
	l := sample()
	if err := ApplyEntry(l, Entry{ID: "e1", Account: "cash", Amount: 1}); err != ErrDuplicate {
		t.Fatalf("duplicate accepted: %v", err)
	}
}

func TestBalanceSumsAccount(t *testing.T) {
	l := sample()
	if got := l.Balance("cash"); got != 7500 {
		t.Fatalf("cash balance = %d, want 7500", got)
	}
}

func TestTransferMovesAmount(t *testing.T) {
	l := sample()
	if err := Transfer(l, "t1", "cash", "savings", 500); err != nil {
		t.Fatal(err)
	}
	if l.Balance("cash") != 7000 || l.Balance("savings") != 5500 {
		t.Fatalf("balances after transfer: cash=%d savings=%d", l.Balance("cash"), l.Balance("savings"))
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	l := sample()
	var buf bytes.Buffer
	if err := Save(&buf, l); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Balance("cash") != l.Balance("cash") {
		t.Fatalf("round trip changed cash balance")
	}
}

func TestQuarterSummaryCountsEntries(t *testing.T) {
	l := sample()
	now := time.Now().UTC()
	summary := SummarizeQuarter(l, now.Year(), (int(now.Month())-1)/3+1)
	if summary.Entries != 3 {
		t.Fatalf("entries in quarter = %d, want 3", summary.Entries)
	}
}

// TestFormatAmountNegative documents a known formatting gap: negative amounts
// below one major unit lose their sign. It fails on purpose so the V1 scenario
// has a real failure to report.
func TestFormatAmountNegative(t *testing.T) {
	if got := FormatAmount(-5, "USD"); got != "-0.05 USD" {
		t.Fatalf("FormatAmount(-5) = %q, want -0.05 USD", got)
	}
}

func TestBetweenUsesHalfOpenRange(t *testing.T) {
	l := NewLedger()
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	_ = ApplyEntry(l, Entry{ID: "a", Account: "x", Amount: 1, Posted: day})
	_ = ApplyEntry(l, Entry{ID: "b", Account: "x", Amount: 1, Posted: day.Add(24 * time.Hour)})
	if got := len(l.Between(day, day.Add(24*time.Hour))); got != 1 {
		t.Fatalf("Between returned %d entries, want 1", got)
	}
}
'''


def go_report() -> str:
    parts = [
        "package ledger\n\n",
        'import (\n\t"fmt"\n\t"sort"\n\t"strings"\n\t"time"\n)\n\n',
        "// QuarterSummary is the aggregate a quarterly report is built from.\n",
        "type QuarterSummary struct {\n\tYear     int\n\tQuarter  int\n\tEntries  int\n\tIncome   int64\n\tExpense  int64\n\tByCat    map[string]int64\n\tAccounts []string\n}\n\n",
        "// Section is one rendered block of a report.\n",
        "type Section struct {\n\tTitle string\n\tLines []string\n}\n\n",
        "func quarterBounds(year, quarter int) (time.Time, time.Time) {\n\tstart := time.Date(year, time.Month((quarter-1)*3+1), 1, 0, 0, 0, 0, time.UTC)\n\treturn start, start.AddDate(0, 3, 0)\n}\n\n",
    ]
    half = SECTIONS // 2
    for i in range(1, SECTIONS + 1):
        if i == half:
            parts.append(GO_SUMMARIZE)
        if i == half + 5:
            parts.append(GO_VALIDATION_BLOCK)
        cat = CATEGORIES[i % len(CATEGORIES)]
        parts.append(textwrap.dedent(f'''\
            // reportSection{i:02d} renders the {cat} view number {i} of the ledger.
            func reportSection{i:02d}(l *Ledger) Section {{
            \tlines := make([]string, 0, 8)
            \tvar total int64
            \tfor _, e := range l.Entries() {{
            \t\tif e.Category != "{cat}" {{
            \t\t\tcontinue
            \t\t}}
            \t\ttotal += e.Amount
            \t\tlines = append(lines, FormatEntry(e))
            \t}}
            \tlines = append(lines, fmt.Sprintf("section {i} total %s", FormatAmount(total, DefaultCurrency)))
            \treturn Section{{Title: "{cat} #{i}", Lines: lines}}
            }}

            '''))
    parts.append("// Render renders every section in order.\nfunc Render(l *Ledger) string {\n\tsections := []Section{\n")
    for i in range(1, SECTIONS + 1):
        parts.append(f"\t\treportSection{i:02d}(l),\n")
    parts.append("\t}\n\tvar b strings.Builder\n\tfor _, s := range sections {\n\t\tb.WriteString(s.Title)\n\t\tb.WriteByte('\\n')\n\t\tfor _, line := range s.Lines {\n\t\t\tb.WriteString(line)\n\t\t\tb.WriteByte('\\n')\n\t\t}\n\t}\n\treturn b.String()\n}\n")
    return "".join(parts)


GO_SUMMARIZE = '''// SummarizeQuarter aggregates the entries posted in one calendar quarter.
func SummarizeQuarter(l *Ledger, year, quarter int) QuarterSummary {
	from, to := quarterBounds(year, quarter)
	summary := QuarterSummary{Year: year, Quarter: quarter, ByCat: map[string]int64{}}
	seen := map[string]struct{}{}
	for _, e := range l.Between(from, to) {
		summary.Entries++
		if e.Amount >= 0 {
			summary.Income += e.Amount
		} else {
			summary.Expense -= e.Amount
		}
		summary.ByCat[e.Category] += e.Amount
		seen[e.Account] = struct{}{}
	}
	for name := range seen {
		summary.Accounts = append(summary.Accounts, name)
	}
	sort.Strings(summary.Accounts)
	return summary
}

'''

GO_VALIDATION_BLOCK = '''// validateSummary checks a summary before it is rendered.
func validateSummary(s QuarterSummary) error {
	// Validation rules for a quarterly summary. Each rule is checked in
	// order and the first failure wins; the messages are user-facing.
	if s.Quarter < 1 || s.Quarter > 4 {
		return fmt.Errorf("report: quarter %d is outside 1-4", s.Quarter)
	}
	if s.Year < 1970 {
		return fmt.Errorf("report: year %d is before the epoch", s.Year)
	}
	if s.Income < 0 || s.Expense < 0 {
		return fmt.Errorf("report: income and expense must be non-negative")
	}
	if len(s.ByCat) == 0 && s.Entries > 0 {
		return fmt.Errorf("report: %d entries but no categories", s.Entries)
	}
	return nil
}

'''


# ---------------------------------------------------------------------------
# Python fixture
# ---------------------------------------------------------------------------

PY_LEDGER = '''"""Core ledger types and operations."""
from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime, timezone

# Currency assumed for entries that do not name one.
DEFAULT_CURRENCY = "USD"

# Bound on the number of entries so an unbounded import cannot exhaust memory.
MAX_ENTRIES = 10000


class DuplicateEntry(Exception):
    """Raised when an entry id was already applied."""


class LedgerFull(Exception):
    """Raised when the ledger holds MAX_ENTRIES entries."""


@dataclass
class Entry:
    """One posted amount, in minor units (cents)."""

    id: str
    account: str
    amount: int
    currency: str = ""
    category: str = ""
    posted: datetime | None = None
    memo: str = ""


@dataclass
class Ledger:
    """An append-only list of entries with a per-account balance cache."""

    entries: list[Entry] = field(default_factory=list)
    balances: dict[str, int] = field(default_factory=dict)
    seen: set[str] = field(default_factory=set)


def new_ledger() -> Ledger:
    """Return an empty ledger."""
    return Ledger()


def apply_entry(ledger: Ledger, entry: Entry) -> None:
    """Validate and append one entry, updating the account balance."""
    if not entry.id:
        raise ValueError("ledger: entry needs an id")
    if not entry.account:
        raise ValueError("ledger: entry needs an account")
    if entry.id in ledger.seen:
        raise DuplicateEntry(entry.id)
    if len(ledger.entries) >= MAX_ENTRIES:
        raise LedgerFull()
    if not entry.currency:
        entry.currency = DEFAULT_CURRENCY
    if entry.posted is None:
        entry.posted = datetime.now(timezone.utc)
    ledger.entries.append(entry)
    ledger.seen.add(entry.id)
    ledger.balances[entry.account] = ledger.balances.get(entry.account, 0) + entry.amount


def balance(ledger: Ledger, account: str) -> int:
    """Return the balance of one account, summed from the entries so the
    cache can be cross-checked."""
    total = 0
    for entry in ledger.entries:
        if entry.account != account:
            continue
        total += entry.amount
    cached = ledger.balances.get(account)
    if cached is not None and cached != total:
        raise RuntimeError(f"ledger: balance cache drift for {account}: {cached} != {total}")
    return total


def accounts(ledger: Ledger) -> list[str]:
    """List the accounts that have at least one entry, sorted."""
    return sorted(ledger.balances)


def by_category(ledger: Ledger) -> dict[str, int]:
    """Group entry amounts by category."""
    out: dict[str, int] = {}
    for entry in ledger.entries:
        out[entry.category] = out.get(entry.category, 0) + entry.amount
    return out


def between(ledger: Ledger, start: datetime, end: datetime) -> list[Entry]:
    """Return the entries posted in [start, end)."""
    out = []
    for entry in ledger.entries:
        assert entry.posted is not None
        if start <= entry.posted < end:
            out.append(entry)
    return out


def transfer(ledger: Ledger, id: str, source: str, target: str, amount: int) -> None:
    """Post a matching pair of entries moving amount between two accounts."""
    if amount <= 0:
        raise ValueError("ledger: transfer amount must be positive")
    apply_entry(ledger, Entry(id=f"{id}:out", account=source, amount=-amount, category="transfer"))
    apply_entry(ledger, Entry(id=f"{id}:in", account=target, amount=amount, category="transfer"))


def import_entries(ledger: Ledger, entries: list[Entry]) -> int:
    """Apply every entry and stop at the first error, returning how many were applied."""
    for index, entry in enumerate(entries):
        try:
            apply_entry(ledger, entry)
        except Exception as error:  # noqa: BLE001 - wrap any failure with its index
            raise RuntimeError(f"ledger: import entry {index}: {error}") from error
    return len(entries)
'''

PY_STORE = '''"""Tab-separated persistence for a ledger."""
from __future__ import annotations

from datetime import datetime
from typing import IO

from .ledger import Entry, Ledger, apply_entry, new_ledger


def save(stream: IO[str], ledger: Ledger) -> None:
    """Write the ledger as tab-separated lines: id, account, amount, currency,
    category, posted (ISO 8601), memo."""
    for entry in ledger.entries:
        assert entry.posted is not None
        fields = [
            entry.id, entry.account, str(entry.amount), entry.currency, entry.category,
            entry.posted.isoformat(), entry.memo,
        ]
        stream.write("\\t".join(fields) + "\\n")


def load(stream: IO[str]) -> Ledger:
    """Read lines written by save and apply them to a new ledger."""
    ledger = new_ledger()
    for line_no, line in enumerate(stream, start=1):
        fields = line.rstrip("\\n").split("\\t")
        if len(fields) != 7:
            raise ValueError(f"store: line {line_no} has {len(fields)} fields, want 7")
        entry = Entry(
            id=fields[0], account=fields[1], amount=int(fields[2]), currency=fields[3],
            category=fields[4], posted=datetime.fromisoformat(fields[5]), memo=fields[6],
        )
        apply_entry(ledger, entry)
    return ledger
'''

PY_FORMAT = '''"""Rendering helpers for entries and statements."""
from __future__ import annotations

from .ledger import DEFAULT_CURRENCY, Entry, Ledger, balance


def format_amount(amount: int, currency: str) -> str:
    """Render minor units as a decimal string with the currency code."""
    sign = ""
    if amount < 0:
        sign = "-"
        amount = -amount
    return f"{sign}{amount // 100}.{amount % 100:02d} {currency}"


def format_entry(entry: Entry) -> str:
    """Render one entry on a single line."""
    memo = entry.memo or "-"
    return f"{entry.id:<12} {entry.account:<10} {format_amount(entry.amount, entry.currency):>14} {entry.category:<10} {memo}"


def format_statement(ledger: Ledger, account: str) -> str:
    """Render every entry of an account followed by its balance."""
    lines = [format_entry(e) for e in ledger.entries if e.account == account]
    lines.append(f"balance {format_amount(balance(ledger, account), DEFAULT_CURRENCY)}")
    return "\\n".join(lines) + "\\n"
'''

PY_TEST = '''"""Unit tests for the ledger package (run with python3 -m unittest)."""
from __future__ import annotations

import io
import unittest
from datetime import datetime, timedelta, timezone

from ledger.fmt import format_amount
from ledger.ledger import DuplicateEntry, Entry, apply_entry, balance, between, new_ledger, transfer
from ledger.report import summarize_quarter
from ledger.store import load, save


def sample():
    ledger = new_ledger()
    apply_entry(ledger, Entry(id="e1", account="cash", amount=10000, category="salary"))
    apply_entry(ledger, Entry(id="e2", account="cash", amount=-2500, category="groceries"))
    apply_entry(ledger, Entry(id="e3", account="savings", amount=5000, category="transfer"))
    return ledger


class LedgerTests(unittest.TestCase):
    def test_apply_entry_rejects_duplicates(self):
        ledger = sample()
        with self.assertRaises(DuplicateEntry):
            apply_entry(ledger, Entry(id="e1", account="cash", amount=1))

    def test_balance_sums_account(self):
        self.assertEqual(balance(sample(), "cash"), 7500)

    def test_transfer_moves_amount(self):
        ledger = sample()
        transfer(ledger, "t1", "cash", "savings", 500)
        self.assertEqual(balance(ledger, "cash"), 7000)
        self.assertEqual(balance(ledger, "savings"), 5500)

    def test_save_load_round_trip(self):
        ledger = sample()
        buffer = io.StringIO()
        save(buffer, ledger)
        loaded = load(io.StringIO(buffer.getvalue()))
        self.assertEqual(balance(loaded, "cash"), balance(ledger, "cash"))

    def test_quarter_summary_counts_entries(self):
        now = datetime.now(timezone.utc)
        summary = summarize_quarter(sample(), now.year, (now.month - 1) // 3 + 1)
        self.assertEqual(summary.entries, 3)

    def test_format_amount_negative(self):
        # Documents a known gap: negative amounts below one major unit lose
        # their sign. Fails on purpose so the V1 scenario has a failure to report.
        self.assertEqual(format_amount(-5, "USD"), "-0.05 USD")

    def test_between_uses_half_open_range(self):
        ledger = new_ledger()
        day = datetime(2026, 3, 1, tzinfo=timezone.utc)
        apply_entry(ledger, Entry(id="a", account="x", amount=1, posted=day))
        apply_entry(ledger, Entry(id="b", account="x", amount=1, posted=day + timedelta(days=1)))
        self.assertEqual(len(between(ledger, day, day + timedelta(days=1))), 1)


if __name__ == "__main__":
    unittest.main()
'''

PY_SUMMARIZE = '''def summarize_quarter(ledger: Ledger, year: int, quarter: int) -> QuarterSummary:
    """Aggregate the entries posted in one calendar quarter."""
    start, end = quarter_bounds(year, quarter)
    summary = QuarterSummary(year=year, quarter=quarter)
    seen: set[str] = set()
    for entry in between(ledger, start, end):
        summary.entries += 1
        if entry.amount >= 0:
            summary.income += entry.amount
        else:
            summary.expense -= entry.amount
        summary.by_cat[entry.category] = summary.by_cat.get(entry.category, 0) + entry.amount
        seen.add(entry.account)
    summary.accounts = sorted(seen)
    return summary


'''

PY_VALIDATION_BLOCK = '''def validate_summary(summary: QuarterSummary) -> None:
    """Check a summary before it is rendered."""
    # Validation rules for a quarterly summary. Each rule is checked in
    # order and the first failure wins; the messages are user-facing.
    if summary.quarter < 1 or summary.quarter > 4:
        raise ValueError(f"report: quarter {summary.quarter} is outside 1-4")
    if summary.year < 1970:
        raise ValueError(f"report: year {summary.year} is before the epoch")
    if summary.income < 0 or summary.expense < 0:
        raise ValueError("report: income and expense must be non-negative")
    if not summary.by_cat and summary.entries > 0:
        raise ValueError(f"report: {summary.entries} entries but no categories")


'''


def py_report() -> str:
    parts = [
        '"""Quarterly and sectioned reports over a ledger."""\n',
        "from __future__ import annotations\n\n",
        "from dataclasses import dataclass, field\n",
        "from datetime import datetime, timezone\n\n",
        "from .fmt import format_amount, format_entry\n",
        "from .ledger import DEFAULT_CURRENCY, Ledger, between\n\n\n",
        "@dataclass\nclass QuarterSummary:\n    \"\"\"The aggregate a quarterly report is built from.\"\"\"\n\n",
        "    year: int\n    quarter: int\n    entries: int = 0\n    income: int = 0\n    expense: int = 0\n",
        "    by_cat: dict[str, int] = field(default_factory=dict)\n    accounts: list[str] = field(default_factory=list)\n\n\n",
        "@dataclass\nclass Section:\n    \"\"\"One rendered block of a report.\"\"\"\n\n    title: str\n    lines: list[str]\n\n\n",
        "def quarter_bounds(year: int, quarter: int) -> tuple[datetime, datetime]:\n",
        "    start = datetime(year, (quarter - 1) * 3 + 1, 1, tzinfo=timezone.utc)\n",
        "    end_month = start.month + 3\n",
        "    end = datetime(year + (1 if end_month > 12 else 0), end_month if end_month <= 12 else end_month - 12, 1, tzinfo=timezone.utc)\n",
        "    return start, end\n\n\n",
    ]
    half = SECTIONS // 2
    for i in range(1, SECTIONS + 1):
        if i == half:
            parts.append(PY_SUMMARIZE)
        if i == half + 5:
            parts.append(PY_VALIDATION_BLOCK)
        cat = CATEGORIES[i % len(CATEGORIES)]
        parts.append(textwrap.dedent(f'''\
            def report_section_{i:02d}(ledger: Ledger) -> Section:
                """Render the {cat} view number {i} of the ledger."""
                lines: list[str] = []
                total = 0
                for entry in ledger.entries:
                    if entry.category != "{cat}":
                        continue
                    total += entry.amount
                    lines.append(format_entry(entry))
                lines.append(f"section {i} total {{format_amount(total, DEFAULT_CURRENCY)}}")
                return Section(title="{cat} #{i}", lines=lines)


            '''))
    parts.append("def render(ledger: Ledger) -> str:\n    \"\"\"Render every section in order.\"\"\"\n    sections = [\n")
    for i in range(1, SECTIONS + 1):
        parts.append(f"        report_section_{i:02d}(ledger),\n")
    parts.append("    ]\n    out: list[str] = []\n    for section in sections:\n        out.append(section.title)\n        out.extend(section.lines)\n    return \"\\n\".join(out) + \"\\n\"\n")
    return "".join(parts)


def main() -> None:
    go = os.path.join(HERE, "go")
    write(os.path.join(go, "go.mod"), GO_MOD)
    write(os.path.join(go, "ledger.go"), GO_LEDGER)
    write(os.path.join(go, "store.go"), GO_STORE)
    write(os.path.join(go, "format.go"), GO_FORMAT)
    write(os.path.join(go, "report.go"), go_report())
    write(os.path.join(go, "ledger_test.go"), GO_TEST)
    py = os.path.join(HERE, "python")
    write(os.path.join(py, "ledger", "__init__.py"), '"""Benchmark fixture package."""\n')
    write(os.path.join(py, "ledger", "ledger.py"), PY_LEDGER)
    write(os.path.join(py, "ledger", "store.py"), PY_STORE)
    write(os.path.join(py, "ledger", "fmt.py"), PY_FORMAT)
    write(os.path.join(py, "ledger", "report.py"), py_report())
    write(os.path.join(py, "tests", "__init__.py"), "")
    write(os.path.join(py, "tests", "test_ledger.py"), PY_TEST)
    write(os.path.join(py, "pyproject.toml"), '[project]\nname = "ledger"\nversion = "0.1.0"\n')
    for root in (go, py):
        for dirpath, dirnames, files in os.walk(root):
            dirnames[:] = [d for d in dirnames if d != "__pycache__"]
            for name in sorted(files):
                path = os.path.join(dirpath, name)
                with open(path, encoding="utf-8") as handle:
                    print(f"{sum(1 for _ in handle):5d} {os.path.relpath(path, HERE)}")


if __name__ == "__main__":
    main()
