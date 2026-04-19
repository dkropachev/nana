package gocli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildGithubLaneExecutionInstructionsCapsPromptBody(t *testing.T) {
	dir := t.TempDir()
	promptPath := filepath.Join(dir, "executor.md")
	if err := os.WriteFile(promptPath, []byte("START\n"+strings.Repeat("payload\n", 4000)+"END\n"), 0o644); err != nil {
		t.Fatalf("write prompt body: %v", err)
	}

	instructions, err := buildGithubLaneExecutionInstructions(githubWorkManifest{
		RunID:           "gh-1",
		RepoSlug:        "acme/widget",
		SandboxPath:     "/tmp/sandbox",
		SandboxRepoPath: "/tmp/sandbox/repo",
		LanePromptArtifacts: []githubLanePromptArtifact{{
			Alias:      "coder",
			Role:       "executor",
			PromptPath: promptPath,
		}},
	}, githubPipelineLane{
		Alias:   "coder",
		Role:    "executor",
		Phase:   "bootstrap",
		Mode:    "execute",
		Owner:   "self",
		Purpose: "Implement the requested change.",
	}, "implement it")
	if err != nil {
		t.Fatalf("buildGithubLaneExecutionInstructions: %v", err)
	}
	if !strings.Contains(instructions, "# NANA Work Lane") || !strings.Contains(instructions, "Caller task: implement it") {
		t.Fatalf("unexpected lane instructions:\n%s", instructions)
	}
	if !strings.Contains(instructions, "START") || strings.Contains(instructions, "END") {
		t.Fatalf("expected capped prompt body to preserve the start and truncate the tail:\n%s", instructions)
	}
	if len(instructions) > githubInstructionCharLimit {
		t.Fatalf("expected instructions to be capped at %d, got %d", githubInstructionCharLimit, len(instructions))
	}
}

func TestReadGithubEmbeddedPromptSurfaceUsesCompactVariantAndFallsBack(t *testing.T) {
	compact, err := readGithubEmbeddedPromptSurface("executor")
	if err != nil {
		t.Fatalf("read compact executor prompt: %v", err)
	}
	if !strings.Contains(compact, "Compact executor contract") {
		t.Fatalf("expected compact executor prompt, got:\n%s", compact)
	}

	fallback, err := readGithubEmbeddedPromptSurface("architect")
	if err != nil {
		t.Fatalf("read fallback architect prompt: %v", err)
	}
	if !strings.Contains(fallback, "Architect") {
		t.Fatalf("expected fallback full prompt, got:\n%s", fallback)
	}
}
