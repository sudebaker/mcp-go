package executor

import (
	"encoding/json"
	"strings"
	"testing"
)

func schemaFromMap(m map[string]interface{}) *ToolProperty {
	p, err := inputSchemaToRootProperty(m)
	if err != nil {
		panic(err)
	}
	return &p
}

func TestValidator_MissingRequired(t *testing.T) {
	schema := schemaFromMap(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
		},
		"required": []interface{}{"name"},
	})

	err := ValidateArguments(schema, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for missing required field")
	}
}

func TestValidator_WrongType(t *testing.T) {
	schema := schemaFromMap(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"count": map[string]interface{}{"type": "integer"},
		},
	})

	err := ValidateArguments(schema, map[string]interface{}{"count": "not-a-number"})
	if err == nil {
		t.Fatal("expected error for wrong type")
	}
}

func TestValidator_EnumConstraint(t *testing.T) {
	schema := schemaFromMap(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"color": map[string]interface{}{
				"type": "string",
				"enum": []interface{}{"red", "green", "blue"},
			},
		},
	})

	err := ValidateArguments(schema, map[string]interface{}{"color": "red"})
	if err != nil {
		t.Fatalf("expected valid enum value: %v", err)
	}

	err = ValidateArguments(schema, map[string]interface{}{"color": "yellow"})
	if err == nil {
		t.Fatal("expected error for invalid enum value")
	}
}

func TestValidator_NestedObject(t *testing.T) {
	schema := schemaFromMap(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"user": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{"type": "string"},
					"age":  map[string]interface{}{"type": "integer"},
				},
				"required": []interface{}{"name"},
			},
		},
	})

	err := ValidateArguments(schema, map[string]interface{}{
		"user": map[string]interface{}{
			"name": "alice",
			"age":  30,
		},
	})
	if err != nil {
		t.Fatalf("expected valid nested object: %v", err)
	}

	err = ValidateArguments(schema, map[string]interface{}{
		"user": map[string]interface{}{
			"age": 30,
		},
	})
	if err == nil {
		t.Fatal("expected error for missing nested required field")
	}
}

func TestValidator_ArrayOfObjects(t *testing.T) {
	schema := schemaFromMap(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"items": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":   map[string]interface{}{"type": "integer"},
						"name": map[string]interface{}{"type": "string"},
					},
					"required": []interface{}{"id"},
				},
			},
		},
	})

	err := ValidateArguments(schema, map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"id": 1, "name": "a"},
			map[string]interface{}{"id": 2},
		},
	})
	if err != nil {
		t.Fatalf("expected valid array of objects: %v", err)
	}

	err = ValidateArguments(schema, map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"name": "missing-id"},
		},
	})
	if err == nil {
		t.Fatal("expected error for missing required field in array item")
	}
}

func TestValidator_MinMaxLength(t *testing.T) {
	schema := schemaFromMap(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"code": map[string]interface{}{
				"type":      "string",
				"minLength": 3,
				"maxLength": 6,
			},
		},
	})

	err := ValidateArguments(schema, map[string]interface{}{"code": "abc"})
	if err != nil {
		t.Fatalf("expected valid length: %v", err)
	}

	err = ValidateArguments(schema, map[string]interface{}{"code": "ab"})
	if err == nil {
		t.Fatal("expected error for string below minLength")
	}

	err = ValidateArguments(schema, map[string]interface{}{"code": "abcdefg"})
	if err == nil {
		t.Fatal("expected error for string above maxLength")
	}
}

func TestValidator_Pattern(t *testing.T) {
	schema := schemaFromMap(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"token": map[string]interface{}{
				"type":    "string",
				"pattern": "^[a-z0-9]+$",
			},
		},
	})

	err := ValidateArguments(schema, map[string]interface{}{"token": "abc123"})
	if err != nil {
		t.Fatalf("expected valid pattern match: %v", err)
	}

	err = ValidateArguments(schema, map[string]interface{}{"token": "ABC"})
	if err == nil {
		t.Fatal("expected error for pattern mismatch")
	}
}

