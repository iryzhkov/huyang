package mcpapi

import "fmt"

// ReplyStyle is how a session's tool results are presented. It is chosen once
// at startup (huyang mcp --reply-style, huyang serve --reply-style for the
// HTTP routes) and is orthogonal to the profile: the same tools, the same
// arguments and the same canonical envelope, rendered differently at the
// transport boundary.
type ReplyStyle string

const (
	// ReplyStylePlain is the default: one text block a model reads directly
	// (source as source, grep-like matches, a short edit receipt, findings
	// and refusals as lines) and no structuredContent. Every guard token,
	// finding, provisional reason and recovery step stays in that text.
	ReplyStylePlain ReplyStyle = "plain"
	// ReplyStyleJSON is the v1alpha1 wire shape before plain replies: the
	// compact JSON envelope as text plus the structured copy. Tests,
	// benchmarks and typed consumers that parse the envelope select it.
	ReplyStyleJSON ReplyStyle = "json"
)

// DefaultReplyStyle is the style of a session that names none, including an
// adapter older than the option.
const DefaultReplyStyle = ReplyStylePlain

// ParseReplyStyle accepts plain or json; the empty string is the default.
func ParseReplyStyle(name string) (ReplyStyle, error) {
	switch ReplyStyle(name) {
	case "":
		return DefaultReplyStyle, nil
	case ReplyStylePlain, ReplyStyleJSON:
		return ReplyStyle(name), nil
	default:
		return "", fmt.Errorf("unknown reply style %q; want plain or json", name)
	}
}

// ReplyStyleConflict refuses a per-call presentation argument that a plain
// session cannot honour. The experimental response_format=both and
// =structured promise a structured copy that plain replies never send, so
// they are refused before the call runs rather than silently ignored;
// response_format=text and response_mode are accepted, and a plain reply
// never drops findings whatever response_mode says.
func ReplyStyleConflict(style ReplyStyle, arguments map[string]any) error {
	if style != ReplyStylePlain {
		return nil
	}
	format, _ := arguments["response_format"].(string)
	if format == "both" || format == "structured" {
		return &ArgumentError{
			Path:     "response_format",
			Expected: "text, or omit it; start huyang mcp with --reply-style json for structured content",
			message:  fmt.Sprintf("response_format=%s needs a session started with --reply-style json; this session replies in plain text", format),
		}
	}
	return nil
}
