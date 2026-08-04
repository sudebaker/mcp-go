package executor

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"time"
	"unicode/utf8"
)

// ToolProperty represents a JSON Schema property for tool input validation.
// It is built from the raw map[string]interface{} stored in config.ToolConfig.InputSchema.
type ToolProperty struct {
	Type            string
	Description     string
	Enum            []interface{}
	MinLength       *int
	MaxLength       *int
	Pattern         string
	PatternCompiled *regexp.Regexp
	Format          string
	Minimum         *float64
	Maximum         *float64
	Properties      map[string]ToolProperty
	Required        []string
	Items           *ToolProperty
}

// toInt converts numeric interface values to int.
func toInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	}
	return 0, false
}

// toolPropertyFromMap converts a raw JSON-schema property (as unmarshaled by
// yaml.v3 into map[string]interface{}) into a typed ToolProperty.
// Regex patterns are compiled once here and cached to avoid ReDoS per-request.
func toolPropertyFromMap(m map[string]interface{}) (ToolProperty, error) {
	var p ToolProperty

	if v, ok := m["type"].(string); ok {
		p.Type = v
	}
	if v, ok := m["description"].(string); ok {
		p.Description = v
	}
	if v, ok := m["enum"].([]interface{}); ok {
		p.Enum = v
	}
	if v, ok := toInt(m["minLength"]); ok {
		p.MinLength = &v
	}
	if v, ok := toInt(m["maxLength"]); ok {
		p.MaxLength = &v
	}
	if v, ok := m["pattern"].(string); ok && v != "" {
		p.Pattern = v
		compiled, err := regexp.Compile(v)
		if err != nil {
			return p, fmt.Errorf("invalid pattern %q: %w", v, err)
		}
		p.PatternCompiled = compiled
	}
	if v, ok := m["format"].(string); ok {
		p.Format = v
	}
	if v, ok := toFloat64(m["minimum"]); ok {
		p.Minimum = &v
	}
	if v, ok := toFloat64(m["maximum"]); ok {
		p.Maximum = &v
	}
	if v, ok := m["required"].([]interface{}); ok {
		p.Required = make([]string, 0, len(v))
		for _, r := range v {
			if s, ok := r.(string); ok {
				p.Required = append(p.Required, s)
			}
		}
	}
	if v, ok := m["properties"].(map[string]interface{}); ok {
		p.Properties = make(map[string]ToolProperty, len(v))
		for name, prop := range v {
			propMap, ok := prop.(map[string]interface{})
			if !ok {
				continue
			}
			propTyped, err := toolPropertyFromMap(propMap)
			if err != nil {
				return p, fmt.Errorf("property %q: %w", name, err)
			}
			p.Properties[name] = propTyped
		}
	}
	if v, ok := m["items"].(map[string]interface{}); ok {
		itemsTyped, err := toolPropertyFromMap(v)
		if err != nil {
			return p, fmt.Errorf("items: %w", err)
		}
		p.Items = &itemsTyped
	}

	return p, nil
}

// inputSchemaToRootProperty converts the top-level input_schema map into a root
// ToolProperty. The top-level schema is expected to be type "object" with
// properties and required fields.
func inputSchemaToRootProperty(inputSchema map[string]interface{}) (ToolProperty, error) {
	return toolPropertyFromMap(inputSchema)
}

// ValidateArguments validates the provided arguments against a JSON schema.
// The schema is the top-level input schema (type object with properties).
func ValidateArguments(schema *ToolProperty, args map[string]interface{}) error {
	if schema == nil {
		return nil
	}
	return validateObject(args, *schema, "")
}

// validateObject validates a map against an object schema.
func validateObject(obj map[string]interface{}, schema ToolProperty, path string) error {
	// Required fields
	for _, req := range schema.Required {
		if _, ok := obj[req]; !ok {
			return fmt.Errorf("%srequired field %q is missing", prefixPath(path), req)
		}
	}

	for name, value := range obj {
		propPath := joinPath(path, name)
		prop, ok := schema.Properties[name]
		if !ok {
			continue // extra args permitted
		}
		if err := validateValue(value, prop, propPath); err != nil {
			return err
		}
	}
	return nil
}

