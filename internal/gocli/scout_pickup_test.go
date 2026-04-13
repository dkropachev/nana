package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunLocalScoutDiscoveredItemsPicksOnePendingProposal(t *testing.T) {
	repo := createLocalWorkRepo(t)
	writeScoutPickupFixture(t, repo, improvementScoutRole, "Improve help text", "Make help clearer")
	oldRun := startRunLocalScoutWork
	tasks := []string{}
	startRunLocalScoutWork = func(repoPath string, task string, codexArgs []string) error {
		if repoPath != repo {
			t.Fatalf("unexpected repo path: %s", repoPath)
		}
		if strings.Join(codexArgs, " ") != "--model gpt-5.4" {
			t.Fatalf("unexpected codex args: %#v", codexArgs)
		}
		tasks = append(tasks, task)
		return nil
	}
	defer func() { startRunLocalScoutWork = oldRun }()

	picked := false
	output, err := captureStdout(t, func() error {
		var runErr error
		picked, runErr = runLocalScoutDiscoveredItems(repo, []string{"--model", "gpt-5.4"})
		return runErr
	})
	if err != nil {
		t.Fatalf("runLocalScoutDiscoveredItems: %v", err)
	}
	if !picked {
		t.Fatalf("expected proposal to be picked")
	}
	if len(tasks) != 1 || !strings.Contains(tasks[0], "Implement local scout proposal: Improve help text") || !strings.Contains(tasks[0], "Source artifact: .nana/improvements/improve-test") {
		t.Fatalf("unexpected tasks: %#v", tasks)
	}
	if !strings.Contains(output, "Local discovered items: 1 pending; working on: Improve help text") || !strings.Contains(output, "Local discovered item completed: Improve help text") {
		t.Fatalf("unexpected output: %q", output)
	}
	state, _, err := readLocalScoutPickupState(repo)
	if err != nil {
		t.Fatalf("read pickup state: %v", err)
	}
	if len(state.Items) != 1 {
		t.Fatalf("expected one state item, got %#v", state)
	}
	for _, item := range state.Items {
		if item.Status != "completed" || item.Title != "Improve help text" {
			t.Fatalf("unexpected state item: %#v", item)
		}
	}

	secondPicked := true
	secondOutput, err := captureStdout(t, func() error {
		var runErr error
		secondPicked, runErr = runLocalScoutDiscoveredItems(repo, nil)
		return runErr
	})
	if err != nil {
		t.Fatalf("second runLocalScoutDiscoveredItems: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("proposal should not be picked twice, tasks=%#v", tasks)
	}
	if secondPicked {
		t.Fatalf("expected second run to pick nothing")
	}
	if !strings.Contains(secondOutput, "Local discovered items: 0 pending (1 already picked).") {
		t.Fatalf("unexpected second output: %q", secondOutput)
	}
}

func TestRunLocalScoutDiscoveredItemsMarksFailureWithoutRetry(t *testing.T) {
	repo := createLocalWorkRepo(t)
	writeScoutPickupFixture(t, repo, enhancementScoutRole, "Add benchmark target", "Expose benchmarks")
	oldRun := startRunLocalScoutWork
	attempts := 0
	startRunLocalScoutWork = func(repoPath string, task string, codexArgs []string) error {
		attempts++
		return fmt.Errorf("work failed")
	}
	defer func() { startRunLocalScoutWork = oldRun }()

	picked := false
	output, err := captureStdout(t, func() error {
		var runErr error
		picked, runErr = runLocalScoutDiscoveredItems(repo, nil)
		return runErr
	})
	if err != nil {
		t.Fatalf("runLocalScoutDiscoveredItems: %v", err)
	}
	if attempts != 1 || !strings.Contains(output, "Local discovered item failed: Add benchmark target: work failed") {
		t.Fatalf("unexpected failure behavior attempts=%d output=%q", attempts, output)
	}
	if !picked {
		t.Fatalf("failed work still counts as picked for this cycle")
	}
	state, _, err := readLocalScoutPickupState(repo)
	if err != nil {
		t.Fatalf("read pickup state: %v", err)
	}
	for _, item := range state.Items {
		if item.Status != "failed" || item.Error != "work failed" {
			t.Fatalf("unexpected failed item: %#v", item)
		}
	}
	if _, err := captureStdout(t, func() error {
		_, runErr := runLocalScoutDiscoveredItems(repo, nil)
		return runErr
	}); err != nil {
		t.Fatalf("second runLocalScoutDiscoveredItems: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("failed proposal should not be retried automatically, attempts=%d", attempts)
	}
}

func writeScoutPickupFixture(t *testing.T, repo string, role string, title string, summary string) {
	t.Helper()
	root := "improve-test"
	if role == enhancementScoutRole {
		root = "enhance-test"
	}
	dir := filepath.Join(repo, ".nana", scoutArtifactRoot(role), root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	report := improvementReport{
		Version: 1,
		Repo:    filepath.Base(repo),
		Proposals: []improvementProposal{{
			Title:             title,
			Area:              "UX",
			Summary:           summary,
			Rationale:         "Users need this.",
			Evidence:          "README.md",
			Impact:            "Better workflow.",
			SuggestedNextStep: "Make the smallest change.",
			Files:             []string{"README.md"},
		}},
	}
	if err := writeGithubJSON(filepath.Join(dir, "proposals.json"), report); err != nil {
		t.Fatalf("write proposals: %v", err)
	}
}
