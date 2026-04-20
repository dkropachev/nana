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
)

func TestVerifyHelpDoesNotRequireProfile(t *testing.T) {
	output, err := captureStdout(t, func() error { return Verify(t.TempDir(), []string{"--help"}) })
	if err != nil {
		t.Fatalf("Verify(--help): %v", err)
	}
	if !strings.Contains(output, "nana verify - Run the repository-native verification profile") {
		t.Fatalf("unexpected help output: %q", output)
	}
}

func TestLoadVerificationProfileSearchesParents(t *testing.T) {
	repo := t.TempDir()
	profilePath := filepath.Join(repo, VerifyProfileFile)
	if err := os.WriteFile(profilePath, []byte(`{
  "version": 1,
  "name": "test-profile",
  "stages": [{"name":"lint","command":"printf ok"}]
}
`), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	nested := filepath.Join(repo, "cmd", "nana")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	repoRoot, foundPath, profile, err := loadVerificationProfile(nested)
	if err != nil {
		t.Fatalf("loadVerificationProfile(): %v", err)
	}
	if repoRoot != repo {
		t.Fatalf("repoRoot = %q, want %q", repoRoot, repo)
	}
	if foundPath != profilePath {
		t.Fatalf("profilePath = %q, want %q", foundPath, profilePath)
	}
	if profile.Name != "test-profile" || len(profile.Stages) != 1 || profile.Stages[0].Name != "lint" {
		t.Fatalf("unexpected profile: %#v", profile)
	}
}

func TestVerifyEmitsJSONEvidence(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "marker.txt"), []byte("marker\n"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	profile := `{
  "version": 1,
  "name": "json-profile",
  "stages": [
    {"name":"lint","description":"lint stage","command":"printf lint-ok"},
    {"name":"test","description":"test stage","command":"test -f marker.txt && printf test-ok"}
  ]
}
`
	if err := os.WriteFile(filepath.Join(repo, VerifyProfileFile), []byte(profile), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	nested := filepath.Join(repo, "internal")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	output, err := captureStdout(t, func() error { return Verify(nested, []string{"--json"}) })
	if err != nil {
		t.Fatalf("Verify(--json): %v\n%s", err, output)
	}
	var report verificationEvidence
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("unmarshal report: %v\n%s", err, output)
	}
	if !report.Passed || report.RepoRoot != repo || report.Profile.Name != "json-profile" {
		t.Fatalf("unexpected report header: %#v", report)
	}
	if len(report.Stages) != 2 {
		t.Fatalf("expected 2 stages, got %#v", report.Stages)
	}
	if report.Stages[0].Name != "lint" || report.Stages[0].Status != "passed" || report.Stages[0].Output != "lint-ok" {
		t.Fatalf("unexpected lint evidence: %#v", report.Stages[0])
	}
	if report.Stages[1].Name != "test" || report.Stages[1].Status != "passed" || report.Stages[1].Output != "test-ok" {
		t.Fatalf("unexpected test evidence: %#v", report.Stages[1])
	}
}

func TestVerifyDryRunJSONListsPlanWithoutRunningCommands(t *testing.T) {
	repo := t.TempDir()
	profile := `{
  "version": 1,
  "name": "dry-profile",
  "description": "dry run profile",
  "stages": [
    {
      "name":"lint",
      "description":"check formatting",
      "command":"printf should-not-run > marker.txt",
      "dependency_group":"static",
      "expected_artifact":"no gofmt drift",
      "estimated_cost":"low",
      "success_criteria":"gofmt reports no changed files"
    },
    {
      "name":"static-analysis",
      "description":"run vet",
      "command":"printf also-should-not-run > marker.txt",
      "dependency_group":"static",
      "expected_artifact":"go vet is clean",
      "estimated_cost":"medium"
    },
    {
      "name":"test",
      "command":"printf default-metadata > marker.txt"
    }
  ]
}
`
	if err := os.WriteFile(filepath.Join(repo, VerifyProfileFile), []byte(profile), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	output, err := captureStdout(t, func() error { return Verify(repo, []string{"--json", "--dry-run"}) })
	if err != nil {
		t.Fatalf("Verify(--json --dry-run): %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(repo, "marker.txt")); !os.IsNotExist(err) {
		t.Fatalf("dry-run executed a stage command; marker stat error=%v", err)
	}
	var plan verificationPlan
	if err := json.Unmarshal([]byte(output), &plan); err != nil {
		t.Fatalf("unmarshal plan: %v\n%s", err, output)
	}
	if !plan.DryRun || plan.RepoRoot != repo || plan.Profile.Name != "dry-profile" {
		t.Fatalf("unexpected plan header: %#v", plan)
	}
	if plan.ExecutionMode != "profile-order" || plan.SuccessCriteria != "all stages exit with status 0" {
		t.Fatalf("unexpected plan execution metadata: %#v", plan)
	}
	if len(plan.Stages) != 3 {
		t.Fatalf("expected 3 plan stages, got %#v", plan.Stages)
	}
	lint := plan.Stages[0]
	if lint.Name != "lint" || lint.Command != "printf should-not-run > marker.txt" {
		t.Fatalf("unexpected lint stage: %#v", lint)
	}
	if lint.DependencyGroup != "static" || !lint.CanRunInParallel {
		t.Fatalf("expected lint to be marked parallel-eligible in static group: %#v", lint)
	}
	if lint.ExpectedArtifact != "no gofmt drift" || lint.EstimatedCost != "low" {
		t.Fatalf("unexpected lint artifact/cost metadata: %#v", lint)
	}
	if lint.SuccessCriteria != "gofmt reports no changed files" {
		t.Fatalf("unexpected lint success criteria: %#v", lint)
	}
	if !strings.Contains(lint.SelectionReason, "check formatting") {
		t.Fatalf("expected selection reason to include stage description: %#v", lint)
	}
	staticAnalysis := plan.Stages[1]
	if staticAnalysis.DependencyGroup != "static" || !staticAnalysis.CanRunInParallel {
		t.Fatalf("expected static-analysis to be marked parallel-eligible in static group: %#v", staticAnalysis)
	}
	defaulted := plan.Stages[2]
	if defaulted.DependencyGroup != "profile-order-3" || defaulted.CanRunInParallel {
		t.Fatalf("unexpected default dependency group metadata: %#v", defaulted)
	}
	if defaulted.SuccessCriteria != "command exits with status 0" {
		t.Fatalf("unexpected default success criteria: %#v", defaulted)
	}
}

func TestVerifyDryRunHumanOutputListsPlanWithoutRunningCommands(t *testing.T) {
	repo := t.TempDir()
	profile := `{
  "version": 1,
  "name": "human-dry-profile",
  "stages": [
    {
      "name":"lint",
      "description":"check formatting",
      "command":"printf should-not-run > marker.txt",
      "dependency_group":"static",
      "expected_artifact":"no gofmt drift",
      "estimated_cost":"low"
    }
  ]
}
`
	if err := os.WriteFile(filepath.Join(repo, VerifyProfileFile), []byte(profile), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	output, err := captureStdout(t, func() error { return Verify(repo, []string{"--dry-run"}) })
	if err != nil {
		t.Fatalf("Verify(--dry-run): %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(repo, "marker.txt")); !os.IsNotExist(err) {
		t.Fatalf("dry-run executed a stage command; marker stat error=%v", err)
	}
	for _, needle := range []string{
		"[verify] dry-run: human-dry-profile",
		"[verify] execution: profile-order; success: all stages exit with status 0",
		"[verify] lint: printf should-not-run > marker.txt",
		"[verify]   dependency_group: static; parallel: no",
		"[verify]   expected_artifact: no gofmt drift",
		"[verify]   estimated_cost: low",
	} {
		if !strings.Contains(output, needle) {
			t.Fatalf("expected %q in dry-run output:\n%s", needle, output)
		}
	}
}

func TestVerifyJSONEvidenceReportsBoundedOutputMetadata(t *testing.T) {
	t.Setenv(verifyOutputLimitEnv, "6")
	repo := t.TempDir()
	profile := `{
  "version": 1,
  "name": "bounded-profile",
  "stages": [
    {"name":"noisy","command":"printf '123456789'; printf 'abcdefghi' >&2"}
  ]
}
`
	if err := os.WriteFile(filepath.Join(repo, VerifyProfileFile), []byte(profile), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	output, err := captureStdout(t, func() error { return Verify(repo, []string{"--json"}) })
	if err != nil {
		t.Fatalf("Verify(--json): %v\n%s", err, output)
	}
	var report verificationEvidence
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("unmarshal report: %v\n%s", err, output)
	}
	if len(report.Stages) != 1 {
		t.Fatalf("expected one stage, got %#v", report.Stages)
	}
	stage := report.Stages[0]
	if stage.Output != "456789\ndefghi" {
		t.Fatalf("unexpected bounded output: %q", stage.Output)
	}
	if !stage.OutputTruncated {
		t.Fatalf("expected output_truncated for noisy stage: %#v", stage)
	}
	if !stage.StdoutTruncated || !stage.StderrTruncated {
		t.Fatalf("expected both streams to be marked truncated: %#v", stage)
	}
	if stage.OutputBytes != 18 || stage.StdoutBytes != 9 || stage.StderrBytes != 9 || stage.OutputLimitBytes != 6 {
		t.Fatalf("unexpected output metadata: %#v", stage)
	}
}

func TestRunVerificationProfileReportsFailuresAndContinues(t *testing.T) {
	repo := t.TempDir()
	profile := verificationProfile{
		Version: 1,
		Name:    "failure-profile",
		Stages: []verificationStageProfile{
			{Name: "lint", Command: "printf lint-failed; exit 7"},
			{Name: "static-analysis", Command: "printf still-ran"},
		},
	}
	report, err := runVerificationProfile(repo, filepath.Join(repo, VerifyProfileFile), profile)
	if err != nil {
		t.Fatalf("runVerificationProfile(): %v", err)
	}
	if report.Passed {
		t.Fatalf("expected report to fail: %#v", report)
	}
	if strings.Join(report.FailedStages, ",") != "lint" {
		t.Fatalf("unexpected failed stages: %#v", report.FailedStages)
	}
	if len(report.Stages) != 2 {
		t.Fatalf("expected both stages to run, got %#v", report.Stages)
	}
	if report.Stages[0].ExitCode != 7 || report.Stages[0].Status != "failed" || report.Stages[0].Output != "lint-failed" {
		t.Fatalf("unexpected failed stage evidence: %#v", report.Stages[0])
	}
	if report.Stages[1].Status != "passed" || report.Stages[1].Output != "still-ran" {
		t.Fatalf("expected second stage to run: %#v", report.Stages[1])
	}
}

func TestRunVerificationStageLimitsCapturedOutputAndStreamsFullCommandOutput(t *testing.T) {
	repo := t.TempDir()
	stdoutPayload := "stdout-prefix-1234567890"
	stderrPayload := "stderr-prefix-abcdef"
	var streamedStdout strings.Builder
	var streamedStderr strings.Builder

	result, err := runVerificationStageWithOptions(repo, verificationStageProfile{
		Name:    "noisy",
		Command: "printf '" + stdoutPayload + "'; printf '" + stderrPayload + "' >&2",
	}, verificationRunOptions{
		OutputLimitBytes: 8,
		Stdout:           &streamedStdout,
		Stderr:           &streamedStderr,
	})
	if err != nil {
		t.Fatalf("runVerificationStageWithOptions(): %v", err)
	}
	if streamedStdout.String() != stdoutPayload || streamedStderr.String() != stderrPayload {
		t.Fatalf("expected full streaming output, got stdout=%q stderr=%q", streamedStdout.String(), streamedStderr.String())
	}
	if result.Output != "34567890\nx-abcdef" {
		t.Fatalf("unexpected bounded evidence output: %q", result.Output)
	}
	if strings.Contains(result.Output, "stdout-prefix") || strings.Contains(result.Output, "stderr-prefix") {
		t.Fatalf("bounded output retained prefix: %q", result.Output)
	}
	if !result.OutputTruncated {
		t.Fatalf("expected output to be marked truncated: %#v", result)
	}
	if !result.StdoutTruncated || !result.StderrTruncated {
		t.Fatalf("expected both streams to be marked truncated: %#v", result)
	}
	if result.OutputBytes != int64(len(stdoutPayload)+len(stderrPayload)) {
		t.Fatalf("OutputBytes = %d, want %d", result.OutputBytes, len(stdoutPayload)+len(stderrPayload))
	}
	if result.StdoutBytes != int64(len(stdoutPayload)) || result.StderrBytes != int64(len(stderrPayload)) {
		t.Fatalf("unexpected stream byte counts: %#v", result)
	}
	if result.OutputLimitBytes != 8 {
		t.Fatalf("OutputLimitBytes = %d, want 8", result.OutputLimitBytes)
	}
}

func TestBoundedOutputCaptureRetainsTailAcrossManySmallWrites(t *testing.T) {
	const limit = 19
	capture := newBoundedOutputCapture(limit)
	var all strings.Builder

	for i := 0; i < 4096; i++ {
		chunk := fmt.Sprintf("%04d|", i)
		all.WriteString(chunk)
		if n, err := capture.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v; want %d, nil", chunk, n, err, len(chunk))
		}
	}

	fullOutput := all.String()
	want := fullOutput[len(fullOutput)-limit:]
	if got := capture.String(); got != want {
		t.Fatalf("captured tail = %q, want %q", got, want)
	}
	if capture.TotalBytes() != int64(len(fullOutput)) {
		t.Fatalf("TotalBytes() = %d, want %d", capture.TotalBytes(), len(fullOutput))
	}
	if !capture.Truncated() {
		t.Fatalf("expected capture to report truncation after many small writes")
	}
}

func TestRunVerificationStageDoesNotRequireBash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell selection does not apply on Windows")
	}
	repo := t.TempDir()
	fakeBin := filepath.Join(repo, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	fakeBash := filepath.Join(fakeBin, "bash")
	if err := os.WriteFile(fakeBash, []byte("#!/bin/sh\nprintf 'bash should not run' >&2\nexit 99\n"), 0o755); err != nil {
		t.Fatalf("write fake bash: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	result, err := runVerificationStage(repo, verificationStageProfile{Name: "shell", Command: "printf ok"})
	if err != nil {
		t.Fatalf("runVerificationStage(): %v", err)
	}
	if result.ExitCode != 0 || result.Output != "ok" {
		t.Fatalf("unexpected stage result: %#v", result)
	}
}

func TestRunVerificationStageClearsInheritedMakeControlFlags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX Makefile recipe syntax")
	}
	if _, err := exec.LookPath("make"); err != nil {
		t.Skipf("make not available: %v", err)
	}
	repo := t.TempDir()
	makefile := "verify-marker:\n\tprintf ran > marker.txt\n"
	if err := os.WriteFile(filepath.Join(repo, "Makefile"), []byte(makefile), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}
	t.Setenv("MAKEFLAGS", "-n")
	t.Setenv("MFLAGS", "-n")
	t.Setenv("GNUMAKEFLAGS", "-n")
	t.Setenv("MAKEFILES", "missing-injected.mk")

	result, err := runVerificationStage(repo, verificationStageProfile{Name: "make", Command: "make verify-marker"})
	if err != nil {
		t.Fatalf("runVerificationStage(): %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("stage failed despite sanitized make environment: %#v", result)
	}
	content, err := os.ReadFile(filepath.Join(repo, "marker.txt"))
	if err != nil {
		t.Fatalf("expected make recipe to execute despite inherited dry-run flags: %v; result=%#v", err, result)
	}
	if strings.TrimSpace(string(content)) != "ran" {
		t.Fatalf("unexpected marker content: %q", string(content))
	}
}

func TestRunVerificationStageClearsInheritedGOFLAGSSoTestsCannotBeSkipped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX Makefile recipe syntax")
	}
	if _, err := exec.LookPath("make"); err != nil {
		t.Skipf("make not available: %v", err)
	}
	repo := t.TempDir()
	files := map[string]string{
		"go.mod":   "module example.com/verifygoflags\n\ngo 1.20\n",
		"Makefile": "test:\n\tgo test ./...\n",
		"verify_flags_test.go": `package verifygoflags

import "testing"

func TestVerificationRunsTestBodies(t *testing.T) {
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

	result, err := runVerificationStageWithOptions(
		repo,
		verificationStageProfile{Name: "test", Command: "make test"},
		verificationRunOptions{OutputLimitBytes: 8192},
	)
	if err != nil {
		t.Fatalf("runVerificationStage(): %v", err)
	}
	if result.ExitCode == 0 {
		t.Fatalf("expected failing test to run despite inherited GOFLAGS=-run=^$; result=%#v", result)
	}
	if result.Status != "failed" {
		t.Fatalf("Status = %q, want failed; result=%#v", result.Status, result)
	}
	if !strings.Contains(result.Output, "intentional failure proves the test body ran") {
		t.Fatalf("expected output to show the failing test body ran, got %q", result.Output)
	}
}
