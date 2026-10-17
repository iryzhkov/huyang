package mcpapi

import (
	"fmt"
	"strings"
)

// ApplyArgumentAliases rewrites the misspellings agents send most often into
// the arguments the schema declares, and returns a warning for each one that
// names the canonical spelling. Only a rewrite with one possible meaning is
// made: the canonical argument must be absent, so nothing the caller did
// spell correctly is overwritten, and anything else is left to validation,
// which refuses it and says what was meant. The warning teaches the right
// shape at no extra call, which a refusal costs.
func ApplyArgumentAliases(tool string, arguments map[string]any) []string {
	switch tool {
	case "read":
		var warnings []string
		warnings = append(warnings, aliasReadTarget(arguments)...)
		warnings = append(warnings, aliasStringTarget(arguments)...)
		warnings = append(warnings, hoistTargetOptions(arguments)...)
		return warnings
	case "search":
		var warnings []string
		warnings = append(warnings, aliasSearchPaths(arguments)...)
		warnings = append(warnings, renameArgument(tool, arguments, "max_results")...)
		warnings = append(warnings, clampLimit(tool, arguments)...)
		return warnings
	case "revision_diff":
		return renameArgument(tool, arguments, "to_revision")
	}
	return nil
}

// renameArgument moves one argument to the name propertyAliases gives it,
// the same table validation suggests from, so a name that is rewritten and
// a name that is refused with a suggestion cannot disagree about what it
// meant. An alias with more than one meaning is never rewritten.
func renameArgument(tool string, arguments map[string]any, alias string) []string {
	canonicals := propertyAliases[alias]
	if len(canonicals) != 1 {
		return nil
	}
	canonical := canonicals[0]
	value, present := arguments[alias]
	if _, taken := arguments[canonical]; !present || taken {
		return nil
	}
	arguments[canonical] = value
	delete(arguments, alias)
	return []string{fmt.Sprintf("%s is not a %s argument and was read as %s; send %s", alias, tool, canonical, canonical)}
}

// aliasReadTarget moves a top-level path or symbol_locator into target, the
// commonest first guess at read. Top-level start_line and end_line are
// already read's window arguments and stay where they are.
func aliasReadTarget(arguments map[string]any) []string {
	if _, present := arguments["target"]; present {
		return nil
	}
	if _, present := arguments["targets"]; present {
		return nil
	}
	path, hasPath := arguments["path"].(string)
	locator, hasLocator := arguments["symbol_locator"].(map[string]any)
	switch {
	case hasPath && !hasLocator:
		arguments["target"] = map[string]any{"path": path}
		delete(arguments, "path")
		return []string{"path is not a top-level read argument and was read as target.path; send target: {path}"}
	case hasLocator && !hasPath:
		arguments["target"] = map[string]any{"symbol_locator": locator}
		delete(arguments, "symbol_locator")
		return []string{"symbol_locator is not a top-level read argument and was read as target.symbol_locator; send target: {symbol_locator: {path, name_path}}"}
	}
	return nil
}

// aliasStringTarget reads a target sent as a bare string as target.path.
// A string that held a JSON object was already decoded by
// NormalizeArguments, so what is left here is a path or nothing useful;
// an empty or JSON-looking string is left to validation.
func aliasStringTarget(arguments map[string]any) []string {
	text, ok := arguments["target"].(string)
	text = strings.TrimSpace(text)
	if !ok || text == "" || strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		return nil
	}
	arguments["target"] = map[string]any{"path": text}
	return []string{"target was a string and was read as target.path; send target: {path}"}
}

// readTargetOptions are read's top-level options that agents put inside
// target, next to path, because target looks like the thing being read.
var readTargetOptions = []string{"view", "start_line", "end_line", "max_lines", "numbered"}

// hoistTargetOptions moves read options nested in target up to the top
// level, where the schema declares them. An option is moved only when the
// top level does not already carry it, so nothing the caller placed
// correctly is overwritten; a conflicting pair is left to validation.
func hoistTargetOptions(arguments map[string]any) []string {
	target, ok := arguments["target"].(map[string]any)
	if !ok {
		return nil
	}
	var moved []string
	for _, name := range readTargetOptions {
		value, nested := target[name]
		if _, taken := arguments[name]; !nested || taken {
			continue
		}
		arguments[name] = value
		delete(target, name)
		moved = append(moved, name)
	}
	if len(moved) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("target.%s is a top-level read argument and was read there; send %s beside target, not inside it",
		strings.Join(moved, ", target."), strings.Join(moved, ", "))}
}

// clampLimit reduces a limit above the tool's declared maximum to that
// maximum with a warning, the way search already treats context_lines. A
// larger limit asks for more of the same answer, so refusing it costs a
// turn and teaches nothing a warning would not.
func clampLimit(tool string, arguments map[string]any) []string {
	limit, ok := arguments["limit"].(float64)
	// A fractional limit is a malformed one, left for validation to refuse
	// rather than rounded into a valid call.
	if !ok || limit != float64(int64(limit)) {
		return nil
	}
	maximum, ok := declaredMaximum(tool, "limit")
	if !ok || limit <= float64(maximum) {
		return nil
	}
	arguments["limit"] = float64(maximum)
	return []string{fmt.Sprintf("limit %d is above the maximum and was reduced to %d", int64(limit), maximum)}
}

// declaredMaximum is the maximum the catalog declares for a top-level
// integer property of a tool.
func declaredMaximum(tool, property string) (int, bool) {
	for _, descriptor := range Tools {
		if descriptor.Name != tool {
			continue
		}
		properties, _ := descriptor.InputSchema["properties"].(map[string]any)
		schema, _ := properties[property].(map[string]any)
		maximum, ok := schema["maximum"].(int)
		return maximum, ok
	}
	return 0, false
}

// searchPathAliases are the names search reads as its paths scope.
var searchPathAliases = []string{"path", "path_prefix", "include"}

// aliasSearchPaths reads search's path, path_prefix or include as the paths
// scope. More than one at once, or any beside paths, is ambiguous and left
// to validation.
func aliasSearchPaths(arguments map[string]any) []string {
	if _, present := arguments["paths"]; present {
		return nil
	}
	alias, value, found := "", any(nil), 0
	for _, name := range searchPathAliases {
		if named, present := arguments[name]; present {
			alias, value = name, named
			found++
		}
	}
	if found != 1 {
		return nil
	}
	var paths []any
	switch typed := value.(type) {
	case string:
		paths = []any{typed}
	case []any:
		for _, item := range typed {
			if _, ok := item.(string); !ok {
				return nil
			}
		}
		paths = typed
	default:
		return nil
	}
	if len(paths) == 0 {
		return nil
	}
	arguments["paths"] = paths
	delete(arguments, alias)
	return []string{fmt.Sprintf("%s is not a search argument and was read as paths; send paths, a list of path substrings or globs", alias)}
}
