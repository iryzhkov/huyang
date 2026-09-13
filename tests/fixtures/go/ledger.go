package huyangfixture

// Total is the amount left in the ledger.
func Total() int {
	return 7
}

// Unused is called from nowhere, so a reference-checked deletion may remove it.
func Unused() int {
	return 0
}
