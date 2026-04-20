package gocli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckManagedAssetFreshnessWarnsOnStaleInstallAndPassesWhenSynced(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex-home")
	if err := os.MkdirAll(filepath.Join(codexHome, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(codexHome, "skills", "plan"), 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "AGENTS.md"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale AGENTS: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "prompts", "executor.md"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale prompt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "skills", "plan", "SKILL.md"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale skill: %v", err)
	}

	agentsCheck := checkManagedAgentsFreshness("user", cwd, codexHome, repoRoot)
	promptCheck := checkManagedPromptFreshness("user", cwd, codexHome, repoRoot)
	skillCheck := checkManagedSkillFreshness("user", cwd, codexHome, repoRoot)
	if agentsCheck.Status != "warn" || promptCheck.Status != "warn" || skillCheck.Status != "warn" {
		t.Fatalf("expected stale warnings, got %+v %+v %+v", agentsCheck, promptCheck, skillCheck)
	}
	for name, check := range map[string]doctorCheck{
		"agents": agentsCheck,
		"prompt": promptCheck,
		"skill":  skillCheck,
	} {
		if strings.Contains(check.Message, "--force") {
			t.Fatalf("%s freshness should not recommend force by default: %#v", name, check)
		}
	}
	if agentsCheck.Remediation == nil || !strings.HasPrefix(agentsCheck.Remediation.SafeAutomaticFix, "no") {
		t.Fatalf("custom AGENTS freshness should require manual remediation, got %#v", agentsCheck.Remediation)
	}
	if promptCheck.Remediation == nil || !strings.Contains(promptCheck.Remediation.SafeAutomaticFix, "nana setup --scope user") {
		t.Fatalf("prompt freshness should use non-force setup remediation, got %#v", promptCheck.Remediation)
	}
	if skillCheck.Remediation == nil || !strings.Contains(skillCheck.Remediation.SafeAutomaticFix, "nana setup --scope user") {
		t.Fatalf("skill freshness should use non-force setup remediation, got %#v", skillCheck.Remediation)
	}

	expectedAgents, err := renderManagedAgentsContent(repoRoot, cwd, codexHome, filepath.Join(codexHome, "AGENTS.md"))
	if err != nil {
		t.Fatalf("renderManagedAgentsContent: %v", err)
	}
	expectedPrompts, err := readExpectedPromptAssets(repoRoot)
	if err != nil {
		t.Fatalf("readExpectedPromptAssets: %v", err)
	}
	expectedSkills, err := readExpectedSkillAssets(repoRoot)
	if err != nil {
		t.Fatalf("readExpectedSkillAssets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "AGENTS.md"), []byte(expectedAgents), 0o644); err != nil {
		t.Fatalf("write expected AGENTS: %v", err)
	}
	for rel, content := range expectedPrompts {
		path := filepath.Join(codexHome, "prompts", rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir prompt dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write prompt %s: %v", rel, err)
		}
	}
	for rel, content := range expectedSkills {
		path := filepath.Join(codexHome, "skills", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir skill dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write skill %s: %v", rel, err)
		}
	}

	agentsCheck = checkManagedAgentsFreshness("user", cwd, codexHome, repoRoot)
	promptCheck = checkManagedPromptFreshness("user", cwd, codexHome, repoRoot)
	skillCheck = checkManagedSkillFreshness("user", cwd, codexHome, repoRoot)
	if agentsCheck.Status != "pass" || promptCheck.Status != "pass" || skillCheck.Status != "pass" {
		t.Fatalf("expected up-to-date checks, got %+v %+v %+v", agentsCheck, promptCheck, skillCheck)
	}
}

func TestCheckManagedAgentsFreshnessOnlyRecommendsForceForSetupGeneratedAgents(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex-home")
	agentsPath := filepath.Join(codexHome, "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(agentsPath), 0o755); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	if err := os.WriteFile(agentsPath, []byte("# custom global instructions\n"), 0o644); err != nil {
		t.Fatalf("write custom AGENTS: %v", err)
	}

	customCheck := checkManagedAgentsFreshness("user", cwd, codexHome, repoRoot)
	if customCheck.Status != "warn" {
		t.Fatalf("expected custom AGENTS freshness warning, got %#v", customCheck)
	}
	if strings.Contains(customCheck.Message, "--force") {
		t.Fatalf("custom AGENTS freshness must not recommend --force: %#v", customCheck)
	}
	if customCheck.Remediation == nil || !strings.HasPrefix(customCheck.Remediation.SafeAutomaticFix, "no") {
		t.Fatalf("custom AGENTS should be manual-only remediation, got %#v", customCheck.Remediation)
	}
	if !strings.Contains(customCheck.Remediation.ManualFallback, "back up and merge custom instructions") {
		t.Fatalf("custom AGENTS remediation should require preserving custom content, got %#v", customCheck.Remediation)
	}

	if err := os.WriteFile(agentsPath, []byte("<!-- nana:generated:agents-md -->\nstale generated content\n"), 0o644); err != nil {
		t.Fatalf("write generated AGENTS: %v", err)
	}
	generatedCheck := checkManagedAgentsFreshness("user", cwd, codexHome, repoRoot)
	if generatedCheck.Status != "warn" {
		t.Fatalf("expected generated AGENTS freshness warning, got %#v", generatedCheck)
	}
	if !strings.Contains(generatedCheck.Message, "nana setup --force --scope user") {
		t.Fatalf("setup-generated AGENTS can be force-refreshed safely, got %#v", generatedCheck)
	}
	if generatedCheck.Remediation == nil || !strings.Contains(generatedCheck.Remediation.SafeAutomaticFix, "nana setup --force --scope user") {
		t.Fatalf("generated AGENTS should include force remediation, got %#v", generatedCheck.Remediation)
	}
}

func TestPromptAndSkillFreshnessPathConflictsRequireManualRemediation(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	cwd := t.TempDir()

	promptCodexHome := filepath.Join(t.TempDir(), ".codex-home-prompts")
	promptRoot := filepath.Join(promptCodexHome, "prompts")
	if err := os.MkdirAll(promptCodexHome, 0o755); err != nil {
		t.Fatalf("mkdir prompt codex home: %v", err)
	}
	if err := os.WriteFile(promptRoot, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatalf("write prompt path conflict: %v", err)
	}
	promptCheck := checkManagedPromptFreshness("user", cwd, promptCodexHome, repoRoot)
	if promptCheck.Status != "warn" || promptCheck.Remediation == nil {
		t.Fatalf("expected prompt conflict warning with remediation, got %#v", promptCheck)
	}
	if !strings.Contains(promptCheck.Message, "path conflict") {
		t.Fatalf("expected prompt conflict cause, got %#v", promptCheck)
	}
	if promptCheck.Remediation.Path != promptRoot {
		t.Fatalf("expected prompt conflict path %s, got %#v", promptRoot, promptCheck.Remediation)
	}
	if !strings.HasPrefix(promptCheck.Remediation.SafeAutomaticFix, "no") {
		t.Fatalf("prompt path conflict must not be marked safe automatic, got %#v", promptCheck.Remediation)
	}

	promptFileCodexHome := filepath.Join(t.TempDir(), ".codex-home-prompt-file")
	promptFileConflictPath := filepath.Join(promptFileCodexHome, "prompts", "executor.md")
	if err := os.MkdirAll(promptFileConflictPath, 0o755); err != nil {
		t.Fatalf("create prompt file path conflict: %v", err)
	}
	promptFileCheck := checkManagedPromptFreshness("user", cwd, promptFileCodexHome, repoRoot)
	if promptFileCheck.Status != "warn" || promptFileCheck.Remediation == nil {
		t.Fatalf("expected prompt file conflict warning with remediation, got %#v", promptFileCheck)
	}
	if promptFileCheck.Remediation.Path != promptFileConflictPath {
		t.Fatalf("expected prompt file conflict path %s, got %#v", promptFileConflictPath, promptFileCheck.Remediation)
	}
	if !strings.HasPrefix(promptFileCheck.Remediation.SafeAutomaticFix, "no") {
		t.Fatalf("prompt file path conflict must not be marked safe automatic, got %#v", promptFileCheck.Remediation)
	}

	skillCodexHome := filepath.Join(t.TempDir(), ".codex-home-skills")
	skillConflictPath := filepath.Join(skillCodexHome, "skills", "plan")
	if err := os.MkdirAll(filepath.Dir(skillConflictPath), 0o755); err != nil {
		t.Fatalf("mkdir skill codex home: %v", err)
	}
	if err := os.WriteFile(skillConflictPath, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatalf("write skill path conflict: %v", err)
	}
	skillCheck := checkManagedSkillFreshness("user", cwd, skillCodexHome, repoRoot)
	if skillCheck.Status != "warn" || skillCheck.Remediation == nil {
		t.Fatalf("expected skill conflict warning with remediation, got %#v", skillCheck)
	}
	if !strings.Contains(skillCheck.Message, "path conflict") {
		t.Fatalf("expected skill conflict cause, got %#v", skillCheck)
	}
	if skillCheck.Remediation.Path != skillConflictPath {
		t.Fatalf("expected skill conflict path %s, got %#v", skillConflictPath, skillCheck.Remediation)
	}
	if !strings.HasPrefix(skillCheck.Remediation.SafeAutomaticFix, "no") {
		t.Fatalf("skill path conflict must not be marked safe automatic, got %#v", skillCheck.Remediation)
	}
}

func TestMaybeWarnManagedAssetDriftRecommendsForceForSetupGeneratedProjectAgents(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	cwd := t.TempDir()
	codexHome := filepath.Join(cwd, ".codex")
	writeSetupScope(t, cwd, "project")
	writeExpectedPromptsAndSkills(t, repoRoot, codexHome)
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("<!-- nana:generated:agents-md -->\nstale generated project AGENTS\n"), 0o644); err != nil {
		t.Fatalf("write stale generated AGENTS: %v", err)
	}

	_, stderr, err := captureOutput(t, func() error {
		maybeWarnManagedAssetDrift(cwd, codexHome, repoRoot)
		return nil
	})
	if err != nil {
		t.Fatalf("capture warning: %v", err)
	}
	if !strings.Contains(stderr, "installed AGENTS.md look stale") {
		t.Fatalf("expected AGENTS-only drift warning, got stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "`nana setup --force --scope project`") {
		t.Fatalf("project setup-generated AGENTS warning should recommend force refresh, got stderr=%q", stderr)
	}
}

func TestMaybeWarnManagedAssetDriftKeepsCustomProjectAgentsManual(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	cwd := t.TempDir()
	codexHome := filepath.Join(cwd, ".codex")
	writeSetupScope(t, cwd, "project")
	writeExpectedPromptsAndSkills(t, repoRoot, codexHome)
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("# custom project instructions\n"), 0o644); err != nil {
		t.Fatalf("write custom AGENTS: %v", err)
	}

	_, stderr, err := captureOutput(t, func() error {
		maybeWarnManagedAssetDrift(cwd, codexHome, repoRoot)
		return nil
	})
	if err != nil {
		t.Fatalf("capture warning: %v", err)
	}
	if !strings.Contains(stderr, "installed AGENTS.md look stale") {
		t.Fatalf("expected AGENTS-only drift warning, got stderr=%q", stderr)
	}
	if strings.Contains(stderr, "`nana setup --scope project`") || strings.Contains(stderr, "`nana setup --force --scope project`") {
		t.Fatalf("custom project AGENTS warning must not present setup as a complete refresh, got stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "custom AGENTS.md") || !strings.Contains(stderr, "manual merge") || !strings.Contains(stderr, "nana doctor") {
		t.Fatalf("custom project AGENTS warning should direct manual remediation, got stderr=%q", stderr)
	}
}

func TestMaybeWarnManagedAssetDriftWarnsOncePerFingerprint(t *testing.T) {
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	fakeBin := filepath.Join(cwd, "bin")
	codexHome := DefaultUserCodexHome(home)
	if err := os.MkdirAll(filepath.Join(codexHome, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(codexHome, "skills", "plan"), 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "AGENTS.md"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale AGENTS: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "prompts", "executor.md"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale prompt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "skills", "plan", "SKILL.md"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale skill: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), "#!/bin/sh\nprintf 'fake-codex:%s\\n' \"$*\"\n")

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	repoRoot := filepath.Join("..", "..")
	stdout, stderr, err := captureOutput(t, func() error {
		maybeWarnManagedAssetDrift(cwd, codexHome, repoRoot)
		return Exec(cwd, []string{"say hi"})
	})
	if err != nil {
		t.Fatalf("Exec(): %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	if !strings.Contains(stderr, "installed AGENTS.md, prompts, skills look stale") {
		t.Fatalf("expected stale asset warning, got stderr=%q", stderr)
	}
	if strings.Contains(stderr, "--force") {
		t.Fatalf("one-time asset drift warning should not recommend --force by default, got stderr=%q", stderr)
	}

	_, secondStderr, err := captureOutput(t, func() error {
		maybeWarnManagedAssetDrift(cwd, codexHome, repoRoot)
		return Exec(cwd, []string{"say hi"})
	})
	if err != nil {
		t.Fatalf("Exec() second run: %v stderr=%q", err, secondStderr)
	}
	if strings.Contains(secondStderr, "look stale") {
		t.Fatalf("expected one-time warning, got stderr=%q", secondStderr)
	}
}

func writeSetupScope(t *testing.T, cwd string, scope string) {
	t.Helper()
	stateDir := filepath.Join(cwd, ".nana")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "setup-scope.json"), []byte(`{"scope":"`+scope+`"}`), 0o644); err != nil {
		t.Fatalf("write setup scope: %v", err)
	}
}

func writeExpectedPromptsAndSkills(t *testing.T, repoRoot string, codexHome string) {
	t.Helper()
	expectedPrompts, err := readExpectedPromptAssets(repoRoot)
	if err != nil {
		t.Fatalf("readExpectedPromptAssets: %v", err)
	}
	for rel, content := range expectedPrompts {
		path := filepath.Join(codexHome, "prompts", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir prompt dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write prompt %s: %v", rel, err)
		}
	}
	expectedSkills, err := readExpectedSkillAssets(repoRoot)
	if err != nil {
		t.Fatalf("readExpectedSkillAssets: %v", err)
	}
	for rel, content := range expectedSkills {
		path := filepath.Join(codexHome, "skills", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir skill dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write skill %s: %v", rel, err)
		}
	}
}
