package gocli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	options, err := parseLocalWorkStartArgs([]string{"--repo", ".", "--max-iterations", "1"})
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

	if err := runLocalWorkCommand(repo, []string{"start", "--max-iterations", "1"}); err != nil {
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

	err := runLocalWorkCommand(repo, []string{"start", "--task", "do it"})
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
		return runLocalWorkCommand(repo, []string{"start", "--task", "Update the local docs flow"})
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
		return runLocalWorkCommand(repo, []string{"start", "--task", "Trigger the hardening pass"})
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
		`  *"Review role: security-reviewer"*|*"Review role: performance-reviewer"*)`,
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
		return runLocalWorkCommand(repo, []string{"start", "--task", "Trigger final gate hardening"})
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
	if manifest.FinalGateStatus != "passed" || summary.FinalGateStatus != "passed" || len(summary.FinalGateRoleResults) != 3 {
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
	if status.FinalGateStatus != "passed" || len(status.FinalGateRoleResults) != 3 {
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
		return runLocalWorkCommand(repo, []string{"start", "--task", "Dirty source before final apply"})
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

	if err := runLocalWorkCommand(repo, []string{"start", "--task", "Create a new file"}); err != nil {
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

	if err := runLocalWorkCommand(repo, []string{"start", "--task", "Do nothing"}); err != nil {
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
		return runLocalWorkCommand(repo, []string{"start", "--task", "Create generated artifact"})
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

func TestApplyLocalWorkFinalDiffBlocksWhenSourceHeadChanged(t *testing.T) {
	repo := createLocalWorkRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
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
	if err := os.WriteFile(filepath.Join(repo, "source.txt"), []byte("source change\n"), 0o644); err != nil {
		t.Fatalf("write source change: %v", err)
	}
	if err := githubRunGit(repo, "add", "source.txt"); err != nil {
		t.Fatalf("git add source: %v", err)
	}
	if err := githubRunGit(repo, "commit", "-m", "source moved"); err != nil {
		t.Fatalf("git commit source: %v", err)
	}

	result := applyLocalWorkFinalDiff(localWorkManifest{
		RunID:           runID,
		RepoRoot:        repo,
		RepoID:          repoID,
		BaselineSHA:     strings.TrimSpace(baseline),
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
	})
	if result.Status != "blocked-before-apply" || !strings.Contains(result.Error, "source checkout HEAD changed") {
		t.Fatalf("expected HEAD-changed blocker, got %#v", result)
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

	if err := runLocalWorkCommand(repo, []string{"start", "--task", "Trigger json status", "--grouping-policy", "singleton", "--validation-parallelism", "2"}); err != nil {
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

	if err := runLocalWorkCommand(repo, []string{"start", "--task", "Fallback grouping"}); err != nil {
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

	if err := runLocalWorkCommand(repo, []string{"start", "--task", "Path grouping bypass", "--grouping-policy", "path"}); err != nil {
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

	err := runLocalWorkCommand(repo, []string{"start", "--task", "Fail validator"})
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

	startErr := runLocalWorkCommand(repo, []string{"start", "--task", "Resume validator failure"})
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

	if err := runLocalWorkCommand(repo, []string{"start", "--task", "Finding history"}); err != nil {
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
		return runLocalWorkCommand(repo, []string{"start", "--task", "Filter preexisting issues"})
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

	startErr := runLocalWorkCommand(repo, []string{"start", "--task", "Recover after one failure"})
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
