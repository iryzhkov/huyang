package main

import "testing"

func TestWorkshopDeliveryCredits(t *testing.T) {
	for _, tc := range []struct {
		credits int
		want    string
	}{
		{1, "sent:1"}, {0, "sent:0"}, {-1, "rejected"},
	} {
		if got := run(tc.credits); got != tc.want {
			t.Errorf("credits=%d: got %q, want %q", tc.credits, got, tc.want)
		}
	}
}
