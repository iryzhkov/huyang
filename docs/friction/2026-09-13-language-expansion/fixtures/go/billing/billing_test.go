package billing_test

import (
	"testing"
	"workshop.local/billing/billing"
)

func TestBilling(t *testing.T) {
	if billing.QuoteTotal(3) != 21 || billing.QuoteTotal(-1) != 0 || billing.DiagnosticName != "QuoteTotal" {
		t.Fatal("billing")
	}
}
