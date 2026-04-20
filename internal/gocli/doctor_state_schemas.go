package gocli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type nanaStateSchemaResult struct {
	checked int
	issues  []string
	notes   []string
}

const (
	// Telemetry logs are append-only local diagnostics. Keep doctor responsive
	// even when a workspace has accumulated a very large log.
	maxContextTelemetrySchemaLines  = 1000
	maxContextTelemetrySchemaIssues = 3
)

var forbiddenContextTelemetryRawFields = []struct {
	name  string
	label string
}{
	{name: "args", label: "arguments"},
	{name: "arguments", label: "arguments"},
	{name: "argv", label: "arguments"},
	{name: "command", label: "command"},
	{name: "output", label: "output"},
	{name: "raw_args", label: "arguments"},
	{name: "raw_output", label: "output"},
	{name: "stderr", label: "stderr"},
	{name: "stdout", label: "stdout"},
}

func checkNanaStateSchemas(cwd string) doctorCheck {
	results := []nanaStateSchemaResult{
		validateProjectMemorySchema(cwd),
		validateVerificationProfileSchema(cwd),
		validateContextTelemetrySchema(cwd),
		validateMarkdownStateSchemas(cwd),
	}
	checked := 0
	issues := []string{}
	notes := []string{}
	for _, result := range results {
		checked += result.checked
		issues = append(issues, result.issues...)
		notes = append(notes, result.notes...)
	}
	if len(issues) > 0 {
		return doctorCheck{Name: "NANA state schemas", Status: "fail", Message: strings.Join(limitStrings(issues, 4), "; ")}
	}
	if checked == 0 {
		return doctorCheck{Name: "NANA state schemas", Status: "pass", Message: "no schema-backed state artifacts yet"}
	}
	message := fmt.Sprintf("%d schema-backed state artifact(s) valid", checked)
	if len(notes) > 0 {
		message = fmt.Sprintf("%s (%s)", message, strings.Join(limitStrings(notes, 2), "; "))
	}
	return doctorCheck{Name: "NANA state schemas", Status: "pass", Message: message}
}

func validateProjectMemorySchema(cwd string) nanaStateSchemaResult {
	path := filepath.Join(cwd, ".nana", "project-memory.json")
	if !fileExists(path) {
		return nanaStateSchemaResult{}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nanaStateSchemaResult{checked: 1, issues: []string{fmt.Sprintf("%s: %v", slashRel(cwd, path), err)}}
	}
	if !json.Valid(content) {
		return nanaStateSchemaResult{checked: 1, issues: []string{fmt.Sprintf("%s: invalid JSON", slashRel(cwd, path))}}
	}
	fields, err := parseJSONObject(content)
	if err != nil {
		return nanaStateSchemaResult{checked: 1, issues: []string{fmt.Sprintf("%s: %v", slashRel(cwd, path), err)}}
	}
	issues := []string{}
	if raw, ok := fields["$schema"]; ok {
		if _, err := requiredString(raw, "$schema"); err != nil {
			issues = append(issues, fmt.Sprintf("%s: %v", slashRel(cwd, path), err))
		}
	}
	if raw, ok := fields["version"]; ok {
		if value, err := requiredInteger(raw, "version"); err != nil {
			issues = append(issues, fmt.Sprintf("%s: %v", slashRel(cwd, path), err))
		} else if value < 1 {
			issues = append(issues, fmt.Sprintf("%s: version must be >= 1", slashRel(cwd, path)))
		}
	}
	for _, field := range []string{"created_at", "generated_at", "updated_at"} {
		if raw, ok := fields[field]; ok {
			if err := validateRFC3339Field(raw, field); err != nil {
				issues = append(issues, fmt.Sprintf("%s: %v", slashRel(cwd, path), err))
			}
		}
	}
	for _, field := range []string{"constraints", "decisions", "facts", "notes", "preferences"} {
		if raw, ok := fields[field]; ok && !rawJSONIsArray(raw) {
			issues = append(issues, fmt.Sprintf("%s: %s must be an array", slashRel(cwd, path), field))
		}
	}
	return nanaStateSchemaResult{checked: 1, issues: issues}
}

