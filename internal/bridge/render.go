package bridge

import (
	"bytes"
	"encoding/json"
)

// serverVersion is the version the MCP server announces and the fallback
// build identifier for the friction spool.
const serverVersion = "0.4.0"

// renderJSON encodes a tool result for the model: readable but compact
// (one-space indent), and without HTML escaping - json.Marshal would turn
// every "<", ">" and "&" in a signature into a six-character \u escape,
// which on generic-heavy code (TypeScript, C++, Go) is pure token waste.
func renderJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
