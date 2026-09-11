package handlers

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// lineSpan returns the byte range covering lines first through last
// (one-based, inclusive). With includeNewline the range ends after the
// terminating newline of the last line, which is the shape a whole-line
// replacement wants; without it the range ends before that newline, which is
// the shape a single-line source handle wants. Lines beyond the document are
// an error for first and are clamped for last.
func lineSpan(content []byte, first, last int, includeNewline bool) (int, int, error) {
	if first < 1 {
		return 0, 0, errors.New("line must be at least one")
	}
	if last < first {
		return 0, 0, fmt.Errorf("line %d is before line %d", last, first)
	}
	start := 0
	for current := 1; current < first; current++ {
		index := bytes.IndexByte(content[start:], '\n')
		if index < 0 {
			return 0, 0, fmt.Errorf("line %d is outside the document", first)
		}
		start += index + 1
	}
	if start >= len(content) && !(start == len(content) && first == 1) {
		return 0, 0, fmt.Errorf("line %d is outside the document", first)
	}
	end := start
	for current := first; current <= last; current++ {
		index := bytes.IndexByte(content[end:], '\n')
		if index < 0 {
			end = len(content)
			break
		}
		if current == last {
			if includeNewline {
				end += index + 1
			} else {
				end += index
			}
			break
		}
		end += index + 1
	}
	return start, end, nil
}

// lineCount returns the number of lines, counting a trailing partial line.
func lineCount(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	count := bytes.Count(content, []byte{'\n'})
	if content[len(content)-1] != '\n' {
		count++
	}
	return count
}

// lineByteRange returns the byte range of one line without its newline.
func lineByteRange(content []byte, line int) (int, int, error) {
	return lineSpan(content, line, line, false)
}

// providerLineByteRange decodes the provider's "first-last" line notation into
// a byte range that includes the last line's newline.
func providerLineByteRange(content []byte, lines string) (int, int, error) {
	parts := strings.SplitN(lines, "-", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid provider line range %q", lines)
	}
	first, err := strconv.Atoi(parts[0])
	if err != nil || first < 1 {
		return 0, 0, fmt.Errorf("invalid provider start line %q", lines)
	}
	last, err := strconv.Atoi(parts[1])
	if err != nil || last < first {
		return 0, 0, fmt.Errorf("invalid provider end line %q", lines)
	}
	start, end, err := lineSpan(content, first, last, true)
	if err != nil {
		return 0, 0, fmt.Errorf("provider line range %q exceeds document", lines)
	}
	return start, end, nil
}

// boundedLines returns the requested line window. A zero start and end mean
// the whole document; a zero end alone means the single start line. The
// returned bounds are the actual lines delivered after clamping the end.
func boundedLines(content []byte, startLine, endLine int) ([]byte, int, int, error) {
	if startLine == 0 && endLine == 0 {
		return content, 0, 0, nil
	}
	if startLine == 0 {
		startLine = 1
	}
	if endLine == 0 {
		endLine = startLine
	}
	if endLine < startLine {
		return nil, 0, 0, errors.New("end_line must be greater than or equal to start_line")
	}
	total := lineCount(content)
	if startLine > total {
		return nil, 0, 0, fmt.Errorf("start_line %d exceeds document line count %d", startLine, total)
	}
	if endLine > total {
		endLine = total
	}
	start, end, err := lineSpan(content, startLine, endLine, true)
	if err != nil {
		return nil, 0, 0, err
	}
	return content[start:end], startLine, endLine, nil
}
