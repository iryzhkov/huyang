package ledger

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// QuarterSummary is the aggregate a quarterly report is built from.
type QuarterSummary struct {
	Year     int
	Quarter  int
	Entries  int
	Income   int64
	Expense  int64
	ByCat    map[string]int64
	Accounts []string
}

// Section is one rendered block of a report.
type Section struct {
	Title string
	Lines []string
}

func quarterBounds(year, quarter int) (time.Time, time.Time) {
	start := time.Date(year, time.Month((quarter-1)*3+1), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 3, 0)
}

// reportSection01 renders the salary view number 1 of the ledger.
func reportSection01(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "salary" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 1 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "salary #1", Lines: lines}
}

// reportSection02 renders the groceries view number 2 of the ledger.
func reportSection02(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "groceries" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 2 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "groceries #2", Lines: lines}
}

// reportSection03 renders the transport view number 3 of the ledger.
func reportSection03(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "transport" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 3 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "transport #3", Lines: lines}
}

// reportSection04 renders the utilities view number 4 of the ledger.
func reportSection04(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "utilities" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 4 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "utilities #4", Lines: lines}
}

// reportSection05 renders the leisure view number 5 of the ledger.
func reportSection05(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "leisure" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 5 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "leisure #5", Lines: lines}
}

// reportSection06 renders the rent view number 6 of the ledger.
func reportSection06(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "rent" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 6 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "rent #6", Lines: lines}
}

// reportSection07 renders the salary view number 7 of the ledger.
func reportSection07(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "salary" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 7 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "salary #7", Lines: lines}
}

// reportSection08 renders the groceries view number 8 of the ledger.
func reportSection08(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "groceries" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 8 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "groceries #8", Lines: lines}
}

// reportSection09 renders the transport view number 9 of the ledger.
func reportSection09(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "transport" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 9 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "transport #9", Lines: lines}
}

// reportSection10 renders the utilities view number 10 of the ledger.
func reportSection10(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "utilities" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 10 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "utilities #10", Lines: lines}
}

// reportSection11 renders the leisure view number 11 of the ledger.
func reportSection11(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "leisure" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 11 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "leisure #11", Lines: lines}
}

// reportSection12 renders the rent view number 12 of the ledger.
func reportSection12(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "rent" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 12 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "rent #12", Lines: lines}
}

// reportSection13 renders the salary view number 13 of the ledger.
func reportSection13(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "salary" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 13 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "salary #13", Lines: lines}
}

// reportSection14 renders the groceries view number 14 of the ledger.
func reportSection14(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "groceries" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 14 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "groceries #14", Lines: lines}
}

// reportSection15 renders the transport view number 15 of the ledger.
func reportSection15(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "transport" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 15 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "transport #15", Lines: lines}
}

// reportSection16 renders the utilities view number 16 of the ledger.
func reportSection16(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "utilities" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 16 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "utilities #16", Lines: lines}
}

// reportSection17 renders the leisure view number 17 of the ledger.
func reportSection17(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "leisure" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 17 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "leisure #17", Lines: lines}
}

// reportSection18 renders the rent view number 18 of the ledger.
func reportSection18(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "rent" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 18 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "rent #18", Lines: lines}
}

// reportSection19 renders the salary view number 19 of the ledger.
func reportSection19(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "salary" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 19 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "salary #19", Lines: lines}
}

// reportSection20 renders the groceries view number 20 of the ledger.
func reportSection20(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "groceries" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 20 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "groceries #20", Lines: lines}
}

// reportSection21 renders the transport view number 21 of the ledger.
func reportSection21(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "transport" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 21 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "transport #21", Lines: lines}
}

// reportSection22 renders the utilities view number 22 of the ledger.
func reportSection22(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "utilities" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 22 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "utilities #22", Lines: lines}
}

// reportSection23 renders the leisure view number 23 of the ledger.
func reportSection23(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "leisure" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 23 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "leisure #23", Lines: lines}
}

// reportSection24 renders the rent view number 24 of the ledger.
func reportSection24(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "rent" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 24 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "rent #24", Lines: lines}
}

// reportSection25 renders the salary view number 25 of the ledger.
func reportSection25(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "salary" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 25 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "salary #25", Lines: lines}
}

// SummarizeQuarter aggregates the entries posted in one calendar quarter.
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

// reportSection26 renders the groceries view number 26 of the ledger.
func reportSection26(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "groceries" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 26 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "groceries #26", Lines: lines}
}

// reportSection27 renders the transport view number 27 of the ledger.
func reportSection27(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "transport" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 27 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "transport #27", Lines: lines}
}

// reportSection28 renders the utilities view number 28 of the ledger.
func reportSection28(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "utilities" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 28 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "utilities #28", Lines: lines}
}

// reportSection29 renders the leisure view number 29 of the ledger.
func reportSection29(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "leisure" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 29 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "leisure #29", Lines: lines}
}

// reportSection30 renders the rent view number 30 of the ledger.
func reportSection30(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "rent" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 30 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "rent #30", Lines: lines}
}

// validateSummary checks a summary before it is rendered.
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

