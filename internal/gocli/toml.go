package gocli

import "strings"

func ReadTopLevelTomlString(content string, key string) string {
	inTopLevel := true
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(strings.Split(trimmed, "#")[0], "]") {
			inTopLevel = false
			continue
		}
		if !inTopLevel || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if strings.TrimSpace(parts[0]) != key {
			continue
		}
		value := strings.TrimSpace(strings.Split(parts[1], "#")[0])
		return parseTomlStringValue(value)
	}
	return ""
}

func UpsertTopLevelTomlString(content string, key string, value string) string {
	eol := "\n"
	if strings.Contains(content, "\r\n") {
		eol = "\r\n"
	}
	assignment := key + ` = "` + escapeTomlString(value) + `"`
	if strings.TrimSpace(content) == "" {
		return assignment + eol
	}

	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	replaced := false
	inTopLevel := true

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(strings.Split(trimmed, "#")[0], "]") {
			inTopLevel = false
			continue
		}
		if !inTopLevel || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if strings.TrimSpace(parts[0]) == key {
			lines[i] = assignment
			replaced = true
			break
		}
	}

	if !replaced {
		insertAt := len(lines)
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(strings.Split(trimmed, "#")[0], "]") {
				insertAt = i
				break
			}
		}
		lines = append(lines[:insertAt], append([]string{assignment}, lines[insertAt:]...)...)
	}

	output := strings.Join(lines, eol)
	if !strings.HasSuffix(output, eol) {
		output += eol
	}
	return output
}

func parseTomlStringValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) >= 2 {
		if (trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"') || (trimmed[0] == '\'' && trimmed[len(trimmed)-1] == '\'') {
			return trimmed[1 : len(trimmed)-1]
		}
	}
	return trimmed
}

func escapeTomlString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}
