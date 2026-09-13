package billing

func QuoteTotal(units int) int {
	if units < 0 {
		return 0
	}
	return units * 7
}

const DiagnosticName = "QuoteTotal"
