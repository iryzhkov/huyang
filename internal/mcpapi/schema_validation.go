package mcpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

func ValidateToolArguments(schema map[string]any, arguments map[string]any) error {
	return validateSchemaValue(schema, arguments, "arguments")
}

// validateSchemaValue checks value against the subset of JSON Schema the
// catalog uses: oneOf alternatives, enums, and the object, string, boolean,
// integer and array types with their closed-property, length and item
// constraints.
func validateSchemaValue(schema map[string]any, value any, path string) error {
	if alternatives, ok := schema["oneOf"].([]any); ok {
		return validateOneOf(schema, alternatives, value, path)
	}
	if enum, ok := schema["enum"].([]any); ok && !slices.Contains(enum, value) {
		return argumentError(schema, path, "%s is not one of the allowed values; it must be %s", path, DescribeShape(schema))
	}
	switch schema["type"] {
	case "object":
		return validateObject(schema, value, path)
	case "string":
		text, ok := value.(string)
		if !ok {
			return argumentError(schema, path, "%s must be a string", path)
		}
		if maximum, ok := schema["maxLength"].(int); ok && len(text) > maximum {
			return argumentError(schema, path, "%s is %d bytes, maximum %d bytes", path, len(text), maximum)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return argumentError(schema, path, "%s must be a boolean", path)
		}
	case "integer":
		number, ok := value.(float64)
		if !ok || number != float64(int64(number)) {
			return argumentError(schema, path, "%s must be an integer", path)
		}
		return validateIntegerBounds(schema, int64(number), path)
	case "array":
		return validateArray(schema, value, path)
	}
	return nil
}

// validateOneOf checks the schema without its alternatives first (an empty
// property set is treated as open), then requires exactly one alternative
// to match.
func validateOneOf(schema map[string]any, alternatives []any, value any, path string) error {
	base := make(map[string]any, len(schema)-1)
	for key, item := range schema {
		if key != "oneOf" {
			base[key] = item
		}
	}
	if properties, ok := base["properties"].(map[string]any); ok && len(properties) == 0 {
		delete(base, "properties")
		delete(base, "additionalProperties")
	}
	if err := validateSchemaValue(base, value, path); err != nil {
		return err
	}
	matches := 0
	for _, candidate := range alternatives {
		candidateSchema, _ := candidate.(map[string]any)
		if candidateSchema != nil && validateSchemaValue(candidateSchema, value, path) == nil {
			matches++
		}
	}
	if matches != 1 {
		return argumentError(schema, path, "%s must match exactly one allowed shape", path)
	}
	return nil
}

// validateIntegerBounds enforces the minimum and maximum an integer property
// declares. A bound the catalog advertises and the service ignores is worse
// than no bound at all: a caller that reads the schema and stays inside it
// gets the same answer as one that ignores it, and the reply carries no sign
// that the argument was out of range.
func validateIntegerBounds(schema map[string]any, number int64, path string) error {
	if minimum, ok := schema["minimum"].(int); ok && number < int64(minimum) {
		return argumentError(schema, path, "%s is %d, minimum %d", path, number, minimum)
	}
	if maximum, ok := schema["maximum"].(int); ok && number > int64(maximum) {
		return argumentError(schema, path, "%s is %d, maximum %d", path, number, maximum)
	}
	return nil
}

func validateObject(schema map[string]any, value any, path string) error {
	object, ok := value.(map[string]any)
	if !ok {
		return argumentError(schema, path, "%s must be an object", path)
	}
	properties, _ := schema["properties"].(map[string]any)
	if closed, present := schema["additionalProperties"].(bool); present && !closed {
		for _, key := range sortedKeys(object) {
			if _, ok := properties[key]; !ok {
				return argumentError(schema, path+"."+key, "%s contains unknown property %q%s", path, key, whereItBelongs(properties, key))
			}
		}
	}
	if required, ok := schema["required"].([]string); ok {
		for _, key := range required {
			if _, present := object[key]; !present {
				return argumentError(schema, path+"."+key, "%s is missing required property %q", path, key)
			}
		}
	}
	for key, child := range object {
		childSchema, _ := properties[key].(map[string]any)
		if childSchema != nil {
			if err := validateSchemaValue(childSchema, child, path+"."+key); err != nil {
				return err
			}
		}
	}
	return nil
}

// whereItBelongs names the place a rejected property is accepted, when the
// schema declares one with that name a level down. "read contains unknown
// property path" is true and leaves the caller to re-read the schema for the
// shape; naming target.path turns the second attempt into the right one.
func whereItBelongs(properties map[string]any, key string) string {
	var places []string
	for _, parent := range sortedKeys(properties) {
		if child, _ := properties[parent].(map[string]any); declaresProperty(child, key) {
			places = append(places, parent+"."+key)
		}
	}
	if len(places) == 0 {
		return ""
	}
	return "; " + key + " belongs under " + strings.Join(places, " or ")
}

// declaresProperty reports whether a schema accepts a property with this
// name, looking through the shapes the catalog uses: an object's properties,
// the alternatives of a oneOf, and an array's items.
func declaresProperty(schema map[string]any, key string) bool {
	if schema == nil {
		return false
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		if _, present := properties[key]; present {
			return true
		}
	}
	for _, raw := range mcpAlternatives(schema) {
		if alternative, _ := raw.(map[string]any); declaresProperty(alternative, key) {
			return true
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		return declaresProperty(items, key)
	}
	return false
}

func mcpAlternatives(schema map[string]any) []any {
	alternatives, _ := schema["oneOf"].([]any)
	return alternatives
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func validateArray(schema map[string]any, value any, path string) error {
	array, ok := value.([]any)
	if !ok {
		return argumentError(schema, path, "%s must be an array", path)
	}
	if maximum, ok := schema["maxItems"].(int); ok && len(array) > maximum {
		return argumentError(schema, path, "%s has %d items, maximum %d; append another bounded batch with change_plan action=edit and edit.mode=add", path, len(array), maximum)
	}
	itemSchema, _ := schema["items"].(map[string]any)
	if itemSchema == nil {
		return nil
	}
	for index, item := range array {
		if err := validateSchemaValue(itemSchema, item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func DecodeArguments(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, nil
	}
	var arguments map[string]any
	if err := json.Unmarshal(raw, &arguments); err != nil {
		return nil, fmt.Errorf("invalid tool arguments: %w", err)
	}
	if arguments == nil {
		return nil, errors.New("tool arguments must be a JSON object")
	}
	return arguments, nil
}

func ValidateToolArgumentSize(raw json.RawMessage) error {
	if len(raw) <= MaxToolArgumentBytes {
		return nil
	}
	return fmt.Errorf("tool arguments are %d bytes, exceeding the %d-byte safe transport limit; split a large change plan into batches of at most %d operations using action=edit and edit.mode=add",
		len(raw), MaxToolArgumentBytes, MaxPlanOperations)
}