// validateValue validates a single value against a property schema.
func validateValue(value interface{}, schema ToolProperty, path string) error {
	if schema.Type == "" && len(schema.Enum) == 0 && schema.MinLength == nil && schema.MaxLength == nil &&
		schema.PatternCompiled == nil && schema.Format == "" && schema.Minimum == nil && schema.Maximum == nil &&
		schema.Properties == nil && schema.Items == nil {
		return nil // no constraints
	}

	if schema.Type != "" {
		if err := checkType(value, schema.Type, path); err != nil {
			return err
		}
	}

	if str, ok := value.(string); ok {
		if err := validateString(str, schema, path); err != nil {
			return err
		}
	}

	if num, ok := toNumeric(value); ok {
		if err := validateNumber(num, schema, path); err != nil {
			return err
		}
	}

	if len(schema.Enum) > 0 {
		if !valueInEnum(value, schema.Enum) {
			return fmt.Errorf("%svalue %v is not one of allowed enum values", path, value)
		}
	}

	if schema.Type == "object" && len(schema.Properties) > 0 {
		obj, ok := value.(map[string]interface{})
		if !ok {
			return fmt.Errorf("%sexpected object, got %T", path, value)
		}
		if err := validateObject(obj, schema, path); err != nil {
			return err
		}
	}

	if schema.Type == "array" && schema.Items != nil {
		arr, ok := value.([]interface{})
		if !ok {
			return fmt.Errorf("%sexpected array, got %T", path, value)
		}
		for i, item := range arr {
			itemPath := fmt.Sprintf("%s[%d]", path, i)
			if err := validateValue(item, *schema.Items, itemPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// validateString applies string-specific constraints.
func validateString(str string, schema ToolProperty, path string) error {
	length := utf8.RuneCountInString(str)

	if schema.MinLength != nil && length < *schema.MinLength {
		return fmt.Errorf("%sstring length %d is below minimum %d", path, length, *schema.MinLength)
	}
	if schema.MaxLength != nil && length > *schema.MaxLength {
		return fmt.Errorf("%sstring length %d exceeds maximum %d", path, length, *schema.MaxLength)
	}
	if schema.PatternCompiled != nil && !schema.PatternCompiled.MatchString(str) {
		return fmt.Errorf("%sstring does not match pattern %q", path, schema.Pattern)
	}
	if schema.Format != "" {
		if err := validateFormat(str, schema.Format, path); err != nil {
			return err
		}
	}
	return nil
}

// validateNumber applies numeric constraints.
func validateNumber(num float64, schema ToolProperty, path string) error {
	if schema.Minimum != nil && num < *schema.Minimum {
		return fmt.Errorf("%svalue %v is below minimum %v", path, num, *schema.Minimum)
	}
	if schema.Maximum != nil && num > *schema.Maximum {
		return fmt.Errorf("%svalue %v exceeds maximum %v", path, num, *schema.Maximum)
	}
	return nil
}

// validateFormat checks known string formats.
func validateFormat(str, format, path string) error {
	switch format {
	case "email":
		if _, err := mail.ParseAddress(str); err != nil {
			return fmt.Errorf("%svalue %q is not a valid email", path, str)
		}
	case "uri":
		if _, err := url.ParseRequestURI(str); err != nil {
			return fmt.Errorf("%svalue %q is not a valid URI", path, str)
		}
	case "date-time":
		if _, err := time.Parse(time.RFC3339, str); err != nil {
			return fmt.Errorf("%svalue %q is not a valid RFC3339 date-time", path, str)
		}
	}
	return nil
}

// checkType validates the JSON schema type of a value.
func checkType(value interface{}, expectedType, path string) error {
	if value == nil {
		if expectedType == "null" {
			return nil
		}
		return fmt.Errorf("%sexpected %s, got null", path, expectedType)
	}

	switch expectedType {
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%sexpected string, got %T", path, value)
		}
	case "number":
		if _, ok := toNumeric(value); !ok {
			return fmt.Errorf("%sexpected number, got %T", path, value)
		}
	case "integer":
		num, ok := toNumeric(value)
		if !ok {
			return fmt.Errorf("%sexpected integer, got %T", path, value)
		}
		if num != float64(int64(num)) {
			return fmt.Errorf("%sexpected integer, got non-integer number", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%sexpected boolean, got %T", path, value)
		}
	case "array":
		if _, ok := value.([]interface{}); !ok {
			return fmt.Errorf("%sexpected array, got %T", path, value)
		}
	case "object":
		if _, ok := value.(map[string]interface{}); !ok {
			return fmt.Errorf("%sexpected object, got %T", path, value)
		}
	case "null":
		return fmt.Errorf("%sexpected null, got %T", path, value)
	}
	return nil
}

// valueInEnum checks if a value matches one of the allowed enum values.
func valueInEnum(value interface{}, enum []interface{}) bool {
	for _, e := range enum {
		if compareValues(value, e) {
			return true
		}
	}
	return false
}

// compareValues compares two values for equality with numeric coercion.
func compareValues(a, b interface{}) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	// Coerce numbers: json.Number, float64, int
	if na, ok := toNumeric(a); ok {
		if nb, ok := toNumeric(b); ok {
			return na == nb
		}
	}

	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

// toNumeric converts json.Number, float64, int, int64, etc. to float64.
func toNumeric(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// toFloat64 converts an interface value to float64 if it is numeric.
func toFloat64(v interface{}) (float64, bool) {
	return toNumeric(v)
}

// joinPath builds a path string for error messages.
func joinPath(base, field string) string {
	if base == "" {
		return field
	}
	return base + "." + field
}

// prefixPath returns a path prefix for error messages.
func prefixPath(path string) string {
	if path == "" {
		return ""
	}
	return path + ": "
}