func validateVerificationProfileSchema(cwd string) nanaStateSchemaResult {
	_, path, ok := findVerificationProfile(cwd)
	if !ok {
		return nanaStateSchemaResult{}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nanaStateSchemaResult{checked: 1, issues: []string{fmt.Sprintf("%s: %v", slashRel(cwd, path), err)}}
	}
	fields, err := parseJSONObject(content)
	if err != nil {
		return nanaStateSchemaResult{checked: 1, issues: []string{fmt.Sprintf("%s: %v", slashRel(cwd, path), err)}}
	}
	issues := []string{}
	if raw, ok := fields["$schema"]; ok {
		if _, err := requiredString(raw, "$schema"); err != nil {
			issues = append(issues, fmt.Sprintf("%s: %v", slashRel(cwd, path), err))
		}
	}
	if err := validateVerificationProfileVersionField(fields); err != nil {
		issues = append(issues, fmt.Sprintf("%s: %v", slashRel(cwd, path), err))
	}
	if len(issues) > 0 {
		return nanaStateSchemaResult{checked: 1, issues: issues}
	}
	if _, err := decodeVerificationProfile(content); err != nil {
		return nanaStateSchemaResult{checked: 1, issues: []string{fmt.Sprintf("%s: %v", slashRel(cwd, path), err)}}
	}
	return nanaStateSchemaResult{checked: 1}
}

func validateContextTelemetrySchema(cwd string) nanaStateSchemaResult {
	path := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	if !fileExists(path) {
		return nanaStateSchemaResult{}
	}
	file, err := os.Open(path)
	if err != nil {
		return nanaStateSchemaResult{checked: 1, issues: []string{fmt.Sprintf("%s: %v", slashRel(cwd, path), err)}}
	}
	defer file.Close()

	issues := []string{}
	notes := []string{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNumber := 0
	noteLineLimit := func() {
		if len(notes) == 0 {
			notes = append(notes, fmt.Sprintf("%s: checked first %d telemetry line(s)", slashRel(cwd, path), maxContextTelemetrySchemaLines))
		}
	}
	for scanner.Scan() {
		lineNumber++
		stopAtLineLimit := lineNumber >= maxContextTelemetrySchemaLines
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			if stopAtLineLimit {
				noteLineLimit()
				break
			}
			continue
		}
		if err := validateContextTelemetryEvent([]byte(line)); err != nil {
			issues = append(issues, fmt.Sprintf("%s:%d: %v", slashRel(cwd, path), lineNumber, err))
			if len(issues) >= maxContextTelemetrySchemaIssues {
				issues = append(issues, fmt.Sprintf("%s: stopped after %d telemetry schema issue(s)", slashRel(cwd, path), maxContextTelemetrySchemaIssues))
				break
			}
		}
		if stopAtLineLimit {
			noteLineLimit()
			break
		}
	}
	if err := scanner.Err(); err != nil {
		issues = append(issues, fmt.Sprintf("%s: %v", slashRel(cwd, path), err))
	}
	return nanaStateSchemaResult{checked: 1, issues: issues, notes: notes}
}

func validateMarkdownStateSchemas(cwd string) nanaStateSchemaResult {
	paths := []string{}
	if path := filepath.Join(cwd, ".nana", "notepad.md"); fileExists(path) {
		paths = append(paths, path)
	}
	plansDir := filepath.Join(cwd, ".nana", "plans")
	if info, err := os.Stat(plansDir); err == nil && info.IsDir() {
		_ = filepath.WalkDir(plansDir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
				return nil
			}
			paths = append(paths, path)
			return nil
		})
	}
	sort.Strings(paths)
	issues := []string{}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			issues = append(issues, fmt.Sprintf("%s: %v", slashRel(cwd, path), err))
			continue
		}
		if !utf8.Valid(content) || strings.ContainsRune(string(content), '\x00') {
			issues = append(issues, fmt.Sprintf("%s: markdown state must be UTF-8 text", slashRel(cwd, path)))
		}
	}
	return nanaStateSchemaResult{checked: len(paths), issues: issues}
}

func validateContextTelemetryEvent(content []byte) error {
	fields, err := parseJSONObject(content)
	if err != nil {
		return err
	}
	for _, forbidden := range forbiddenContextTelemetryRawFields {
		if _, ok := fields[forbidden.name]; ok {
			return fmt.Errorf("must not persist raw %s", forbidden.label)
		}
	}
	if err := validateRFC3339RequiredField(fields, "timestamp"); err != nil {
		return err
	}
	if raw, ok := fields["tool"]; ok {
		if _, err := requiredString(raw, "tool"); err != nil {
			return err
		}
	}
	eventName, err := requiredObjectString(fields, "event")
	if err != nil {
		return err
	}
	if !validTelemetryEventName(eventName) {
		return fmt.Errorf("event must be lower_snake_case")
	}
	if err := validateKnownTelemetryFields(fields); err != nil {
		return err
	}
	if eventName == "shell_output_compaction" || eventName == "shell_output_compaction_failed" {
		if err := validateShellCompactionTelemetry(fields, eventName); err != nil {
			return err
		}
	}
	return nil
}

