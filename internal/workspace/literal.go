package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// LiteralHit is one occurrence of a literal in a document.
type LiteralHit struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	ByteStart int    `json:"byte_start"`
	ByteEnd   int    `json:"byte_end"`
}

// LiteralMatch is the outcome of locating a literal. Exact hits are
// preferred; when none exist, hits found after collapsing whitespace are
// returned with Normalised set, together with an explanation of how the
// document differs from the requested text.
type LiteralMatch struct {
	Hits       []LiteralHit
	Normalised bool
	// IndentPrefix is set when every non-blank line of the document text
	// equals the requested line with this prefix added (IndentAdded) or
	// removed (IndentAdded false); a replacement can then be adjusted the
	// same way. It is empty when the whitespace differs in another way.
	IndentPrefix string
	IndentAdded  bool
	// Actual is the document text of the first normalised hit, so a caller
	// that cannot adapt its replacement can retry with the exact bytes.
	Actual string
	Hint   string
}

// LocateLiteral finds old in path, or in every text file of the workspace
// when path is empty. It never mutates the document.
func (w *Workspace) LocateLiteral(path, old string) (LiteralMatch, error) {
	if old == "" {
		return LiteralMatch{}, errors.New("old text is required")
	}
	files, err := w.literalCandidates(path)
	if err != nil {
		return LiteralMatch{}, err
	}
	var match LiteralMatch
	for _, name := range files {
		read, readErr := w.Read(name)
		if readErr != nil {
			return LiteralMatch{}, readErr
		}
		for _, span := range matchRanges(read.Content, []byte(old), nil) {
			line, column := bytePosition(read.Content, span[0])
			match.Hits = append(match.Hits, LiteralHit{Path: read.Path, Line: line, Column: column, ByteStart: span[0], ByteEnd: span[1]})
		}
	}
	if len(match.Hits) > 0 {
		return match, nil
	}
	return w.locateNormalised(files, old)
}

// literalCandidates returns the files a literal is looked for in.
func (w *Workspace) literalCandidates(path string) ([]string, error) {
	if path != "" {
		absolute, err := w.confinedPath(path)
		if err != nil {
			return nil, err
		}
		return []string{absolute}, nil
	}
	files, _, err := w.collectFiles()
	if err != nil {
		return nil, err
	}
	var text []string
	for _, name := range files {
		if disk, _, inspectErr := inspectPath(name); inspectErr == nil && disk.Kind == ObjectRegularText {
			text = append(text, name)
		}
	}
	return text, nil
}

// locateNormalised looks for old with runs of spaces and tabs collapsed and
// line edges trimmed, and works out whether the difference is a uniform
// indentation change.
func (w *Workspace) locateNormalised(files []string, old string) (LiteralMatch, error) {
	wanted, _ := normaliseWhitespace([]byte(old))
	if len(bytes.TrimSpace(wanted)) == 0 {
		return LiteralMatch{}, nil
	}
	match := LiteralMatch{Normalised: true}
	for _, name := range files {
		read, err := w.Read(name)
		if err != nil {
			return LiteralMatch{}, err
		}
		normalised, offsets := normaliseWhitespace(read.Content)
		for _, span := range matchRanges(normalised, wanted, nil) {
			start, end := originalSpan(read.Content, offsets, span[0], span[1])
			line, column := bytePosition(read.Content, start)
			match.Hits = append(match.Hits, LiteralHit{Path: read.Path, Line: line, Column: column, ByteStart: start, ByteEnd: end})
			if match.Actual == "" {
				match.Actual = string(read.Content[start:end])
			}
		}
	}
	if len(match.Hits) == 0 {
		return LiteralMatch{}, nil
	}
	match.IndentPrefix, match.IndentAdded, match.Hint = describeIndentation(old, match.Actual)
	return match, nil
}