func TestValidator_Format(t *testing.T) {
	schema := schemaFromMap(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"email": map[string]interface{}{"type": "string", "format": "email"},
			"uri":   map[string]interface{}{"type": "string", "format": "uri"},
			"when":  map[string]interface{}{"type": "string", "format": "date-time"},
		},
	})

	err := ValidateArguments(schema, map[string]interface{}{
		"email": "test@example.com",
		"uri":   "https://example.com/path",
		"when":  "2026-07-23T12:00:00Z",
	})
	if err != nil {
		t.Fatalf("expected valid formats: %v", err)
	}

	err = ValidateArguments(schema, map[string]interface{}{"email": "not-an-email"})
	if err == nil {
		t.Fatal("expected error for invalid email")
	}
}

func TestValidator_MinimumMaximum(t *testing.T) {
	schema := schemaFromMap(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"score": map[string]interface{}{
				"type":    "number",
				"minimum": 0.0,
				"maximum": 100.0,
			},
		},
	})

	err := ValidateArguments(schema, map[string]interface{}{"score": 50.0})
	if err != nil {
		t.Fatalf("expected valid number range: %v", err)
	}

	err = ValidateArguments(schema, map[string]interface{}{"score": -1.0})
	if err == nil {
		t.Fatal("expected error for value below minimum")
	}

	err = ValidateArguments(schema, map[string]interface{}{"score": 101.0})
	if err == nil {
		t.Fatal("expected error for value above maximum")
	}
}

func TestValidator_Coercion_IntAsFloat(t *testing.T) {
	schema := schemaFromMap(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"count": map[string]interface{}{"type": "integer"},
		},
	})

	err := ValidateArguments(schema, map[string]interface{}{"count": 1.0})
	if err != nil {
		t.Fatalf("expected integer coercion to pass: %v", err)
	}
}

func TestValidator_Coercion_JsonNumber(t *testing.T) {
	schema := schemaFromMap(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"count": map[string]interface{}{"type": "integer"},
		},
	})

	err := ValidateArguments(schema, map[string]interface{}{"count": json.Number("42")})
	if err != nil {
		t.Fatalf("expected json.Number coercion to pass: %v", err)
	}
}

func TestValidator_NilRequired(t *testing.T) {
	schema := schemaFromMap(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
		},
		"required": []interface{}{"name"},
	})

	err := ValidateArguments(schema, map[string]interface{}{"name": nil})
	if err == nil {
		t.Fatal("expected error for nil required value")
	}
}

func TestValidator_UnicodeLength(t *testing.T) {
	schema := schemaFromMap(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"text": map[string]interface{}{
				"type":      "string",
				"maxLength": 2,
			},
		},
	})

	// "é" as single code point: length 1
	err := ValidateArguments(schema, map[string]interface{}{"text": "é"})
	if err != nil {
		t.Fatalf("expected single code point to fit: %v", err)
	}

	// "é" as e + combining accent: length 2
	err = ValidateArguments(schema, map[string]interface{}{"text": "é"})
	if err != nil {
		t.Fatalf("expected combining chars to count as 2: %v", err)
	}

	// emoji + skin tone: length 2
	err = ValidateArguments(schema, map[string]interface{}{"text": "👍🏽"})
	if err != nil {
		t.Fatalf("expected emoji with modifier to count as 2: %v", err)
	}
}

func TestValidator_HugeString(t *testing.T) {
	schema := schemaFromMap(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"payload": map[string]interface{}{
				"type":      "string",
				"maxLength": 10,
			},
		},
	})

	big := strings.Repeat("x", 100)
	err := ValidateArguments(schema, map[string]interface{}{"payload": big})
	if err == nil {
		t.Fatal("expected error for huge string above maxLength")
	}
}

func TestValidator_ExtraArgsAllowed(t *testing.T) {
	schema := schemaFromMap(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"known": map[string]interface{}{"type": "string"},
		},
	})

	err := ValidateArguments(schema, map[string]interface{}{
		"known":  "ok",
		"extra":  "also ok",
		"number": 42,
	})
	if err != nil {
		t.Fatalf("expected extra args to be allowed: %v", err)
	}
}

func TestValidator_NilSchema(t *testing.T) {
	err := ValidateArguments(nil, map[string]interface{}{"anything": "goes"})
	if err != nil {
		t.Fatalf("expected nil schema to skip validation: %v", err)
	}
}