func validateKnownTelemetryFields(fields map[string]json.RawMessage) error {
	for _, field := range []string{"run_id", "skill", "command_name", "error"} {
		if raw, ok := fields[field]; ok {
			if _, err := schemaString(raw, field); err != nil {
				return err
			}
		}
	}
	if raw, ok := fields["summarized"]; ok {
		if _, err := schemaBool(raw, "summarized"); err != nil {
			return err
		}
	}
	integerFields := []struct {
		name string
		min  int64
	}{
		{name: "argument_count", min: 0},
		{name: "captured_bytes", min: 0},
		{name: "exit_code", min: -1},
		{name: "stderr_bytes", min: 0},
		{name: "stderr_lines", min: 0},
		{name: "stdout_bytes", min: 0},
		{name: "stdout_lines", min: 0},
		{name: "summary_bytes", min: 0},
		{name: "summary_lines", min: 0},
	}
	for _, field := range integerFields {
		raw, ok := fields[field.name]
		if !ok {
			continue
		}
		value, err := requiredInteger(raw, field.name)
		if err != nil {
			return err
		}
		if value < field.min {
			return fmt.Errorf("%s must be >= %d", field.name, field.min)
		}
	}
	return nil
}

func validateShellCompactionTelemetry(fields map[string]json.RawMessage, eventName string) error {
	if _, err := requiredObjectInteger(fields, "exit_code"); err != nil {
		return err
	}
	for _, field := range []string{"argument_count", "captured_bytes", "stderr_bytes", "stderr_lines", "stdout_bytes", "stdout_lines"} {
		value, err := requiredObjectInteger(fields, field)
		if err != nil {
			return err
		}
		if value < 0 {
			return fmt.Errorf("%s must be >= 0", field)
		}
	}
	if _, err := requiredObjectBool(fields, "summarized"); err != nil {
		return err
	}
	for _, field := range []string{"summary_bytes", "summary_lines"} {
		if raw, ok := fields[field]; ok {
			value, err := requiredInteger(raw, field)
			if err != nil {
				return err
			}
			if value < 0 {
				return fmt.Errorf("%s must be >= 0", field)
			}
		}
	}
	if eventName == "shell_output_compaction_failed" {
		raw, ok := fields["error"]
		if !ok {
			return fmt.Errorf("missing error")
		}
		if _, err := schemaString(raw, "error"); err != nil {
			return err
		}
	}
	return nil
}

func parseJSONObject(content []byte) (map[string]json.RawMessage, error) {
	if !strings.HasPrefix(strings.TrimSpace(string(content)), "{") {
		return nil, fmt.Errorf("must be a JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(content, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, fmt.Errorf("must be a JSON object")
	}
	return fields, nil
}

func validateRFC3339RequiredField(fields map[string]json.RawMessage, name string) error {
	raw, ok := fields[name]
	if !ok {
		return fmt.Errorf("missing %s", name)
	}
	return validateRFC3339Field(raw, name)
}

func validateRFC3339Field(raw json.RawMessage, name string) error {
	value, err := requiredString(raw, name)
	if err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return fmt.Errorf("%s must be RFC3339", name)
	}
	return nil
}

func requiredObjectString(fields map[string]json.RawMessage, name string) (string, error) {
	raw, ok := fields[name]
	if !ok {
		return "", fmt.Errorf("missing %s", name)
	}
	return requiredString(raw, name)
}

func requiredString(raw json.RawMessage, name string) (string, error) {
	value, err := schemaString(raw, name)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s must be non-empty", name)
	}
	return value, nil
}

func schemaString(raw json.RawMessage, name string) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s must be a string", name)
	}
	return value, nil
}

func requiredObjectBool(fields map[string]json.RawMessage, name string) (bool, error) {
	raw, ok := fields[name]
	if !ok {
		return false, fmt.Errorf("missing %s", name)
	}
	return schemaBool(raw, name)
}

func schemaBool(raw json.RawMessage, name string) (bool, error) {
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return value, nil
}

func requiredObjectInteger(fields map[string]json.RawMessage, name string) (int64, error) {
	raw, ok := fields[name]
	if !ok {
		return 0, fmt.Errorf("missing %s", name)
	}
	return requiredInteger(raw, name)
}

func requiredInteger(raw json.RawMessage, name string) (int64, error) {
	var number json.Number
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	value, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return value, nil
}

func rawJSONIsArray(raw json.RawMessage) bool {
	return strings.HasPrefix(strings.TrimSpace(string(raw)), "[")
}

func validTelemetryEventName(value string) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && index > 0:
		case r == '_' && index > 0:
		default:
			return false
		}
	}
	return !strings.HasSuffix(value, "_") && !strings.Contains(value, "__")
}

func slashRel(cwd string, path string) string {
	return filepath.ToSlash(mustRelative(cwd, path))
}
