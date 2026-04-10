package gocli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
	_, _, err := readLocalWorkInput(cwd, localWorkStartOptions{PlanFile: "TODO.md"})
	if err == nil || !strings.Contains(err.Error(), "plan file not found:") {
		t.Fatalf("expected explicit missing plan file error, got %v", err)
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

func TestWorkLocalRejectsDirtyRepo(t *testing.T) {
	repo := createLocalWorkRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	err := WorkLocal(repo, []string{"start", "--task", "do it"})
	if err == nil || !strings.Contains(err.Error(), "clean repo") {
		t.Fatalf("expected clean repo error, got %v", err)
	}
}

func TestWorkLocalStartStatusRetrospectiveAndGlobalRunLookup(t *testing.T) {
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
		`    printf 'implemented\n' >> README.md`,
		`    printf 'fake-codex:%s\n' "$*"`,
		`    ;;`,
		"esac",
		"",
	}, "\n"))
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	startOutput, err := captureStdout(t, func() error {
		return WorkLocal(repo, []string{"start", "--task", "Update the local docs flow"})
	})
	if err != nil {
		t.Fatalf("WorkLocal(start): %v\n%s", err, startOutput)
	}
	if !strings.Contains(startOutput, "Starting run lw-") || !strings.Contains(startOutput, "Completed run lw-") {
		t.Fatalf("unexpected start output: %q", startOutput)
	}
	if !strings.Contains(startOutput, "benchmark=0") {
		t.Fatalf("expected benchmark count in onboarding output, got %q", startOutput)
	}
	if _, err := os.Stat(filepath.Join(repo, ".nana", "work-local")); !os.IsNotExist(err) {
		t.Fatalf("expected source repo to stay free of work-local artifacts, got err=%v", err)
	}

	var latest localWorkLatestRunPointer
	if err := readGithubJSON(localWorkLatestRunPath(repo), &latest); err != nil {
		t.Fatalf("read latest run: %v", err)
	}
	repoDir := localWorkRepoDir(repo)
	manifestPath := localWorkManifestPath(repo, latest.RunID)
	var manifest localWorkManifest
	if err := readGithubJSON(manifestPath, &manifest); err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.Status != "completed" {
		t.Fatalf("expected completed manifest, got %#v", manifest)
	}
	if len(manifest.Iterations) != 1 {
		t.Fatalf("expected single iteration, got %#v", manifest.Iterations)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "retrospective.md")); err == nil {
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
		return WorkLocal(outside, []string{"status", "--run-id", latest.RunID})
	})
	if err != nil {
		t.Fatalf("WorkLocal(status --run-id): %v", err)
	}
	if !strings.Contains(statusOutput, "Status: completed") || !strings.Contains(statusOutput, "Run artifacts: "+filepath.Dir(manifestPath)) {
		t.Fatalf("unexpected status output: %q", statusOutput)
	}

	repoScopedStatus, err := captureStdout(t, func() error {
		return WorkLocal(outside, []string{"status", "--repo", repo, "--last"})
	})
	if err != nil {
		t.Fatalf("WorkLocal(status --repo --last): %v", err)
	}
	if !strings.Contains(repoScopedStatus, latest.RunID) {
		t.Fatalf("expected repo-scoped last run in output, got %q", repoScopedStatus)
	}

	globalStatus, err := captureStdout(t, func() error {
		return WorkLocal(outside, []string{"status", "--global-last"})
	})
	if err != nil {
		t.Fatalf("WorkLocal(status --global-last): %v", err)
	}
	if !strings.Contains(globalStatus, latest.RunID) {
		t.Fatalf("expected global last run in output, got %q", globalStatus)
	}

	retroOutput, err := captureStdout(t, func() error {
		return WorkLocal(outside, []string{"retrospective", "--run-id", latest.RunID})
	})
	if err != nil {
		t.Fatalf("WorkLocal(retrospective): %v", err)
	}
	if !strings.Contains(retroOutput, "# NANA Work-local Retrospective") {
		t.Fatalf("unexpected retrospective output: %q", retroOutput)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(manifestPath), "retrospective.md")); err != nil {
		t.Fatalf("expected retrospective artifact: %v", err)
	}

	logsOutput, err := captureStdout(t, func() error {
		return WorkLocal(outside, []string{"logs", "--run-id", latest.RunID, "--tail", "20"})
	})
	if err != nil {
		t.Fatalf("WorkLocal(logs): %v", err)
	}
	if !strings.Contains(logsOutput, "== implement-stdout.log ==") || !strings.Contains(logsOutput, "fake-codex:exec -C") || !strings.Contains(logsOutput, CodexBypassFlag) {
		t.Fatalf("unexpected logs output: %q", logsOutput)
	}
}

func TestWorkLocalRunsHardeningPassWhenReviewFindingsRemain(t *testing.T) {
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
		return WorkLocal(repo, []string{"start", "--task", "Trigger the hardening pass"})
	})
	if err != nil {
		t.Fatalf("WorkLocal(start): %v\n%s", err, output)
	}
	var latest localWorkLatestRunPointer
	if err := readGithubJSON(localWorkLatestRunPath(repo), &latest); err != nil {
		t.Fatalf("read latest run: %v", err)
	}
	iterationDir := localWorkIterationDir(filepath.Dir(localWorkManifestPath(repo, latest.RunID)), 1)
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
	var manifest localWorkManifest
	if err := readGithubJSON(localWorkManifestPath(repo, latest.RunID), &manifest); err != nil {
		t.Fatalf("read manifest: %v", err)
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

func TestWorkLocalResumeAfterFailedImplement(t *testing.T) {
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

	startErr := WorkLocal(repo, []string{"start", "--task", "Recover after one failure"})
	if startErr == nil {
		t.Fatal("expected initial start to fail")
	}

	outside := t.TempDir()
	resumeOutput, err := captureStdout(t, func() error {
		return WorkLocal(outside, []string{"resume", "--repo", repo, "--last"})
	})
	if err != nil {
		t.Fatalf("WorkLocal(resume): %v\n%s", err, resumeOutput)
	}
	if !strings.Contains(resumeOutput, "Resuming run lw-") || !strings.Contains(resumeOutput, "Completed run lw-") {
		t.Fatalf("unexpected resume output: %q", resumeOutput)
	}

	var latest localWorkLatestRunPointer
	if err := readGithubJSON(localWorkLatestRunPath(repo), &latest); err != nil {
		t.Fatalf("read latest run: %v", err)
	}
	var manifest localWorkManifest
	if err := readGithubJSON(localWorkManifestPath(repo, latest.RunID), &manifest); err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.Status != "completed" || len(manifest.Iterations) != 1 {
		t.Fatalf("unexpected resumed manifest: %#v", manifest)
	}
}

func createLocalWorkRepo(t *testing.T) string {
	t.Helper()
	return createLocalWorkRepoAt(t, t.TempDir())
}

func createLocalWorkRepoAt(t *testing.T, repo string) string {
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

func writeExecutable(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}
