package mcpapi

import (
	"bytes"
	"encoding/json"
)

// ServerVersion is the version the MCP server announces and the fallback
// build identifier for the friction spool.
const ServerVersion = "0.4.0"

// RenderJSON encodes a tool result for the model: readable but compact
// (one-space indent), and without HTML escaping - json.Marshal would turn
// every "<", ">" and "&" in a signature into a six-character \u escape,
// which on generic-heavy code (TypeScript, C++, Go) is pure token waste.
func RenderJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// No indentation: every byte of a tool result is a token the model pays
	// for, and one-space indentation alone cost about a third more tokens.
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
