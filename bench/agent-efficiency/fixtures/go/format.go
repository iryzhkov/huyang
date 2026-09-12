package ledger

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
			b.WriteByte('\n')
		}
	}
	fmt.Fprintf(&b, "balance %s\n", FormatAmount(l.Balance(account), DefaultCurrency))
	return b.String()
}
