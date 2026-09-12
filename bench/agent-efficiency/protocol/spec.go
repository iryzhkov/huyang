package main

// literalEdit is one content-addressed replacement.
type literalEdit struct {
	Path  string
	Old   string
	New   string
	Count int // expected occurrences; 0 means exactly one
}

// languageSpec holds the fixture-specific strings every scenario needs.
type languageSpec struct {
	Language    string
	CoreFile    string // R1, E1, E3
	ReportFile  string // R2, E2
	AuditFile   string // E4
	RelatedFile []string
	Symbol      string // R2
	CallQuery   string // R3
	Build, Test string
	E1          literalEdit
	E2          literalEdit
	E3          literalEdit
	E4Content   string
	E5          []literalEdit
	E6Signature literalEdit
	E6Callers   []literalEdit
}

var goSpec = languageSpec{
	Language: "go", CoreFile: "ledger.go", ReportFile: "report.go", AuditFile: "audit.go",
	RelatedFile: []string{"ledger.go", "store.go", "format.go"},
	Symbol:      "SummarizeQuarter", CallQuery: "ApplyEntry(", Build: "go build ./...", Test: "go test ./...",
	E1: literalEdit{Path: "ledger.go", Old: "const MaxEntries = 10000", New: "const MaxEntries = 20000"},
	E2: literalEdit{Path: "report.go",
		Old: "\t// Validation rules for a quarterly summary. Each rule is checked in\n\t// order and the first failure wins; the messages are user-facing.\n\tif s.Quarter < 1 || s.Quarter > 4 {\n\t\treturn fmt.Errorf(\"report: quarter %d is outside 1-4\", s.Quarter)\n\t}\n\tif s.Year < 1970 {\n\t\treturn fmt.Errorf(\"report: year %d is before the epoch\", s.Year)\n\t}\n",
		New: "\t// Validation rules for a quarterly summary. Each rule is checked in\n\t// order and the first failure wins; the messages are user-facing.\n\tif s.Year < 1970 {\n\t\treturn fmt.Errorf(\"report: year %d is before the epoch\", s.Year)\n\t}\n\tif s.Quarter < 1 || s.Quarter > 4 {\n\t\treturn fmt.Errorf(\"report: quarter %d is outside 1-4\", s.Quarter)\n\t}\n"},
	E3: literalEdit{Path: "ledger.go", Old: "total", New: "sum", Count: 5},
	E4Content: `package ledger

import "fmt"

// Audit compares every cached account balance with the balance summed from
// the entries and returns one line per account that disagrees. An empty
// result means the cache is consistent.
func Audit(l *Ledger) []string {
	var lines []string
	for _, account := range l.Accounts() {
		cached := l.balances[account]
		summed := sumEntries(l, account)
		if cached != summed {
			lines = append(lines, fmt.Sprintf("%s: cached %d, summed %d", account, cached, summed))
		}
	}
	return lines
}

// sumEntries adds the amounts of one account without consulting the cache.
func sumEntries(l *Ledger, account string) int64 {
	var total int64
	for _, e := range l.entries {
		if e.Account == account {
			total += e.Amount
		}
	}
	return total
}

// AuditOK reports whether Audit found nothing.
func AuditOK(l *Ledger) bool {
	return len(Audit(l)) == 0
}
`,
	E5: []literalEdit{
		{Path: "ledger.go", Old: "\tMemo     string\n}", New: "\tMemo     string\n\tTags     []string\n}"},
		{Path: "store.go", Old: "e.Posted.UTC().Format(time.RFC3339), e.Memo,\n\t\t}, \"\\t\")", New: "e.Posted.UTC().Format(time.RFC3339), e.Memo, strings.Join(e.Tags, \",\"),\n\t\t}, \"\\t\")"},
		{Path: "store.go", Old: "\t\tif len(fields) != 7 {\n\t\t\treturn nil, fmt.Errorf(\"store: line %d has %d fields, want 7\", lineNo, len(fields))", New: "\t\tif len(fields) != 8 {\n\t\t\treturn nil, fmt.Errorf(\"store: line %d has %d fields, want 8\", lineNo, len(fields))"},
		{Path: "store.go", Old: "Posted: posted, Memo: fields[6]}", New: "Posted: posted, Memo: fields[6], Tags: strings.FieldsFunc(fields[7], func(r rune) bool { return r == ',' })}"},
		{Path: "format.go", Old: "\treturn fmt.Sprintf(\"%-12s %-10s %14s %-10s %s\", e.ID, e.Account, FormatAmount(e.Amount, e.Currency), e.Category, memo)", New: "\tline := fmt.Sprintf(\"%-12s %-10s %14s %-10s %s\", e.ID, e.Account, FormatAmount(e.Amount, e.Currency), e.Category, memo)\n\tif len(e.Tags) > 0 {\n\t\tline += \" [\" + strings.Join(e.Tags, \",\") + \"]\"\n\t}\n\treturn line"},
	},
	E6Signature: literalEdit{Path: "ledger.go", Old: "func ApplyEntry(l *Ledger, e Entry) error {\n\tif e.ID == \"\" {", New: "func ApplyEntry(l *Ledger, e Entry, strict bool) error {\n\tif strict && e.Category == \"\" {\n\t\treturn errors.New(\"ledger: entry needs a category\")\n\t}\n\tif e.ID == \"\" {"},
	E6Callers: []literalEdit{
		{Path: "ledger.go", Old: "Amount: -amount, Category: \"transfer\"}); err != nil", New: "Amount: -amount, Category: \"transfer\"}, false); err != nil"},
		{Path: "ledger.go", Old: "Amount: amount, Category: \"transfer\"}); err != nil", New: "Amount: amount, Category: \"transfer\"}, false); err != nil"},
		{Path: "", Old: "if err := ApplyEntry(l, e); err != nil {", New: "if err := ApplyEntry(l, e, false); err != nil {", Count: 2},
		{Path: "ledger_test.go", Old: "Amount: 10000, Category: \"salary\"})", New: "Amount: 10000, Category: \"salary\"}, false)"},
		{Path: "ledger_test.go", Old: "Amount: -2500, Category: \"groceries\"})", New: "Amount: -2500, Category: \"groceries\"}, false)"},
		{Path: "ledger_test.go", Old: "Amount: 5000, Category: \"transfer\"})", New: "Amount: 5000, Category: \"transfer\"}, false)"},
		{Path: "ledger_test.go", Old: "Entry{ID: \"e1\", Account: \"cash\", Amount: 1}); err != ErrDuplicate", New: "Entry{ID: \"e1\", Account: \"cash\", Amount: 1}, false); err != ErrDuplicate"},
		{Path: "ledger_test.go", Old: "Amount: 1, Posted: day})", New: "Amount: 1, Posted: day}, false)"},
		{Path: "ledger_test.go", Old: "Posted: day.Add(24 * time.Hour)})", New: "Posted: day.Add(24 * time.Hour)}, false)"},
	},
}

