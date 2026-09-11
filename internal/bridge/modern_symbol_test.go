package bridge

import "testing"

func TestProviderLineByteRange(t *testing.T) {
	content := []byte("one\ntwo\nthree\n")
	start, end, err := providerLineByteRange(content, "2-3")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content[start:end]); got != "two\nthree\n" {
		t.Fatalf("range = %q", got)
	}
	if _, _, err := providerLineByteRange(content, "4-4"); err == nil {
		t.Fatal("expected out-of-range line error")
	}
}
