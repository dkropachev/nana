package gocli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLaunchLocalWorkDetachedRunnerInheritsDBProxyEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on windows")
	}
	_ = setLocalWorkDBProxyTestHome(t)

	repoRoot := t.TempDir()
	supervisor, err := launchLocalWorkDBProxySupervisor()
	if err != nil {
		t.Fatalf("launchLocalWorkDBProxySupervisor: %v", err)
	}
	defer supervisor.Close()
	logPath := filepath.Join(t.TempDir(), "runtime.log")
	started := false
	setStartManagedNanaStartForTest(t, func(cmd *exec.Cmd) error {
		started = true
		assertStartManagedNanaLaunchUsesSocketPresence(t, cmd)
		return nil
	})

	if err := launchLocalWorkDetachedRunner(repoRoot, "run-1", nil, logPath); err != nil {
		t.Fatalf("launchLocalWorkDetachedRunner: %v", err)
	}
	if !started {
		t.Fatalf("expected managed child launch to start")
	}
}

func TestDetectGithubVerificationPlanForGoUsesCheckOnlyLint(t *testing.T) {
	repo := t.TempDir()
	writeFile := func(path string, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	writeFile("go.mod", "module example.com/worklocal\n\ngo 1.24\n")
	writeFile("main.go", "package main\n\nfunc main() {}\n")

	plan := detectGithubVerificationPlan(repo)
	if len(plan.Lint) < 2 {
		t.Fatalf("expected go lint commands, got %#v", plan.Lint)
	}
	if plan.Lint[0] != `test -z "$(gofmt -l .)"` {
		t.Fatalf("expected check-only gofmt lint, got %#v", plan.Lint)
	}
	if len(plan.Compile) != 1 || plan.Compile[0] != "go test -run '^$' ./..." {
		t.Fatalf("expected compile-only go test command, got %#v", plan.Compile)
	}
	if len(plan.Unit) != 1 || plan.Unit[0] != "go test ./..." {
		t.Fatalf("expected full go test unit command, got %#v", plan.Unit)
	}
	for _, command := range plan.Lint {
		if strings.Contains(command, "-w") {
			t.Fatalf("lint command should not rewrite files: %#v", plan.Lint)
		}
	}
}

func TestDetectGithubVerificationPlanPrefersSplitUnitIntegrationAndBenchmarks(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "Makefile"), []byte(strings.Join([]string{
		"lint:",
		"\t@true",
		"build:",
		"\t@true",
		"test:",
		"\t@echo mixed",
		"test-unit:",
		"\t@echo unit",
		"test-integration:",
		"\t@echo integration",
		"test-benchmark:",
		"\t@echo benchmark",
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}

	plan := detectGithubVerificationPlan(repo)
	if len(plan.Unit) != 1 || plan.Unit[0] != "make test-unit" {
		t.Fatalf("expected split unit target, got %#v", plan.Unit)
	}
	if len(plan.Integration) != 1 || plan.Integration[0] != "make test-integration" {
		t.Fatalf("expected integration target, got %#v", plan.Integration)
	}
	if len(plan.Benchmarks) != 1 || plan.Benchmarks[0] != "make test-benchmark" {
		t.Fatalf("expected benchmark target, got %#v", plan.Benchmarks)
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("did not expect warnings for properly split targets, got %#v", plan.Warnings)
	}
}

func TestDetectGithubVerificationPlanWarnsWhenUnitAndIntegrationAreMixed(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "Makefile"), []byte(strings.Join([]string{
		"test:",
		"\t@echo mixed",
		"test-integration:",
		"\t@echo integration",
		"test-benchmark-jmh:",
		"\t@echo bench",
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}

	plan := detectGithubVerificationPlan(repo)
	if len(plan.Unit) != 1 || plan.Unit[0] != "make test" {
		t.Fatalf("expected mixed test target fallback, got %#v", plan.Unit)
	}
	if len(plan.Integration) != 1 || plan.Integration[0] != "make test-integration" {
		t.Fatalf("expected integration target, got %#v", plan.Integration)
	}
	if len(plan.Warnings) < 2 {
		t.Fatalf("expected split and benchmark warnings, got %#v", plan.Warnings)
	}
}

func TestBuildLocalWorkImplementPromptCapsReviewTitlesAndPlan(t *testing.T) {
	repo := t.TempDir()
	inputPath := filepath.Join(repo, "plan.md")
	if err := os.WriteFile(inputPath, []byte(strings.Repeat("plan-line\n", 4000)), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	titles := []string{}
	for index := 1; index <= 12; index++ {
		titles = append(titles, fmt.Sprintf("review item %02d", index))
	}
	prompt, err := buildLocalWorkImplementPrompt(localWorkManifest{
		RunID:             "lw-1",
		RepoRoot:          repo,
		SandboxRepoPath:   repo,
		BaselineSHA:       "abc123",
		SourceBranch:      "feature/test",
		MaxIterations:     4,
		IntegrationPolicy: "final",
		InputPath:         inputPath,
		Iterations: []localWorkIterationSummary{{
			VerificationSummary: "failed",
			ReviewFindings:      12,
			ReviewFindingTitles: titles,
		}},
	}, 2)
	if err != nil {
		t.Fatalf("buildLocalWorkImplementPrompt: %v", err)
	}
	if strings.Contains(prompt, "review item 11") || strings.Contains(prompt, "review item 12") {
		t.Fatalf("expected review titles to be capped:\n%s", prompt)
	}
	for _, needle := range []string{"... 2 additional review items omitted", "... [truncated]"} {
		if !strings.Contains(prompt, needle) {
			t.Fatalf("expected implement prompt to contain %q:\n%s", needle, prompt)
		}
	}
	if len(prompt) > localWorkImplementPromptCharLimit+128 {
		t.Fatalf("expected implement prompt to stay near cap, len=%d", len(prompt))
	}
}

func TestBuildLocalWorkHardeningPromptCapsFindingsAndCommandOutput(t *testing.T) {
	findings := make([]githubPullReviewFinding, 0, 8)
	for index := 0; index < 8; index++ {
		findings = append(findings, githubPullReviewFinding{
			Title:    fmt.Sprintf("finding %d", index),
			Severity: "medium",
			Path:     "README.md",
			Line:     index + 1,
			Detail:   strings.Repeat("detail ", 300),
			Fix:      strings.Repeat("fix ", 150),
		})
	}
	prompt, err := buildLocalWorkHardeningPrompt(localWorkManifest{
		RunID:             "lw-2",
		RepoRoot:          t.TempDir(),
		SandboxRepoPath:   t.TempDir(),
		BaselineSHA:       "def456",
		IntegrationPolicy: "final",
		CurrentIteration:  1,
	}, localWorkVerificationReport{
		Passed: false,
		Stages: []localWorkVerificationStageResult{{
			Name:   "unit",
			Status: "failed",
			Commands: []localWorkVerificationCommandResult{{
				Command:  "go test ./...",
				ExitCode: 1,
				Output:   strings.Repeat("output ", 500),
			}},
		}},
	}, findings)
	if err != nil {
		t.Fatalf("buildLocalWorkHardeningPrompt: %v", err)
	}
	if strings.Contains(prompt, "finding 6") || strings.Contains(prompt, "finding 7") {
		t.Fatalf("expected hardening findings to be capped:\n%s", prompt)
	}
	for _, needle := range []string{"additional findings omitted for brevity", "... [truncated]"} {
		if !strings.Contains(prompt, needle) {
			t.Fatalf("expected hardening prompt to contain %q:\n%s", needle, prompt)
		}
	}
	if len(prompt) > localWorkHardeningPromptCharLimit+128 {
		t.Fatalf("expected hardening prompt to stay near cap, len=%d", len(prompt))
	}
}

func TestReadWorkRunIndexRetriesWhenDatabaseIsLocked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := writeWorkRunIndex(workRunIndexEntry{
		RunID:        "locked-run",
		Backend:      "github",
		RepoKey:      "acme/widget",
		RepoSlug:     "acme/widget",
		ManifestPath: filepath.Join(home, "manifest.json"),
		UpdatedAt:    ISOTimeNow(),
		TargetKind:   "issue",
	}); err != nil {
		t.Fatalf("writeWorkRunIndex: %v", err)
	}

	oldOpen := localWorkOpenReadStore
	oldSleep := localWorkRetrySleep
	attempts := 0
	localWorkOpenReadStore = func() (*localWorkDBStore, error) {
		attempts++
		if attempts == 1 {
			return nil, fmt.Errorf("database is locked")
		}
		return openLocalWorkReadDB()
	}
	localWorkRetrySleep = func(time.Duration) {}
	defer func() {
		localWorkOpenReadStore = oldOpen
		localWorkRetrySleep = oldSleep
	}()

	entry, err := readWorkRunIndex("locked-run")
	if err != nil {
		t.Fatalf("readWorkRunIndex: %v", err)
	}
	if entry.RunID != "locked-run" || entry.RepoSlug != "acme/widget" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if attempts != 2 {
		t.Fatalf("expected one retry before success, got %d attempts", attempts)
	}
}

func TestCleanupStaleLocalWorkRunsForRepoMarksOrphanedRunsFailed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoRoot := createLocalWorkRepoAt(t, filepath.Join(home, "repo"))
	oldUpdatedAt := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)

	manifest := localWorkManifest{
		Version:      1,
		RunID:        "lw-stale",
		CreatedAt:    oldUpdatedAt,
		UpdatedAt:    oldUpdatedAt,
		Status:       "running",
		CurrentPhase: "implement",
		RepoRoot:     repoRoot,
		RepoName:     filepath.Base(repoRoot),
		RepoID:       localWorkRepoID(repoRoot),
		SourceBranch: "main",
		BaselineSHA:  strings.TrimSpace(runLocalWorkTestGitOutput(t, repoRoot, "rev-parse", "HEAD")),
		SandboxPath:  filepath.Join(home, "sandboxes", "lw-stale"),
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}

	oldSnapshot := localWorkProcessSnapshot
	localWorkProcessSnapshot = func() (string, error) { return "", nil }
	defer func() { localWorkProcessSnapshot = oldSnapshot }()

	cleaned, err := cleanupStaleLocalWorkRunsForRepo(repoRoot)
	if err != nil {
		t.Fatalf("cleanupStaleLocalWorkRunsForRepo: %v", err)
	}
	if cleaned != 1 {
		t.Fatalf("expected one cleaned run, got %d", cleaned)
	}

	updated, err := readLocalWorkManifestByRunID("lw-stale")
	if err != nil {
		t.Fatalf("readLocalWorkManifestByRunID: %v", err)
	}
	if updated.Status != "failed" {
		t.Fatalf("expected failed status, got %+v", updated)
	}
	if updated.CompletedAt == "" || !strings.Contains(updated.LastError, "stale running run cleaned up at start") {
		t.Fatalf("expected stale cleanup markers, got %+v", updated)
	}
}

func TestCleanupStaleLocalWorkRunsForRepoPreservesLiveRuns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoRoot := createLocalWorkRepoAt(t, filepath.Join(home, "repo"))
	oldUpdatedAt := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)

	manifest := localWorkManifest{
		Version:         1,
		RunID:           "lw-live",
		CreatedAt:       oldUpdatedAt,
		UpdatedAt:       oldUpdatedAt,
		Status:          "running",
		CurrentPhase:    "implement",
		RepoRoot:        repoRoot,
		RepoName:        filepath.Base(repoRoot),
		RepoID:          localWorkRepoID(repoRoot),
		SourceBranch:    "main",
		BaselineSHA:     strings.TrimSpace(runLocalWorkTestGitOutput(t, repoRoot, "rev-parse", "HEAD")),
		SandboxPath:     filepath.Join(home, "sandboxes", "lw-live"),
		SandboxRepoPath: filepath.Join(home, "sandboxes", "lw-live", "repo"),
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}

	oldSnapshot := localWorkProcessSnapshot
	localWorkProcessSnapshot = func() (string, error) {
		return "123 codex exec -C " + manifest.SandboxRepoPath + " " + manifest.RunID, nil
	}
	defer func() { localWorkProcessSnapshot = oldSnapshot }()

	cleaned, err := cleanupStaleLocalWorkRunsForRepo(repoRoot)
	if err != nil {
		t.Fatalf("cleanupStaleLocalWorkRunsForRepo: %v", err)
	}
	if cleaned != 0 {
		t.Fatalf("expected no cleaned runs, got %d", cleaned)
	}

	updated, err := readLocalWorkManifestByRunID("lw-live")
	if err != nil {
		t.Fatalf("readLocalWorkManifestByRunID: %v", err)
	}
	if updated.Status != "running" {
		t.Fatalf("expected running status, got %+v", updated)
	}
}

func TestCleanupStaleLocalWorkRunsForRepoPreservesDetachedResumeProcess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoRoot := createLocalWorkRepoAt(t, filepath.Join(home, "repo"))
	oldUpdatedAt := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)

	manifest := localWorkManifest{
		Version:         1,
		RunID:           "lw-detached",
		CreatedAt:       oldUpdatedAt,
		UpdatedAt:       oldUpdatedAt,
		Status:          "running",
		CurrentPhase:    "verify",
		RepoRoot:        repoRoot,
		RepoName:        filepath.Base(repoRoot),
		RepoID:          localWorkRepoID(repoRoot),
		SourceBranch:    "main",
		BaselineSHA:     strings.TrimSpace(runLocalWorkTestGitOutput(t, repoRoot, "rev-parse", "HEAD")),
		SandboxPath:     filepath.Join(home, "sandboxes", "lw-detached"),
		SandboxRepoPath: filepath.Join(home, "sandboxes", "lw-detached", "repo"),
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}

	oldSnapshot := localWorkProcessSnapshot
	localWorkProcessSnapshot = func() (string, error) {
		return "321 /tmp/nana work resume --run-id " + manifest.RunID + " --repo " + manifest.RepoRoot, nil
	}
	defer func() { localWorkProcessSnapshot = oldSnapshot }()

	cleaned, err := cleanupStaleLocalWorkRunsForRepo(repoRoot)
	if err != nil {
		t.Fatalf("cleanupStaleLocalWorkRunsForRepo: %v", err)
	}
	if cleaned != 0 {
		t.Fatalf("expected no cleaned runs, got %d", cleaned)
	}

	updated, err := readLocalWorkManifestByRunID(manifest.RunID)
	if err != nil {
		t.Fatalf("readLocalWorkManifestByRunID: %v", err)
	}
	if updated.Status != "running" {
		t.Fatalf("expected running status, got %+v", updated)
	}
}

func TestCleanupStaleLocalWorkRunsIgnoresNonWorkerProcessMentions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoRoot := createLocalWorkRepoAt(t, filepath.Join(home, "repo"))
	oldUpdatedAt := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)

	manifest := localWorkManifest{
		Version:         1,
		RunID:           "lw-shell-hit",
		CreatedAt:       oldUpdatedAt,
		UpdatedAt:       oldUpdatedAt,
		Status:          "running",
		CurrentPhase:    "review",
		RepoRoot:        repoRoot,
		RepoName:        filepath.Base(repoRoot),
		RepoID:          localWorkRepoID(repoRoot),
		SourceBranch:    "main",
		BaselineSHA:     strings.TrimSpace(runLocalWorkTestGitOutput(t, repoRoot, "rev-parse", "HEAD")),
		SandboxPath:     filepath.Join(home, "sandboxes", "lw-shell-hit"),
		SandboxRepoPath: filepath.Join(home, "sandboxes", "lw-shell-hit", "repo"),
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}

	oldSnapshot := localWorkProcessSnapshot
	localWorkProcessSnapshot = func() (string, error) {
		return "999 bash -c grep " + manifest.RunID + " " + manifest.SandboxPath + " " + manifest.SandboxRepoPath, nil
	}
	defer func() { localWorkProcessSnapshot = oldSnapshot }()

	cleaned, err := cleanupStaleLocalWorkRunsForRepo(repoRoot)
	if err != nil {
		t.Fatalf("cleanupStaleLocalWorkRunsForRepo: %v", err)
	}
	if cleaned != 1 {
		t.Fatalf("expected stale run to be cleaned despite shell mention, got %d", cleaned)
	}

	updated, err := readLocalWorkManifestByRunID("lw-shell-hit")
	if err != nil {
		t.Fatalf("readLocalWorkManifestByRunID: %v", err)
	}
	if updated.Status != "failed" {
		t.Fatalf("expected failed status, got %+v", updated)
	}
}

func TestPersistUnexpectedLocalWorkFailureMarksRunningRunFailed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoRoot := createLocalWorkRepoAt(t, filepath.Join(home, "repo"))
	now := time.Now().UTC().Format(time.RFC3339)

	manifest := localWorkManifest{
		Version:      1,
		RunID:        "lw-unexpected",
		CreatedAt:    now,
		UpdatedAt:    now,
		Status:       "running",
		CurrentPhase: "review",
		RepoRoot:     repoRoot,
		RepoName:     filepath.Base(repoRoot),
		RepoID:       localWorkRepoID(repoRoot),
		SourceBranch: "main",
		BaselineSHA:  strings.TrimSpace(runLocalWorkTestGitOutput(t, repoRoot, "rev-parse", "HEAD")),
		SandboxPath:  filepath.Join(home, "sandboxes", "lw-unexpected"),
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}

	if err := persistUnexpectedLocalWorkFailure(manifest.RunID, fmt.Errorf("database is locked")); err != nil {
		t.Fatalf("persistUnexpectedLocalWorkFailure: %v", err)
	}

	updated, err := readLocalWorkManifestByRunID(manifest.RunID)
	if err != nil {
		t.Fatalf("readLocalWorkManifestByRunID: %v", err)
	}
	if updated.Status != "failed" {
		t.Fatalf("expected failed status, got %+v", updated)
	}
	if updated.LastError != "database is locked" {
		t.Fatalf("expected last error to be preserved, got %+v", updated)
	}
	if updated.CompletedAt == "" {
		t.Fatalf("expected completed_at to be set, got %+v", updated)
	}
}