var pythonSpec = languageSpec{
	Language: "python", CoreFile: "ledger/ledger.py", ReportFile: "ledger/report.py", AuditFile: "ledger/audit.py",
	RelatedFile: []string{"ledger/ledger.py", "ledger/store.py", "ledger/fmt.py"},
	Symbol:      "summarize_quarter", CallQuery: "apply_entry(", Build: "python3 -m compileall -q ledger", Test: "python3 -m unittest 2>&1",
	E1: literalEdit{Path: "ledger/ledger.py", Old: "MAX_ENTRIES = 10000", New: "MAX_ENTRIES = 20000"},
	E2: literalEdit{Path: "ledger/report.py",
		Old: "    # Validation rules for a quarterly summary. Each rule is checked in\n    # order and the first failure wins; the messages are user-facing.\n    if summary.quarter < 1 or summary.quarter > 4:\n        raise ValueError(f\"report: quarter {summary.quarter} is outside 1-4\")\n    if summary.year < 1970:\n        raise ValueError(f\"report: year {summary.year} is before the epoch\")\n",
		New: "    # Validation rules for a quarterly summary. Each rule is checked in\n    # order and the first failure wins; the messages are user-facing.\n    if summary.year < 1970:\n        raise ValueError(f\"report: year {summary.year} is before the epoch\")\n    if summary.quarter < 1 or summary.quarter > 4:\n        raise ValueError(f\"report: quarter {summary.quarter} is outside 1-4\")\n"},
	E3: literalEdit{Path: "ledger/ledger.py", Old: "total", New: "sum_", Count: 5},
	E4Content: `"""Consistency checks between the balance cache and the entries."""
from __future__ import annotations

from .ledger import Ledger


def audit(ledger: Ledger) -> list[str]:
    """Return one line per account whose cached balance disagrees with the
    balance summed from the entries. An empty list means the cache is
    consistent."""
    lines: list[str] = []
    for account in sorted(ledger.balances):
        cached = ledger.balances[account]
        summed = _sum_entries(ledger, account)
        if cached != summed:
            lines.append(f"{account}: cached {cached}, summed {summed}")
    return lines


def _sum_entries(ledger: Ledger, account: str) -> int:
    """Add the amounts of one account without consulting the cache."""
    total = 0
    for entry in ledger.entries:
        if entry.account == account:
            total += entry.amount
    return total


def audit_ok(ledger: Ledger) -> bool:
    """Report whether audit found nothing."""
    return not audit(ledger)
`,
	E5: []literalEdit{
		{Path: "ledger/ledger.py", Old: "    memo: str = \"\"\n", New: "    memo: str = \"\"\n    tags: list[str] = field(default_factory=list)\n"},
		{Path: "ledger/store.py", Old: "            entry.posted.isoformat(), entry.memo,\n        ]", New: "            entry.posted.isoformat(), entry.memo, \",\".join(entry.tags),\n        ]"},
		{Path: "ledger/store.py", Old: "        if len(fields) != 7:\n            raise ValueError(f\"store: line {line_no} has {len(fields)} fields, want 7\")", New: "        if len(fields) != 8:\n            raise ValueError(f\"store: line {line_no} has {len(fields)} fields, want 8\")"},
		{Path: "ledger/store.py", Old: "category=fields[4], posted=datetime.fromisoformat(fields[5]), memo=fields[6],\n        )", New: "category=fields[4], posted=datetime.fromisoformat(fields[5]), memo=fields[6],\n            tags=[tag for tag in fields[7].split(\",\") if tag],\n        )"},
		{Path: "ledger/fmt.py", Old: "    return f\"{entry.id:<12} {entry.account:<10} {format_amount(entry.amount, entry.currency):>14} {entry.category:<10} {memo}\"", New: "    line = f\"{entry.id:<12} {entry.account:<10} {format_amount(entry.amount, entry.currency):>14} {entry.category:<10} {memo}\"\n    if entry.tags:\n        line += \" [\" + \",\".join(entry.tags) + \"]\"\n    return line"},
	},
	E6Signature: literalEdit{Path: "ledger/ledger.py", Old: "def apply_entry(ledger: Ledger, entry: Entry) -> None:\n    \"\"\"Validate and append one entry, updating the account balance.\"\"\"\n", New: "def apply_entry(ledger: Ledger, entry: Entry, strict: bool) -> None:\n    \"\"\"Validate and append one entry, updating the account balance.\"\"\"\n    if strict and not entry.category:\n        raise ValueError(\"ledger: entry needs a category\")\n"},
	E6Callers: []literalEdit{
		{Path: "ledger/ledger.py", Old: "amount=-amount, category=\"transfer\"))", New: "amount=-amount, category=\"transfer\"), False)"},
		{Path: "ledger/ledger.py", Old: "account=target, amount=amount, category=\"transfer\"))", New: "account=target, amount=amount, category=\"transfer\"), False)"},
		{Path: "", Old: "apply_entry(ledger, entry)\n", New: "apply_entry(ledger, entry, False)\n", Count: 2},
		{Path: "tests/test_ledger.py", Old: "amount=10000, category=\"salary\"))", New: "amount=10000, category=\"salary\"), False)"},
		{Path: "tests/test_ledger.py", Old: "amount=-2500, category=\"groceries\"))", New: "amount=-2500, category=\"groceries\"), False)"},
		{Path: "tests/test_ledger.py", Old: "amount=5000, category=\"transfer\"))", New: "amount=5000, category=\"transfer\"), False)"},
		{Path: "tests/test_ledger.py", Old: "Entry(id=\"e1\", account=\"cash\", amount=1))", New: "Entry(id=\"e1\", account=\"cash\", amount=1), False)"},
		{Path: "tests/test_ledger.py", Old: "amount=1, posted=day))", New: "amount=1, posted=day), False)"},
		{Path: "tests/test_ledger.py", Old: "posted=day + timedelta(days=1)))", New: "posted=day + timedelta(days=1)), False)"},
	},
}

var specs = []languageSpec{goSpec, pythonSpec}