// normaliseWhitespace collapses every run of spaces and tabs to one space
// and drops the runs at line edges. offsets maps each normalised byte to
// the original offset it came from.
func normaliseWhitespace(content []byte) ([]byte, []int) {
	normalised := make([]byte, 0, len(content))
	offsets := make([]int, 0, len(content))
	lineStart := true
	pendingSpace := -1
	for index, char := range content {
		switch {
		case char == ' ' || char == '\t':
			if !lineStart && pendingSpace < 0 {
				pendingSpace = index
			}
		case char == '\n':
			pendingSpace = -1
			normalised = append(normalised, '\n')
			offsets = append(offsets, index)
			lineStart = true
		default:
			if pendingSpace >= 0 {
				normalised = append(normalised, ' ')
				offsets = append(offsets, pendingSpace)
				pendingSpace = -1
			}
			normalised = append(normalised, char)
			offsets = append(offsets, index)
			lineStart = false
		}
	}
	return normalised, offsets
}

// originalSpan maps a normalised span back to original bytes, extending the
// start back over the leading whitespace of its line so a multi-line match
// covers whole lines the way the requested text did.
func originalSpan(content []byte, offsets []int, start, end int) (int, int) {
	first := offsets[start]
	last := offsets[end-1] + 1
	for first > 0 && (content[first-1] == ' ' || content[first-1] == '\t') {
		first--
	}
	return first, last
}

// describeIndentation compares the requested text with the document text
// line by line and reports a uniform indentation prefix when there is one.
func describeIndentation(requested, actual string) (string, bool, string) {
	wantLines := strings.Split(strings.TrimRight(requested, "\n"), "\n")
	haveLines := strings.Split(strings.TrimRight(actual, "\n"), "\n")
	if len(wantLines) != len(haveLines) {
		return "", false, "the text matches only after collapsing whitespace; use the exact document text"
	}
	prefix, added, uniform := "", false, true
	for index := range wantLines {
		want, have := wantLines[index], haveLines[index]
		if strings.TrimSpace(want) == "" && strings.TrimSpace(have) == "" {
			continue
		}
		var candidate string
		var candidateAdded bool
		switch {
		case strings.HasSuffix(have, want):
			candidate, candidateAdded = strings.TrimSuffix(have, want), true
		case strings.HasSuffix(want, have):
			candidate, candidateAdded = strings.TrimSuffix(want, have), false
		default:
			uniform = false
		}
		if !uniform || strings.TrimSpace(candidate) != "" {
			uniform = false
			break
		}
		if prefix == "" && !added && candidate == "" {
			continue
		}
		if prefix != "" && (candidate != prefix || candidateAdded != added) {
			uniform = false
			break
		}
		prefix, added = candidate, candidateAdded
	}
	if !uniform {
		return "", false, "the text matches only after collapsing whitespace (" + whitespaceKinds(actual) + "); use the exact document text"
	}
	if prefix == "" {
		return "", false, "the text matches only after collapsing whitespace; use the exact document text"
	}
	direction := "more"
	if !added {
		direction = "less"
	}
	return prefix, added, fmt.Sprintf("the document is indented %s than the requested text by %q on every line; the replacement was adjusted the same way", direction, prefix)
}

// whitespaceKinds names the indentation characters a text uses.
func whitespaceKinds(text string) string {
	tabs, spaces := false, false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		lead := line[:len(line)-len(trimmed)]
		tabs = tabs || strings.Contains(lead, "\t")
		spaces = spaces || strings.Contains(lead, " ")
	}
	switch {
	case tabs && spaces:
		return "the document mixes tabs and spaces"
	case tabs:
		return "the document indents with tabs"
	case spaces:
		return "the document indents with spaces"
	}
	return "whitespace differs inside lines"
}

// AdjustIndentation applies the uniform prefix found by LocateLiteral to a
// replacement so it lands at the document's indentation.
func (match LiteralMatch) AdjustIndentation(replacement string) string {
	if match.IndentPrefix == "" {
		return replacement
	}
	trailing := strings.HasSuffix(replacement, "\n")
	lines := strings.Split(strings.TrimSuffix(replacement, "\n"), "\n")
	for index, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if match.IndentAdded {
			lines[index] = match.IndentPrefix + line
		} else {
			lines[index] = strings.TrimPrefix(line, match.IndentPrefix)
		}
	}
	result := strings.Join(lines, "\n")
	if trailing {
		result += "\n"
	}
	return result
}
