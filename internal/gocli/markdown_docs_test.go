package gocli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarkdownFencesAreBalanced(t *testing.T) {
	repoRoot := repoRootFromCaller(t)
	var failures []string

	walkErr := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".nana", "dist", "node_modules", "target":
				if path != repoRoot {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if strings.ToLower(filepath.Ext(path)) != ".md" {
			return nil
		}

		relPath := strings.TrimPrefix(path, repoRoot+string(filepath.Separator))
		fileFailures, err := markdownFenceFailures(path, relPath)
		if err != nil {
			return err
		}
		failures = append(failures, fileFailures...)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk markdown files: %v", walkErr)
	}
	if len(failures) > 0 {
		t.Fatalf("markdown fence issues:\n%s", strings.Join(failures, "\n"))
	}
}

type markdownFenceState struct {
	char   byte
	length int
	line   int
}

func markdownFenceFailures(path, relPath string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", relPath, err)
	}

	var failures []string
	var open *markdownFenceState
	for index, line := range strings.Split(string(content), "\n") {
		lineNumber := index + 1
		char, length, rest, ok := markdownFenceCandidate(line)
		if !ok {
			continue
		}
		if open == nil {
			open = &markdownFenceState{char: char, length: length, line: lineNumber}
			continue
		}
		if char != open.char || length < open.length {
			continue
		}
		if strings.TrimSpace(rest) == "" {
			open = nil
			continue
		}
		failures = append(failures, fmt.Sprintf(
			"%s:%d: found %q before closing %c-fence opened at line %d; use a longer outer fence for literal fenced examples",
			relPath,
			lineNumber,
			strings.TrimSpace(line),
			open.char,
			open.line,
		))
	}
	if open != nil {
		failures = append(failures, fmt.Sprintf("%s:%d: unclosed %c-fence", relPath, open.line, open.char))
	}
	return failures, nil
}

func markdownFenceCandidate(line string) (byte, int, string, bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 {
		return 0, 0, "", false
	}
	char := trimmed[0]
	if char != '`' && char != '~' {
		return 0, 0, "", false
	}
	length := 0
	for length < len(trimmed) && trimmed[length] == char {
		length++
	}
	if length < 3 {
		return 0, 0, "", false
	}
	return char, length, trimmed[length:], true
}
