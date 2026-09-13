package huyangfixture

import "testing"

func TestSumReadsTheStore(t *testing.T) {
	if Sum(Memory{amount: 2}) != 2 {
		t.Fatal("memory store")
	}
	if Sum(Ledger{}) != Total() {
		t.Fatal("ledger store")
	}
}
