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

func initVerifyTestRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	cmd := exec.Command("git", "init", "-b", "main")
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	return repo
}

func writeManagedVerificationTestProfile(t *testing.T, repoRoot string, content string) string {
	t.Helper()
	path := managedVerificationPlanPathForRepoRoot(repoRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir managed verification dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write managed verification plan: %v", err)
	}
	return path
}

func TestVerifyHelpDoesNotRequireProfile(t *testing.T) {
	output, err := captureStdout(t, func() error { return Verify(t.TempDir(), []string{"--help"}) })
	if err != nil {
		t.Fatalf("Verify(--help): %v", err)
	}
	if !strings.Contains(output, "nana verify - Run the managed verification plan for an onboarded repo") {
		t.Fatalf("unexpected help output: %q", output)
	}
}

func TestLoadVerificationProfileSearchesParents(t *testing.T) {
	repo := initVerifyTestRepo(t)
	profilePath := writeManagedVerificationTestProfile(t, repo, `{
  "version": 1,
  "name": "test-profile",
  "stages": [{"name":"lint","command":"printf ok"}]
}
`)
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

func TestLoadVerificationProfileDefaultsOmittedVersion(t *testing.T) {
	repo := initVerifyTestRepo(t)
	writeManagedVerificationTestProfile(t, repo, `{
  "name": "implicit-version-profile",
  "stages": [{"name":"lint","command":"printf ok"}]
}
`)

	_, _, profile, err := loadVerificationProfile(repo)
	if err != nil {
		t.Fatalf("loadVerificationProfile(): %v", err)
	}
	if profile.Version != 1 {
		t.Fatalf("omitted version should default to 1, got %#v", profile)
	}
}

func TestVerifyProfileRejectsExplicitNonPositiveVersion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
	}{
		{name: "zero", version: 0},
		{name: "negative", version: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := initVerifyTestRepo(t)
			profile := fmt.Sprintf(`{"version":%d,"stages":[{"name":"noop","command":"true"}]}`+"\n", tc.version)
			writeManagedVerificationTestProfile(t, repo, profile)

			stdout, stderr, err := captureOutput(t, func() error { return Verify(repo, []string{"--profile"}) })
			if err == nil {
				t.Fatalf("Verify(--profile) accepted explicit version %d", tc.version)
			}
			if strings.TrimSpace(stdout) != "" {
				t.Fatalf("invalid profile should not write profile output, got %q", stdout)
			}
			for _, got := range []string{err.Error(), stderr} {
				if !strings.Contains(got, "version must be >= 1") {
					t.Fatalf("expected version validation error for version %d, got err=%v stderr=%q", tc.version, err, stderr)
				}
			}
		})
	}
}

func TestLoadVerificationProfileIncludesChangedScopeGuidance(t *testing.T) {
	repo := initVerifyTestRepo(t)
	writeManagedVerificationTestProfile(t, repo, `{
  "version": 1,
  "name": "changed-scope-profile",
  "stages": [
    {"name":"lint","command":"make lint"},
    {"name":"test","command":"make test"}
  ],
  "changed_scope": {
    "description": "Use targeted checks for local iteration.",
    "full_check": {
      "description": "Release fallback.",
      "command": "make verify"
    },
    "paths": [
      {
        "name": "go",
        "patterns": [" internal/**/*.go ", ""],
        "stages": [" lint ", "test"],
        "checks": [" go test ./internal/gocli -run TestVerify "]
      }
    ]
  }
}
`)

	_, _, profile, err := loadVerificationProfile(repo)
	if err != nil {
		t.Fatalf("loadVerificationProfile(): %v", err)
	}
	if profile.ChangedScope == nil {
		t.Fatalf("expected changed scope guidance: %#v", profile)
	}
	scope := profile.ChangedScope
	if scope.FullCheck.Command != "make verify" {
		t.Fatalf("unexpected full_check command: %#v", scope.FullCheck)
	}
	if len(scope.Paths) != 1 {
		t.Fatalf("expected one changed-scope path, got %#v", scope.Paths)
	}
	pathScope := scope.Paths[0]
	if pathScope.Name != "go" {
		t.Fatalf("unexpected path scope name: %#v", pathScope)
	}
	if strings.Join(pathScope.Patterns, ",") != "internal/**/*.go" {
		t.Fatalf("patterns were not normalized: %#v", pathScope.Patterns)
	}
	if strings.Join(pathScope.Stages, ",") != "lint,test" {
		t.Fatalf("stages were not normalized: %#v", pathScope.Stages)
	}
	if strings.Join(pathScope.Checks, ",") != "go test ./internal/gocli -run TestVerify" {
		t.Fatalf("checks were not normalized: %#v", pathScope.Checks)
	}
}

func TestLoadVerificationProfileRequiresChangedScopeFullCheck(t *testing.T) {
	repo := initVerifyTestRepo(t)
	writeManagedVerificationTestProfile(t, repo, `{
  "version": 1,
  "stages": [{"name":"lint","command":"make lint"}],
  "changed_scope": {
    "paths": [{"name":"docs","patterns":["*.md"],"checks":["git diff --check"]}]
  }
}
`)

	_, _, _, err := loadVerificationProfile(repo)
	if err == nil || !strings.Contains(err.Error(), "changed_scope.full_check is missing command") {
		t.Fatalf("expected missing full_check error, got %v", err)
	}
}

func TestRepositoryVerificationUsesLowMemoryGoTestDefaults(t *testing.T) {
	repoRoot := repoRootFromCaller(t)

	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	makefileText := string(makefile)
	for _, needle := range []string{
		"GO_TEST_PARALLEL ?= 1",
		"GOFLAGS= go test -p=$(GO_TEST_PARALLEL) -run '^$$' ./...",
		"GOFLAGS= go test -p=$(GO_TEST_PARALLEL) ./...",
		"GOFLAGS= go test -p=$(GO_TEST_PARALLEL) -run=^$$ -bench=. -benchmem ./...",
	} {
		if !strings.Contains(makefileText, needle) {
			t.Fatalf("Makefile is missing low-memory Go test guardrail %q", needle)
		}
	}

}

