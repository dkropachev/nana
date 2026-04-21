package gocli

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRepositoryVerificationPlanExampleValidatesAgainstPublishedSchema(t *testing.T) {
	repoRoot := repoRootFromCaller(t)
	schema := readJSONSchemaTestFile(t, filepath.Join(repoRoot, "docs", "schemas", "verification-plan.schema.json"))
	profile := readJSONValueTestFile(t, filepath.Join(repoRoot, "docs", "examples", "verification-plan.example.json"))

	if err := validateJSONSchemaSubset(schema, profile, "$"); err != nil {
		t.Fatalf("verification-plan example should validate against docs/schemas/verification-plan.schema.json: %v", err)
	}
}

func TestContextTelemetryPublishedSchemaRejectsRawArgumentFields(t *testing.T) {
	repoRoot := repoRootFromCaller(t)
	schema := readJSONSchemaTestFile(t, filepath.Join(repoRoot, "docs", "schemas", "context-telemetry-event.schema.json"))

	for _, tc := range []struct {
		name  string
		event string
	}{
		{
			name:  "arguments",
			event: `{"timestamp":"2026-04-20T00:00:00Z","tool":"codex","event":"skill_doc_load","arguments":["test","./...","SECRET_TOKEN"]}`,
		},
		{
			name:  "raw_args",
			event: `{"timestamp":"2026-04-20T00:00:00Z","tool":"codex","event":"skill_doc_load","raw_args":"test ./... SECRET_TOKEN"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value any
			if err := json.Unmarshal([]byte(tc.event), &value); err != nil {
				t.Fatalf("unmarshal event: %v", err)
			}
			if err := validateJSONSchemaSubset(schema, value, "$"); err == nil {
				t.Fatalf("context telemetry schema accepted raw argument field %q", tc.name)
			}
		})
	}
}

func TestContextTelemetryPublishedSchemaAcceptsSkillTelemetryWithoutTool(t *testing.T) {
	repoRoot := repoRootFromCaller(t)
	schema := readJSONSchemaTestFile(t, filepath.Join(repoRoot, "docs", "schemas", "context-telemetry-event.schema.json"))
	event := decodeJSONTestValue(t, `{
  "timestamp": "2026-04-20T00:00:00Z",
  "event": "skill_doc_load",
  "skill": "plan",
  "path": "/home/alice/.codex/skills/plan/SKILL.md",
  "doc_label": "runtime",
  "cache": "miss",
  "loader": "nana_skill_runtime_cache",
  "schema": "skill_doc_load.v1"
}`)

	if err := validateJSONSchemaSubset(schema, event, "$"); err != nil {
		t.Fatalf("context telemetry schema rejected existing skill telemetry without tool: %v", err)
	}
}

func TestVerificationPublishedSchemaMatchesVerifyNormalizationEdges(t *testing.T) {
	repoRoot := repoRootFromCaller(t)
	schema := readJSONSchemaTestFile(t, filepath.Join(repoRoot, "docs", "schemas", "verification-plan.schema.json"))

	for _, tc := range []struct {
		name      string
		profile   string
		wantValid bool
	}{
		{
			name: "accepts omitted version as default version one",
			profile: `{
  "stages": [{"name":"lint","command":"make lint"}]
}`,
			wantValid: true,
		},
		{
			name: "rejects explicit zero version",
			profile: `{
  "version": 0,
  "stages": [{"name":"lint","command":"make lint"}]
}`,
			wantValid: false,
		},
		{
			name: "rejects negative version",
			profile: `{
  "version": -1,
  "stages": [{"name":"lint","command":"make lint"}]
}`,
			wantValid: false,
		},
		{
			name: "accepts trimmed changed_scope lists with blank entries",
			profile: `{
  "version": 1,
  "stages": [{"name":" lint ","command":" make lint "}],
  "changed_scope": {
    "full_check": {"command": " make verify "},
    "paths": [{
      "name": " go ",
      "patterns": [" internal/**/*.go ", ""],
      "stages": [" lint ", ""],
      "checks": ["", " go test ./internal/gocli "]
    }]
  }
}`,
			wantValid: true,
		},
		{
			name: "accepts blank stages list when checks are nonblank",
			profile: `{
  "version": 1,
  "stages": [{"name":"lint","command":"make lint"}],
  "changed_scope": {
    "full_check": {"command": "make verify"},
    "paths": [{
      "name": "docs",
      "patterns": ["*.md"],
      "stages": ["", "  "],
      "checks": [" git diff --check "]
    }]
  }
}`,
			wantValid: true,
		},
		{
			name: "rejects whitespace-only stage name",
			profile: `{
  "version": 1,
  "stages": [{"name":"   ","command":"make lint"}]
}`,
			wantValid: false,
		},
		{
			name: "rejects whitespace-only stage command",
			profile: `{
  "version": 1,
  "stages": [{"name":"lint","command":"   "}]
}`,
			wantValid: false,
		},
		{
			name: "rejects whitespace-only changed_scope full_check command",
			profile: `{
  "version": 1,
  "stages": [{"name":"lint","command":"make lint"}],
  "changed_scope": {
    "full_check": {"command": "   "},
    "paths": [{"name": "go", "patterns": ["*.go"], "stages": ["lint"]}]
  }
}`,
			wantValid: false,
		},
		{
			name: "rejects whitespace-only changed_scope path name",
			profile: `{
  "version": 1,
  "stages": [{"name":"lint","command":"make lint"}],
  "changed_scope": {
    "full_check": {"command": "make verify"},
    "paths": [{"name": "   ", "patterns": ["*.go"], "stages": ["lint"]}]
  }
}`,
			wantValid: false,
		},
		{
			name: "rejects changed_scope patterns without a nonblank item",
			profile: `{
  "version": 1,
  "stages": [{"name":"lint","command":"make lint"}],
  "changed_scope": {
    "full_check": {"command": "make verify"},
    "paths": [{"name": "go", "patterns": ["", "  "], "stages": ["lint"]}]
  }
}`,
			wantValid: false,
		},
		{
			name: "rejects changed_scope targets without a nonblank stage or check",
			profile: `{
  "version": 1,
  "stages": [{"name":"lint","command":"make lint"}],
  "changed_scope": {
    "full_check": {"command": "make verify"},
    "paths": [{"name": "go", "patterns": ["*.go"], "stages": ["", "  "], "checks": []}]
  }
}`,
			wantValid: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := decodeJSONTestValue(t, tc.profile)
			err := validateJSONSchemaSubset(schema, profile, "$")
			_, decodeErr := decodeVerificationProfile([]byte(tc.profile))
			if tc.wantValid && decodeErr != nil {
				t.Fatalf("test fixture should be accepted by verify normalization: %v", decodeErr)
			}
			if !tc.wantValid && decodeErr == nil {
				t.Fatalf("test fixture should be rejected by verify normalization")
			}
			if tc.wantValid && err != nil {
				t.Fatalf("schema rejected profile accepted by verify normalization: %v", err)
			}
			if !tc.wantValid && err == nil {
				t.Fatalf("schema accepted profile rejected by verify normalization")
			}
			if (err == nil) != (decodeErr == nil) {
				t.Fatalf("schema and verify normalization disagreed: schemaErr=%v decodeErr=%v", err, decodeErr)
			}
		})
	}
}

func readJSONSchemaTestFile(t *testing.T, path string) map[string]any {
	t.Helper()
	value := readJSONValueTestFile(t, path)
	schema, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("schema %s is not a JSON object", path)
	}
	return schema
}

func readJSONValueTestFile(t *testing.T, path string) any {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var value any
	if err := json.Unmarshal(content, &value); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return value
}

func decodeJSONTestValue(t *testing.T, raw string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatalf("unmarshal test JSON: %v\n%s", err, raw)
	}
	return value
}

func validateJSONSchemaSubset(schema map[string]any, value any, path string) error {
	if notSchema, ok := schema["not"].(map[string]any); ok {
		if err := validateJSONSchemaSubset(notSchema, value, path); err == nil {
			return fmt.Errorf("%s matched forbidden schema", path)
		}
	}
	if anyOf, ok := schema["anyOf"].([]any); ok {
		var failures []string
		for _, candidate := range anyOf {
			candidateSchema, ok := candidate.(map[string]any)
			if !ok {
				return fmt.Errorf("%s has non-object anyOf entry", path)
			}
			if err := validateJSONSchemaSubset(candidateSchema, value, path); err == nil {
				failures = nil
				break
			} else {
				failures = append(failures, err.Error())
			}
		}
		if len(failures) > 0 {
			return fmt.Errorf("%s did not match anyOf: %s", path, strings.Join(failures, "; "))
		}
	}

	if rawType, ok := schema["type"].(string); ok {
		if err := validateJSONSchemaSubsetType(rawType, value, path); err != nil {
			return err
		}
	}

	if rawRequired, ok := schema["required"].([]any); ok {
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object for required fields", path)
		}
		for _, rawName := range rawRequired {
			name, ok := rawName.(string)
			if !ok {
				return fmt.Errorf("%s has non-string required entry", path)
			}
			if _, ok := object[name]; !ok {
				return fmt.Errorf("%s missing required property %q", path, name)
			}
		}
	}

	if rawMinimum, ok := schema["minimum"].(float64); ok {
		number, ok := value.(float64)
		if !ok {
			return fmt.Errorf("%s must be numeric for minimum", path)
		}
		if number < rawMinimum {
			return fmt.Errorf("%s must be >= %v", path, rawMinimum)
		}
	}
	if rawMinLength, ok := schema["minLength"].(float64); ok {
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string for minLength", path)
		}
		if len(text) < int(rawMinLength) {
			return fmt.Errorf("%s length must be >= %d", path, int(rawMinLength))
		}
	}
	if rawPattern, ok := schema["pattern"].(string); ok {
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string for pattern", path)
		}
		pattern, err := regexp.Compile(rawPattern)
		if err != nil {
			return fmt.Errorf("%s has invalid pattern %q: %w", path, rawPattern, err)
		}
		if !pattern.MatchString(text) {
			return fmt.Errorf("%s must match pattern %q", path, rawPattern)
		}
	}
	if rawMinItems, ok := schema["minItems"].(float64); ok {
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array for minItems", path)
		}
		if len(items) < int(rawMinItems) {
			return fmt.Errorf("%s item count must be >= %d", path, int(rawMinItems))
		}
	}
	if rawContains, ok := schema["contains"].(map[string]any); ok {
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array for contains", path)
		}
		minContains := 1
		if rawMinContains, ok := schema["minContains"].(float64); ok {
			minContains = int(rawMinContains)
		}
		matches := 0
		for index, item := range items {
			if err := validateJSONSchemaSubset(rawContains, item, fmt.Sprintf("%s[%d]", path, index)); err == nil {
				matches++
			}
		}
		if matches < minContains {
			return fmt.Errorf("%s must contain at least %d matching item(s), got %d", path, minContains, matches)
		}
	}

	if rawProperties, ok := schema["properties"].(map[string]any); ok {
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object for properties", path)
		}
		for name, rawPropertySchema := range rawProperties {
			propertyValue, ok := object[name]
			if !ok {
				continue
			}
			propertySchema, ok := rawPropertySchema.(map[string]any)
			if !ok {
				return fmt.Errorf("%s property %q schema is not an object", path, name)
			}
			if err := validateJSONSchemaSubset(propertySchema, propertyValue, path+"."+name); err != nil {
				return err
			}
		}
		if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
			for name := range object {
				if _, ok := rawProperties[name]; !ok {
					return fmt.Errorf("%s has unexpected property %q", path, name)
				}
			}
		}
	}

	if rawItems, ok := schema["items"].(map[string]any); ok {
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array for items", path)
		}
		for index, item := range items {
			if err := validateJSONSchemaSubset(rawItems, item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}

	return nil
}

func validateJSONSchemaSubsetType(want string, value any, path string) error {
	switch want {
	case "array":
		if _, ok := value.([]any); !ok {
			return fmt.Errorf("%s must be an array", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", path)
		}
	case "integer":
		number, ok := value.(float64)
		if !ok || math.Trunc(number) != number {
			return fmt.Errorf("%s must be an integer", path)
		}
	case "object":
		if _, ok := value.(map[string]any); !ok {
			return fmt.Errorf("%s must be an object", path)
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be a string", path)
		}
	default:
		return fmt.Errorf("%s has unsupported schema type %q", path, want)
	}
	return nil
}
