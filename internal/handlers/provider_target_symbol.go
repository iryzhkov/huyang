package handlers

import (
	"bytes"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// declarationOffset returns the byte offset of leaf where name is declared
// in path: inside the declaration the native sectioner found, on its
// declaration line rather than in a comment or an earlier mention. It falls
// back to the first non-comment mention in the file and finally to the first
// occurrence, so the language server is always pointed at an identifier.
func declarationOffset(workspace *workspacecore.Workspace, path string, content []byte, name, leaf string) int {
	start, end := 0, len(content)
	relative := workspacePath(workspace, path)
	if records, _, err := workspace.FindSymbols(name); err == nil {
		for _, record := range records {
			if record.Locator.Path == relative && record.Locator.NamePath == name {
				start, end = record.Locator.ByteStart, record.Locator.ByteEnd
				break
			}
		}
	}
	if offset := leafOnLine(content[start:end], leaf, true); offset >= 0 {
		return start + offset
	}
	if offset := leafOnLine(content[start:end], leaf, false); offset >= 0 {
		return start + offset
	}
	if offset := leafOnLine(content, leaf, false); offset >= 0 {
		return offset
	}
	return bytes.Index(content, []byte(leaf))
}

var declarationKeywords = [][]byte{
	[]byte("func "), []byte("type "), []byte("var "), []byte("const "), []byte("def "), []byte("async def "),
	[]byte("class "), []byte("fn "), []byte("pub "), []byte("struct "), []byte("enum "), []byte("impl "),
	[]byte("function "), []byte("export "), []byte("interface "), []byte("public "), []byte("private "),
	[]byte("static "), []byte("let "), []byte("val "), []byte("local function "),
}

// leafOnLine scans lines for leaf, ignoring comment lines and, with
// declarationOnly, lines that do not open a declaration.
func leafOnLine(content []byte, leaf string, declarationOnly bool) int {
	offset := 0
	for _, line := range bytes.SplitAfter(content, []byte("\n")) {
		trimmed := bytes.TrimLeft(line, " \t")
		comment := bytes.HasPrefix(trimmed, []byte("//")) || bytes.HasPrefix(trimmed, []byte("#")) ||
			bytes.HasPrefix(trimmed, []byte("/*")) || bytes.HasPrefix(trimmed, []byte("*"))
		if !comment && (!declarationOnly || opensDeclaration(trimmed)) {
			if index := bytes.Index(line, []byte(leaf)); index >= 0 {
				return offset + index
			}
		}
		offset += len(line)
	}
	return -1
}

func opensDeclaration(trimmed []byte) bool {
	for _, keyword := range declarationKeywords {
		if bytes.HasPrefix(trimmed, keyword) {
			return true
		}
	}
	return false
}
