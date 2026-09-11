package handlers

import "testing"

// The line helpers agree on one-based inclusive lines: a single line without
// its newline, a provider range with the last newline, a clamped window, and
// an error for a start beyond the document.
func TestLineSpanHelpersAgree(t *testing.T) {
	content := []byte("one\ntwo\nthree")
	start, end, err := lineByteRange(content, 2)
	if err != nil || string(content[start:end]) != "two" {
		t.Fatalf("lineByteRange = %q, %v", content[start:end], err)
	}
	start, end, err = providerLineByteRange(content, "1-2")
	if err != nil || string(content[start:end]) != "one\ntwo\n" {
		t.Fatalf("providerLineByteRange = %q, %v", content[start:end], err)
	}
	window, first, last, err := boundedLines(content, 2, 9)
	if err != nil || string(window) != "two\nthree" || first != 2 || last != 3 {
		t.Fatalf("boundedLines = %q, %d-%d, %v", window, first, last, err)
	}
	if _, _, err := lineByteRange(content, 4); err == nil {
		t.Fatal("line beyond the document was accepted")
	}
	if _, _, _, err := boundedLines(content, 4, 5); err == nil {
		t.Fatal("start_line beyond the document was accepted")
	}
	if _, _, err := providerLineByteRange(content, "3-9"); err != nil {
		t.Fatalf("clamped provider range rejected: %v", err)
	}
	if _, _, err := providerLineByteRange(content, "4-4"); err == nil {
		t.Fatal("provider range starting beyond the document was accepted")
	}
}