func TestRepoOnboardPrintsAutoOnboardingGuidanceAndSplit(t *testing.T) {
	repo := createLocalWorkRepoAt(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(repo, "Makefile"), []byte(strings.Join([]string{
		"lint:",
		"\t@true",
		"build:",
		"\t@true",
		"test-unit:",
		"\t@true",
		"test-integration:",
		"\t@true",
		"test-benchmark:",
		"\t@true",
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return Repo(repo, []string{"onboard"})
	})
	if err != nil {
		t.Fatalf("Repo(onboard): %v", err)
	}
	if strings.Contains(output, "Usually this happens automatically") || strings.Contains(output, "Run this manually when you changed build/test targets") {
		t.Fatalf("expected onboarding guidance to live only in help output, got %q", output)
	}
	if !strings.Contains(output, "Unit: make test-unit") || !strings.Contains(output, "Integration: make test-integration") || !strings.Contains(output, "Benchmark: make test-benchmark") {
		t.Fatalf("missing split plan in output: %q", output)
	}
	if !strings.Contains(output, "Warnings: (none)") {
		t.Fatalf("expected no warnings in output: %q", output)
	}
}

func TestRepoOnboardPrintsWarningsForMixedTargets(t *testing.T) {
	repo := createLocalWorkRepoAt(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(repo, "Makefile"), []byte(strings.Join([]string{
		"test:",
		"\t@true",
		"test-integration:",
		"\t@true",
		"test-benchmark-jmh:",
		"\t@true",
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return Repo(repo, []string{"onboard"})
	})
	if err != nil {
		t.Fatalf("Repo(onboard mixed): %v", err)
	}
	if !strings.Contains(output, "Split unit and integration") || !strings.Contains(output, "benchmark entrypoint") {
		t.Fatalf("expected onboarding warnings in output: %q", output)
	}
}

func TestRepoOnboardJSONIncludesRepoProfile(t *testing.T) {
	repo := createLocalWorkRepoAt(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(repo, ".github"), 0o755); err != nil {
		t.Fatalf("mkdir .github: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".github", "PULL_REQUEST_TEMPLATE.md"), []byte("## Summary\n\n## Validation\n"), 0o644); err != nil {
		t.Fatalf("write pr template: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return Repo(repo, []string{"onboard", "--json"})
	})
	if err != nil {
		t.Fatalf("Repo(onboard --json): %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("unmarshal onboard json: %v\n%s", err, output)
	}
	repoProfile, ok := payload["repo_profile"].(map[string]any)
	if !ok {
		t.Fatalf("expected repo_profile in payload: %#v", payload)
	}
	if strings.TrimSpace(repoProfile["fingerprint"].(string)) == "" {
		t.Fatalf("expected repo profile fingerprint: %#v", repoProfile)
	}
	template, ok := repoProfile["pull_request_template"].(map[string]any)
	if !ok || template["path"] != ".github/PULL_REQUEST_TEMPLATE.md" {
		t.Fatalf("expected pr template profile, got %#v", repoProfile["pull_request_template"])
	}
}

func TestBuildFindingGroupsFromGroupingResultAcceptsAISplit(t *testing.T) {
	findings := []githubPullReviewFinding{
		{Fingerprint: "a", Path: "migrator/src/main/Foo.scala"},
		{Fingerprint: "b", Path: "tests/src/test/Foo.scala"},
		{Fingerprint: "c", Path: "docker-compose-tests.yml"},
	}
	grouped, err := buildFindingGroupsFromGroupingResult(findings, localWorkGroupingResult{
		Groups: []localWorkGroupingGroup{
			{GroupID: "config-and-validation", Findings: []string{"a", "c"}},
			{GroupID: "tests", Findings: []string{"b"}},
		},
	})
	if err != nil {
		t.Fatalf("buildFindingGroupsFromGroupingResult: %v", err)
	}
	if len(grouped) != 2 || grouped[0].GroupID != "config-and-validation" || grouped[1].GroupID != "tests" {
		t.Fatalf("unexpected grouped output: %#v", grouped)
	}
}

func TestBuildFindingGroupsFromGroupingResultRejectsMissingOrDuplicateFindings(t *testing.T) {
	findings := []githubPullReviewFinding{
		{Fingerprint: "a", Path: "migrator/src/main/Foo.scala"},
		{Fingerprint: "b", Path: "tests/src/test/Foo.scala"},
	}
	if _, err := buildFindingGroupsFromGroupingResult(findings, localWorkGroupingResult{
		Groups: []localWorkGroupingGroup{{GroupID: "one", Findings: []string{"a"}}},
	}); err == nil || !strings.Contains(err.Error(), "omitted finding") {
		t.Fatalf("expected omitted finding error, got %v", err)
	}
	if _, err := buildFindingGroupsFromGroupingResult(findings, localWorkGroupingResult{
		Groups: []localWorkGroupingGroup{
			{GroupID: "one", Findings: []string{"a"}},
			{GroupID: "two", Findings: []string{"a", "b"}},
		},
	}); err == nil || !strings.Contains(err.Error(), "multiple groups") {
		t.Fatalf("expected duplicate assignment error, got %v", err)
	}
}

func TestFilterRejectedFindingsDropsRejectedFingerprints(t *testing.T) {
	findings := []githubPullReviewFinding{
		{Fingerprint: "keep", Title: "keep"},
		{Fingerprint: "drop", Title: "drop"},
	}
	filtered := filterRejectedFindings(findings, []string{"drop"})
	if len(filtered) != 1 || filtered[0].Fingerprint != "keep" {
		t.Fatalf("unexpected filtered findings: %#v", filtered)
	}
}

func TestFilterKnownFindingsSeparatesRejectedAndPreexistingFingerprints(t *testing.T) {
	findings := []githubPullReviewFinding{
		{Fingerprint: "keep", Title: "keep"},
		{Fingerprint: "rejected", Title: "rejected"},
		{Fingerprint: "preexisting", Title: "preexisting"},
	}
	filtered := filterKnownFindings(findings, []string{"rejected"}, []string{"preexisting"})
	if len(filtered.Findings) != 1 || filtered.Findings[0].Fingerprint != "keep" {
		t.Fatalf("unexpected filtered findings: %#v", filtered)
	}
	if filtered.SkippedRejected != 1 || filtered.SkippedPreexisting != 1 {
		t.Fatalf("unexpected skipped counts: %#v", filtered)
	}
}

func TestFindingsFromValidatedReplacesModifiedFindings(t *testing.T) {
	original := githubPullReviewFinding{Fingerprint: "old", Title: "old", Path: "migrator/file.scala", Summary: "s", Detail: "d"}
	replacement := githubPullReviewFinding{Fingerprint: "new", Title: "new", Path: "migrator/file.scala", Summary: "s2", Detail: "d2"}
	validated := []localWorkValidatedFinding{
		{
			GroupID:               "migrator",
			OriginalFingerprint:   "old",
			CurrentFingerprint:    "old",
			Status:                localWorkFindingSuperseded,
			SupersedesFingerprint: "new",
			Finding:               &original,
		},
		{
			GroupID:             "migrator",
			OriginalFingerprint: "old",
			CurrentFingerprint:  "new",
			Status:              localWorkFindingModified,
			Finding:             &replacement,
		},
	}
	result := findingsFromValidated(validated)
	if len(result) != 1 || result[0].Fingerprint != "new" {
		t.Fatalf("expected replacement finding only, got %#v", result)
	}
}

func TestRunLocalVerificationDedupesDuplicateCommands(t *testing.T) {
	repo := t.TempDir()
	logPath := filepath.Join(repo, "verify.log")
	if err := os.WriteFile(filepath.Join(repo, "count.sh"), []byte("#!/bin/sh\nprintf 'hit\\n' >> verify.log\n"), 0o755); err != nil {
		t.Fatalf("write count.sh: %v", err)
	}

	report, err := runLocalVerification(repo, githubVerificationPlan{
		PlanFingerprint: "dup",
		Compile:         []string{"./count.sh"},
		Unit:            []string{"./count.sh"},
	}, false)
	if err != nil {
		t.Fatalf("runLocalVerification: %v", err)
	}
	if !report.Passed {
		t.Fatalf("expected verification to pass: %#v", report)
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read verify.log: %v", err)
	}
	if got := strings.Count(string(content), "hit"); got != 1 {
		t.Fatalf("expected duplicate command to run once, got %d hits in %q", got, content)
	}
	if len(report.Stages) < 3 || len(report.Stages[2].Commands) == 0 || !report.Stages[2].Commands[0].Cached {
		t.Fatalf("expected unit stage to reuse cached result, got %#v", report.Stages)
	}
}

func TestRunLocalVerificationClearsInheritedGOFLAGSSoTestsCannotBeSkipped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX Makefile recipe syntax")
	}
	if _, err := exec.LookPath("make"); err != nil {
		t.Skipf("make not available: %v", err)
	}
	repo := t.TempDir()
	files := map[string]string{
		"go.mod":   "module example.com/localverifyflags\n\ngo 1.20\n",
		"Makefile": "test:\n\tgo test ./...\n",
		"verify_flags_test.go": `package localverifyflags

import "testing"

func TestLocalVerificationRunsTestBodies(t *testing.T) {
	t.Fatal("intentional failure proves the test body ran")
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	t.Setenv("GOFLAGS", "-run=^$")

	report, err := runLocalVerification(repo, githubVerificationPlan{
		PlanFingerprint: "env-preserve",
		Unit:            []string{"make test"},
	}, false)
	if err != nil {
		t.Fatalf("runLocalVerification: %v", err)
	}
	if report.Passed {
		t.Fatalf("expected failing test body to run despite inherited GOFLAGS=-run=^$, got %#v", report)
	}
	if len(report.Stages) < 3 || report.Stages[2].Status != "failed" {
		t.Fatalf("expected unit stage to fail, got %#v", report.Stages)
	}
	if len(report.Stages[2].Commands) == 0 {
		t.Fatalf("expected unit command result, got %#v", report.Stages)
	}
	if !strings.Contains(report.Stages[2].Commands[0].Output, "intentional failure proves the test body ran") {
		t.Fatalf("expected output to show the failing test body ran, got %#v", report.Stages[2].Commands[0])
	}
}

func TestRunLocalVerificationClearsInheritedMakeFlagsSoRecipesStillRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX Makefile recipe syntax")
	}
	if _, err := exec.LookPath("make"); err != nil {
		t.Skipf("make not available: %v", err)
	}
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "Makefile"), []byte("verify-marker:\n\t@printf 'ran' > marker.txt\n"), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}
	t.Setenv("MAKEFLAGS", "-n")
	t.Setenv("MFLAGS", "-n")
	t.Setenv("GNUMAKEFLAGS", "-n")
	t.Setenv("MAKEFILES", "missing-injected.mk")

	report, err := runLocalVerification(repo, githubVerificationPlan{
		PlanFingerprint: "make-sanitize",
		Unit:            []string{"make verify-marker"},
	}, false)
	if err != nil {
		t.Fatalf("runLocalVerification: %v", err)
	}
	if !report.Passed {
		t.Fatalf("expected local verification to clear inherited make dry-run flags, got %#v", report)
	}
	content, err := os.ReadFile(filepath.Join(repo, "marker.txt"))
	if err != nil {
		t.Fatalf("expected make recipe to execute despite inherited dry-run flags: %v; report=%#v", err, report)
	}
	if strings.TrimSpace(string(content)) != "ran" {
		t.Fatalf("unexpected marker content: %q", string(content))
	}
}

func TestNormalizeLocalWorkCodexArgsDefaultsToBypass(t *testing.T) {
	got := normalizeLocalWorkCodexArgs(nil)
	if len(got) == 0 || got[0] != CodexBypassFlag {
		t.Fatalf("expected default bypass flag, got %#v", got)
	}

	got = normalizeLocalWorkCodexArgs([]string{"--sandbox=workspace-write", "--model", "gpt-5.4"})
	if got[0] == CodexBypassFlag {
		t.Fatalf("did not expect bypass when sandbox policy already specified: %#v", got)
	}
}

func TestReadLocalWorkInputRejectsMissingPlanFile(t *testing.T) {
	cwd := t.TempDir()
	_, _, err := readLocalWorkInput(cwd, localWorkStartOptions{PlanFile: "TODO.md"}, "feature/test")
	if err == nil || !strings.Contains(err.Error(), "plan file not found:") {
		t.Fatalf("expected explicit missing plan file error, got %v", err)
	}
}

func TestParseLocalWorkStartArgsAllowsBranchTaskInference(t *testing.T) {
	options, err := parseLocalWorkStartArgs([]string{"--repo", ".", "--work-type", workTypeFeature, "--max-iterations", "1"})
	if err != nil {
		t.Fatalf("parseLocalWorkStartArgs: %v", err)
	}
	if strings.TrimSpace(options.Task) != "" || strings.TrimSpace(options.PlanFile) != "" {
		t.Fatalf("expected task and plan file to stay empty for inference, got %#v", options)
	}

	_, err = parseLocalWorkStartArgs([]string{"--task", "one", "--plan-file", "plan.md"})
	if err == nil || !strings.Contains(err.Error(), "Specify at most one of --task or --plan-file") {
		t.Fatalf("expected at-most-one validation, got %v", err)
	}
}

func TestParseLocalWorkStartArgsSupportsDetach(t *testing.T) {
	options, err := parseLocalWorkStartArgs([]string{"--detach", "--repo", ".", "--task", "ship it", "--work-type", workTypeFeature})
	if err != nil {
		t.Fatalf("parseLocalWorkStartArgs: %v", err)
	}
	if !options.Detach {
		t.Fatalf("expected detach option to be set, got %#v", options)
	}
}

func TestStartLocalWorkWithRunIDDetachSpawnsBackgroundRunner(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoRoot := createLocalWorkRepoAt(t, filepath.Join(home, "repo"))

	oldDetachedRunner := localWorkStartDetachedRunner
	oldExecuteLoop := localWorkExecuteLoop
	detachedCalls := 0
	executeCalls := 0
	detachedLogPath := ""
	localWorkStartDetachedRunner = func(repoPath string, runID string, codexArgs []string, logPath string) error {
		detachedCalls++
		detachedLogPath = logPath
		if repoPath != repoRoot {
			t.Fatalf("unexpected detached repo path: %s", repoPath)
		}
		if strings.TrimSpace(runID) == "" {
			t.Fatalf("expected detached runner run id")
		}
		if len(codexArgs) != 0 {
			t.Fatalf("unexpected detached codex args: %#v", codexArgs)
		}
		return nil
	}
	localWorkExecuteLoop = func(runID string, codexArgs []string, rateLimitPolicy codexRateLimitPolicy) error {
		executeCalls++
		return nil
	}
	defer func() {
		localWorkStartDetachedRunner = oldDetachedRunner
		localWorkExecuteLoop = oldExecuteLoop
	}()

	runID, err := startLocalWorkWithRunID(repoRoot, localWorkStartOptions{
		Detach:                true,
		RepoPath:              repoRoot,
		Task:                  "Implement detached local work launch",
		WorkType:              workTypeFeature,
		MaxIterations:         1,
		IntegrationPolicy:     "final",
		GroupingPolicy:        localWorkDefaultGroupingPolicy,
		ValidationParallelism: localWorkValidationParallelism,
	})
	if err != nil {
		t.Fatalf("startLocalWorkWithRunID(detach): %v", err)
	}
	if detachedCalls != 1 {
		t.Fatalf("expected one detached runner launch, got %d", detachedCalls)
	}
	if executeCalls != 0 {
		t.Fatalf("expected execute loop to be skipped for detach, got %d calls", executeCalls)
	}
	if !strings.HasSuffix(detachedLogPath, filepath.Join("runs", runID, "runtime.log")) {
		t.Fatalf("unexpected detached log path: %s", detachedLogPath)
	}
	manifest, err := readLocalWorkManifestByRunID(runID)
	if err != nil {
		t.Fatalf("readLocalWorkManifestByRunID(%s): %v", runID, err)
	}
	if manifest.Status != "running" || manifest.CurrentPhase != "bootstrap" {
		t.Fatalf("expected detached manifest to remain running/bootstrap, got %+v", manifest)
	}
}

func TestStartLocalWorkWithRunIDKeepsBaselineAlignedWithSandboxHead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoRoot := createLocalWorkRepoAt(t, filepath.Join(home, "repo"))

	oldDetachedRunner := localWorkStartDetachedRunner
	localWorkStartDetachedRunner = func(repoPath string, runID string, codexArgs []string, logPath string) error {
		return nil
	}
	defer func() {
		localWorkStartDetachedRunner = oldDetachedRunner
	}()

	runID, err := startLocalWorkWithRunID(repoRoot, localWorkStartOptions{
		Detach:                true,
		RepoPath:              repoRoot,
		Task:                  "Keep baseline aligned with sandbox",
		WorkType:              workTypeFeature,
		MaxIterations:         1,
		IntegrationPolicy:     "final",
		GroupingPolicy:        localWorkDefaultGroupingPolicy,
		ValidationParallelism: localWorkValidationParallelism,
	})
	if err != nil {
		t.Fatalf("startLocalWorkWithRunID: %v", err)
	}
	manifest := mustLocalWorkManifestByRunID(t, runID)
	sandboxHead := strings.TrimSpace(runLocalWorkTestGitOutput(t, manifest.SandboxRepoPath, "rev-parse", "HEAD"))
	if sandboxHead != strings.TrimSpace(manifest.BaselineSHA) {
		t.Fatalf("expected sandbox head to match manifest baseline, sandbox=%q baseline=%q", sandboxHead, manifest.BaselineSHA)
	}
}

func TestInferLocalWorkTaskFromBranch(t *testing.T) {
	task, err := inferLocalWorkTaskFromBranch("feature/add-branch-task-inference")
	if err != nil {
		t.Fatalf("inferLocalWorkTaskFromBranch: %v", err)
	}
	if !strings.Contains(task, `local branch "feature/add-branch-task-inference"`) || !strings.Contains(task, "add branch task inference") {
		t.Fatalf("unexpected inferred task: %q", task)
	}

	for _, branch := range []string{"main", "master", "HEAD", ""} {
		_, err := inferLocalWorkTaskFromBranch(branch)
		if err == nil || !strings.Contains(err.Error(), "provide --task yourself because inference failed") {
			t.Fatalf("expected inference failure for branch %q, got %v", branch, err)
		}
	}
}

func TestReadLocalWorkInputInfersTaskFromBranch(t *testing.T) {
	content, mode, err := readLocalWorkInput(t.TempDir(), localWorkStartOptions{}, "fix/parser-edge-case")
	if err != nil {
		t.Fatalf("readLocalWorkInput inferred branch: %v", err)
	}
	if mode != "inferred-branch" {
		t.Fatalf("expected inferred-branch mode, got %q", mode)
	}
	if !strings.Contains(content, "parser edge case") {
		t.Fatalf("expected inferred task content, got %q", content)
	}
}

func TestLocalWorkStartInfersTaskFromCurrentBranch(t *testing.T) {
	repo := createLocalWorkRepo(t)
	cmd := exec.Command("git", "switch", "-c", "feature/add-branch-task-inference")
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git switch branch failed: %v\n%s", err, output)
	}

	home := t.TempDir()
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		`PAYLOAD="$(cat) $*"`,
		`case "$PAYLOAD" in`,
		`  *"Review this local implementation and return JSON only."*)`,
		`    printf '{"findings":[]}\n'`,
		`    ;;`,
		`  *)`,
		`    printf 'fake-codex:%s\n' "$*"`,
		`    ;;`,
		"esac",
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	if err := runLocalWorkCommand(repo, []string{"start", "--work-type", workTypeFeature, "--max-iterations", "1"}); err != nil {
		t.Fatalf("runLocalWorkCommand inferred start: %v", err)
	}
	manifest, _ := mustLatestLocalWorkRun(t, repo)
	if manifest.InputMode != "inferred-branch" {
		t.Fatalf("expected inferred input mode, got %q", manifest.InputMode)
	}
	if manifest.SourceBranch != "feature/add-branch-task-inference" {
		t.Fatalf("expected source branch to be recorded, got %q", manifest.SourceBranch)
	}
	content, err := os.ReadFile(manifest.InputPath)
	if err != nil {
		t.Fatalf("read input path: %v", err)
	}
	if !strings.Contains(string(content), "add branch task inference") {
		t.Fatalf("expected inferred task in input file, got %q", content)
	}
}

func TestBuildLocalWorkHardeningPromptTruncatesLargeFailureOutput(t *testing.T) {
	manifest := localWorkManifest{
		RunID:             "lw-test",
		RepoRoot:          "/repo",
		SandboxRepoPath:   "/repo-sandbox",
		BaselineSHA:       "abc123",
		IntegrationPolicy: "final",
		CurrentIteration:  1,
	}
	huge := strings.Repeat("x", localWorkPromptSnippetChars*2)
	prompt, err := buildLocalWorkHardeningPrompt(manifest, localWorkVerificationReport{
		Passed: false,
		Stages: []localWorkVerificationStageResult{{
			Name:   "unit",
			Status: "failed",
			Commands: []localWorkVerificationCommandResult{{
				Command:  "make test",
				ExitCode: 2,
				Output:   huge,
			}},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("buildLocalWorkHardeningPrompt: %v", err)
	}
	if len(prompt) > localWorkPromptCharLimit+64 {
		t.Fatalf("expected prompt to stay bounded, len=%d", len(prompt))
	}
	if !strings.Contains(prompt, "[truncated]") {
		t.Fatalf("expected truncation marker in prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "Avoid rerunning full integration/container-heavy checks manually") {
		t.Fatalf("expected early-iteration integration guidance, got %q", prompt)
	}
}

func TestLocalWorkRejectsDirtyRepo(t *testing.T) {
	repo := createLocalWorkRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	err := runLocalWorkCommand(repo, []string{"start", "--work-type", workTypeFeature, "--task", "do it"})
	if err == nil || !strings.Contains(err.Error(), "clean repo") {
		t.Fatalf("expected clean repo error, got %v", err)
	}
}

func TestLocalWorkStartStatusRetrospectiveAndGlobalRunLookup(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`PAYLOAD="$(cat)"`,
		`case "$PAYLOAD" in`,
		`  *"Review this local implementation and return JSON only."*)`,
		`    printf '{"findings":[]}\n'`,
		`    ;;`,
		`  *)`,
		`    printf 'implemented\n' >> "$NANA_PROJECT_AGENTS_ROOT/README.md"`,
		`    printf 'fake-codex:%s\n' "$*"`,
		`    ;;`,
		"esac",
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	startOutput, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"start", "--work-type", workTypeFeature, "--task", "Update the local docs flow"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(start): %v\n%s", err, startOutput)
	}
	if !strings.Contains(startOutput, "Starting run lw-") || !strings.Contains(startOutput, "Completed run lw-") {
		t.Fatalf("unexpected start output: %q", startOutput)
	}
	if !strings.Contains(startOutput, "benchmark=0") {
		t.Fatalf("expected benchmark count in onboarding output, got %q", startOutput)
	}
	if _, err := os.Stat(filepath.Join(repo, ".nana", "work")); !os.IsNotExist(err) {
		t.Fatalf("expected source repo to stay free of work runtime artifacts, got err=%v", err)
	}

	manifest, runDir := mustLatestLocalWorkRun(t, repo)
	if manifest.Status != "completed" {
		t.Fatalf("expected completed manifest, got %#v", manifest)
	}
	if manifest.FinalApplyStatus != "committed" || strings.TrimSpace(manifest.FinalApplyCommitSHA) == "" {
		t.Fatalf("expected committed final apply, got %#v", manifest)
	}
	sourceHead, err := githubGitOutput(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("read source HEAD: %v", err)
	}
	if strings.TrimSpace(sourceHead) != manifest.FinalApplyCommitSHA || strings.TrimSpace(sourceHead) == manifest.BaselineSHA {
		t.Fatalf("expected source HEAD to advance to final apply commit, head=%q manifest=%#v", sourceHead, manifest)
	}
	commitSubject, err := githubGitOutput(repo, "log", "-1", "--pretty=%s")
	if err != nil {
		t.Fatalf("read commit subject: %v", err)
	}
	if strings.TrimSpace(commitSubject) != "nana work: apply "+manifest.RunID {
		t.Fatalf("unexpected final apply commit subject %q", commitSubject)
	}
	sourceStatus, err := githubGitOutput(repo, "status", "--porcelain")
	if err != nil {
		t.Fatalf("read source status: %v", err)
	}
	if strings.TrimSpace(sourceStatus) != "" {
		t.Fatalf("expected clean source checkout after final commit, got %q", sourceStatus)
	}
	if _, err := os.Stat(filepath.Join(repo, "verify.log")); !os.IsNotExist(err) {
		t.Fatalf("expected sandbox verification artifact to stay out of source checkout, err=%v", err)
	}
	committedFiles, err := githubGitOutput(repo, "show", "--name-only", "--pretty=", "HEAD")
	if err != nil {
		t.Fatalf("read committed files: %v", err)
	}
	if strings.Contains(committedFiles, "verify.log") {
		t.Fatalf("expected final commit to exclude verification artifact, got %q", committedFiles)
	}
	if len(manifest.Iterations) != 1 {
		t.Fatalf("expected single iteration, got %#v", manifest.Iterations)
	}
	if _, err := os.Stat(filepath.Join(localWorkRepoDir(repo), "retrospective.md")); err == nil {
		t.Fatalf("retrospective should live inside the run directory, not repo root")
	}
	logContent, err := os.ReadFile(filepath.Join(manifest.SandboxRepoPath, "verify.log"))
	if err != nil {
		t.Fatalf("read verify.log: %v", err)
	}
	for _, marker := range []string{"lint", "build", "test", "integration"} {
		if !strings.Contains(string(logContent), marker) {
			t.Fatalf("expected %s in verification log %q", marker, logContent)
		}
	}

	outside := t.TempDir()
	statusOutput, err := captureStdout(t, func() error {
		return runLocalWorkCommand(outside, []string{"status", "--run-id", manifest.RunID})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(status --run-id): %v", err)
	}
	if !strings.Contains(statusOutput, "Status: completed") || !strings.Contains(statusOutput, "Run artifacts: "+runDir) {
		t.Fatalf("unexpected status output: %q", statusOutput)
	}

	repoScopedStatus, err := captureStdout(t, func() error {
		return runLocalWorkCommand(outside, []string{"status", "--repo", repo, "--last"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(status --repo --last): %v", err)
	}
	if !strings.Contains(repoScopedStatus, manifest.RunID) {
		t.Fatalf("expected repo-scoped last run in output, got %q", repoScopedStatus)
	}

	globalStatus, err := captureStdout(t, func() error {
		return runLocalWorkCommand(outside, []string{"status", "--global-last"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(status --global-last): %v", err)
	}
	if !strings.Contains(globalStatus, manifest.RunID) {
		t.Fatalf("expected global last run in output, got %q", globalStatus)
	}

	retroOutput, err := captureStdout(t, func() error {
		return runLocalWorkCommand(outside, []string{"retrospective", "--run-id", manifest.RunID})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(retrospective): %v", err)
	}
	if !strings.Contains(retroOutput, "# NANA Work-local Retrospective") {
		t.Fatalf("unexpected retrospective output: %q", retroOutput)
	}
	for _, needle := range []string{
		"## Changed files",
		"## Verification evidence",
		"## Simplifications made",
		"## Remaining risks",
		"## routing_decision",
		"## Report quality checklist",
	} {
		if !strings.Contains(retroOutput, needle) {
			t.Fatalf("retrospective missing report-quality section %q:\n%s", needle, retroOutput)
		}
	}
	if _, err := os.Stat(filepath.Join(runDir, "retrospective.md")); err != nil {
		t.Fatalf("expected retrospective artifact: %v", err)
	}

	logsOutput, err := captureStdout(t, func() error {
		return runLocalWorkCommand(outside, []string{"logs", "--run-id", manifest.RunID, "--tail", "20"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(logs): %v", err)
	}
	if !strings.Contains(logsOutput, "== implement-stdout.log ==") || !strings.Contains(logsOutput, "fake-codex:exec -C") || !strings.Contains(logsOutput, CodexBypassFlag) {
		t.Fatalf("unexpected logs output: %q", logsOutput)
	}
}

func TestLocalWorkRunsHardeningPassWhenReviewFindingsRemain(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	markerPath := filepath.Join(home, "hardening.marker")
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`PAYLOAD="$(cat)"`,
		`case "$PAYLOAD" in`,
		`  *"# NANA Work-local Finding Grouping"*)`,
		`    printf '{"groups":[{"group_id":"migrator/src","rationale":"shared context","findings":["readme.md|need stronger regression coverage|1|add regression"]}]}\n'`,
		`    ;;`,
		`  *"# NANA Work-local Finding Validation"*)`,
		`    printf '{"group":"migrator/src","decisions":[{"fingerprint":"readme.md|need stronger regression coverage|1|add regression","status":"confirmed","reason":"valid finding"}]}\n'`,
		`    ;;`,
		`  *"# NANA Work-local Hardening Pass"*)`,
		`    printf 'fixed\n' >> README.md`,
		`    : > "${FAKE_CODEX_HARDENED_PATH}"`,
		`    printf 'hardening-complete\n'`,
		`    ;;`,
		`  *"Review this local implementation and return JSON only."*)`,
		`    if [ -f "${FAKE_CODEX_HARDENED_PATH}" ]; then`,
		`      printf '{"findings":[]}\n'`,
		`    else`,
		`      printf '{"findings":[{"title":"Need stronger regression coverage","severity":"medium","path":"README.md","line":1,"summary":"add regression","detail":"detail","fix":"fix","rationale":"why"}]}\n'`,
		`    fi`,
		`    ;;`,
		`  *)`,
		`    printf 'implemented\n' >> README.md`,
		`    printf 'fake-codex:%s\n' "$*"`,
		`    ;;`,
		"esac",
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_HARDENED_PATH", markerPath)

	output, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"start", "--work-type", workTypeFeature, "--task", "Trigger the hardening pass"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(start): %v\n%s", err, output)
	}
	manifest, runDir := mustLatestLocalWorkRun(t, repo)
	iterationDir := localWorkIterationDir(runDir, 1)
	for _, path := range []string{
		"review-initial-findings.json",
		"hardening-round-1-prompt.md",
		"hardening-round-1-stdout.log",
		"verification-round-1-post-hardening.json",
		"review-round-1-findings.json",
	} {
		if _, err := os.Stat(filepath.Join(iterationDir, path)); err != nil {
			t.Fatalf("expected hardening artifact %s: %v", path, err)
		}
	}
	if len(manifest.Iterations) != 1 {
		t.Fatalf("unexpected hardening iteration summary: %#v", manifest.Iterations)
	}
	summary := manifest.Iterations[0]
	if summary.InitialReviewFindings == 0 || summary.ReviewFindings != 0 || summary.ReviewRoundsUsed != 1 {
		t.Fatalf("unexpected hardening iteration summary: %#v", summary)
	}
	if len(summary.ReviewFindingsByRound) != 1 || len(summary.HardeningRoundFingerprints) != 1 || len(summary.PostHardeningVerificationFingerprints) != 1 {
		t.Fatalf("expected round metadata in summary: %#v", summary)
	}
}

func TestLocalWorkFinalReviewGateBlocksCompletionUntilHardened(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	finalGateHardenedPath := filepath.Join(home, "final-gate-hardened.marker")
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`PAYLOAD="$(cat)"`,
		`case "$PAYLOAD" in`,
		`  *"# NANA Work-local Finding Grouping"*)`,
		`    printf '{"groups":[{"group_id":"quality-final-gate","rationale":"quality final gate","findings":["readme.md|quality final gate found missing regression|1|add regression"]}]}\n'`,
		`    ;;`,
		`  *"# NANA Work-local Finding Validation"*)`,
		`    printf '{"group":"quality-final-gate","decisions":[{"fingerprint":"readme.md|quality final gate found missing regression|1|add regression","status":"confirmed","reason":"valid final gate finding"}]}\n'`,
		`    ;;`,
		`  *"# NANA Work-local Hardening Pass"*)`,
		`    : > "$FAKE_FINAL_GATE_HARDENED_PATH"`,
		`    printf 'final gate fixed\n' >> "$NANA_PROJECT_AGENTS_ROOT/README.md"`,
		`    printf 'hardening-complete\n'`,
		`    ;;`,
		`  *"Review role: quality-reviewer"*)`,
		`    if [ -f "$FAKE_FINAL_GATE_HARDENED_PATH" ]; then`,
		`      printf '{"findings":[]}\n'`,
		`    else`,
		`      printf '{"findings":[{"title":"Quality final gate found missing regression","severity":"medium","path":"README.md","line":1,"summary":"add regression","detail":"detail","fix":"fix","rationale":"why"}]}\n'`,
		`    fi`,
		`    ;;`,
		`  *"Review role: security-reviewer"*|*"Review role: performance-reviewer"*|*"Review role: qa-tester"*)`,
		`    printf '{"findings":[]}\n'`,
		`    ;;`,
		`  *"Review this local implementation and return JSON only."*)`,
		`    printf '{"findings":[]}\n'`,
		`    ;;`,
		`  *)`,
		`    printf 'implemented\n' >> "$NANA_PROJECT_AGENTS_ROOT/README.md"`,
		`    printf 'fake-codex:%s\n' "$*"`,
		`    ;;`,
		"esac",
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_FINAL_GATE_HARDENED_PATH", finalGateHardenedPath)

	output, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"start", "--work-type", workTypeFeature, "--task", "Trigger final gate hardening"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(start): %v\n%s", err, output)
	}
	manifest, runDir := mustLatestLocalWorkRun(t, repo)
	if manifest.Status != "completed" || manifest.FinalApplyStatus != "committed" {
		t.Fatalf("expected completed committed run, got %#v", manifest)
	}
	if len(manifest.Iterations) != 1 {
		t.Fatalf("unexpected iterations: %#v", manifest.Iterations)
	}
	summary := manifest.Iterations[0]
	if summary.FinalGateFindings != 1 || len(summary.FinalGateRoles) != 1 || summary.FinalGateRoles[0] != "quality-reviewer" || summary.ReviewRoundsUsed != 1 {
		t.Fatalf("expected final gate to drive one hardening round, got %#v", summary)
	}
	if manifest.FinalGateStatus != "passed" || summary.FinalGateStatus != "passed" || len(summary.FinalGateRoleResults) != 1 {
		t.Fatalf("expected persisted final gate role summary, manifest=%#v summary=%#v", manifest, summary)
	}
	iterationDir := localWorkIterationDir(runDir, 1)
	for _, name := range []string{
		"final-gate-initial-quality-reviewer-findings.json",
		"final-gate-round-1-quality-reviewer-findings.json",
		"hardening-round-1-prompt.md",
	} {
		if _, err := os.Stat(filepath.Join(iterationDir, name)); err != nil {
			t.Fatalf("expected final gate artifact %s: %v", name, err)
		}
	}
	statusOutput, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"status", "--last", "--json"})
	})
	if err != nil {
		t.Fatalf("status json: %v", err)
	}
	var status struct {
		FinalGateStatus      string                         `json:"final_gate_status"`
		FinalGateRoleResults []localWorkFinalGateRoleResult `json:"final_gate_role_results"`
	}
	if err := json.Unmarshal([]byte(statusOutput), &status); err != nil {
		t.Fatalf("unmarshal status: %v\n%s", err, statusOutput)
	}
	if status.FinalGateStatus != "passed" || len(status.FinalGateRoleResults) != 1 {
		t.Fatalf("expected final gate summary in status, got %+v", status)
	}
}

func TestLocalWorkFinalApplyBlocksOnDirtySourceAndResumeCommits(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`PAYLOAD="$(cat)"`,
		`case "$PAYLOAD" in`,
		`  *"Review this local implementation and return JSON only."*)`,
		`    printf '{"findings":[]}\n'`,
		`    ;;`,
		`  *)`,
		`    printf 'implemented\n' >> "$NANA_PROJECT_AGENTS_ROOT/README.md"`,
		`    printf 'source drift\n' > "$FAKE_SOURCE_REPO/drift.txt"`,
		`    printf 'fake-codex:%s\n' "$*"`,
		`    ;;`,
		"esac",
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_SOURCE_REPO", repo)

	output, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"start", "--work-type", workTypeFeature, "--task", "Dirty source before final apply"})
	})
	if err == nil || !strings.Contains(err.Error(), "source checkout has local changes") {
		t.Fatalf("expected dirty-source apply blocker, err=%v output=%s", err, output)
	}
	manifest, _ := mustLatestLocalWorkRun(t, repo)
	if manifest.Status != "blocked" || manifest.FinalApplyStatus != "blocked-before-apply" || !strings.Contains(manifest.FinalApplyError, "source checkout has local changes") {
		t.Fatalf("expected blocked final apply manifest, got %#v", manifest)
	}
	headBeforeResume, err := githubGitOutput(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("read source head: %v", err)
	}
	if strings.TrimSpace(headBeforeResume) != manifest.BaselineSHA {
		t.Fatalf("expected source HEAD to remain at baseline while blocked, head=%q manifest=%#v", headBeforeResume, manifest)
	}
	if err := os.Remove(filepath.Join(repo, "drift.txt")); err != nil {
		t.Fatalf("remove drift: %v", err)
	}

	resumeOutput, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"resume", "--run-id", manifest.RunID})
	})
	if err != nil {
		t.Fatalf("resume blocked apply: %v\n%s", err, resumeOutput)
	}
	manifest = mustLocalWorkManifestByRunID(t, manifest.RunID)
	if manifest.Status != "completed" || manifest.FinalApplyStatus != "committed" || strings.TrimSpace(manifest.FinalApplyCommitSHA) == "" {
		t.Fatalf("expected resume to complete final commit, got %#v", manifest)
	}
}

func TestLocalWorkFinalApplyCommitsNewSandboxFiles(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`PAYLOAD="$(cat)"`,
		`case "$PAYLOAD" in`,
		`  *"Review this local implementation and return JSON only."*)`,
		`    printf '{"findings":[]}\n'`,
		`    ;;`,
		`  *)`,
		`    printf 'new file\n' > "$NANA_PROJECT_AGENTS_ROOT/NEW.md"`,
		`    printf 'fake-codex:%s\n' "$*"`,
		`    ;;`,
		"esac",
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	if err := runLocalWorkCommand(repo, []string{"start", "--work-type", workTypeFeature, "--task", "Create a new file"}); err != nil {
		t.Fatalf("runLocalWorkCommand(start): %v", err)
	}
	manifest, _ := mustLatestLocalWorkRun(t, repo)
	if manifest.FinalApplyStatus != "committed" {
		t.Fatalf("expected committed apply, got %#v", manifest)
	}
	content, err := os.ReadFile(filepath.Join(repo, "NEW.md"))
	if err != nil {
		t.Fatalf("expected new file in source checkout: %v", err)
	}
	if strings.TrimSpace(string(content)) != "new file" {
		t.Fatalf("unexpected NEW.md content %q", content)
	}
	committedFiles, err := githubGitOutput(repo, "show", "--name-only", "--pretty=", "HEAD")
	if err != nil {
		t.Fatalf("read committed files: %v", err)
	}
	if !strings.Contains(committedFiles, "NEW.md") {
		t.Fatalf("expected final commit to include NEW.md, got %q", committedFiles)
	}
}

func TestLocalWorkSkipsFinalGateWhenSandboxDiffIsEmpty(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`PAYLOAD="$(cat)"`,
		`case "$PAYLOAD" in`,
		`  *"Review role:"*)`,
		`    printf 'final gate should not run for empty diff\n' >&2`,
		`    exit 99`,
		`    ;;`,
		`  *"Review this local implementation and return JSON only."*)`,
		`    printf '{"findings":[]}\n'`,
		`    ;;`,
		`  *)`,
		`    printf 'no-op implementation\n'`,
		`    ;;`,
		"esac",
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	if err := runLocalWorkCommand(repo, []string{"start", "--work-type", workTypeFeature, "--task", "Do nothing"}); err != nil {
		t.Fatalf("runLocalWorkCommand(start): %v", err)
	}
	manifest, runDir := mustLatestLocalWorkRun(t, repo)
	if manifest.FinalApplyStatus != "no-op" || manifest.FinalGateStatus != "no-op" {
		t.Fatalf("expected no-op apply and final gate, got %#v", manifest)
	}
	if got := manifest.Iterations[0].FinalGateStatus; got != "no-op" {
		t.Fatalf("expected no-op final gate in iteration summary, got %q", got)
	}
	matches, err := filepath.Glob(filepath.Join(localWorkIterationDir(runDir, 1), "final-gate-*"))
	if err != nil {
		t.Fatalf("glob final gate artifacts: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no final gate artifacts for no-op diff, got %#v", matches)
	}
}

func TestLocalWorkCandidateAuditBlocksGeneratedFilesBeforeFinalGate(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`PAYLOAD="$(cat)"`,
		`case "$PAYLOAD" in`,
		`  *"Review role:"*)`,
		`    printf 'final gate should not run when candidate audit blocks\n' >&2`,
		`    exit 99`,
		`    ;;`,
		`  *"Review this local implementation and return JSON only."*)`,
		`    printf '{"findings":[]}\n'`,
		`    ;;`,
		`  *)`,
		`    mkdir -p "$NANA_PROJECT_AGENTS_ROOT/target/classes"`,
		`    printf 'generated\n' > "$NANA_PROJECT_AGENTS_ROOT/target/classes/generated.txt"`,
		`    printf 'fake-codex:%s\n' "$*"`,
		`    ;;`,
		"esac",
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	output, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"start", "--work-type", workTypeFeature, "--task", "Create generated artifact"})
	})
	if err == nil || !strings.Contains(err.Error(), "candidate diff contains generated or runtime files") {
		t.Fatalf("expected candidate audit blocker, err=%v output=%s", err, output)
	}
	manifest, _ := mustLatestLocalWorkRun(t, repo)
	if manifest.Status != "blocked" || manifest.CandidateAuditStatus != "blocked-candidate-files" || len(manifest.CandidateBlockedPaths) != 1 {
		t.Fatalf("expected blocked candidate audit manifest, got %#v", manifest)
	}
	if manifest.CandidateBlockedPaths[0] != "target/classes/generated.txt" {
		t.Fatalf("unexpected candidate blocked paths: %#v", manifest.CandidateBlockedPaths)
	}
	statusOutput, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"status", "--last"})
	})
	if err != nil {
		t.Fatalf("status blocked run: %v", err)
	}
	if !strings.Contains(statusOutput, "Next action: remove generated/runtime files") {
		t.Fatalf("expected recovery action in status, got %q", statusOutput)
	}
}

func TestLocalWorkCandidateAuditAllowsSourceFiles(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	baseline, err := githubGitOutput(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	runID := "lw-candidate-audit"
	repoID := localWorkRepoID(repo)
	runDir := localWorkRunDirByID(repoID, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	sandboxPath := filepath.Join(home, "sandbox")
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := cloneGithubSourceToSandbox(repo, sandboxRepoPath); err != nil {
		t.Fatalf("clone sandbox: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sandboxRepoPath, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sandboxRepoPath, "README.md"), []byte("# local work\nupdated\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sandboxRepoPath, "src", "new.go"), []byte("package src\n"), 0o644); err != nil {
		t.Fatalf("write src/new.go: %v", err)
	}
	if err := refreshLocalWorkSandboxIntentToAdd(sandboxRepoPath); err != nil {
		t.Fatalf("intent-to-add: %v", err)
	}
	result, err := auditLocalWorkCandidateFiles(localWorkManifest{
		RunID:           runID,
		RepoRoot:        repo,
		RepoID:          repoID,
		BaselineSHA:     strings.TrimSpace(baseline),
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
	})
	if err != nil {
		t.Fatalf("audit candidate files: %v", err)
	}
	if result.Status != "passed" || len(result.BlockedPaths) != 0 {
		t.Fatalf("expected source files to pass candidate audit, got %#v", result)
	}
}

func TestApplyLocalWorkFinalDiffSyncsTrackedBranchWhenSourceHeadChanged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	originBare := filepath.Join(home, "origin.git")
	seedRepo := createLocalWorkRepoAt(t, filepath.Join(home, "seed"))
	runLocalWorkTestGit(t, home, "init", "--bare", originBare)
	runLocalWorkTestGit(t, seedRepo, "remote", "add", "origin", originBare)
	runLocalWorkTestGit(t, seedRepo, "push", "-u", "origin", "main")
	runLocalWorkTestGit(t, "", "--git-dir", originBare, "symbolic-ref", "HEAD", "refs/heads/main")

	repo := filepath.Join(home, "source")
	runLocalWorkTestGit(t, home, "clone", originBare, repo)
	baseline, err := githubGitOutput(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	runID := "lw-head-changed"
	repoID := localWorkRepoID(repo)
	runDir := localWorkRunDirByID(repoID, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	sandboxPath := filepath.Join(home, "sandbox")
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := cloneGithubSourceToSandbox(repo, sandboxRepoPath); err != nil {
		t.Fatalf("clone sandbox: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sandboxRepoPath, "README.md"), []byte("# local work\nsandbox change\n"), 0o644); err != nil {
		t.Fatalf("write sandbox readme: %v", err)
	}

	advanceRepo := filepath.Join(home, "advance")
	runLocalWorkTestGit(t, home, "clone", originBare, advanceRepo)
	if err := os.WriteFile(filepath.Join(advanceRepo, "source.txt"), []byte("source change\n"), 0o644); err != nil {
		t.Fatalf("write source change: %v", err)
	}
	runLocalWorkTestGit(t, advanceRepo, "add", "source.txt")
	runLocalWorkTestGit(t, advanceRepo, "commit", "-m", "source moved")
	runLocalWorkTestGit(t, advanceRepo, "push", "origin", "main")

	result := applyLocalWorkFinalDiff(localWorkManifest{
		RunID:           runID,
		RepoRoot:        repo,
		RepoID:          repoID,
		BaselineSHA:     strings.TrimSpace(baseline),
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
		SourceBranch:    "main",
	})
	if result.Status != "pushed" || strings.TrimSpace(result.CommitSHA) == "" {
		t.Fatalf("expected synced final apply to push successfully, got %#v", result)
	}
	readmeContent, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatalf("read source README: %v", err)
	}
	if !strings.Contains(string(readmeContent), "sandbox change") {
		t.Fatalf("expected source repo to contain sandbox change, got %q", string(readmeContent))
	}
	sourceContent, err := os.ReadFile(filepath.Join(repo, "source.txt"))
	if err != nil {
		t.Fatalf("read synced source file: %v", err)
	}
	if strings.TrimSpace(string(sourceContent)) != "source change" {
		t.Fatalf("expected synced remote change preserved, got %q", string(sourceContent))
	}
	originHead := runLocalWorkTestGitOutput(t, "", "--git-dir", originBare, "rev-parse", "refs/heads/main")
	if strings.TrimSpace(originHead) != strings.TrimSpace(result.CommitSHA) {
		t.Fatalf("expected pushed commit on origin/main, origin=%q result=%q", originHead, result.CommitSHA)
	}
}

func TestEnsureGithubSourceCloneRepairsOriginToCanonicalSSH(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	originBare := filepath.Join(home, "origin.git")
	seedRepo := createLocalWorkRepoAt(t, filepath.Join(home, "seed"))
	runLocalWorkTestGit(t, home, "init", "--bare", originBare)
	runLocalWorkTestGit(t, seedRepo, "remote", "add", "origin", originBare)
	runLocalWorkTestGit(t, seedRepo, "push", "-u", "origin", "main")
	runLocalWorkTestGit(t, "", "--git-dir", originBare, "symbolic-ref", "HEAD", "refs/heads/main")
	configureTestGitInsteadOf(t, "git@github.com:acme/widget.git", originBare)

	paths := githubManagedPaths("acme/widget")
	repoMeta := &githubManagedRepoMetadata{
		RepoSlug:      "acme/widget",
		CloneURL:      originBare,
		DefaultBranch: "main",
		HTMLURL:       "https://github.com/acme/widget",
	}
	if err := ensureGithubSourceClone(paths, repoMeta); err != nil {
		t.Fatalf("ensureGithubSourceClone: %v", err)
	}
	wantOrigin := "git@github.com:acme/widget.git"
	gotOrigin := strings.TrimSpace(runLocalWorkTestGitOutput(t, paths.SourcePath, "config", "--get", "remote.origin.url"))
	if gotOrigin != wantOrigin {
		t.Fatalf("expected canonical ssh origin, got %q want %q", gotOrigin, wantOrigin)
	}

	runLocalWorkTestGit(t, paths.SourcePath, "remote", "set-url", "origin", originBare)
	if err := ensureGithubSourceClone(paths, repoMeta); err != nil {
		t.Fatalf("ensureGithubSourceClone repair: %v", err)
	}
	gotOrigin = strings.TrimSpace(runLocalWorkTestGitOutput(t, paths.SourcePath, "config", "--get", "remote.origin.url"))
	if gotOrigin != wantOrigin {
		t.Fatalf("expected repaired canonical ssh origin, got %q want %q", gotOrigin, wantOrigin)
	}

	advanceRepo := filepath.Join(home, "advance-fetch")
	runLocalWorkTestGit(t, home, "clone", originBare, advanceRepo)
	if err := os.WriteFile(filepath.Join(advanceRepo, "fetched.txt"), []byte("remote change\n"), 0o644); err != nil {
		t.Fatalf("write remote change: %v", err)
	}
	runLocalWorkTestGit(t, advanceRepo, "add", "fetched.txt")
	runLocalWorkTestGit(t, advanceRepo, "commit", "-m", "remote advanced")
	runLocalWorkTestGit(t, advanceRepo, "push", "origin", "main")

	repoMeta.CloneURL = "https://example.invalid/acme/widget.git"
	if err := ensureGithubSourceClone(paths, repoMeta); err != nil {
		t.Fatalf("ensureGithubSourceClone fetch: %v", err)
	}
	remoteHead := strings.TrimSpace(runLocalWorkTestGitOutput(t, "", "--git-dir", originBare, "rev-parse", "refs/heads/main"))
	trackingHead := strings.TrimSpace(runLocalWorkTestGitOutput(t, paths.SourcePath, "rev-parse", "refs/remotes/origin/main"))
	if trackingHead != remoteHead {
		t.Fatalf("expected origin tracking ref to refresh from clone_url fetch, tracking=%q remote=%q", trackingHead, remoteHead)
	}
}

func TestStartLocalWorkWithRunIDRefreshesManagedSourceBeforeSandboxClone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	originBare := filepath.Join(home, "origin.git")
	seedRepo := createLocalWorkRepoAt(t, filepath.Join(home, "seed"))
	runLocalWorkTestGit(t, home, "init", "--bare", originBare)
	runLocalWorkTestGit(t, seedRepo, "remote", "add", "origin", originBare)
	runLocalWorkTestGit(t, seedRepo, "push", "-u", "origin", "main")
	runLocalWorkTestGit(t, "", "--git-dir", originBare, "symbolic-ref", "HEAD", "refs/heads/main")
	configureTestGitInsteadOf(t, "git@github.com:acme/widget.git", originBare)

	managedRoot := filepath.Join(home, ".nana", "work", "repos", "acme", "widget")
	if err := os.MkdirAll(managedRoot, 0o755); err != nil {
		t.Fatalf("mkdir managed root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(managedRoot, "settings.json"), []byte(`{"version":1,"repo_mode":"repo","issue_pick_mode":"auto","pr_forward_mode":"auto","updated_at":"`+ISOTimeNow()+`"}`), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	managedSource := filepath.Join(managedRoot, "source")
	runLocalWorkTestGit(t, home, "clone", originBare, managedSource)

	if err := os.WriteFile(filepath.Join(managedSource, "README.md"), []byte("# local work\nlocal managed change\n"), 0o644); err != nil {
		t.Fatalf("write managed source change: %v", err)
	}
	runLocalWorkTestGit(t, managedSource, "add", "README.md")
	runLocalWorkTestGit(t, managedSource, "commit", "-m", "managed ahead")

	advanceRepo := filepath.Join(home, "advance")
	runLocalWorkTestGit(t, home, "clone", originBare, advanceRepo)
	if err := os.WriteFile(filepath.Join(advanceRepo, "README.md"), []byte("# local work\nremote managed change\n"), 0o644); err != nil {
		t.Fatalf("write remote change: %v", err)
	}
	runLocalWorkTestGit(t, advanceRepo, "add", "README.md")
	runLocalWorkTestGit(t, advanceRepo, "commit", "-m", "remote ahead")
	runLocalWorkTestGit(t, advanceRepo, "push", "origin", "main")
	remoteHead := strings.TrimSpace(runLocalWorkTestGitOutput(t, advanceRepo, "rev-parse", "origin/main"))

	oldDetachedRunner := localWorkStartDetachedRunner
	localWorkStartDetachedRunner = func(repoRoot string, runID string, codexArgs []string, logPath string) error {
		return nil
	}
	defer func() {
		localWorkStartDetachedRunner = oldDetachedRunner
	}()

	runID, err := startLocalWorkWithRunID(managedSource, localWorkStartOptions{
		Detach:                true,
		Task:                  "Refresh managed source before start",
		WorkType:              workTypeFeature,
		MaxIterations:         localWorkDefaultMaxIterations,
		IntegrationPolicy:     "final",
		GroupingPolicy:        localWorkDefaultGroupingPolicy,
		ValidationParallelism: localWorkValidationParallelism,
	})
	if err != nil {
		t.Fatalf("startLocalWorkWithRunID: %v", err)
	}
	manifest := mustLocalWorkManifestByRunID(t, runID)
	if strings.TrimSpace(manifest.BaselineSHA) != remoteHead {
		t.Fatalf("expected baseline to refresh to remote head, got baseline=%q remote=%q", manifest.BaselineSHA, remoteHead)
	}
	sourceHead := strings.TrimSpace(runLocalWorkTestGitOutput(t, managedSource, "rev-parse", "HEAD"))
	if sourceHead != remoteHead {
		t.Fatalf("expected managed source HEAD to refresh to remote head, got head=%q remote=%q", sourceHead, remoteHead)
	}
	gotOrigin := strings.TrimSpace(runLocalWorkTestGitOutput(t, managedSource, "config", "--get", "remote.origin.url"))
	if gotOrigin != "git@github.com:acme/widget.git" {
		t.Fatalf("expected managed source origin repaired to canonical ssh, got %q", gotOrigin)
	}
	sandboxHead := strings.TrimSpace(runLocalWorkTestGitOutput(t, manifest.SandboxRepoPath, "rev-parse", "HEAD"))
	if sandboxHead != remoteHead {
		t.Fatalf("expected sandbox HEAD to clone refreshed source head, got sandbox=%q remote=%q", sandboxHead, remoteHead)
	}
	branches := runLocalWorkTestGitOutput(t, managedSource, "branch", "--format", "%(refname:short)")
	if !strings.Contains(branches, "nana/autosave/main-") {
		t.Fatalf("expected autosave backup branch for refreshed managed source, got %q", branches)
	}
}

func TestRefreshLocalWorkIterationBaselineReappliesDirtySandboxChanges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	originBare := filepath.Join(home, "origin.git")
	seedRepo := createLocalWorkRepoAt(t, filepath.Join(home, "seed"))
	runLocalWorkTestGit(t, home, "init", "--bare", originBare)
	runLocalWorkTestGit(t, seedRepo, "remote", "add", "origin", originBare)
	runLocalWorkTestGit(t, seedRepo, "push", "-u", "origin", "main")
	runLocalWorkTestGit(t, "", "--git-dir", originBare, "symbolic-ref", "HEAD", "refs/heads/main")

	sourceRepo := filepath.Join(home, "source")
	runLocalWorkTestGit(t, home, "clone", originBare, sourceRepo)
	baseline := strings.TrimSpace(runLocalWorkTestGitOutput(t, sourceRepo, "rev-parse", "HEAD"))

	sandboxPath := filepath.Join(home, "sandbox")
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := cloneGithubSourceToSandbox(sourceRepo, sandboxRepoPath); err != nil {
		t.Fatalf("clone sandbox: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sandboxRepoPath, "README.md"), []byte("# sandbox change\n"), 0o644); err != nil {
		t.Fatalf("write sandbox readme: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sandboxRepoPath, "extra.txt"), []byte("sandbox extra\n"), 0o644); err != nil {
		t.Fatalf("write sandbox extra: %v", err)
	}
	if err := refreshLocalWorkSandboxIntentToAdd(sandboxRepoPath); err != nil {
		t.Fatalf("intent-to-add extra.txt: %v", err)
	}

	advanceRepo := filepath.Join(home, "advance")
	runLocalWorkTestGit(t, home, "clone", originBare, advanceRepo)
	if err := os.WriteFile(filepath.Join(advanceRepo, "README.md"), []byte("# upstream change\n"), 0o644); err != nil {
		t.Fatalf("write upstream readme: %v", err)
	}
	runLocalWorkTestGit(t, advanceRepo, "add", "README.md")
	runLocalWorkTestGit(t, advanceRepo, "commit", "-m", "upstream advanced")
	runLocalWorkTestGit(t, advanceRepo, "push", "origin", "main")
	remoteHead := strings.TrimSpace(runLocalWorkTestGitOutput(t, advanceRepo, "rev-parse", "origin/main"))

	manifest := localWorkManifest{
		Version:         5,
		RunID:           "lw-iteration-refresh",
		CreatedAt:       ISOTimeNow(),
		UpdatedAt:       ISOTimeNow(),
		Status:          "running",
		RepoRoot:        sourceRepo,
		RepoName:        filepath.Base(sourceRepo),
		RepoID:          localWorkRepoID(sourceRepo),
		SourceBranch:    "main",
		BaselineSHA:     baseline,
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
	}

	changed, err := refreshLocalWorkIterationBaseline(&manifest, 2)
	if err != nil {
		t.Fatalf("refreshLocalWorkIterationBaseline: %v", err)
	}
	if !changed {
		t.Fatalf("expected iteration baseline refresh to detect source change")
	}
	if strings.TrimSpace(manifest.BaselineSHA) != remoteHead {
		t.Fatalf("expected manifest baseline to update, got baseline=%q remote=%q", manifest.BaselineSHA, remoteHead)
	}
	sourceHead := strings.TrimSpace(runLocalWorkTestGitOutput(t, sourceRepo, "rev-parse", "HEAD"))
	if sourceHead != remoteHead {
		t.Fatalf("expected source repo to refresh to remote head, got source=%q remote=%q", sourceHead, remoteHead)
	}
	readmeContent, err := os.ReadFile(filepath.Join(sandboxRepoPath, "README.md"))
	if err != nil {
		t.Fatalf("read sandbox README: %v", err)
	}
	if strings.TrimSpace(string(readmeContent)) != "# sandbox change" {
		t.Fatalf("expected sandbox change to win after refresh conflict, got %q", string(readmeContent))
	}
	extraContent, err := os.ReadFile(filepath.Join(sandboxRepoPath, "extra.txt"))
	if err != nil {
		t.Fatalf("read sandbox extra.txt: %v", err)
	}
	if strings.TrimSpace(string(extraContent)) != "sandbox extra" {
		t.Fatalf("expected sandbox extra.txt to survive refresh, got %q", string(extraContent))
	}
	conflicted := strings.TrimSpace(runLocalWorkTestGitOutput(t, sandboxRepoPath, "diff", "--name-only", "--diff-filter=U"))
	if conflicted != "" {
		t.Fatalf("expected refreshed sandbox to have no unresolved conflicts, got %q", conflicted)
	}
	stashList := strings.TrimSpace(runLocalWorkTestGitOutput(t, sandboxRepoPath, "stash", "list"))
	if stashList != "" {
		t.Fatalf("expected temporary sandbox stash to be dropped, got %q", stashList)
	}
}

func TestApplyLocalWorkFinalDiffResetsManagedSourceWhenTrackedBranchDiverged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	originBare := filepath.Join(home, "origin.git")
	seedRepo := createLocalWorkRepoAt(t, filepath.Join(home, "seed"))
	runLocalWorkTestGit(t, home, "init", "--bare", originBare)
	runLocalWorkTestGit(t, seedRepo, "remote", "add", "origin", originBare)
	runLocalWorkTestGit(t, seedRepo, "push", "-u", "origin", "main")
	runLocalWorkTestGit(t, "", "--git-dir", originBare, "symbolic-ref", "HEAD", "refs/heads/main")
	configureTestGitInsteadOf(t, "git@github.com:acme/widget.git", originBare)

	managedRoot := filepath.Join(home, ".nana", "work", "repos", "acme", "widget")
	if err := os.MkdirAll(managedRoot, 0o755); err != nil {
		t.Fatalf("mkdir managed root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(managedRoot, "settings.json"), []byte(`{"version":1,"repo_mode":"repo","issue_pick_mode":"auto","pr_forward_mode":"auto","updated_at":"`+ISOTimeNow()+`"}`), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	managedSource := filepath.Join(managedRoot, "source")
	runLocalWorkTestGit(t, home, "clone", originBare, managedSource)

	baseline := strings.TrimSpace(runLocalWorkTestGitOutput(t, managedSource, "rev-parse", "HEAD"))
	runID := "lw-managed-diverged"
	repoID := localWorkRepoID(managedSource)
	runDir := localWorkRunDirByID(repoID, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	sandboxPath := filepath.Join(home, "sandbox")
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := cloneGithubSourceToSandbox(managedSource, sandboxRepoPath); err != nil {
		t.Fatalf("clone sandbox: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sandboxRepoPath, "sandbox.txt"), []byte("sandbox change\n"), 0o644); err != nil {
		t.Fatalf("write sandbox file: %v", err)
	}
	if err := refreshLocalWorkSandboxIntentToAdd(sandboxRepoPath); err != nil {
		t.Fatalf("intent-to-add sandbox.txt: %v", err)
	}

	if err := os.WriteFile(filepath.Join(managedSource, "README.md"), []byte("# local work\nlocal managed change\n"), 0o644); err != nil {
		t.Fatalf("write managed source change: %v", err)
	}
	runLocalWorkTestGit(t, managedSource, "add", "README.md")
	runLocalWorkTestGit(t, managedSource, "commit", "-m", "managed ahead")

	advanceRepo := filepath.Join(home, "advance")
	runLocalWorkTestGit(t, home, "clone", originBare, advanceRepo)
	if err := os.WriteFile(filepath.Join(advanceRepo, "README.md"), []byte("# local work\nremote managed change\n"), 0o644); err != nil {
		t.Fatalf("write remote change: %v", err)
	}
	runLocalWorkTestGit(t, advanceRepo, "add", "README.md")
	runLocalWorkTestGit(t, advanceRepo, "commit", "-m", "remote ahead")
	runLocalWorkTestGit(t, advanceRepo, "push", "origin", "main")

	result := applyLocalWorkFinalDiff(localWorkManifest{
		RunID:           runID,
		RepoRoot:        managedSource,
		RepoID:          repoID,
		BaselineSHA:     baseline,
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
		SourceBranch:    "main",
	})
	if result.Status != "pushed" || strings.TrimSpace(result.CommitSHA) == "" {
		t.Fatalf("expected managed-source final apply to push successfully, got %#v", result)
	}
	readmeContent, err := os.ReadFile(filepath.Join(managedSource, "README.md"))
	if err != nil {
		t.Fatalf("read managed source README: %v", err)
	}
	if !strings.Contains(string(readmeContent), "remote managed change") {
		t.Fatalf("expected refreshed remote change preserved, got %q", string(readmeContent))
	}
	sandboxContent, err := os.ReadFile(filepath.Join(managedSource, "sandbox.txt"))
	if err != nil {
		t.Fatalf("read sandbox.txt: %v", err)
	}
	if strings.TrimSpace(string(sandboxContent)) != "sandbox change" {
		t.Fatalf("expected sandbox change applied, got %q", string(sandboxContent))
	}
	gotOrigin := strings.TrimSpace(runLocalWorkTestGitOutput(t, managedSource, "config", "--get", "remote.origin.url"))
	if gotOrigin != "git@github.com:acme/widget.git" {
		t.Fatalf("expected managed source origin repaired to canonical ssh, got %q", gotOrigin)
	}
	originHead := strings.TrimSpace(runLocalWorkTestGitOutput(t, "", "--git-dir", originBare, "rev-parse", "refs/heads/main"))
	if originHead != strings.TrimSpace(result.CommitSHA) {
		t.Fatalf("expected pushed commit on origin/main, origin=%q result=%q", originHead, result.CommitSHA)
	}
	branches := runLocalWorkTestGitOutput(t, managedSource, "branch", "--format", "%(refname:short)")
	if !strings.Contains(branches, "nana/autosave/main-") {
		t.Fatalf("expected autosave backup branch for refreshed managed source, got %q", branches)
	}
}

func TestApplyLocalWorkFinalDiffAutoResolvesManagedSourceConflicts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	originBare := filepath.Join(home, "origin.git")
	seedRepo := createLocalWorkRepoAt(t, filepath.Join(home, "seed"))
	runLocalWorkTestGit(t, home, "init", "--bare", originBare)
	runLocalWorkTestGit(t, seedRepo, "remote", "add", "origin", originBare)
	runLocalWorkTestGit(t, seedRepo, "push", "-u", "origin", "main")
	runLocalWorkTestGit(t, "", "--git-dir", originBare, "symbolic-ref", "HEAD", "refs/heads/main")
	configureTestGitInsteadOf(t, "git@github.com:acme/widget.git", originBare)

	managedRoot := filepath.Join(home, ".nana", "work", "repos", "acme", "widget")
	if err := os.MkdirAll(managedRoot, 0o755); err != nil {
		t.Fatalf("mkdir managed root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(managedRoot, "settings.json"), []byte(`{"version":1,"repo_mode":"repo","issue_pick_mode":"auto","pr_forward_mode":"auto","updated_at":"`+ISOTimeNow()+`"}`), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	managedSource := filepath.Join(managedRoot, "source")
	runLocalWorkTestGit(t, home, "clone", originBare, managedSource)

	baseline := strings.TrimSpace(runLocalWorkTestGitOutput(t, managedSource, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(managedSource, "README.md"), []byte("# local source change\n"), 0o644); err != nil {
		t.Fatalf("write managed source change: %v", err)
	}
	runLocalWorkTestGit(t, managedSource, "add", "README.md")
	runLocalWorkTestGit(t, managedSource, "commit", "-m", "managed local change")

	runID := "lw-managed-conflict"
	repoID := localWorkRepoID(managedSource)
	runDir := localWorkRunDirByID(repoID, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	sandboxPath := filepath.Join(home, "sandbox-conflict")
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := cloneGithubSourceToSandbox(filepath.Join(home, "seed"), sandboxRepoPath); err != nil {
		t.Fatalf("clone sandbox from baseline seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sandboxRepoPath, "README.md"), []byte("# sandbox change\n"), 0o644); err != nil {
		t.Fatalf("write sandbox readme: %v", err)
	}

	result := applyLocalWorkFinalDiff(localWorkManifest{
		RunID:           runID,
		RepoRoot:        managedSource,
		RepoID:          repoID,
		BaselineSHA:     baseline,
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
		SourceBranch:    "main",
	})
	if result.Status != "pushed" || strings.TrimSpace(result.CommitSHA) == "" {
		t.Fatalf("expected managed-source final apply to auto-resolve conflicts and push, got %#v", result)
	}
	readmeContent, err := os.ReadFile(filepath.Join(managedSource, "README.md"))
	if err != nil {
		t.Fatalf("read managed source README: %v", err)
	}
	if strings.TrimSpace(string(readmeContent)) != "# sandbox change" {
		t.Fatalf("expected sandbox version to win conflicted file, got %q", string(readmeContent))
	}
	gotOrigin := strings.TrimSpace(runLocalWorkTestGitOutput(t, managedSource, "config", "--get", "remote.origin.url"))
	if gotOrigin != "git@github.com:acme/widget.git" {
		t.Fatalf("expected managed source origin repaired to canonical ssh, got %q", gotOrigin)
	}
	originContent := runLocalWorkTestGitOutput(t, "", "--git-dir", originBare, "show", strings.TrimSpace(result.CommitSHA)+":README.md")
	if strings.TrimSpace(originContent) != "# sandbox change" {
		t.Fatalf("expected pushed commit to contain sandbox version, got %q", originContent)
	}
}

func TestApplyLocalWorkFinalDiffBlocksManagedSourceWhenOriginPreflightFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	managedRoot := filepath.Join(home, ".nana", "work", "repos", "acme", "widget")
	if err := os.MkdirAll(managedRoot, 0o755); err != nil {
		t.Fatalf("mkdir managed root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(managedRoot, "settings.json"), []byte(`{"version":1,"repo_mode":"repo","issue_pick_mode":"auto","pr_forward_mode":"auto","updated_at":"`+ISOTimeNow()+`"}`), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	managedSource := createLocalWorkRepoAt(t, filepath.Join(managedRoot, "source"))
	baseline := strings.TrimSpace(runLocalWorkTestGitOutput(t, managedSource, "rev-parse", "HEAD"))

	runID := "lw-managed-preflight"
	repoID := localWorkRepoID(managedSource)
	runDir := localWorkRunDirByID(repoID, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	sandboxPath := filepath.Join(home, "sandbox-preflight")
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := cloneGithubSourceToSandbox(managedSource, sandboxRepoPath); err != nil {
		t.Fatalf("clone sandbox: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sandboxRepoPath, "README.md"), []byte("# preflight change\n"), 0o644); err != nil {
		t.Fatalf("write sandbox readme: %v", err)
	}

	oldPreflight := githubManagedOriginPreflight
	githubManagedOriginPreflight = func(repoPath string, repoMeta *githubManagedRepoMetadata) error {
		return fmt.Errorf("managed source checkout %s requires working SSH access to git@github.com:acme/widget.git", repoPath)
	}
	defer func() {
		githubManagedOriginPreflight = oldPreflight
	}()

	result := applyLocalWorkFinalDiff(localWorkManifest{
		RunID:           runID,
		RepoRoot:        managedSource,
		RepoID:          repoID,
		BaselineSHA:     baseline,
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
		SourceBranch:    "main",
	})
	if result.Status != "blocked-before-apply" || !strings.Contains(result.Error, "requires working SSH access") {
		t.Fatalf("expected managed source preflight blocker, got %#v", result)
	}
}

func TestApplyLocalWorkFinalDiffBlocksWhenFinalApplyLockHeld(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	baseline := strings.TrimSpace(runLocalWorkTestGitOutput(t, repo, "rev-parse", "HEAD"))
	runID := "lw-lock-held"
	repoID := localWorkRepoID(repo)
	runDir := localWorkRunDirByID(repoID, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	sandboxPath := filepath.Join(home, "sandbox-lock")
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := cloneGithubSourceToSandbox(repo, sandboxRepoPath); err != nil {
		t.Fatalf("clone sandbox: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sandboxRepoPath, "README.md"), []byte("# local work\nsandbox change\n"), 0o644); err != nil {
		t.Fatalf("write sandbox readme: %v", err)
	}
	if err := writeLocalWorkManifest(localWorkManifest{
		Version:         4,
		RunID:           "lw-other",
		CreatedAt:       ISOTimeNow(),
		UpdatedAt:       ISOTimeNow(),
		Status:          "running",
		CurrentPhase:    "apply-blocked",
		RepoRoot:        repo,
		RepoName:        filepath.Base(repo),
		RepoID:          repoID,
		SourceBranch:    "main",
		BaselineSHA:     baseline,
		SandboxPath:     filepath.Join(home, "sandbox-other"),
		SandboxRepoPath: filepath.Join(home, "sandbox-other", "repo"),
	}); err != nil {
		t.Fatalf("write lock owner manifest: %v", err)
	}
	releaseLock, err := acquireLocalWorkFinalApplyLock(localWorkManifest{
		RunID:        "lw-other",
		RepoRoot:     repo,
		SourceBranch: "main",
	}, "final-apply")
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	defer releaseLock()

	result := applyLocalWorkFinalDiff(localWorkManifest{
		RunID:           runID,
		RepoRoot:        repo,
		RepoID:          repoID,
		BaselineSHA:     baseline,
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
		SourceBranch:    "main",
	})
	if result.Status != "blocked-before-apply" || !strings.Contains(result.Error, "final apply already in progress") {
		t.Fatalf("expected apply lock blocker, got %#v", result)
	}
}

func TestApplyLocalWorkFinalDiffReclaimsStaleFinalApplyLock(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	baseline := strings.TrimSpace(runLocalWorkTestGitOutput(t, repo, "rev-parse", "HEAD"))
	runID := "lw-lock-stale"
	repoID := localWorkRepoID(repo)
	runDir := localWorkRunDirByID(repoID, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	sandboxPath := filepath.Join(home, "sandbox-stale-lock")
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := cloneGithubSourceToSandbox(repo, sandboxRepoPath); err != nil {
		t.Fatalf("clone sandbox: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sandboxRepoPath, "README.md"), []byte("# local work\nsandbox change\n"), 0o644); err != nil {
		t.Fatalf("write sandbox readme: %v", err)
	}
	lockPath := localWorkFinalApplyLockPath(repo)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("mkdir lock dir: %v", err)
	}
	if err := writeGithubJSON(lockPath, localWorkFinalApplyLockState{
		Version:      1,
		RunID:        "lw-stale-owner",
		RepoRoot:     repo,
		SourceBranch: "main",
		Phase:        "final-apply",
		CreatedAt:    time.Now().UTC().Add(-2 * localWorkStaleRunThreshold).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}

	result := applyLocalWorkFinalDiff(localWorkManifest{
		RunID:           runID,
		RepoRoot:        repo,
		RepoID:          repoID,
		BaselineSHA:     baseline,
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
		SourceBranch:    "main",
	})
	if result.Status != "committed" || strings.TrimSpace(result.CommitSHA) == "" {
		t.Fatalf("expected stale lock to be reclaimed, got %#v", result)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("expected stale lock to be removed, stat err=%v", err)
	}
}

func TestApplyLocalWorkFinalDiffBlocksAfterApplyWhenCommitFails(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	baseline, err := githubGitOutput(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	runID := "lw-commit-fails"
	repoID := localWorkRepoID(repo)
	runDir := localWorkRunDirByID(repoID, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	sandboxPath := filepath.Join(home, "sandbox")
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := cloneGithubSourceToSandbox(repo, sandboxRepoPath); err != nil {
		t.Fatalf("clone sandbox: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sandboxRepoPath, "README.md"), []byte("# local work\nsandbox change\n"), 0o644); err != nil {
		t.Fatalf("write sandbox readme: %v", err)
	}
	hooksDir := filepath.Join(repo, ".git", "hooks")
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte("#!/bin/sh\nexit 17\n"), 0o755); err != nil {
		t.Fatalf("write pre-commit hook: %v", err)
	}

	result := applyLocalWorkFinalDiff(localWorkManifest{
		RunID:           runID,
		RepoRoot:        repo,
		RepoID:          repoID,
		BaselineSHA:     strings.TrimSpace(baseline),
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
	})
	if result.Status != "blocked-after-apply" || !strings.Contains(result.Error, "staged final-apply changes") {
		t.Fatalf("expected post-apply blocker, got %#v", result)
	}
	cachedFiles, err := githubGitOutput(repo, "diff", "--cached", "--name-only")
	if err != nil {
		t.Fatalf("read cached diff: %v", err)
	}
	if !strings.Contains(cachedFiles, "README.md") {
		t.Fatalf("expected README.md staged after commit failure, got %q", cachedFiles)
	}
	manifest := localWorkManifest{
		Version:               4,
		RunID:                 runID,
		CreatedAt:             ISOTimeNow(),
		UpdatedAt:             ISOTimeNow(),
		Status:                "blocked",
		CurrentPhase:          "apply-blocked",
		RepoRoot:              repo,
		RepoName:              filepath.Base(repo),
		RepoID:                repoID,
		BaselineSHA:           strings.TrimSpace(baseline),
		SandboxPath:           sandboxPath,
		SandboxRepoPath:       sandboxRepoPath,
		InputPath:             filepath.Join(runDir, "input-plan.md"),
		InputMode:             "task",
		IntegrationPolicy:     "final",
		GroupingPolicy:        localWorkDefaultGroupingPolicy,
		ValidationParallelism: localWorkValidationParallelism,
		MaxIterations:         8,
		FinalApplyStatus:      result.Status,
		FinalApplyError:       result.Error,
		LastError:             result.Error,
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("write blocked manifest: %v", err)
	}
	if _, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"resume", "--run-id", runID})
	}); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected blocked-after-apply resume to refuse retry, got %v", err)
	}
}

func TestWorkResolveCompletesBlockedBeforeApplyRun(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	baseline := strings.TrimSpace(runLocalWorkTestGitOutput(t, repo, "rev-parse", "HEAD"))
	runID := "lw-resolve-before-apply"
	repoID := localWorkRepoID(repo)
	runDir := localWorkRunDirByID(repoID, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	inputPath := filepath.Join(runDir, "input-plan.md")
	if err := os.WriteFile(inputPath, []byte("resolve blocked apply\n"), 0o644); err != nil {
		t.Fatalf("write input plan: %v", err)
	}
	sandboxPath := filepath.Join(home, "sandbox")
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := cloneGithubSourceToSandbox(repo, sandboxRepoPath); err != nil {
		t.Fatalf("clone sandbox: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sandboxRepoPath, "README.md"), []byte("# local work\nresolved\n"), 0o644); err != nil {
		t.Fatalf("write sandbox readme: %v", err)
	}

	manifest := localWorkManifest{
		Version:               4,
		RunID:                 runID,
		CreatedAt:             ISOTimeNow(),
		UpdatedAt:             ISOTimeNow(),
		Status:                "blocked",
		CurrentPhase:          "apply-blocked",
		RepoRoot:              repo,
		RepoName:              filepath.Base(repo),
		RepoID:                repoID,
		SourceBranch:          "main",
		BaselineSHA:           baseline,
		SandboxPath:           sandboxPath,
		SandboxRepoPath:       sandboxRepoPath,
		InputPath:             inputPath,
		InputMode:             "task",
		IntegrationPolicy:     "final",
		GroupingPolicy:        localWorkDefaultGroupingPolicy,
		ValidationParallelism: localWorkValidationParallelism,
		MaxIterations:         8,
		FinalApplyStatus:      "blocked-before-apply",
		FinalApplyError:       "blocked for test",
		LastError:             "blocked for test",
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("write blocked manifest: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return Work(repo, []string{"resolve", "--run-id", runID})
	})
	if err != nil {
		t.Fatalf("Work(resolve): %v\n%s", err, output)
	}
	updated := mustLocalWorkManifestByRunID(t, runID)
	if updated.Status != "completed" || updated.FinalApplyStatus != "committed" || strings.TrimSpace(updated.FinalApplyCommitSHA) == "" {
		t.Fatalf("expected blocked-before-apply resolve to complete, got %#v", updated)
	}
}

func TestWorkResolveCompletesBlockedAfterApplyRun(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	baseline := strings.TrimSpace(runLocalWorkTestGitOutput(t, repo, "rev-parse", "HEAD"))
	runID := "lw-resolve-after-apply"
	repoID := localWorkRepoID(repo)
	runDir := localWorkRunDirByID(repoID, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	inputPath := filepath.Join(runDir, "input-plan.md")
	if err := os.WriteFile(inputPath, []byte("resolve blocked post-apply\n"), 0o644); err != nil {
		t.Fatalf("write input plan: %v", err)
	}
	sandboxPath := filepath.Join(home, "sandbox")
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := cloneGithubSourceToSandbox(repo, sandboxRepoPath); err != nil {
		t.Fatalf("clone sandbox: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sandboxRepoPath, "README.md"), []byte("# local work\nsandbox change\n"), 0o644); err != nil {
		t.Fatalf("write sandbox readme: %v", err)
	}
	hooksDir := filepath.Join(repo, ".git", "hooks")
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte("#!/bin/sh\nexit 17\n"), 0o755); err != nil {
		t.Fatalf("write pre-commit hook: %v", err)
	}

	result := applyLocalWorkFinalDiff(localWorkManifest{
		RunID:           runID,
		RepoRoot:        repo,
		RepoID:          repoID,
		BaselineSHA:     baseline,
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
		SourceBranch:    "main",
	})
	if result.Status != "blocked-after-apply" {
		t.Fatalf("expected blocked-after-apply setup, got %#v", result)
	}
	if err := os.Remove(filepath.Join(hooksDir, "pre-commit")); err != nil {
		t.Fatalf("remove pre-commit hook: %v", err)
	}

	manifest := localWorkManifest{
		Version:               4,
		RunID:                 runID,
		CreatedAt:             ISOTimeNow(),
		UpdatedAt:             ISOTimeNow(),
		Status:                "blocked",
		CurrentPhase:          "apply-blocked",
		RepoRoot:              repo,
		RepoName:              filepath.Base(repo),
		RepoID:                repoID,
		SourceBranch:          "main",
		BaselineSHA:           baseline,
		SandboxPath:           sandboxPath,
		SandboxRepoPath:       sandboxRepoPath,
		InputPath:             inputPath,
		InputMode:             "task",
		IntegrationPolicy:     "final",
		GroupingPolicy:        localWorkDefaultGroupingPolicy,
		ValidationParallelism: localWorkValidationParallelism,
		MaxIterations:         8,
		FinalApplyStatus:      result.Status,
		FinalApplyCommitSHA:   result.CommitSHA,
		FinalApplyError:       result.Error,
		LastError:             result.Error,
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("write blocked manifest: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return Work(repo, []string{"resolve", "--run-id", runID})
	})
	if err != nil {
		t.Fatalf("Work(resolve post-apply): %v\n%s", err, output)
	}
	updated := mustLocalWorkManifestByRunID(t, runID)
	if updated.Status != "completed" || updated.FinalApplyStatus != "committed" || strings.TrimSpace(updated.FinalApplyCommitSHA) == "" {
		t.Fatalf("expected blocked-after-apply resolve to complete, got %#v", updated)
	}
}

func TestWorkResolveRejectsSupersededBlockedRun(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	runID := "lw-superseded-blocked"
	manifest := localWorkManifest{
		Version:               5,
		RunID:                 runID,
		CreatedAt:             ISOTimeNow(),
		UpdatedAt:             ISOTimeNow(),
		Status:                "blocked",
		CurrentPhase:          "apply-blocked",
		RepoRoot:              repo,
		RepoName:              filepath.Base(repo),
		RepoID:                localWorkRepoID(repo),
		SourceBranch:          "main",
		BaselineSHA:           strings.TrimSpace(runLocalWorkTestGitOutput(t, repo, "rev-parse", "HEAD")),
		SandboxPath:           filepath.Join(home, "sandbox"),
		SandboxRepoPath:       filepath.Join(home, "sandbox", "repo"),
		FinalApplyStatus:      "blocked-before-apply",
		FinalApplyError:       "blocked for test",
		LastError:             "blocked for test",
		SupersededByRunID:     "lw-newer-completed",
		SupersededAt:          ISOTimeNow(),
		SupersededReason:      "newer completed run lw-newer-completed already applied branch main",
		GroupingPolicy:        localWorkDefaultGroupingPolicy,
		ValidationParallelism: localWorkValidationParallelism,
		MaxIterations:         8,
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}

	err := Work(repo, []string{"resolve", "--run-id", runID})
	if err == nil || !strings.Contains(err.Error(), "lw-newer-completed") {
		t.Fatalf("expected resolve to reject superseded run, got %v", err)
	}
}

func TestApplyLocalWorkFinalDiffNoOpWhenSandboxHasNoDiff(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	baseline, err := githubGitOutput(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	runID := "lw-no-op"
	repoID := localWorkRepoID(repo)
	runDir := localWorkRunDirByID(repoID, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	sandboxPath := filepath.Join(home, "sandbox")
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := cloneGithubSourceToSandbox(repo, sandboxRepoPath); err != nil {
		t.Fatalf("clone sandbox: %v", err)
	}

	result := applyLocalWorkFinalDiff(localWorkManifest{
		RunID:           runID,
		RepoRoot:        repo,
		RepoID:          repoID,
		BaselineSHA:     strings.TrimSpace(baseline),
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
	})
	if result.Status != "no-op" || strings.TrimSpace(result.CommitSHA) != "" || strings.TrimSpace(result.Error) != "" {
		t.Fatalf("expected no-op apply, got %#v", result)
	}
	head, err := githubGitOutput(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("read source head: %v", err)
	}
	if strings.TrimSpace(head) != strings.TrimSpace(baseline) {
		t.Fatalf("expected no-op to leave source HEAD unchanged, head=%q baseline=%q", head, baseline)
	}
}

func TestApplyLocalWorkFinalDiffNoOpWhileSourceReadLockHeld(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := setRepoAccessLockTestTiming(t, 200*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond, time.Second)
	defer restore()

	baseline, err := githubGitOutput(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	runID := "lw-no-op-read-lock"
	repoID := localWorkRepoID(repo)
	runDir := localWorkRunDirByID(repoID, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	sandboxPath := filepath.Join(home, "sandbox")
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := cloneGithubSourceToSandbox(repo, sandboxRepoPath); err != nil {
		t.Fatalf("clone sandbox: %v", err)
	}
	sourceReadLock, err := acquireSourceReadLock(repo, repoAccessLockOwner{
		Backend: "test",
		RunID:   "local-no-op-reader",
		Purpose: "inspect",
		Label:   "local-no-op-reader",
	})
	if err != nil {
		t.Fatalf("acquire source read lock: %v", err)
	}
	defer func() { _ = sourceReadLock.Release() }()

	result := applyLocalWorkFinalDiff(localWorkManifest{
		RunID:           runID,
		RepoRoot:        repo,
		RepoID:          repoID,
		BaselineSHA:     strings.TrimSpace(baseline),
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
	})
	if result.Status != "no-op" || strings.TrimSpace(result.CommitSHA) != "" || strings.TrimSpace(result.Error) != "" {
		t.Fatalf("expected no-op apply while read lock held, got %#v", result)
	}
}

func TestLocalWorkStatusAndLogsJSON(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	markerPath := filepath.Join(home, "hardening.marker")
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`PAYLOAD="$(cat)"`,
		`case "$PAYLOAD" in`,
		`  *"# NANA Work-local Finding Validation"*)`,
		`    printf '{"group":"readme-md","decisions":[{"fingerprint":"readme.md|need stronger regression coverage|1|add regression","status":"confirmed","reason":"valid finding"}]}\n'`,
		`    ;;`,
		`  *"# NANA Work-local Hardening Pass"*)`,
		`    printf 'fixed\n' >> README.md`,
		`    : > "${FAKE_CODEX_HARDENED_PATH}"`,
		`    printf 'hardening-complete\n'`,
		`    ;;`,
		`  *"Review this local implementation and return JSON only."*)`,
		`    if [ -f "${FAKE_CODEX_HARDENED_PATH}" ]; then`,
		`      printf '{"findings":[]}\n'`,
		`    else`,
		`      printf '{"findings":[{"title":"Need stronger regression coverage","severity":"medium","path":"README.md","line":1,"summary":"add regression","detail":"detail","fix":"fix","rationale":"why"}]}\n'`,
		`    fi`,
		`    ;;`,
		`  *)`,
		`    printf 'implemented\n' >> README.md`,
		`    printf 'fake-codex:%s\n' "$*"`,
		`    ;;`,
		"esac",
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_HARDENED_PATH", markerPath)

	if err := runLocalWorkCommand(repo, []string{"start", "--work-type", workTypeFeature, "--task", "Trigger json status", "--grouping-policy", "singleton", "--validation-parallelism", "2"}); err != nil {
		t.Fatalf("runLocalWorkCommand(start): %v", err)
	}

	statusOutput, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"status", "--last", "--json"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(status --json): %v", err)
	}
	var status struct {
		RunID         string                    `json:"run_id"`
		RejectedCount int                       `json:"rejected_fingerprint_count"`
		LastIteration localWorkIterationSummary `json:"last_iteration"`
	}
	if err := json.Unmarshal([]byte(statusOutput), &status); err != nil {
		t.Fatalf("unmarshal status json: %v\n%s", err, statusOutput)
	}
	if status.RunID == "" || status.LastIteration.EffectiveGroupingPolicy != localWorkSingletonPolicy || status.LastIteration.ValidatedFindings == 0 {
		t.Fatalf("unexpected status json: %+v", status)
	}

	logsOutput, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"logs", "--last", "--json", "--tail", "10"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(logs --json): %v", err)
	}
	var logs struct {
		Grouping localWorkGroupingResult `json:"grouping"`
		Files    []map[string]string     `json:"files"`
	}
	if err := json.Unmarshal([]byte(logsOutput), &logs); err != nil {
		t.Fatalf("unmarshal logs json: %v\n%s", err, logsOutput)
	}
	if logs.Grouping.EffectivePolicy != localWorkSingletonPolicy || len(logs.Files) == 0 {
		t.Fatalf("unexpected logs json: %+v", logs)
	}
}

func TestLocalWorkStatusJSONIncludesLockState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := setRepoAccessLockTestTiming(t, 200*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond, time.Second)
	defer restore()

	repoRoot := createLocalWorkRepoAt(t, filepath.Join(home, "repo"))
	repoID := localWorkRepoID(repoRoot)
	runID := "lw-lock-status"
	runDir := localWorkRunDirByID(repoID, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	sandboxPath := filepath.Join(localWorkSandboxesDir(), repoID, runID)
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := os.MkdirAll(sandboxRepoPath, 0o755); err != nil {
		t.Fatalf("mkdir sandbox repo: %v", err)
	}
	manifest := localWorkManifest{
		Version:          4,
		RunID:            runID,
		CreatedAt:        ISOTimeNow(),
		UpdatedAt:        ISOTimeNow(),
		Status:           "running",
		CurrentPhase:     "implement",
		CurrentIteration: 1,
		RepoRoot:         repoRoot,
		RepoName:         filepath.Base(repoRoot),
		RepoID:           repoID,
		SourceBranch:     "main",
		BaselineSHA:      strings.TrimSpace(runLocalWorkTestGitOutput(t, repoRoot, "rev-parse", "HEAD")),
		SandboxPath:      sandboxPath,
		SandboxRepoPath:  sandboxRepoPath,
		MaxIterations:    8,
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	sourceLock, err := acquireSourceWriteLock(repoRoot, repoAccessLockOwner{
		Backend: "test",
		RunID:   "local-source-status",
		Purpose: "source-final-apply",
		Label:   "local-source-status",
	})
	if err != nil {
		t.Fatalf("acquire source lock: %v", err)
	}
	defer func() { _ = sourceLock.Release() }()
	sandboxLock, err := acquireSandboxReadLock(sandboxRepoPath, repoAccessLockOwner{
		Backend: "test",
		RunID:   "local-sandbox-status",
		Purpose: "review",
		Label:   "local-sandbox-status",
	})
	if err != nil {
		t.Fatalf("acquire sandbox lock: %v", err)
	}
	defer func() { _ = sandboxLock.Release() }()

	statusOutput, err := captureStdout(t, func() error {
		return localWorkStatus(repoRoot, localWorkStatusOptions{
			RunSelection: localWorkRunSelection{RunID: runID, RepoPath: repoRoot},
			JSON:         true,
		})
	})
	if err != nil {
		t.Fatalf("localWorkStatus(--json): %v", err)
	}
	var status struct {
		RunID     string                       `json:"run_id"`
		LockState repoAccessLockStatusSnapshot `json:"lock_state"`
	}
	if err := json.Unmarshal([]byte(statusOutput), &status); err != nil {
		t.Fatalf("unmarshal status json: %v\n%s", err, statusOutput)
	}
	if status.RunID != runID {
		t.Fatalf("unexpected run id: %+v", status)
	}
	if status.LockState.Source == nil || status.LockState.Source.Writer == nil || !strings.Contains(status.LockState.Source.Writer.Label, "local-source-status") {
		t.Fatalf("expected source lock state, got %+v", status.LockState)
	}
	if status.LockState.Sandbox == nil || len(status.LockState.Sandbox.Readers) != 1 || !strings.Contains(status.LockState.Sandbox.Readers[0].Label, "local-sandbox-status") {
		t.Fatalf("expected sandbox lock state, got %+v", status.LockState)
	}
}

func TestLocalWorkAIFallbacksToSingletonGrouping(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	markerPath := filepath.Join(home, "hardening.marker")
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`PAYLOAD="$(cat)"`,
		`case "$PAYLOAD" in`,
		`  *"# NANA Work-local Finding Grouping"*)`,
		`    printf 'not-json\n'`,
		`    ;;`,
		`  *"# NANA Work-local Finding Validation"*)`,
		`    printf '{"group":"need-stronger-regression-coverage","decisions":[{"fingerprint":"readme.md|need stronger regression coverage|1|add regression","status":"confirmed","reason":"valid finding"}]}\n'`,
		`    ;;`,
		`  *"# NANA Work-local Hardening Pass"*)`,
		`    printf 'fixed\n' >> README.md`,
		`    : > "${FAKE_CODEX_HARDENED_PATH}"`,
		`    printf 'hardening-complete\n'`,
		`    ;;`,
		`  *"Review this local implementation and return JSON only."*)`,
		`    if [ -f "${FAKE_CODEX_HARDENED_PATH}" ]; then`,
		`      printf '{"findings":[]}\n'`,
		`    else`,
		`      printf '{"findings":[{"title":"Need stronger regression coverage","severity":"medium","path":"README.md","line":1,"summary":"add regression","detail":"detail","fix":"fix","rationale":"why"}]}\n'`,
		`    fi`,
		`    ;;`,
		`  *)`,
		`    printf 'implemented\n' >> README.md`,
		`    printf 'fake-codex:%s\n' "$*"`,
		`    ;;`,
		"esac",
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_HARDENED_PATH", markerPath)

	if err := runLocalWorkCommand(repo, []string{"start", "--work-type", workTypeFeature, "--task", "Fallback grouping"}); err != nil {
		t.Fatalf("runLocalWorkCommand(start): %v", err)
	}
	manifest, _ := mustLatestLocalWorkRun(t, repo)
	if len(manifest.Iterations) != 1 {
		t.Fatalf("unexpected iterations: %#v", manifest.Iterations)
	}
	summary := manifest.Iterations[0]
	if summary.EffectiveGroupingPolicy != localWorkSingletonPolicy || summary.GroupingAttempts != localWorkMaxGroupingAttempts || summary.GroupingFallbackReason == "" {
		t.Fatalf("expected singleton fallback in summary, got %#v", summary)
	}
}

func TestLocalWorkPathGroupingBypassesAIGrouper(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	markerPath := filepath.Join(home, "hardening.marker")
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`PAYLOAD="$(cat)"`,
		`case "$PAYLOAD" in`,
		`  *"# NANA Work-local Finding Grouping"*)`,
		`    printf 'grouper should not be called for path policy\n' >&2`,
		`    exit 99`,
		`    ;;`,
		`  *"# NANA Work-local Finding Validation"*)`,
		`    printf '{"group":"README.md","decisions":[{"fingerprint":"readme.md|need stronger regression coverage|1|add regression","status":"confirmed","reason":"valid finding"}]}\n'`,
		`    ;;`,
		`  *"# NANA Work-local Hardening Pass"*)`,
		`    printf 'fixed\n' >> README.md`,
		`    : > "${FAKE_CODEX_HARDENED_PATH}"`,
		`    printf 'hardening-complete\n'`,
		`    ;;`,
		`  *"Review this local implementation and return JSON only."*)`,
		`    if [ -f "${FAKE_CODEX_HARDENED_PATH}" ]; then`,
		`      printf '{"findings":[]}\n'`,
		`    else`,
		`      printf '{"findings":[{"title":"Need stronger regression coverage","severity":"medium","path":"README.md","line":1,"summary":"add regression","detail":"detail","fix":"fix","rationale":"why"}]}\n'`,
		`    fi`,
		`    ;;`,
		`  *)`,
		`    printf 'implemented\n' >> README.md`,
		`    printf 'fake-codex:%s\n' "$*"`,
		`    ;;`,
		"esac",
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_HARDENED_PATH", markerPath)

	if err := runLocalWorkCommand(repo, []string{"start", "--work-type", workTypeFeature, "--task", "Path grouping bypass", "--grouping-policy", "path"}); err != nil {
		t.Fatalf("runLocalWorkCommand(start): %v", err)
	}
	manifest, _ := mustLatestLocalWorkRun(t, repo)
	if got := manifest.Iterations[0].EffectiveGroupingPolicy; got != localWorkPathGroupingPolicy {
		t.Fatalf("expected path grouping policy, got %q", got)
	}
}

func TestLocalWorkValidationFailurePersistsRuntimeStateAndFailureDetails(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`PAYLOAD="$(cat)"`,
		`case "$PAYLOAD" in`,
		`  *"# NANA Work-local Finding Grouping"*)`,
		`    printf '{"groups":[{"group_id":"readme-validation","rationale":"shared readme context","findings":["readme.md|need stronger regression coverage|1|add regression"]}]}\n'`,
		`    ;;`,
		`  *"# NANA Work-local Finding Validation"*)`,
		`    printf 'not-json\n'`,
		`    ;;`,
		`  *"Review this local implementation and return JSON only."*)`,
		`    printf '{"findings":[{"title":"Need stronger regression coverage","severity":"medium","path":"README.md","line":1,"summary":"add regression","detail":"detail","fix":"fix","rationale":"why"}]}\n'`,
		`    ;;`,
		`  *)`,
		`    printf 'implemented\n' >> README.md`,
		`    printf 'fake-codex:%s\n' "$*"`,
		`    ;;`,
		"esac",
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	err := runLocalWorkCommand(repo, []string{"start", "--work-type", workTypeFeature, "--task", "Fail validator"})
	if err == nil || !strings.Contains(err.Error(), "validator group readme-validation failed after 3 attempt(s)") {
		t.Fatalf("expected validator failure, got %v", err)
	}
	manifest, _ := mustLatestLocalWorkRun(t, repo)
	if _, err := readLocalWorkRuntimeState(manifest.RunID, 1); err != nil {
		t.Fatalf("expected runtime-state row: %v", err)
	}

	statusOutput, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"status", "--last", "--json"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(status --json): %v", err)
	}
	var status struct {
		LastError               string                           `json:"last_error"`
		ActiveValidationContext *localWorkValidationContextState `json:"active_validation_context"`
	}
	if err := json.Unmarshal([]byte(statusOutput), &status); err != nil {
		t.Fatalf("unmarshal status json: %v\n%s", err, statusOutput)
	}
	if status.ActiveValidationContext == nil || !strings.Contains(status.LastError, "validator group readme-validation failed") {
		t.Fatalf("unexpected status snapshot: %+v", status)
	}
	if len(status.ActiveValidationContext.GroupStates) == 0 || status.ActiveValidationContext.GroupStates[0].Status != "failed" || status.ActiveValidationContext.GroupStates[0].Attempts != localWorkMaxValidatorAttempts {
		t.Fatalf("expected failed group state, got %+v", status.ActiveValidationContext)
	}

	humanStatus, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"status", "--last"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(status): %v", err)
	}
	if !strings.Contains(humanStatus, "Validation group: readme-validation status=failed attempts=3") {
		t.Fatalf("expected failed validation group in human status: %q", humanStatus)
	}

	logsOutput, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"logs", "--last", "--json", "--tail", "10"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(logs --json): %v", err)
	}
	var logs struct {
		RuntimeState *localWorkIterationRuntimeState `json:"runtime_state"`
	}
	if err := json.Unmarshal([]byte(logsOutput), &logs); err != nil {
		t.Fatalf("unmarshal logs json: %v\n%s", err, logsOutput)
	}
	if logs.RuntimeState == nil || len(logs.RuntimeState.ValidationContexts) == 0 {
		t.Fatalf("expected runtime state in logs json: %+v", logs)
	}

	humanLogs, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"logs", "--last", "--tail", "5"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(logs): %v", err)
	}
	if !strings.Contains(humanLogs, "Validation group: readme-validation status=failed attempts=3") {
		t.Fatalf("expected failed validation group in human logs: %q", humanLogs)
	}

	retroOutput, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"retrospective", "--last"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(retrospective): %v", err)
	}
	if !strings.Contains(retroOutput, "failing group: readme-validation") || !strings.Contains(retroOutput, "attempts exhausted: 3") {
		t.Fatalf("expected validation failure details in retrospective: %q", retroOutput)
	}
	if strings.Contains(retroOutput, "- (not run)") {
		t.Fatalf("retrospective falsely reported verification as not run: %q", retroOutput)
	}
	for _, needle := range []string{"verification passed (lint, compile, unit)", "verification.json"} {
		if !strings.Contains(retroOutput, needle) {
			t.Fatalf("expected verification artifact evidence %q in retrospective: %q", needle, retroOutput)
		}
	}
}

func TestLocalWorkRetrospectiveCompletedIterationUsesLegacyVerificationArtifact(t *testing.T) {
	runDir := t.TempDir()
	iterationDir := localWorkIterationDir(runDir, 1)
	if err := os.MkdirAll(iterationDir, 0o755); err != nil {
		t.Fatalf("mkdir iteration dir: %v", err)
	}
	previousVerificationArtifact := filepath.Join(iterationDir, "verification.json")
	if err := os.WriteFile(previousVerificationArtifact, mustMarshalJSON(localWorkVerificationReport{
		GeneratedAt:         ISOTimeNow(),
		IntegrationIncluded: false,
		Passed:              true,
	}), 0o644); err != nil {
		t.Fatalf("write previous verification artifact: %v", err)
	}

	lines := localWorkRetrospectiveVerificationLines(localWorkManifest{
		Iterations: []localWorkIterationSummary{{
			Iteration:           1,
			Status:              "completed",
			ReviewRoundsUsed:    0,
			VerificationSummary: "verification passed (lint, compile, unit)",
		}},
	}, runDir)
	content := strings.Join(lines, "\n")
	if !strings.Contains(content, "artifact="+previousVerificationArtifact) {
		t.Fatalf("expected completed iteration to cite previous verification artifact %q, got:\n%s", previousVerificationArtifact, content)
	}
	if strings.Contains(content, "verification-round-0-post-hardening.json") {
		t.Fatalf("completed iteration cited missing zero-round artifact instead of verification.json fallback:\n%s", content)
	}
}

func TestLocalWorkRetrospectiveRisksOmitResolvedIterationFailuresForCompletedRun(t *testing.T) {
	lines := localWorkRetrospectiveRiskLines(localWorkManifest{
		Status: "completed",
		Iterations: []localWorkIterationSummary{
			{
				Iteration:                1,
				Status:                   "retrying",
				VerificationFailedStages: []string{"unit"},
				ReviewFindings:           2,
			},
			{
				Iteration:          2,
				Status:             "completed",
				VerificationPassed: true,
				ReviewFindings:     0,
			},
		},
	})
	content := strings.Join(lines, "\n")
	if strings.Contains(content, "Iteration 1 verification failed stages") || strings.Contains(content, "Iteration 1 remaining review findings") {
		t.Fatalf("completed retrospective should not list resolved iteration failures as remaining risks:\n%s", content)
	}
}

func TestLocalWorkRetrospectiveRisksUseLatestUnresolvedIteration(t *testing.T) {
	lines := localWorkRetrospectiveRiskLines(localWorkManifest{
		Status: "failed",
		Iterations: []localWorkIterationSummary{
			{
				Iteration:                1,
				Status:                   "retrying",
				VerificationFailedStages: []string{"unit"},
				ReviewFindings:           2,
			},
			{
				Iteration:                2,
				Status:                   "retrying",
				VerificationFailedStages: []string{"lint"},
				ReviewFindings:           1,
			},
		},
	})
	content := strings.Join(lines, "\n")
	if strings.Contains(content, "Iteration 1") {
		t.Fatalf("remaining risks should focus on the latest unresolved iteration, got:\n%s", content)
	}
	for _, needle := range []string{"Iteration 2 verification failed stages: lint", "Iteration 2 remaining review findings: 1"} {
		if !strings.Contains(content, needle) {
			t.Fatalf("remaining risks missing %q:\n%s", needle, content)
		}
	}
}

func TestLocalWorkResumeAfterValidatorFailureReusesGroupingAndCleansRuntimeState(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	fakeBin := filepath.Join(home, "bin")
	groupCountPath := filepath.Join(home, "group-count")
	validateCountPath := filepath.Join(home, "validate-count")
	hardenedPath := filepath.Join(home, "hardened")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`PAYLOAD="$(cat)"`,
		`inc() {`,
		`  path="$1"`,
		`  count=0`,
		`  if [ -f "$path" ]; then count=$(cat "$path"); fi`,
		`  count=$((count+1))`,
		`  printf '%s' "$count" > "$path"`,
		`}`,
		`case "$PAYLOAD" in`,
		`  *"# NANA Work-local Finding Grouping"*)`,
		`    inc "$FAKE_GROUP_COUNT_PATH"`,
		`    printf '{"groups":[{"group_id":"readme-validation","rationale":"shared readme context","findings":["readme.md|need stronger regression coverage|1|add regression"]}]}\n'`,
		`    ;;`,
		`  *"# NANA Work-local Finding Validation"*)`,
		`    inc "$FAKE_VALIDATE_COUNT_PATH"`,
		`    count=$(cat "$FAKE_VALIDATE_COUNT_PATH")`,
		`    if [ "$count" -le 3 ]; then`,
		`      printf 'not-json\n'`,
		`    else`,
		`      printf '{"group":"readme-validation","decisions":[{"fingerprint":"readme.md|need stronger regression coverage|1|add regression","status":"confirmed","reason":"valid finding"}]}\n'`,
		`    fi`,
		`    ;;`,
		`  *"# NANA Work-local Hardening Pass"*)`,
		`    : > "$FAKE_HARDENED_PATH"`,
		`    printf 'fixed\n' >> README.md`,
		`    printf 'hardening-complete\n'`,
		`    ;;`,
		`  *"Review this local implementation and return JSON only."*)`,
		`    if [ -f "$FAKE_HARDENED_PATH" ]; then`,
		`      printf '{"findings":[]}\n'`,
		`    else`,
		`      printf '{"findings":[{"title":"Need stronger regression coverage","severity":"medium","path":"README.md","line":1,"summary":"add regression","detail":"detail","fix":"fix","rationale":"why"}]}\n'`,
		`    fi`,
		`    ;;`,
		`  *)`,
		`    printf 'implemented\n' >> README.md`,
		`    printf 'fake-codex:%s\n' "$*"`,
		`    ;;`,
		"esac",
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_GROUP_COUNT_PATH", groupCountPath)
	t.Setenv("FAKE_VALIDATE_COUNT_PATH", validateCountPath)
	t.Setenv("FAKE_HARDENED_PATH", hardenedPath)

	startErr := runLocalWorkCommand(repo, []string{"start", "--work-type", workTypeFeature, "--task", "Resume validator failure"})
	if startErr == nil {
		t.Fatal("expected initial start to fail")
	}
	manifest, _ := mustLatestLocalWorkRun(t, repo)
	if _, err := readLocalWorkRuntimeState(manifest.RunID, 1); err != nil {
		t.Fatalf("expected runtime-state after failed run: %v", err)
	}

	outside := t.TempDir()
	resumeOutput, err := captureStdout(t, func() error {
		return runLocalWorkCommand(outside, []string{"resume", "--repo", repo, "--last"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(resume): %v\n%s", err, resumeOutput)
	}
	if !strings.Contains(resumeOutput, "Completed run lw-") {
		t.Fatalf("unexpected resume output: %q", resumeOutput)
	}
	groupCount, err := os.ReadFile(groupCountPath)
	if err != nil {
		t.Fatalf("read group count: %v", err)
	}
	if strings.TrimSpace(string(groupCount)) != "1" {
		t.Fatalf("expected grouping to be reused, got count %q", groupCount)
	}
	if _, err := readLocalWorkRuntimeState(manifest.RunID, 1); !os.IsNotExist(err) {
		t.Fatalf("expected runtime-state cleanup after success, got err=%v", err)
	}
}

func TestLocalWorkFindingHistoryRecordsLifecycle(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	hardenedPath := filepath.Join(home, "hardened")
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`PAYLOAD="$(cat)"`,
		`case "$PAYLOAD" in`,
		`  *"# NANA Work-local Finding Grouping"*)`,
		`    printf '{"groups":[{"group_id":"readme-batch","rationale":"same file","findings":["readme.md|need stronger regression coverage|1|add regression","readme.md|drop outdated note|1|remove note"]}]}\n'`,
		`    ;;`,
		`  *"# NANA Work-local Finding Validation"*)`,
		`    printf '{"group":"readme-batch","decisions":[{"fingerprint":"readme.md|need stronger regression coverage|1|add regression","status":"modified","reason":"narrower wording","replacement":{"title":"Need targeted regression coverage","severity":"medium","path":"README.md","line":1,"summary":"add targeted regression","detail":"detail2","fix":"fix2","rationale":"why2"}},{"fingerprint":"readme.md|drop outdated note|1|remove note","status":"rejected","reason":"not actionable"}]}\n'`,
		`    ;;`,
		`  *"# NANA Work-local Hardening Pass"*)`,
		`    : > "$FAKE_HARDENED_PATH"`,
		`    printf 'fixed\n' >> README.md`,
		`    printf 'hardening-complete\n'`,
		`    ;;`,
		`  *"Review this local implementation and return JSON only."*)`,
		`    if [ -f "$FAKE_HARDENED_PATH" ]; then`,
		`      printf '{"findings":[]}\n'`,
		`    else`,
		`      printf '{"findings":[{"title":"Need stronger regression coverage","severity":"medium","path":"README.md","line":1,"summary":"add regression","detail":"detail","fix":"fix","rationale":"why"},{"title":"Drop outdated note","severity":"low","path":"README.md","line":1,"summary":"remove note","detail":"detailb","fix":"fixb","rationale":"whyb"}]}\n'`,
		`    fi`,
		`    ;;`,
		`  *)`,
		`    printf 'implemented\n' >> README.md`,
		`    printf 'fake-codex:%s\n' "$*"`,
		`    ;;`,
		"esac",
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_HARDENED_PATH", hardenedPath)

	if err := runLocalWorkCommand(repo, []string{"start", "--work-type", workTypeFeature, "--task", "Finding history"}); err != nil {
		t.Fatalf("runLocalWorkCommand(start): %v", err)
	}
	manifest, _ := mustLatestLocalWorkRun(t, repo)
	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	defer store.Close()
	rows, err := store.db.Query(`SELECT event_json FROM finding_history WHERE run_id = ? ORDER BY id`, manifest.RunID)
	if err != nil {
		t.Fatalf("query finding history: %v", err)
	}
	defer rows.Close()
	history := []localWorkFindingHistoryEvent{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scan finding history: %v", err)
		}
		var event localWorkFindingHistoryEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatalf("unmarshal finding history: %v", err)
		}
		history = append(history, event)
	}
	statuses := map[localWorkFindingDecisionStatus]int{}
	for _, event := range history {
		statuses[event.Status]++
	}
	if statuses[localWorkFindingRejected] == 0 || statuses[localWorkFindingModified] == 0 || statuses[localWorkFindingSuperseded] == 0 {
		t.Fatalf("expected rejected/modified/superseded events in history, got %#v", history)
	}
}

func TestLocalWorkReportsAndFiltersPreexistingFindings(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	hardenedPath := filepath.Join(home, "hardened")
	validateCountPath := filepath.Join(home, "validate-count")
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`PAYLOAD="$(cat)"`,
		`inc() {`,
		`  path="$1"`,
		`  count=0`,
		`  if [ -f "$path" ]; then count=$(cat "$path"); fi`,
		`  count=$((count+1))`,
		`  printf '%s' "$count" > "$path"`,
		`}`,
		`case "$PAYLOAD" in`,
		`  *"# NANA Work-local Finding Grouping"*)`,
		`    printf '{"groups":[{"group_id":"readme-batch","rationale":"shared readme context","findings":["readme.md|legacy heading needs cleanup|1|rename heading","readme.md|need stronger regression coverage|2|add regression"]}]}\n'`,
		`    ;;`,
		`  *"# NANA Work-local Finding Validation"*)`,
		`    inc "$FAKE_VALIDATE_COUNT_PATH"`,
		`    case "$PAYLOAD" in`,
		`      *"baseline code: # local work"*)`,
		`        printf '{"group":"readme-batch","decisions":[{"fingerprint":"readme.md|legacy heading needs cleanup|1|rename heading","status":"preexisting","reason":"the baseline code already contains the same heading issue"},{"fingerprint":"readme.md|need stronger regression coverage|2|add regression","status":"confirmed","reason":"new work needs coverage"}]}\n'`,
		`        ;;`,
		`      *)`,
		`        printf 'baseline context missing\n' >&2`,
		`        exit 91`,
		`        ;;`,
		`    esac`,
		`    ;;`,
		`  *"# NANA Work-local Hardening Pass"*)`,
		`    : > "$FAKE_HARDENED_PATH"`,
		`    printf 'added regression note\n' >> README.md`,
		`    printf 'hardening-complete\n'`,
		`    ;;`,
		`  *"Review this local implementation and return JSON only."*)`,
		`    if [ -f "$FAKE_HARDENED_PATH" ]; then`,
		`      printf '{"findings":[{"title":"Legacy heading needs cleanup","severity":"low","path":"README.md","line":1,"summary":"rename heading","detail":"detail-old","fix":"fix-old","rationale":"why-old"}]}\n'`,
		`    else`,
		`      printf '{"findings":[{"title":"Legacy heading needs cleanup","severity":"low","path":"README.md","line":1,"summary":"rename heading","detail":"detail-old","fix":"fix-old","rationale":"why-old"},{"title":"Need stronger regression coverage","severity":"medium","path":"README.md","line":2,"summary":"add regression","detail":"detail-new","fix":"fix-new","rationale":"why-new"}]}\n'`,
		`    fi`,
		`    ;;`,
		`  *)`,
		`    printf 'implemented\n' >> README.md`,
		`    printf 'fake-codex:%s\n' "$*"`,
		`    ;;`,
		"esac",
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_HARDENED_PATH", hardenedPath)
	t.Setenv("FAKE_VALIDATE_COUNT_PATH", validateCountPath)

	output, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"start", "--work-type", workTypeFeature, "--task", "Filter preexisting issues"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(start): %v\n%s", err, output)
	}
	if !strings.Contains(output, "Pre-existing issues excluded from propagation: 1") || !strings.Contains(output, "Legacy heading needs cleanup") {
		t.Fatalf("expected preexisting issue report in output, got %q", output)
	}

	validateCount, err := os.ReadFile(validateCountPath)
	if err != nil {
		t.Fatalf("read validate count: %v", err)
	}
	if strings.TrimSpace(string(validateCount)) != "1" {
		t.Fatalf("expected validator to skip remembered preexisting finding on later rounds, got count %q", validateCount)
	}

	manifest, _ := mustLatestLocalWorkRun(t, repo)
	if len(manifest.PreexistingFindings) != 1 || len(manifest.PreexistingFindingFingerprints) != 1 {
		t.Fatalf("expected remembered preexisting finding in manifest, got %#v", manifest)
	}
	if len(manifest.Iterations) != 1 {
		t.Fatalf("expected single iteration, got %#v", manifest.Iterations)
	}
	summary := manifest.Iterations[0]
	if summary.PreexistingFindings != 1 || summary.SkippedPreexistingFindings != 1 || summary.ReviewFindings != 0 || summary.ReviewRoundsUsed != 1 {
		t.Fatalf("unexpected iteration summary: %#v", summary)
	}

	retroOutput, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"retrospective", "--last"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(retrospective): %v", err)
	}
	if !strings.Contains(retroOutput, "## Pre-existing issues excluded") || !strings.Contains(retroOutput, "Legacy heading needs cleanup") {
		t.Fatalf("expected retrospective to include preexisting issue section, got %q", retroOutput)
	}
}

func TestActiveWorkGoSourcesAvoidLegacySymbolNames(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	for _, relRoot := range []string{"cmd", "internal/gocli"} {
		root := filepath.Join(repoRoot, relRoot)
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, forbidden := range []string{"GithubWorkOn", "githubWorkOn", "WorkLocal("} {
				if strings.Contains(string(content), forbidden) {
					t.Fatalf("active Go source %s contains legacy symbol %q", path, forbidden)
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}

func TestLocalWorkDocsMentionRuntimeStateAndValidationControls(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	readme, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	workLocalDoc, err := os.ReadFile(filepath.Join(repoRoot, "docs", "work.md"))
	if err != nil {
		t.Fatalf("read docs/work.md: %v", err)
	}
	for _, needle := range []string{
		"state.db",
		"--grouping-policy",
		"--validation-parallelism",
		"status --last --json",
		"go-1.25%2B",
	} {
		if !strings.Contains(string(readme), needle) && !strings.Contains(string(workLocalDoc), needle) {
			t.Fatalf("expected docs to mention %q", needle)
		}
	}
	for _, needle := range []string{
		"validator groups retry up to 3 times",
		"run fails and stays resumable",
		"reuse completed grouping/validator work",
		"ignored if they still exist on disk",
		"Go 1.25 baseline",
		"nana work resolve",
		"pushes to the tracked remote when one exists",
	} {
		if !strings.Contains(string(workLocalDoc), needle) {
			t.Fatalf("expected work doc to mention %q", needle)
		}
	}
}

func TestMergedWorkDocsAndHelpAvoidLegacyCommandMentions(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	cleanFiles := []string{
		"README.md",
		"docs/work.md",
		"docs/getting-started.html",
		"docs/agents.html",
		"docs/skills.html",
		"docs/index.html",
		"internal/gocli/github_help.go",
		"internal/gocli/repo.go",
	}
	for _, rel := range cleanFiles {
		content, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		text := string(content)
		if strings.Contains(text, "nana work-on") {
			t.Fatalf("expected %s to avoid `nana work-on` references", rel)
		}
		if strings.Contains(text, "nana work-local") {
			t.Fatalf("expected %s to avoid `nana work-local` references", rel)
		}
	}

	migrationDoc, err := os.ReadFile(filepath.Join(repoRoot, "docs", "work-local.md"))
	if err != nil {
		t.Fatalf("read docs/work-local.md: %v", err)
	}
	migrationText := string(migrationDoc)
	for _, needle := range []string{
		"no longer a supported user-facing command",
		"nana work start --task",
		"~/.nana/work/state.db",
		"[docs/work.md](./work.md)",
	} {
		if !strings.Contains(migrationText, needle) {
			t.Fatalf("expected docs/work-local.md to mention %q", needle)
		}
	}
}

func TestLocalWorkStatusPrefersNewerRuntimeState(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot, err := resolveLocalWorkRepoRoot(repo, "")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	runID := "lw-stale"
	repoID := localWorkRepoID(repoRoot)
	manifest := localWorkManifest{
		Version:               3,
		RunID:                 runID,
		CreatedAt:             ISOTimeNow(),
		UpdatedAt:             ISOTimeNow(),
		Status:                "running",
		CurrentIteration:      1,
		CurrentPhase:          "implement",
		CurrentSubphase:       "implement",
		RepoRoot:              repoRoot,
		RepoName:              filepath.Base(repoRoot),
		RepoID:                repoID,
		SandboxPath:           filepath.Join(home, "sandbox"),
		SandboxRepoPath:       repoRoot,
		InputPath:             filepath.Join(home, "input.md"),
		InputMode:             "task",
		IntegrationPolicy:     "final",
		GroupingPolicy:        localWorkDefaultGroupingPolicy,
		ValidationParallelism: localWorkValidationParallelism,
		MaxIterations:         8,
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	state := localWorkIterationRuntimeState{
		Version:         1,
		Iteration:       1,
		CurrentPhase:    "validation",
		CurrentSubphase: "validation",
		CurrentRound:    1,
	}
	if err := writeLocalWorkRuntimeState(runID, state); err != nil {
		t.Fatalf("write runtime state: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"status", "--last", "--json"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(status --json): %v", err)
	}
	var snapshot struct {
		Phase    string `json:"phase"`
		Subphase string `json:"subphase"`
		Round    int    `json:"round"`
	}
	if err := json.Unmarshal([]byte(output), &snapshot); err != nil {
		t.Fatalf("unmarshal status json: %v\n%s", err, output)
	}
	if snapshot.Phase != "validation" || snapshot.Subphase != "validation" || snapshot.Round != 1 {
		t.Fatalf("expected runtime-state to override stale manifest, got %+v", snapshot)
	}
}

func TestLocalWorkStatusCleansOrphanedRuntimeStateAfterCompletion(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot, err := resolveLocalWorkRepoRoot(repo, "")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	runID := "lw-complete"
	repoID := localWorkRepoID(repoRoot)
	manifest := localWorkManifest{
		Version:               3,
		RunID:                 runID,
		CreatedAt:             ISOTimeNow(),
		UpdatedAt:             ISOTimeNow(),
		CompletedAt:           ISOTimeNow(),
		Status:                "completed",
		CurrentIteration:      1,
		CurrentPhase:          "completed",
		CurrentSubphase:       "completed",
		RepoRoot:              repoRoot,
		RepoName:              filepath.Base(repoRoot),
		RepoID:                repoID,
		SandboxPath:           filepath.Join(home, "sandbox"),
		SandboxRepoPath:       repoRoot,
		InputPath:             filepath.Join(home, "input.md"),
		InputMode:             "task",
		IntegrationPolicy:     "final",
		GroupingPolicy:        localWorkDefaultGroupingPolicy,
		ValidationParallelism: localWorkValidationParallelism,
		MaxIterations:         8,
		Iterations: []localWorkIterationSummary{{
			Iteration:           1,
			StartedAt:           ISOTimeNow(),
			CompletedAt:         ISOTimeNow(),
			Status:              "completed",
			VerificationSummary: "verification passed (lint, compile, unit, integration)",
		}},
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	state := localWorkIterationRuntimeState{
		Version:         1,
		Iteration:       1,
		CurrentPhase:    "validation",
		CurrentSubphase: "validation",
	}
	if err := writeLocalWorkRuntimeState(runID, state); err != nil {
		t.Fatalf("write runtime state: %v", err)
	}

	if _, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"status", "--last"})
	}); err != nil {
		t.Fatalf("runLocalWorkCommand(status): %v", err)
	}
	if _, err := readLocalWorkRuntimeState(runID, 1); err != nil {
		t.Fatalf("expected runtime-state row to remain readable, got err=%v", err)
	}
}

func TestLocalWorkStatusUsesDBInsteadOfLatestRunFile(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot, err := resolveLocalWorkRepoRoot(repo, "")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	manifest := localWorkManifest{
		Version:               3,
		RunID:                 "lw-latest-repair",
		CreatedAt:             ISOTimeNow(),
		UpdatedAt:             ISOTimeNow(),
		Status:                "completed",
		CurrentIteration:      1,
		CurrentPhase:          "completed",
		CurrentSubphase:       "completed",
		RepoRoot:              repoRoot,
		RepoName:              filepath.Base(repoRoot),
		RepoID:                localWorkRepoID(repoRoot),
		SandboxPath:           filepath.Join(home, "sandbox"),
		SandboxRepoPath:       repoRoot,
		InputPath:             filepath.Join(home, "input.md"),
		InputMode:             "task",
		IntegrationPolicy:     "final",
		GroupingPolicy:        localWorkDefaultGroupingPolicy,
		ValidationParallelism: localWorkValidationParallelism,
		MaxIterations:         8,
		Iterations:            []localWorkIterationSummary{{Iteration: 1, Status: "completed", VerificationSummary: "verification passed (lint, compile, unit, integration)"}},
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"status", "--last"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(status --last): %v", err)
	}
	if !strings.Contains(output, manifest.RunID) {
		t.Fatalf("expected DB-backed status output to mention run id, got %q", output)
	}
	if _, err := os.Stat(filepath.Join(localWorkRepoDir(repoRoot), "latest-run.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no latest-run state file, got err=%v", err)
	}
}

func TestLocalWorkManifestWritesSharedWorkRunIndex(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot, err := resolveLocalWorkRepoRoot(repo, "")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	manifest := localWorkManifest{
		Version:               3,
		RunID:                 "lw-index-shared",
		CreatedAt:             ISOTimeNow(),
		UpdatedAt:             ISOTimeNow(),
		Status:                "completed",
		CurrentIteration:      1,
		CurrentPhase:          "completed",
		CurrentSubphase:       "completed",
		RepoRoot:              repoRoot,
		RepoName:              filepath.Base(repoRoot),
		RepoID:                localWorkRepoID(repoRoot),
		SandboxPath:           filepath.Join(home, "sandbox"),
		SandboxRepoPath:       repoRoot,
		InputPath:             filepath.Join(home, "input.md"),
		InputMode:             "task",
		IntegrationPolicy:     "final",
		GroupingPolicy:        localWorkDefaultGroupingPolicy,
		ValidationParallelism: localWorkValidationParallelism,
		MaxIterations:         8,
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	entry, err := readWorkRunIndex(manifest.RunID)
	if err != nil {
		t.Fatalf("read shared index: %v", err)
	}
	if entry.Backend != "local" || entry.RepoKey != manifest.RepoID || entry.RepoRoot != repoRoot {
		t.Fatalf("unexpected shared index entry: %+v", entry)
	}
}

func TestLocalWorkStatusRunIDUsesDBInsteadOfIndexFile(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot, err := resolveLocalWorkRepoRoot(repo, "")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	manifest := localWorkManifest{
		Version:               3,
		RunID:                 "lw-index-repair",
		CreatedAt:             ISOTimeNow(),
		UpdatedAt:             ISOTimeNow(),
		Status:                "completed",
		CurrentIteration:      1,
		CurrentPhase:          "completed",
		CurrentSubphase:       "completed",
		RepoRoot:              repoRoot,
		RepoName:              filepath.Base(repoRoot),
		RepoID:                localWorkRepoID(repoRoot),
		SandboxPath:           filepath.Join(home, "sandbox"),
		SandboxRepoPath:       repoRoot,
		InputPath:             filepath.Join(home, "input.md"),
		InputMode:             "task",
		IntegrationPolicy:     "final",
		GroupingPolicy:        localWorkDefaultGroupingPolicy,
		ValidationParallelism: localWorkValidationParallelism,
		MaxIterations:         8,
		Iterations:            []localWorkIterationSummary{{Iteration: 1, Status: "completed", VerificationSummary: "verification passed (lint, compile, unit, integration)"}},
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	outside := t.TempDir()
	output, err := captureStdout(t, func() error {
		return runLocalWorkCommand(outside, []string{"status", "--run-id", manifest.RunID})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(status --run-id): %v", err)
	}
	if !strings.Contains(output, manifest.RunID) {
		t.Fatalf("expected DB-backed status output to mention run id, got %q", output)
	}
	if _, err := os.Stat(filepath.Join(localWorkHomeRoot(), "index", "runs.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no run-index state file, got err=%v", err)
	}
}

func TestLocalWorkStatusUsesDBInsteadOfRepoMetadataFile(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot, err := resolveLocalWorkRepoRoot(repo, "")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	manifest := localWorkManifest{
		Version:               3,
		RunID:                 "lw-repo-meta",
		CreatedAt:             ISOTimeNow(),
		UpdatedAt:             ISOTimeNow(),
		Status:                "completed",
		CurrentIteration:      1,
		CurrentPhase:          "completed",
		CurrentSubphase:       "completed",
		RepoRoot:              repoRoot,
		RepoName:              filepath.Base(repoRoot),
		RepoID:                localWorkRepoID(repoRoot),
		SandboxPath:           filepath.Join(home, "sandbox"),
		SandboxRepoPath:       repoRoot,
		InputPath:             filepath.Join(home, "input.md"),
		InputMode:             "task",
		IntegrationPolicy:     "final",
		GroupingPolicy:        localWorkDefaultGroupingPolicy,
		ValidationParallelism: localWorkValidationParallelism,
		MaxIterations:         8,
		Iterations:            []localWorkIterationSummary{{Iteration: 1, Status: "completed", VerificationSummary: "verification passed (lint, compile, unit, integration)"}},
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"status", "--last"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(status --last): %v", err)
	}
	if !strings.Contains(output, repoRoot) {
		t.Fatalf("expected DB-backed status output to mention repo root, got %q", output)
	}
	if _, err := os.Stat(filepath.Join(localWorkRepoDir(repoRoot), "repo.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no repo-metadata state file, got err=%v", err)
	}
}

func TestWriteLocalWorkManifestAllowsDBStateWithoutIndexFile(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot, err := resolveLocalWorkRepoRoot(repo, "")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	manifest := localWorkManifest{
		Version:               3,
		RunID:                 "lw-fresh-entry",
		CreatedAt:             ISOTimeNow(),
		UpdatedAt:             ISOTimeNow(),
		Status:                "completed",
		CurrentIteration:      1,
		CurrentPhase:          "completed",
		CurrentSubphase:       "completed",
		RepoRoot:              repoRoot,
		RepoName:              filepath.Base(repoRoot),
		RepoID:                localWorkRepoID(repoRoot),
		SandboxPath:           filepath.Join(home, "sandbox"),
		SandboxRepoPath:       repoRoot,
		InputPath:             filepath.Join(home, "input.md"),
		InputMode:             "task",
		IntegrationPolicy:     "final",
		GroupingPolicy:        localWorkDefaultGroupingPolicy,
		ValidationParallelism: localWorkValidationParallelism,
		MaxIterations:         8,
		Iterations:            []localWorkIterationSummary{{Iteration: 1, Status: "completed", VerificationSummary: "verification passed (lint, compile, unit, integration)"}},
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	loaded, err := readLocalWorkManifestByRunID(manifest.RunID)
	if err != nil {
		t.Fatalf("readLocalWorkManifestByRunID: %v", err)
	}
	if loaded.RunID != manifest.RunID {
		t.Fatalf("expected stored run %s, got %#v", manifest.RunID, loaded)
	}
}

func TestLocalWorkStatusIgnoresLegacyMalformedManifestDuringRunIDLookup(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot, err := resolveLocalWorkRepoRoot(repo, "")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	validManifest := localWorkManifest{
		Version:               3,
		RunID:                 "lw-good-index",
		CreatedAt:             ISOTimeNow(),
		UpdatedAt:             ISOTimeNow(),
		Status:                "completed",
		CurrentIteration:      1,
		CurrentPhase:          "completed",
		CurrentSubphase:       "completed",
		RepoRoot:              repoRoot,
		RepoName:              filepath.Base(repoRoot),
		RepoID:                localWorkRepoID(repoRoot),
		SandboxPath:           filepath.Join(home, "sandbox"),
		SandboxRepoPath:       repoRoot,
		InputPath:             filepath.Join(home, "input.md"),
		InputMode:             "task",
		IntegrationPolicy:     "final",
		GroupingPolicy:        localWorkDefaultGroupingPolicy,
		ValidationParallelism: localWorkValidationParallelism,
		MaxIterations:         8,
		Iterations:            []localWorkIterationSummary{{Iteration: 1, Status: "completed", VerificationSummary: "verification passed (lint, compile, unit, integration)"}},
	}
	if err := writeLocalWorkManifest(validManifest); err != nil {
		t.Fatalf("write valid manifest: %v", err)
	}
	badRunDir := filepath.Join(localWorkRunsDir(repoRoot), "lw-bad-index")
	if err := os.MkdirAll(badRunDir, 0o755); err != nil {
		t.Fatalf("mkdir bad run dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badRunDir, "manifest.json"), []byte("{bad-json\n"), 0o644); err != nil {
		t.Fatalf("write malformed manifest: %v", err)
	}
	outside := t.TempDir()
	output, err := captureStdout(t, func() error {
		return runLocalWorkCommand(outside, []string{"status", "--run-id", validManifest.RunID})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(status --run-id): %v", err)
	}
	if !strings.Contains(output, validManifest.RunID) {
		t.Fatalf("expected valid DB-backed run resolution, got %q", output)
	}
}

func TestLocalWorkStatusIgnoresLegacyMalformedManifestDuringLastLookup(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot, err := resolveLocalWorkRepoRoot(repo, "")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	validManifest := localWorkManifest{
		Version:               3,
		RunID:                 "lw-good-latest",
		CreatedAt:             ISOTimeNow(),
		UpdatedAt:             ISOTimeNow(),
		Status:                "completed",
		CurrentIteration:      1,
		CurrentPhase:          "completed",
		CurrentSubphase:       "completed",
		RepoRoot:              repoRoot,
		RepoName:              filepath.Base(repoRoot),
		RepoID:                localWorkRepoID(repoRoot),
		SandboxPath:           filepath.Join(home, "sandbox"),
		SandboxRepoPath:       repoRoot,
		InputPath:             filepath.Join(home, "input.md"),
		InputMode:             "task",
		IntegrationPolicy:     "final",
		GroupingPolicy:        localWorkDefaultGroupingPolicy,
		ValidationParallelism: localWorkValidationParallelism,
		MaxIterations:         8,
		Iterations:            []localWorkIterationSummary{{Iteration: 1, Status: "completed", VerificationSummary: "verification passed (lint, compile, unit, integration)"}},
	}
	if err := writeLocalWorkManifest(validManifest); err != nil {
		t.Fatalf("write valid manifest: %v", err)
	}
	badRunDir := filepath.Join(localWorkRunsDir(repoRoot), "lw-bad-latest")
	if err := os.MkdirAll(badRunDir, 0o755); err != nil {
		t.Fatalf("mkdir bad run dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badRunDir, "manifest.json"), []byte("{bad-json\n"), 0o644); err != nil {
		t.Fatalf("write malformed manifest: %v", err)
	}
	output, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"status", "--last"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(status --last): %v", err)
	}
	if !strings.Contains(output, validManifest.RunID) {
		t.Fatalf("expected valid DB-backed last-run resolution, got %q", output)
	}
}

func TestLocalWorkStatusFailsOnMalformedRuntimeStateRow(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot, err := resolveLocalWorkRepoRoot(repo, "")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	manifest := localWorkManifest{
		Version:               3,
		RunID:                 "lw-bad-runtime",
		CreatedAt:             ISOTimeNow(),
		UpdatedAt:             ISOTimeNow(),
		Status:                "running",
		CurrentIteration:      1,
		CurrentPhase:          "review",
		CurrentSubphase:       "review",
		RepoRoot:              repoRoot,
		RepoName:              filepath.Base(repoRoot),
		RepoID:                localWorkRepoID(repoRoot),
		SandboxPath:           filepath.Join(home, "sandbox"),
		SandboxRepoPath:       repoRoot,
		InputPath:             filepath.Join(home, "input.md"),
		InputMode:             "task",
		IntegrationPolicy:     "final",
		GroupingPolicy:        localWorkDefaultGroupingPolicy,
		ValidationParallelism: localWorkValidationParallelism,
		MaxIterations:         8,
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`INSERT INTO runtime_states(run_id, iteration, state_json) VALUES(?, ?, ?)`, manifest.RunID, 1, "{bad-json"); err != nil {
		t.Fatalf("insert malformed runtime-state row: %v", err)
	}

	if _, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"status", "--last"})
	}); err == nil {
		t.Fatal("expected malformed runtime-state row to fail status")
	}
}

func TestLocalWorkResumeAfterFailedImplement(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	failOncePath := filepath.Join(home, "fail-once.marker")
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`PAYLOAD="$(cat)"`,
		`case "$PAYLOAD" in`,
		`  *"# NANA Work-local Finding Grouping"*)`,
		`    printf '{"groups":[]}\n'`,
		`    ;;`,
		`  *"Review this local implementation and return JSON only."*)`,
		`    printf '{"findings":[]}\n'`,
		`    ;;`,
		`  *)`,
		`    if [ "${FAKE_CODEX_FAIL_ONCE_PATH:-}" != "" ] && [ ! -f "${FAKE_CODEX_FAIL_ONCE_PATH}" ]; then`,
		`      : > "${FAKE_CODEX_FAIL_ONCE_PATH}"`,
		`      printf 'failing once\n' >&2`,
		`      exit 1`,
		`    fi`,
		`    printf 'implemented\n' >> README.md`,
		`    printf 'fake-codex:%s\n' "$*"`,
		`    ;;`,
		"esac",
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_FAIL_ONCE_PATH", failOncePath)

	startErr := runLocalWorkCommand(repo, []string{"start", "--work-type", workTypeFeature, "--task", "Recover after one failure"})
	if startErr == nil {
		t.Fatal("expected initial start to fail")
	}

	outside := t.TempDir()
	resumeOutput, err := captureStdout(t, func() error {
		return runLocalWorkCommand(outside, []string{"resume", "--repo", repo, "--last"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(resume): %v\n%s", err, resumeOutput)
	}
	if !strings.Contains(resumeOutput, "Resuming run lw-") || !strings.Contains(resumeOutput, "Completed run lw-") {
		t.Fatalf("unexpected resume output: %q", resumeOutput)
	}

	manifest, _ := mustLatestLocalWorkRun(t, repo)
	if manifest.Status != "completed" || len(manifest.Iterations) != 1 {
		t.Fatalf("unexpected resumed manifest: %#v", manifest)
	}
}

func TestLocalWorkResumeAfterFailedImplementUsesExecResume(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	failOncePath := filepath.Join(home, "fail-once.marker")
	commandLogPath := filepath.Join(home, "codex-commands.log")
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`printf '%s\n' "$*" >> "${FAKE_CODEX_LOG_PATH}"`,
		`mkdir -p "$CODEX_HOME/sessions/2026/04/17"`,
		`printf '{"type":"session_meta","payload":{"id":"session-local","timestamp":"2099-01-01T00:00:00Z","cwd":"%s"}}\n' "$PWD" > "$CODEX_HOME/sessions/2026/04/17/rollout-session-local.jsonl"`,
		`PAYLOAD="$(cat)"`,
		`case "$PAYLOAD" in`,
		`  *"# NANA Work-local Finding Grouping"*)`,
		`    printf '{"groups":[]}\n'`,
		`    ;;`,
		`  *"Review this local implementation and return JSON only."*)`,
		`    printf '{"findings":[]}\n'`,
		`    ;;`,
		`  *)`,
		`    if printf '%s' "$*" | grep -q "exec resume session-local"; then`,
		`      printf 'implemented\n' >> README.md`,
		`      printf 'fake-codex:%s\n' "$*"`,
		`      exit 0`,
		`    fi`,
		`    if [ "${FAKE_CODEX_FAIL_ONCE_PATH:-}" != "" ] && [ ! -f "${FAKE_CODEX_FAIL_ONCE_PATH}" ]; then`,
		`      : > "${FAKE_CODEX_FAIL_ONCE_PATH}"`,
		`      printf 'rate limited\n' >&2`,
		`      exit 1`,
		`    fi`,
		`    printf 'implemented\n' >> README.md`,
		`    printf 'fake-codex:%s\n' "$*"`,
		`    ;;`,
		"esac",
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_FAIL_ONCE_PATH", failOncePath)
	t.Setenv("FAKE_CODEX_LOG_PATH", commandLogPath)

	startErr := runLocalWorkCommand(repo, []string{"start", "--work-type", workTypeFeature, "--task", "Recover with exec resume"})
	if startErr == nil {
		t.Fatal("expected initial start to fail")
	}
	resumeOutput, err := captureStdout(t, func() error {
		return runLocalWorkCommand(repo, []string{"resume", "--repo", repo, "--last"})
	})
	if err != nil {
		t.Fatalf("runLocalWorkCommand(resume): %v\n%s", err, resumeOutput)
	}
	commandLog, err := os.ReadFile(commandLogPath)
	if err != nil {
		t.Fatalf("read command log: %v", err)
	}
	if !strings.Contains(string(commandLog), "exec resume session-local") {
		t.Fatalf("expected resume command in log, got %q", string(commandLog))
	}
}

func mustLatestLocalWorkRun(t *testing.T, repo string) (localWorkManifest, string) {
	t.Helper()
	manifest, runDir, err := resolveLocalWorkRun(repo, localWorkRunSelection{UseLast: true})
	if err != nil {
		t.Fatalf("resolveLocalWorkRun(--last): %v", err)
	}
	return manifest, runDir
}

func mustLocalWorkManifestByRunID(t *testing.T, runID string) localWorkManifest {
	t.Helper()
	manifest, err := readLocalWorkManifestByRunID(runID)
	if err != nil {
		t.Fatalf("readLocalWorkManifestByRunID(%s): %v", runID, err)
	}
	return manifest
}

func createLocalWorkRepo(t *testing.T) string {
	t.Helper()
	return createLocalWorkRepoAt(t, t.TempDir())
}

func createLocalWorkRepoAt(t testing.TB, repo string) string {
	t.Helper()
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	writeFile := func(path string, content string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), mode); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	writeFile("README.md", "# local work\n", 0o644)
	writeFile("Makefile", strings.Join([]string{
		"lint:",
		"\t@printf 'lint\\n' >> verify.log",
		"build:",
		"\t@printf 'build\\n' >> verify.log",
		"test:",
		"\t@printf 'test\\n' >> verify.log",
		"test-integration:",
		"\t@printf 'integration\\n' >> verify.log",
		"",
	}, "\n"), 0o644)

	gitEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	for _, args := range [][]string{{"init", "-b", "main"}, {"add", "."}, {"commit", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = gitEnv
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, output)
		}
	}
	return repo
}

func runLocalWorkTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func runLocalWorkTestGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
	return string(output)
}

func configureTestGitInsteadOf(t *testing.T, from string, to string) {
	t.Helper()
	runLocalWorkTestGit(t, "", "config", "--global", fmt.Sprintf("url.%s.insteadOf", to), from)
}

func TestLocalWorkDBInitAddsRepoSlugColumnToWorkRunIndex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	if _, err := store.db.Exec(`DROP TABLE work_run_index`); err != nil {
		_ = store.Close()
		t.Fatalf("drop work_run_index: %v", err)
	}
	if _, err := store.db.Exec(`CREATE TABLE work_run_index (
		run_id TEXT PRIMARY KEY,
		backend TEXT NOT NULL,
		repo_key TEXT,
		repo_root TEXT,
		repo_name TEXT,
		manifest_path TEXT,
		updated_at TEXT NOT NULL,
		target_kind TEXT
	)`); err != nil {
		_ = store.Close()
		t.Fatalf("recreate legacy work_run_index: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO work_run_index(run_id, backend, repo_key, repo_root, repo_name, manifest_path, updated_at, target_kind)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		"legacy-run", "local", "repo-legacy", "/tmp/legacy", "legacy", "", "2026-04-14T00:00:00Z", "local",
	); err != nil {
		_ = store.Close()
		t.Fatalf("insert legacy row: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	store, err = openLocalWorkDB()
	if err != nil {
		t.Fatalf("reopenLocalWorkDB: %v", err)
	}
	defer store.Close()

	rows, err := store.db.Query(`PRAGMA table_info(work_run_index)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()
	foundRepoSlug := false
	for rows.Next() {
		var cid int
		var name string
		var ctype string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name == "repo_slug" {
			foundRepoSlug = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}
	if !foundRepoSlug {
		t.Fatalf("expected repo_slug column to be added")
	}

	legacy, err := readWorkRunIndex("legacy-run")
	if err != nil {
		t.Fatalf("read legacy run index: %v", err)
	}
	if legacy.RunID != "legacy-run" || legacy.RepoSlug != "" {
		t.Fatalf("unexpected legacy run index: %+v", legacy)
	}

	if err := writeWorkRunIndex(workRunIndexEntry{
		RunID:        "new-run",
		Backend:      "github",
		RepoKey:      "acme/widget",
		RepoRoot:     "/tmp/repo",
		RepoName:     "widget",
		RepoSlug:     "acme/widget",
		ManifestPath: "/tmp/manifest.json",
		UpdatedAt:    "2026-04-14T00:01:00Z",
		TargetKind:   "issue",
	}); err != nil {
		t.Fatalf("writeWorkRunIndex: %v", err)
	}
	written, err := readWorkRunIndex("new-run")
	if err != nil {
		t.Fatalf("read new run index: %v", err)
	}
	if written.RepoSlug != "acme/widget" {
		t.Fatalf("expected repo slug to round-trip, got %+v", written)
	}
}