// reportSection31 renders the salary view number 31 of the ledger.
func reportSection31(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "salary" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 31 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "salary #31", Lines: lines}
}

// reportSection32 renders the groceries view number 32 of the ledger.
func reportSection32(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "groceries" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 32 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "groceries #32", Lines: lines}
}

// reportSection33 renders the transport view number 33 of the ledger.
func reportSection33(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "transport" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 33 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "transport #33", Lines: lines}
}

// reportSection34 renders the utilities view number 34 of the ledger.
func reportSection34(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "utilities" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 34 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "utilities #34", Lines: lines}
}

// reportSection35 renders the leisure view number 35 of the ledger.
func reportSection35(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "leisure" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 35 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "leisure #35", Lines: lines}
}

// reportSection36 renders the rent view number 36 of the ledger.
func reportSection36(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "rent" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 36 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "rent #36", Lines: lines}
}

// reportSection37 renders the salary view number 37 of the ledger.
func reportSection37(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "salary" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 37 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "salary #37", Lines: lines}
}

// reportSection38 renders the groceries view number 38 of the ledger.
func reportSection38(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "groceries" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 38 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "groceries #38", Lines: lines}
}

// reportSection39 renders the transport view number 39 of the ledger.
func reportSection39(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "transport" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 39 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "transport #39", Lines: lines}
}

// reportSection40 renders the utilities view number 40 of the ledger.
func reportSection40(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "utilities" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 40 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "utilities #40", Lines: lines}
}

// reportSection41 renders the leisure view number 41 of the ledger.
func reportSection41(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "leisure" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 41 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "leisure #41", Lines: lines}
}

// reportSection42 renders the rent view number 42 of the ledger.
func reportSection42(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "rent" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 42 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "rent #42", Lines: lines}
}

// reportSection43 renders the salary view number 43 of the ledger.
func reportSection43(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "salary" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 43 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "salary #43", Lines: lines}
}

// reportSection44 renders the groceries view number 44 of the ledger.
func reportSection44(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "groceries" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 44 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "groceries #44", Lines: lines}
}

// reportSection45 renders the transport view number 45 of the ledger.
func reportSection45(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "transport" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 45 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "transport #45", Lines: lines}
}

// reportSection46 renders the utilities view number 46 of the ledger.
func reportSection46(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "utilities" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 46 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "utilities #46", Lines: lines}
}

// reportSection47 renders the leisure view number 47 of the ledger.
func reportSection47(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "leisure" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 47 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "leisure #47", Lines: lines}
}

// reportSection48 renders the rent view number 48 of the ledger.
func reportSection48(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "rent" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 48 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "rent #48", Lines: lines}
}

// reportSection49 renders the salary view number 49 of the ledger.
func reportSection49(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "salary" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 49 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "salary #49", Lines: lines}
}

// reportSection50 renders the groceries view number 50 of the ledger.
func reportSection50(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "groceries" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 50 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "groceries #50", Lines: lines}
}

// reportSection51 renders the transport view number 51 of the ledger.
func reportSection51(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "transport" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 51 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "transport #51", Lines: lines}
}

// reportSection52 renders the utilities view number 52 of the ledger.
func reportSection52(l *Ledger) Section {
	lines := make([]string, 0, 8)
	var total int64
	for _, e := range l.Entries() {
		if e.Category != "utilities" {
			continue
		}
		total += e.Amount
		lines = append(lines, FormatEntry(e))
	}
	lines = append(lines, fmt.Sprintf("section 52 total %s", FormatAmount(total, DefaultCurrency)))
	return Section{Title: "utilities #52", Lines: lines}
}

// Render renders every section in order.
func Render(l *Ledger) string {
	sections := []Section{
		reportSection01(l),
		reportSection02(l),
		reportSection03(l),
		reportSection04(l),
		reportSection05(l),
		reportSection06(l),
		reportSection07(l),
		reportSection08(l),
		reportSection09(l),
		reportSection10(l),
		reportSection11(l),
		reportSection12(l),
		reportSection13(l),
		reportSection14(l),
		reportSection15(l),
		reportSection16(l),
		reportSection17(l),
		reportSection18(l),
		reportSection19(l),
		reportSection20(l),
		reportSection21(l),
		reportSection22(l),
		reportSection23(l),
		reportSection24(l),
		reportSection25(l),
		reportSection26(l),
		reportSection27(l),
		reportSection28(l),
		reportSection29(l),
		reportSection30(l),
		reportSection31(l),
		reportSection32(l),
		reportSection33(l),
		reportSection34(l),
		reportSection35(l),
		reportSection36(l),
		reportSection37(l),
		reportSection38(l),
		reportSection39(l),
		reportSection40(l),
		reportSection41(l),
		reportSection42(l),
		reportSection43(l),
		reportSection44(l),
		reportSection45(l),
		reportSection46(l),
		reportSection47(l),
		reportSection48(l),
		reportSection49(l),
		reportSection50(l),
		reportSection51(l),
		reportSection52(l),
	}
	var b strings.Builder
	for _, s := range sections {
		b.WriteString(s.Title)
		b.WriteByte('\n')
		for _, line := range s.Lines {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
