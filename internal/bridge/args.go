package bridge

// argInt reads an integer tool argument. JSON numbers decode as float64, so
// the value is narrowed here; a missing or non-numeric argument yields def.
func argInt(args map[string]any, key string, def int) int {
	if v, ok := args[key]; ok {
		if f, ok := v.(float64); ok {
			return int(f)
		}
	}
	return def
}