func TestRunLocalWorkCodexPromptPersistsTokenUsageInSQLiteAndManifest(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	countPath := filepath.Join(home, "codex-count.txt")
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`count=0`,
		`if [ -f "${FAKE_CODEX_COUNT_PATH:-}" ]; then count=$(cat "$FAKE_CODEX_COUNT_PATH"); fi`,
		`count=$((count + 1))`,
		`printf '%s' "$count" > "$FAKE_CODEX_COUNT_PATH"`,
		`session_dir="$CODEX_HOME/sessions/2026/04/17"`,
		`mkdir -p "$session_dir"`,
		`cat > "$session_dir/rollout-$count.jsonl" <<EOF`,
		`{"timestamp":"2026-04-17T00:00:00Z","type":"session_meta","payload":{"id":"sess-$count","timestamp":"2026-04-17T00:00:00Z","agent_role":"leader","agent_nickname":"lane-$count"}}`,
		`{"timestamp":"2026-04-17T00:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":10,"output_tokens":20,"reasoning_output_tokens":5,"total_tokens":135}}}}`,
		`EOF`,
		`printf 'fake-codex:%s\n' "$*"`,
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_COUNT_PATH", countPath)

	sandboxPath := filepath.Join(home, "sandbox-success")
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := os.MkdirAll(sandboxRepoPath, 0o755); err != nil {
		t.Fatalf("mkdir sandbox repo: %v", err)
	}
	manifest := localWorkManifest{
		Version:               4,
		RunID:                 "lw-token-success",
		CreatedAt:             "2026-04-17T00:00:00Z",
		UpdatedAt:             "2026-04-17T00:00:00Z",
		Status:                "running",
		RepoRoot:              repo,
		RepoName:              filepath.Base(repo),
		RepoID:                localWorkRepoID(repo),
		SourceBranch:          "main",
		BaselineSHA:           strings.TrimSpace(runLocalWorkTestGitOutput(t, repo, "rev-parse", "HEAD")),
		SandboxPath:           sandboxPath,
		SandboxRepoPath:       sandboxRepoPath,
		InputPath:             filepath.Join(home, "task.md"),
		InputMode:             "task",
		IntegrationPolicy:     "final",
		GroupingPolicy:        localWorkDefaultGroupingPolicy,
		ValidationParallelism: 1,
		MaxIterations:         1,
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}

	result, err := runLocalWorkCodexPrompt(manifest, nil, "Implement token persistence", "leader", filepath.Join(home, "leader-checkpoint.json"))
	if err != nil {
		t.Fatalf("runLocalWorkCodexPrompt: %v", err)
	}
	if !strings.Contains(result.Stdout, "fake-codex:exec -C") {
		t.Fatalf("unexpected prompt stdout: %q", result.Stdout)
	}

	updated, err := readLocalWorkManifestByRunID(manifest.RunID)
	if err != nil {
		t.Fatalf("readLocalWorkManifestByRunID: %v", err)
	}
	if updated.TokenUsage == nil {
		t.Fatalf("expected token usage in manifest, got %#v", updated)
	}
	if updated.TokenUsage.InputTokens != 100 || updated.TokenUsage.CachedInputTokens != 10 || updated.TokenUsage.OutputTokens != 20 || updated.TokenUsage.ReasoningOutputTokens != 5 || updated.TokenUsage.TotalTokens != 135 || updated.TokenUsage.SessionsAccounted != 1 {
		t.Fatalf("unexpected manifest token usage: %#v", updated.TokenUsage)
	}

	totals, err := loadLocalWorkTokenUsageTotalsFromSQLite(manifest.RunID)
	if err != nil {
		t.Fatalf("loadLocalWorkTokenUsageTotalsFromSQLite: %v", err)
	}
	if totals == nil || totals.TotalTokens != 135 || totals.SessionsAccounted != 1 {
		t.Fatalf("unexpected SQLite token usage totals: %#v", totals)
	}

	runDir := localWorkRunDirByID(manifest.RepoID, manifest.RunID)
	if _, err := os.Stat(filepath.Join(runDir, "thread-usage.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no thread-usage artifact, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, threadUsageHistoryArtifactName)); !os.IsNotExist(err) {
		t.Fatalf("expected no thread-usage-history artifact, got err=%v", err)
	}

	stale := manifest
	stale.Status = "completed"
	stale.CompletedAt = "2026-04-17T00:05:00Z"
	stale.UpdatedAt = stale.CompletedAt
	if err := writeLocalWorkManifest(stale); err != nil {
		t.Fatalf("write stale manifest: %v", err)
	}
	preserved, err := readLocalWorkManifestByRunID(manifest.RunID)
	if err != nil {
		t.Fatalf("read preserved manifest: %v", err)
	}
	if preserved.TokenUsage == nil || preserved.TokenUsage.TotalTokens != 135 || preserved.TokenUsage.SessionsAccounted != 1 {
		t.Fatalf("expected token usage to survive stale manifest write, got %#v", preserved.TokenUsage)
	}
}

func TestRunLocalWorkCodexPromptPersistsTokenUsageWhenCodexFails(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	countPath := filepath.Join(home, "codex-count.txt")
	writeExecutable(t, filepath.Join(fakeBin, "codex"), strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		`count=0`,
		`if [ -f "${FAKE_CODEX_COUNT_PATH:-}" ]; then count=$(cat "$FAKE_CODEX_COUNT_PATH"); fi`,
		`count=$((count + 1))`,
		`printf '%s' "$count" > "$FAKE_CODEX_COUNT_PATH"`,
		`session_dir="$CODEX_HOME/sessions/2026/04/17"`,
		`mkdir -p "$session_dir"`,
		`cat > "$session_dir/rollout-$count.jsonl" <<EOF`,
		`{"timestamp":"2026-04-17T00:10:00Z","type":"session_meta","payload":{"id":"fail-$count","timestamp":"2026-04-17T00:10:00Z","agent_role":"leader","agent_nickname":"lane-$count"}}`,
		`{"timestamp":"2026-04-17T00:10:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":200,"cached_input_tokens":20,"output_tokens":40,"reasoning_output_tokens":6,"total_tokens":266}}}}`,
		`EOF`,
		`printf 'codex exploded\n' >&2`,
		`exit 1`,
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_COUNT_PATH", countPath)

	sandboxPath := filepath.Join(home, "sandbox-fail")
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := os.MkdirAll(sandboxRepoPath, 0o755); err != nil {
		t.Fatalf("mkdir sandbox repo: %v", err)
	}
	manifest := localWorkManifest{
		Version:               4,
		RunID:                 "lw-token-fail",
		CreatedAt:             "2026-04-17T00:10:00Z",
		UpdatedAt:             "2026-04-17T00:10:00Z",
		Status:                "running",
		RepoRoot:              repo,
		RepoName:              filepath.Base(repo),
		RepoID:                localWorkRepoID(repo),
		SourceBranch:          "main",
		BaselineSHA:           strings.TrimSpace(runLocalWorkTestGitOutput(t, repo, "rev-parse", "HEAD")),
		SandboxPath:           sandboxPath,
		SandboxRepoPath:       sandboxRepoPath,
		InputPath:             filepath.Join(home, "task.md"),
		InputMode:             "task",
		IntegrationPolicy:     "final",
		GroupingPolicy:        localWorkDefaultGroupingPolicy,
		ValidationParallelism: 1,
		MaxIterations:         1,
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("writeLocalWorkManifest: %v", err)
	}

	if _, err := runLocalWorkCodexPrompt(manifest, nil, "Fail after writing token usage", "leader", filepath.Join(home, "leader-fail-checkpoint.json")); err == nil {
		t.Fatal("expected runLocalWorkCodexPrompt to fail")
	}

	updated, err := readLocalWorkManifestByRunID(manifest.RunID)
	if err != nil {
		t.Fatalf("readLocalWorkManifestByRunID: %v", err)
	}
	if updated.TokenUsage == nil {
		t.Fatalf("expected token usage in failed manifest, got %#v", updated)
	}
	if updated.TokenUsage.TotalTokens != 266 || updated.TokenUsage.InputTokens != 200 || updated.TokenUsage.CachedInputTokens != 20 || updated.TokenUsage.OutputTokens != 40 || updated.TokenUsage.ReasoningOutputTokens != 6 || updated.TokenUsage.SessionsAccounted != 1 {
		t.Fatalf("unexpected failed manifest token usage: %#v", updated.TokenUsage)
	}

	totals, err := loadLocalWorkTokenUsageTotalsFromSQLite(manifest.RunID)
	if err != nil {
		t.Fatalf("loadLocalWorkTokenUsageTotalsFromSQLite: %v", err)
	}
	if totals == nil || totals.TotalTokens != 266 || totals.SessionsAccounted != 1 {
		t.Fatalf("unexpected failed SQLite token usage totals: %#v", totals)
	}

	runDir := localWorkRunDirByID(manifest.RepoID, manifest.RunID)
	if _, err := os.Stat(filepath.Join(runDir, "thread-usage.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no failed thread-usage artifact, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, threadUsageHistoryArtifactName)); !os.IsNotExist(err) {
		t.Fatalf("expected no failed thread-usage-history artifact, got err=%v", err)
	}
}

func writeExecutable(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}
