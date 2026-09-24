package mcpapi

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ArgumentError is the refusal of one argument. It says where the argument
// is, what is wrong with it and the shape the schema accepts there, so the
// reply can tell the caller what to send instead of only what it sent.
type ArgumentError struct {
	// Path locates the argument below the arguments object, for example
	// target.symbol_locator or operations[3]; empty for the object itself.
	Path string
	// Expected summarises the schema that refused the argument: the object
	// that holds an unknown or missing property, or the value itself.
	Expected string
	// Suggestion is the property the caller most likely meant, when the
	// refusal could name one.
	Suggestion string
	message    string
}

func (e *ArgumentError) Error() string { return e.message }

// argumentError builds a refusal for the argument at path, described by
// schema.
func argumentError(schema map[string]any, path, format string, values ...any) *ArgumentError {
	return &ArgumentError{Path: argumentPath(path), Expected: DescribeShape(schema), message: fmt.Sprintf(format, values...)}
}

// argumentPath drops the arguments. prefix every validation path carries:
// the caller wrote target.path, not arguments.target.path.
func argumentPath(path string) string {
	if path == "arguments" {
		return ""
	}
	return strings.TrimPrefix(path, "arguments.")
}

// InvalidArguments is the envelope for a call whose arguments the catalog
// refused. It is an ordinary Huyang reply - outcome failed, code
// invalid_arguments, a request ID - with the offending argument in data and
// a next step that restates the shape expected there. retryable is false:
// the same arguments are refused again, so the call has to change.
func InvalidArguments(requestID, tool string, err error) map[string]any {
	data := map[string]any{}
	step := map[string]any{"tool": tool, "action": "resend_with_corrected_arguments"}
	var argument *ArgumentError
	if errors.As(err, &argument) {
		if argument.Path != "" {
			data["argument"] = argument.Path
			step["argument"] = argument.Path
		}
		if argument.Suggestion != "" {
			data["did_you_mean"] = argument.Suggestion
			step["use"] = argument.Suggestion
		}
		if argument.Expected != "" {
			step["expected"] = argument.Expected
		}
	}
	result := Envelope(requestID, nil, "failed", "invalid_arguments", err.Error(), data)
	result["retryable"] = false
	result["next"] = []any{step}
	return result
}

// shapeDepth bounds how far a shape summary descends. Two levels name a
// target's alternatives and the fields inside them without restating a
// whole tool schema in every refusal.
const shapeDepth = 2

// DescribeShape summarises a schema in one line: {a, b?} for an object
// whose b is optional, x | y for alternatives, [item] for an array, and the
// enum values or scalar type otherwise.
func DescribeShape(schema map[string]any) string {
	return describeShape(schema, shapeDepth)
}

func describeShape(schema map[string]any, depth int) string {
	if alternatives := mcpAlternatives(schema); len(alternatives) > 0 {
		shapes := make([]string, 0, len(alternatives))
		for _, raw := range alternatives {
			if alternative, ok := raw.(map[string]any); ok {
				shapes = append(shapes, describeShape(alternative, depth))
			}
		}
		return strings.Join(shapes, " | ")
	}
	if enum, ok := schema["enum"].([]any); ok {
		values := make([]string, len(enum))
		for index, value := range enum {
			values[index] = fmt.Sprint(value)
		}
		return "one of " + strings.Join(values, ", ")
	}
	switch schema["type"] {
	case "object":
		return describeObject(schema, depth)
	case "array":
		shape := "array"
		if items, ok := schema["items"].(map[string]any); ok && depth > 0 {
			shape = "[" + describeShape(items, depth-1) + "]"
		}
		if maximum, ok := schema["maxItems"].(int); ok {
			shape += fmt.Sprintf(" of at most %d", maximum)
		}
		return shape
	case "integer":
		minimum, hasMinimum := schema["minimum"].(int)
		maximum, hasMaximum := schema["maximum"].(int)
		switch {
		case hasMinimum && hasMaximum:
			return fmt.Sprintf("integer %d..%d", minimum, maximum)
		case hasMinimum:
			return fmt.Sprintf("integer >= %d", minimum)
		}
		return "integer"
	}
	if kind, ok := schema["type"].(string); ok {
		return kind
	}
	return "any value"
}

// describeObject lists an object's properties, required ones first and
// optional ones marked with ?, descending into nested objects and
// alternatives while depth lasts.
func describeObject(schema map[string]any, depth int) string {
	properties, _ := schema["properties"].(map[string]any)
	if len(properties) == 0 {
		return "object"
	}
	if depth <= 0 {
		return "{...}"
	}
	required, _ := schema["required"].([]string)
	var mandatory, optional []string
	for _, name := range sortedKeys(properties) {
		field := name
		if child, ok := properties[name].(map[string]any); ok && depth > 1 && composite(child) {
			field += ": " + describeShape(child, depth-1)
		}
		if slices.Contains(required, name) {
			mandatory = append(mandatory, field)
		} else {
			optional = append(optional, strings.Replace(field, name, name+"?", 1))
		}
	}
	return "{" + strings.Join(append(mandatory, optional...), ", ") + "}"
}

// composite reports whether a property's schema has structure worth naming
// in a summary: declared properties or alternatives.
func composite(schema map[string]any) bool {
	if len(mcpAlternatives(schema)) > 0 {
		return true
	}
	properties, _ := schema["properties"].(map[string]any)
	return len(properties) > 0
}
