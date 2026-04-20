package gocliassets

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	for _, name := range []string{"executor.md", "team-executor.md", "investigator.md", "investigation-validator.md", "improvement-scout.md", "enhancement-scout.md", "ui-scout.md", "product-analyst.md"} {
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

func TestPrimaryPromptAssetsStayWithinBudget(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	budgets := map[string]int{
		"executor.md":          4096,
		"critic.md":            4096,
		"explore.md":           4096,
		"test-engineer.md":     4096,
		"security-reviewer.md": 4096,
		"quality-reviewer.md":  4096,
		"architect.md":         3072,
	}
	for name, budget := range budgets {
		content, err := os.ReadFile(filepath.Join(repoRoot, "prompts", name))
		if err != nil {
			t.Fatalf("read prompt %s: %v", name, err)
		}
		if len(content) > budget {
			t.Fatalf("prompt %s exceeds budget: %d > %d", name, len(content), budget)
		}
	}
}

func TestCompactEmbeddedPromptsStayInSyncAndWithinBudget(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	prompts, err := Prompts()
	if err != nil {
		t.Fatalf("Prompts(): %v", err)
	}
	for _, name := range []string{
		"executor-embedded.md",
		"critic-embedded.md",
		"test-engineer-embedded.md",
		"quality-reviewer-embedded.md",
		"security-reviewer-embedded.md",
		"qa-tester-embedded.md",
	} {
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
		if len(diskContent) > 3072 {
			t.Fatalf("compact prompt %s exceeds budget: %d", name, len(diskContent))
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

func TestRuntimeSkillAssetsStayInSyncAndWithinBudget(t *testing.T) {
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
		"autopilot/RUNTIME.md",
		"ultrawork/RUNTIME.md",
		"analyze/RUNTIME.md",
		"plan/RUNTIME.md",
		"deep-interview/RUNTIME.md",
		"ralplan/RUNTIME.md",
		"ecomode/RUNTIME.md",
		"cancel/RUNTIME.md",
		"tdd/RUNTIME.md",
		"build-fix/RUNTIME.md",
		"code-review/RUNTIME.md",
		"security-review/RUNTIME.md",
		"web-clone/RUNTIME.md",
	} {
		diskContent, err := os.ReadFile(filepath.Join(repoRoot, "skills", filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read runtime skill %s: %v", name, err)
		}
		embedded, ok := skills[name]
		if !ok {
			t.Fatalf("embedded runtime skill missing %s", name)
		}
		if embedded != string(diskContent) {
			t.Fatalf("embedded runtime skill %s is out of sync", name)
		}
		if len(diskContent) > 3072 {
			t.Fatalf("runtime skill %s exceeds budget: %d", name, len(diskContent))
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
	if len(diskContent) > 8192 {
		t.Fatalf("template AGENTS.md exceeds budget: %d", len(diskContent))
	}
	rootAgents, err := os.ReadFile(filepath.Join(repoRoot, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read root AGENTS.md: %v", err)
	}
	if len(rootAgents) > 8192 {
		t.Fatalf("root AGENTS.md exceeds budget: %d", len(rootAgents))
	}
	for _, needle := range []string{
		"`~/.codex/skills/autopilot/RUNTIME.md`",
		"`~/.codex/skills/deep-interview/RUNTIME.md`",
		"`~/.codex/skills/security-review/RUNTIME.md`",
		"`~/.codex/skills/web-clone/RUNTIME.md`",
	} {
		if !strings.Contains(string(diskContent), needle) {
			t.Fatalf("template AGENTS.md missing runtime skill reference %q", needle)
		}
		if !strings.Contains(string(rootAgents), strings.ReplaceAll(needle, "~/.codex", "./.codex")) {
			t.Fatalf("root AGENTS.md missing runtime skill reference %q", needle)
		}
	}
}
