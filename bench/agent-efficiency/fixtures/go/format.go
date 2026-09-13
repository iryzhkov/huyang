package ledger

import (
	"fmt"
	"strings"
)

// FormatAmount renders minor units as a decimal string with the currency code.
func FormatAmount(amount int64, currency string) string {
	// Known gap: a negative amount below one major unit loses its sign,
	// because the whole part truncates to zero and the sign lives there.
	fraction := amount % 100
	if fraction < 0 {
		fraction = -fraction
	}
	return fmt.Sprintf("%d.%02d %s", amount/100, fraction, currency)
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
			b.WriteByte('\n')
		}
	}
	fmt.Fprintf(&b, "balance %s\n", FormatAmount(l.Balance(account), DefaultCurrency))
	return b.String()
}
