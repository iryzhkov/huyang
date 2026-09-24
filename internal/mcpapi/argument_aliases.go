package mcpapi

import "fmt"

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
		return aliasReadTarget(arguments)
	case "search":
		var warnings []string
		warnings = append(warnings, aliasSearchPaths(arguments)...)
		warnings = append(warnings, renameArgument(tool, arguments, "max_results", "limit")...)
		return warnings
	case "revision_diff":
		return renameArgument(tool, arguments, "to_revision", "to_revision_or_current")
	}
	return nil
}

// renameArgument moves one argument to its canonical name.
func renameArgument(tool string, arguments map[string]any, alias, canonical string) []string {
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

// aliasSearchPaths reads search's path or path_prefix as the paths scope.
// Both at once, or either beside paths, is ambiguous and left to validation.
func aliasSearchPaths(arguments map[string]any) []string {
	if _, present := arguments["paths"]; present {
		return nil
	}
	path, hasPath := arguments["path"]
	prefix, hasPrefix := arguments["path_prefix"]
	alias, value := "path", path
	switch {
	case hasPath == hasPrefix:
		return nil
	case hasPrefix:
		alias, value = "path_prefix", prefix
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
