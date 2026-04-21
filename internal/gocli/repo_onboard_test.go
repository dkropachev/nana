package gocli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoOnboardWritesManagedVerificationPlanFromDetection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := createLocalWorkRepoAt(t, filepath.Join(home, "repo"))

	output, err := captureStdout(t, func() error {
		return Repo(repoRoot, []string{"onboard", "--repo", repoRoot})
	})
	if err != nil {
		t.Fatalf("Repo(onboard): %v", err)
	}

	planPath := managedVerificationPlanPathForRepoRoot(repoRoot)
	if _, err := os.Stat(planPath); err != nil {
		t.Fatalf("expected managed verification plan at %s: %v", planPath, err)
	}
	var plan managedVerificationPlan
	if err := readGithubJSON(planPath, &plan); err != nil {
		t.Fatalf("read managed verification plan: %v", err)
	}
	if plan.Name != "repo" {
		t.Fatalf("expected detected plan name to default to repo basename, got %+v", plan)
	}
	if plan.Description != "Canonical managed repository verification plan." {
		t.Fatalf("expected detected plan description, got %+v", plan)
	}
	if got := len(plan.Stages); got != 4 {
		t.Fatalf("expected four detected stages, got %+v", plan.Stages)
	}
	stageNames := []string{
		plan.Stages[0].Name,
		plan.Stages[1].Name,
		plan.Stages[2].Name,
		plan.Stages[3].Name,
	}
	if strings.Join(stageNames, ",") != "lint,compile,unit,integration" {
		t.Fatalf("unexpected detected stages: %+v", plan.Stages)
	}
	if len(plan.Lint) == 0 || len(plan.Compile) == 0 || len(plan.Unit) == 0 || len(plan.Integration) == 0 {
		t.Fatalf("expected detected categorized plan fields to be preserved, got %+v", plan)
	}
	if !strings.Contains(output, "[repo] Verification plan path: "+planPath) {
		t.Fatalf("expected onboarding output to report managed plan path, got %q", output)
	}
	if strings.Contains(output, "Imported legacy verification profile") {
		t.Fatalf("expected onboarding output to ignore repo-root legacy files, got %q", output)
	}
}

func TestVerifyUsesManagedVerificationPlanAfterOnboard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := createLocalWorkRepoAt(t, filepath.Join(home, "repo"))
	if err := Repo(repoRoot, []string{"onboard", "--repo", repoRoot}); err != nil {
		t.Fatalf("Repo(onboard): %v", err)
	}

	output, err := captureStdout(t, func() error { return Verify(repoRoot, nil) })
	if err != nil {
		t.Fatalf("Verify(--json): %v\n%s", err, output)
	}
	verifyLog, err := os.ReadFile(filepath.Join(repoRoot, "verify.log"))
	if err != nil {
		t.Fatalf("read verify.log: %v", err)
	}
	for _, line := range []string{"lint\n", "build\n", "test\n", "integration\n"} {
		if !strings.Contains(string(verifyLog), line) {
			t.Fatalf("expected verify to execute detected managed stages; log=%q missing=%q", string(verifyLog), line)
		}
	}
}
