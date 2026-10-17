package mcpapi

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// MaxPlainLineBytes bounds each source line a search hit carries into a
// plain reply. A minified file is one line; printing all of it beside every
// hit would be the file again per match.
const MaxPlainLineBytes = 400

// maxPlainMatchLines bounds the lines one multi-line match prints.
const maxPlainMatchLines = 20

// PlainSourceLine is the source text a plain search reply prints for a match
// at content[start:end]: every whole line the match touches, each bounded to
// MaxPlainLineBytes around the match with [...] marking a cut. content must
// be the document revision the search read, so the line and the match never
// come from different bytes. cut reports that something was shortened; ok is
// false when the span does not lie inside content.
func PlainSourceLine(content []byte, start, end int) (text string, cut, ok bool) {
	if start < 0 || end < start || end > len(content) {
		return "", false, false
	}
	lineStart := bytes.LastIndexByte(content[:start], '\n') + 1
	stop := end
	// A match that ends with its newline does not reach into the next line.
	if stop > start && content[stop-1] == '\n' {
		stop--
	}
	lineEnd := len(content)
	if index := bytes.IndexByte(content[stop:], '\n'); index >= 0 {
		lineEnd = stop + index
	}
	lines := strings.Split(string(content[lineStart:lineEnd]), "\n")
	if len(lines) > maxPlainMatchLines {
		lines = lines[:maxPlainMatchLines]
		lines[len(lines)-1] += "[...]"
		cut = true
	}
	for index, line := range lines {
		if len(line) <= MaxPlainLineBytes {
			continue
		}
		anchor := 0
		if index == 0 {
			anchor = start - lineStart
		}
		lines[index] = cutAround(line, anchor)
		cut = true
	}
	return strings.Join(lines, "\n"), cut, true
}

// cutAround keeps MaxPlainLineBytes of line starting a little before
// anchor, on UTF-8 boundaries, marking each cut end with [...].
func cutAround(line string, anchor int) string {
	from := max(0, anchor-MaxPlainLineBytes/4)
	to := min(len(line), from+MaxPlainLineBytes)
	if to-from < MaxPlainLineBytes {
		from = max(0, to-MaxPlainLineBytes)
	}
	for from > 0 && !utf8.RuneStart(line[from]) {
		from--
	}
	for to < len(line) && !utf8.RuneStart(line[to]) {
		to--
	}
	text := line[from:to]
	if from > 0 {
		text = "[...]" + text
	}
	if to < len(line) {
		text += "[...]"
	}
	return text
}
