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
