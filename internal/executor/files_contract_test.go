package executor

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// toolsWithFileArray are the tools whose __files__ schema declares items of type
// object in configs/config.yaml. The MCP smoke client (tests/mcp_test_client.py)
// covers them, but no CI job runs that file, so this test pins the same contract
// where CI does run it: a string in __files__ is rejected by the schema before
// the subprocess is spawned.
var toolsWithFileArray = []string{
	"analyze_data",
	"batch_summarize",
	"regulation_diff",
	"config_auditor",
	"document_classifier",
}

// repoToolSchemas decodes the input_schema blocks of the shipped
// configs/config.yaml. It parses the file instead of calling config.Load because
// the full load runs tool discovery against container-only paths
// (tools_dir: /app/tools) that do not exist on a developer host or in CI.
func repoToolSchemas(t *testing.T) map[string]map[string]interface{} {
	t.Helper()

	raw, err := os.ReadFile("../../configs/config.yaml")
	if err != nil {
		t.Fatalf("read configs/config.yaml: %v", err)
	}

	var doc struct {
		Tools []struct {
			Name        string                 `yaml:"name"`
			InputSchema map[string]interface{} `yaml:"input_schema"`
		} `yaml:"tools"`
	}
	if err := yaml.Unmarshal([]byte(os.ExpandEnv(string(raw))), &doc); err != nil {
		t.Fatalf("parse configs/config.yaml: %v", err)
	}

	schemas := make(map[string]map[string]interface{}, len(doc.Tools))
	for _, tool := range doc.Tools {
		schemas[tool.Name] = tool.InputSchema
	}
	return schemas
}

func TestToolSchemas_RejectStringFilesItem(t *testing.T) {
	schemas := repoToolSchemas(t)

	for _, name := range toolsWithFileArray {
		t.Run(name, func(t *testing.T) {
			inputSchema, ok := schemas[name]
			if !ok || inputSchema == nil {
				t.Fatalf("tool %q has no input_schema in configs/config.yaml", name)
			}
			root, err := inputSchemaToRootProperty(inputSchema)
			if err != nil {
				t.Fatalf("inputSchemaToRootProperty(%s): %v", name, err)
			}

			// A bare string must be rejected: __files__ items are objects.
			args := requiredArgs(root, "__files__")
			args["__files__"] = []interface{}{"res://docs/report.pdf"}
			err = ValidateArguments(&root, args)
			if err == nil {
				t.Fatalf("%s accepted a string in __files__; the schema wants objects", name)
			}
			if !strings.Contains(err.Error(), "__files__[0]") {
				t.Errorf("%s: error does not point at __files__[0]: %v", name, err)
			}
			if !strings.Contains(err.Error(), "expected object, got string") {
				t.Errorf("%s: unexpected rejection reason: %v", name, err)
			}

			// The documented object form must not be rejected by the __files__ rule.
			args = requiredArgs(root, "__files__")
			args["__files__"] = []interface{}{
				map[string]interface{}{"url": "res://docs/report.pdf", "name": "report.pdf"},
			}
			err = ValidateArguments(&root, args)
			if err != nil && strings.Contains(err.Error(), "__files__") {
				t.Errorf("%s rejected the documented object form: %v", name, err)
			}
		})
	}
}

// requiredArgs fills every required property except skip with a placeholder of
// the right type, so validation reaches the __files__ item rule instead of
// stopping at "required field is missing".
func requiredArgs(root ToolProperty, skip string) map[string]interface{} {
	args := make(map[string]interface{}, len(root.Required))
	for _, req := range root.Required {
		if req == skip {
			continue
		}
		switch root.Properties[req].Type {
		case "array":
			args[req] = []interface{}{}
		case "object":
			args[req] = map[string]interface{}{}
		case "integer", "number":
			args[req] = 1
		case "boolean":
			args[req] = false
		default:
			args[req] = "qa"
		}
	}
	return args
}
