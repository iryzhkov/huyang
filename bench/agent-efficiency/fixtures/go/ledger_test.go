package ledger

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
