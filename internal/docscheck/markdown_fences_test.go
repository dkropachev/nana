package docscheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type markdownFence struct {
	char   byte
	length int
	line   int
}

func TestMarkdownFencesAreWellFormed(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	paths := markdownFiles(t, repoRoot)
	for _, path := range paths {
		path := path
		t.Run(filepath.ToSlash(path), func(t *testing.T) {
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read markdown file: %v", err)
			}
			checkMarkdownFences(t, path, string(content))
		})
	}
}

func markdownFiles(t *testing.T, repoRoot string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(repoRoot, "*.md"))
	if err != nil {
		t.Fatalf("glob top-level markdown files: %v", err)
	}
	docsRoot := filepath.Join(repoRoot, "docs")
	err = filepath.WalkDir(docsRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs markdown files: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no markdown files found")
	}
	return paths
}

func checkMarkdownFences(t *testing.T, path string, content string) {
	t.Helper()
	var open *markdownFence
	for lineNumber, line := range strings.Split(content, "\n") {
		lineNumber++
		char, length, suffix, ok := parseFenceCandidate(line)
		if !ok {
			continue
		}
		if open == nil {
			open = &markdownFence{char: char, length: length, line: lineNumber}
			continue
		}
		if char != open.char || length < open.length {
			continue
		}
		if strings.TrimSpace(suffix) == "" {
			open = nil
			continue
		}
		t.Fatalf("%s:%d: fence marker inside block opened at line %d is not a valid close: %q", path, lineNumber, open.line, line)
	}
	if open != nil {
		t.Fatalf("%s:%d: unclosed markdown fence", path, open.line)
	}
}

func parseFenceCandidate(line string) (char byte, length int, suffix string, ok bool) {
	spaces := 0
	for spaces < len(line) && line[spaces] == ' ' {
		spaces++
		if spaces > 3 {
			return 0, 0, "", false
		}
	}
	if len(line)-spaces < 3 {
		return 0, 0, "", false
	}
	char = line[spaces]
	if char != '`' && char != '~' {
		return 0, 0, "", false
	}
	for spaces+length < len(line) && line[spaces+length] == char {
		length++
	}
	if length < 3 {
		return 0, 0, "", false
	}
	return char, length, line[spaces+length:], true
}
