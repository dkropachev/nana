package gocli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandReferenceDocsGeneratedFromHelpConstants(t *testing.T) {
	repoRoot := repoRootFromCaller(t)
	path := filepath.Join(repoRoot, "docs", "command-reference.html")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read docs/command-reference.html: %v", err)
	}
	expected := RenderCommandReferenceHTML()
	if string(content) != expected {
		t.Fatalf("docs/command-reference.html is stale; run go run ./cmd/nana-docs")
	}
	for _, section := range CommandReferenceSections() {
		if !strings.Contains(string(content), section.Source) {
			t.Fatalf("expected command reference to include source %q", section.Source)
		}
	}
}

func TestCommandReferenceIncludesTopLevelMoreHelpTopics(t *testing.T) {
	sectionsBySource := make(map[string]CommandReferenceSection)
	for _, section := range CommandReferenceSections() {
		sectionsBySource[section.Source] = section
	}

	for _, source := range topLevelMoreHelpTopics(t) {
		section, ok := sectionsBySource[source]
		if !ok {
			t.Fatalf("expected CommandReferenceSections to include advertised help topic %q", source)
		}
		if strings.TrimSpace(section.Help) == "" {
			t.Fatalf("expected advertised help topic %q to have non-empty help text", source)
		}
	}
}

func topLevelMoreHelpTopics(t *testing.T) []string {
	t.Helper()

	var topics []string
	inMoreHelp := false
	for _, line := range strings.Split(TopLevelHelp, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "More help:" {
			inMoreHelp = true
			continue
		}
		if !inMoreHelp || trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "nana help ") {
			break
		}
		topics = append(topics, trimmed)
	}
	if len(topics) == 0 {
		t.Fatal("expected TopLevelHelp to advertise at least one More help topic")
	}
	return topics
}

func TestDocsPrimaryNavLinksCLIReferenceAndWork(t *testing.T) {
	repoRoot := repoRootFromCaller(t)
	files := []string{
		"docs/index.html",
		"docs/getting-started.html",
		"docs/command-reference.html",
		"docs/agents.html",
		"docs/skills.html",
		"docs/integrations.html",
	}
	for _, relPath := range files {
		content, err := os.ReadFile(filepath.Join(repoRoot, relPath))
		if err != nil {
			t.Fatalf("read %s: %v", relPath, err)
		}
		text := string(content)
		for _, needle := range []string{
			`href="./command-reference.html">CLI Reference</a>`,
			`href="./work.md">Work</a>`,
		} {
			if !strings.Contains(text, needle) {
				t.Fatalf("expected %s nav to contain %q", relPath, needle)
			}
		}
	}
}
