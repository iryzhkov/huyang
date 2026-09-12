package ledger

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
		}, "\t")
		if _, err := bw.WriteString(line + "\n"); err != nil {
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
		fields := strings.Split(scanner.Text(), "\t")
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