func TestVerifyJSONPreflightReportsMissingProfileRecovery(t *testing.T) {
	repo := initVerifyTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "Makefile"), []byte(strings.Join([]string{
		"lint:",
		"\t@true",
		"build:",
		"\t@true",
		"test:",
		"\t@true",
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}

	stdout, stderr, err := captureOutput(t, func() error { return Verify(repo, []string{"--json"}) })
	if err == nil {
		t.Fatalf("expected missing profile error")
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("missing-profile preflight should not corrupt JSON stdout, got %q", stdout)
	}
	for _, needle := range []string{
		"[verify] preflight: managed verification plan was not found at " + managedVerificationPlanPathForRepoRoot(repo),
		"[verify] repo root: " + repo,
		"[verify] expected managed verification plan path(s):",
		managedVerificationPlanPathForRepoRoot(repo),
		"[verify] run: nana repo onboard --repo " + repo,
	} {
		if !strings.Contains(stderr, needle) {
			t.Fatalf("missing-profile recovery missing %q; stderr:\n%s", needle, stderr)
		}
	}
}

func TestVerifyJSONPreflightReportsInvalidProfileRecovery(t *testing.T) {
	repo := initVerifyTestRepo(t)
	profilePath := writeManagedVerificationTestProfile(t, repo, `{"version":1,"stages":[]}`)
	if err := os.WriteFile(filepath.Join(repo, "Makefile"), []byte("test:\n\t@true\n"), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}

	stdout, stderr, err := captureOutput(t, func() error { return Verify(repo, []string{"--json"}) })
	if err == nil {
		t.Fatalf("expected invalid profile error")
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("invalid-profile preflight should not corrupt JSON stdout, got %q", stdout)
	}
	for _, needle := range []string{
		"[verify] preflight: cannot use managed verification plan " + profilePath,
		"at least one stage is required",
		"[verify] repo root: " + repo,
		managedVerificationPlanPathForRepoRoot(repo),
		"[verify] run: nana repo onboard --repo " + repo,
	} {
		if !strings.Contains(stderr, needle) {
			t.Fatalf("invalid-profile recovery missing %q; stderr:\n%s", needle, stderr)
		}
	}
}

func TestVerifyJSONPreflightIsSilentForValidProfile(t *testing.T) {
	repo := initVerifyTestRepo(t)
	profile := `{
  "version": 1,
  "name": "valid-profile",
  "stages": [{"name":"test","command":"printf ok"}]
}
`
	writeManagedVerificationTestProfile(t, repo, profile)

	stdout, stderr, err := captureOutput(t, func() error { return Verify(repo, []string{"--json"}) })
	if err != nil {
		t.Fatalf("Verify(--json): %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("valid profile should not emit recovery preflight, got stderr:\n%s", stderr)
	}
	var report verificationEvidence
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("unmarshal report: %v\n%s", err, stdout)
	}
	if !report.Passed || report.Profile.Name != "valid-profile" || len(report.Stages) != 1 || report.Stages[0].Output != "ok" {
		t.Fatalf("unexpected valid-profile report: %#v", report)
	}
}

func TestVerifyEmitsJSONEvidence(t *testing.T) {
	repo := initVerifyTestRepo(t)
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
	writeManagedVerificationTestProfile(t, repo, profile)
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

func TestVerifyJSONEvidenceReportsBoundedOutputMetadata(t *testing.T) {
	t.Setenv(verifyOutputLimitEnv, "6")
	repo := initVerifyTestRepo(t)
	profile := `{
  "version": 1,
  "name": "bounded-profile",
  "stages": [
    {"name":"noisy","command":"printf '123456789'; printf 'abcdefghi' >&2"}
  ]
}
`
	writeManagedVerificationTestProfile(t, repo, profile)

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
	repo := initVerifyTestRepo(t)
	profile := verificationProfile{
		Version: 1,
		Name:    "failure-profile",
		Stages: []verificationStageProfile{
			{Name: "lint", Command: "printf lint-failed; exit 7"},
			{Name: "static-analysis", Command: "printf still-ran"},
		},
	}
	report, err := runVerificationProfile(repo, managedVerificationPlanPathForRepoRoot(repo), profile)
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

func TestRunVerificationProfileDoesNotDedupeDuplicateStageCommands(t *testing.T) {
	repo := t.TempDir()
	logPath := filepath.Join(repo, "verify.log")
	if err := os.WriteFile(filepath.Join(repo, "count.sh"), []byte("#!/bin/sh\nprintf 'hit\\n' >> verify.log\n"), 0o755); err != nil {
		t.Fatalf("write count.sh: %v", err)
	}

	report, err := runVerificationProfileWithOptions(repo, filepath.Join(repo, VerifyProfileFile), verificationProfile{
		Version: 1,
		Name:    "dup-profile",
		Stages: []verificationStageProfile{
			{Name: "compile", Command: "./count.sh"},
			{Name: "unit", Command: "./count.sh"},
		},
	}, verificationRunOptions{OutputLimitBytes: 8192})
	if err != nil {
		t.Fatalf("runVerificationProfileWithOptions: %v", err)
	}
	if !report.Passed {
		t.Fatalf("expected verification to pass: %#v", report)
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read verify.log: %v", err)
	}
	if got := strings.Count(string(content), "hit"); got != 2 {
		t.Fatalf("expected duplicate stage commands to run twice, got %d hits in %q", got, content)
	}
}
