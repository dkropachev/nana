package gocliassets

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPromptAssetsStayInSyncWithPromptFiles(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	prompts, err := Prompts()
	if err != nil {
		t.Fatalf("Prompts(): %v", err)
	}
	for _, name := range []string{"investigator.md", "investigation-validator.md", "improvement-scout.md", "enhancement-scout.md", "ui-scout.md", "product-analyst.md"} {
		diskContent, err := os.ReadFile(filepath.Join(repoRoot, "prompts", name))
		if err != nil {
			t.Fatalf("read prompt %s: %v", name, err)
		}
		embedded, ok := prompts[name]
		if !ok {
			t.Fatalf("embedded prompts missing %s", name)
		}
		if embedded != string(diskContent) {
			t.Fatalf("embedded prompt %s is out of sync with prompts/%s", name, name)
		}
	}
}

func TestSkillAssetsStayInSyncWithSkillFiles(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skills, err := Skills()
	if err != nil {
		t.Fatalf("Skills(): %v", err)
	}
	for _, name := range []string{
		"ai-slop-cleaner/SKILL.md",
		"autopilot/SKILL.md",
		"deep-interview/SKILL.md",
		"pipeline/SKILL.md",
		"visual-verdict/SKILL.md",
		"web-clone/SKILL.md",
	} {
		diskContent, err := os.ReadFile(filepath.Join(repoRoot, "skills", filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read skill %s: %v", name, err)
		}
		embedded, ok := skills[name]
		if !ok {
			t.Fatalf("embedded skills missing %s", name)
		}
		if embedded != string(diskContent) {
			t.Fatalf("embedded skill %s is out of sync with skills/%s", name, name)
		}
	}
}

func TestTemplateAssetsStayInSyncWithTemplateFiles(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	templates, err := Templates()
	if err != nil {
		t.Fatalf("Templates(): %v", err)
	}
	diskContent, err := os.ReadFile(filepath.Join(repoRoot, "templates", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read template AGENTS.md: %v", err)
	}
	embedded, ok := templates["AGENTS.md"]
	if !ok {
		t.Fatalf("embedded templates missing AGENTS.md")
	}
	if embedded != string(diskContent) {
		t.Fatalf("embedded template AGENTS.md is out of sync with templates/AGENTS.md")
	}
}
