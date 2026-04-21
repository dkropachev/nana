package gocli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTelemetrySummaryFiltersCurrentRunAndRedactsRawFields(t *testing.T) {
	cwd := t.TempDir()
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T10:00:00Z","run_id":"run-current","event":"skill_doc_load","skill":"plan","path":"/home/alice/.codex/skills/plan/SKILL.md","raw_args":"SECRET_TOKEN"}`,
		`{"timestamp":"2026-04-20T10:01:00Z","run_id":"run-current","event":"skill_reference_load","skill_name":"plan","path":"/home/alice/.codex/skills/plan/references/checklist.md","output":"SECRET_OUTPUT"}`,
		`{"timestamp":"2026-04-20T10:02:00Z","run_id":"run-current","event":"shell_output_compaction","command_name":"go","argument_count":3,"captured_bytes":1200,"stdout_bytes":1000,"stderr_bytes":200,"stdout_lines":40,"stderr_lines":5,"summary_bytes":120,"summary_lines":4,"arguments":["test","./...","SECRET_TOKEN"],"stdout":"SECRET_OUTPUT"}`,
		`{"timestamp":"2026-04-20T10:32:00Z","run_id":"run-current","event":"shell_output_compaction_failed","command_name":"pytest","argument_count":1,"captured_bytes":800,"stdout_lines":10,"stderr_lines":2,"error":"codex_timeout","stderr":"SECRET_OUTPUT"}`,
		`{"timestamp":"2026-04-20T10:33:00Z","run_id":"other-run","event":"skill_doc_load","skill":"tdd","path":"skills/tdd/SKILL.md"}`,
		`{"timestamp":"2026-04-20T10:34:00Z","run_id":"run-current","event":"unrelated_event","payload":"SECRET_TOKEN"}`,
		`not-json`,
	})

	t.Setenv("NANA_CONTEXT_TELEMETRY_LOG", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "")
	t.Setenv("NANA_WORK_RUN_ID", "run-current")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")

	output, err := captureStdout(t, func() error { return Telemetry(cwd, []string{"summary"}) })
	if err != nil {
		t.Fatalf("Telemetry(summary): %v", err)
	}

	for _, want := range []string{
		"Scope: current run_id=run-current",
		"Events: scanned=7 matched=4 ignored=2 invalid=1",
		"skill_doc_load: 1",
		"skill_reference_load: 1",
		"shell_output_compaction: 1",
		"shell_output_compaction_failed: 1",
		"plan @ skills/plan/SKILL.md: 1 (doc=1 reference=0)",
		"plan @ skills/plan/references/checklist.md: 1 (doc=0 reference=1)",
		"total: 2 (failed=1)",
		"captured: 2000 bytes across 57 lines",
		"frequency: 4.0/hour over 30m0s",
		"commands: go=1, pytest=1 failed=1",
		"raw command arguments and shell output are not emitted",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected %q in telemetry output:\n%s", want, output)
		}
	}
	for _, leaked := range []string{"SECRET_TOKEN", "SECRET_OUTPUT", "/home/alice"} {
		if strings.Contains(output, leaked) {
			t.Fatalf("telemetry summary leaked %q in output:\n%s", leaked, output)
		}
	}
}

func TestTelemetrySummaryJSONAllRuns(t *testing.T) {
	cwd := t.TempDir()
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T11:00:00Z","run_id":"run-a","event":"skill_doc_load","skill":"plan","path":"skills/plan/SKILL.md"}`,
		`{"timestamp":"2026-04-20T11:10:00Z","run_id":"run-b","event":"shell_output_compaction","command_name":"/usr/bin/go","stdout_bytes":400,"stderr_bytes":100,"summary_bytes":50,"summary_lines":2,"stdout":"SECRET_OUTPUT"}`,
	})

	t.Setenv("NANA_CONTEXT_TELEMETRY_LOG", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")

	output, err := captureStdout(t, func() error { return Telemetry(cwd, []string{"summary", "--all", "--json"}) })
	if err != nil {
		t.Fatalf("Telemetry(summary --all --json): %v", err)
	}
	if strings.Contains(output, "SECRET_OUTPUT") {
		t.Fatalf("telemetry JSON leaked raw output: %s", output)
	}

	var report telemetrySummaryReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("unmarshal telemetry JSON: %v\n%s", err, output)
	}
	if !report.Scope.AllRuns || report.Scope.FilteredByRun {
		t.Fatalf("expected all-runs scope, got %+v", report.Scope)
	}
	if report.EventsScanned != 2 || report.EventsMatched != 2 || report.EventsIgnored != 0 {
		t.Fatalf("unexpected event counts: %+v", report)
	}
	if len(report.SkillLoads) != 1 || report.SkillLoads[0].Skill != "plan" || report.SkillLoads[0].Path != "skills/plan/SKILL.md" {
		t.Fatalf("unexpected skill loads: %+v", report.SkillLoads)
	}
	if report.Shell.Compactions != 1 || report.Shell.CapturedBytes != 500 || len(report.Shell.Commands) != 1 || report.Shell.Commands[0].Command != "go" {
		t.Fatalf("unexpected shell summary: %+v", report.Shell)
	}
}

func TestTelemetrySummaryReportsSkillRuntimeCacheStatus(t *testing.T) {
	cwd := t.TempDir()
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T11:00:00Z","run_id":"run-current","event":"skill_doc_load","skill":"autopilot","path":"skills/autopilot/RUNTIME.md","cache":"miss"}`,
		`{"timestamp":"2026-04-20T11:00:01Z","run_id":"run-current","event":"skill_doc_load","skill":"autopilot","path":"skills/autopilot/RUNTIME.md","cache":"hit"}`,
	})

	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "run-current")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")

	output, err := captureStdout(t, func() error { return Telemetry(cwd, []string{"summary"}) })
	if err != nil {
		t.Fatalf("Telemetry(summary): %v", err)
	}
	want := "autopilot @ skills/autopilot/RUNTIME.md: 2 (doc=2 reference=0) cache(hit=1 miss=1)"
	if !strings.Contains(output, want) {
		t.Fatalf("expected cache status %q in output:\n%s", want, output)
	}
}

func TestTelemetrySummaryWarnsWhenSkillLoadBudgetsAreExceededByTurn(t *testing.T) {
	cwd := t.TempDir()
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T11:00:00Z","run_id":"run-current","turn_id":"turn-1","event":"skill_doc_load","skill":"autopilot","path":"skills/autopilot/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T11:00:01Z","run_id":"run-current","turn_id":"turn-1","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T11:00:02Z","run_id":"run-current","turn_id":"turn-1","event":"skill_doc_load","skill":"tdd","path":"skills/tdd/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T11:00:03Z","run_id":"run-current","turn_id":"turn-1","event":"skill_doc_load","skill":"build-fix","path":"skills/build-fix/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T11:00:04Z","run_id":"run-current","turn_id":"turn-1","event":"skill_reference_load","skill":"plan","path":"skills/plan/references/a.md"}`,
		`{"timestamp":"2026-04-20T11:00:05Z","run_id":"run-current","turn_id":"turn-1","event":"skill_reference_load","skill":"plan","path":"skills/plan/references/b.md"}`,
		`{"timestamp":"2026-04-20T11:00:06Z","run_id":"run-current","turn_id":"turn-1","event":"skill_reference_load","skill":"plan","path":"skills/plan/references/c.md"}`,
		`{"timestamp":"2026-04-20T11:00:07Z","run_id":"run-current","turn_id":"turn-1","event":"skill_reference_load","skill":"plan","path":"skills/plan/references/d.md"}`,
		`{"timestamp":"2026-04-20T11:00:08Z","run_id":"run-current","turn_id":"turn-1","event":"skill_reference_load","skill":"plan","path":"skills/plan/references/e.md"}`,
		`{"timestamp":"2026-04-20T11:00:09Z","run_id":"other-run","turn_id":"turn-1","event":"skill_doc_load","skill":"extra","path":"skills/extra/RUNTIME.md"}`,
	})

	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "run-current")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")

	output, err := captureStdout(t, func() error { return Telemetry(cwd, []string{"summary"}) })
	if err != nil {
		t.Fatalf("Telemetry(summary): %v", err)
	}
	for _, want := range []string{
		"Skill/context budget:",
		"limits: skill_doc_loads_per_turn<=3 skill_reference_loads_per_turn<=4",
		"run_id=run-current turn_id=turn-1: skill_doc_loads_per_turn=4 exceeds 3",
		"run_id=run-current turn_id=turn-1: skill_reference_loads_per_turn=5 exceeds 4",
		"avoid bulk-loading reference folders",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected %q in telemetry output:\n%s", want, output)
		}
	}

	jsonOutput, err := captureStdout(t, func() error { return Telemetry(cwd, []string{"summary", "--json"}) })
	if err != nil {
		t.Fatalf("Telemetry(summary --json): %v", err)
	}
	var report telemetrySummaryReport
	if err := json.Unmarshal([]byte(jsonOutput), &report); err != nil {
		t.Fatalf("unmarshal telemetry JSON: %v\n%s", err, jsonOutput)
	}
	if report.Budget.SkillDocLoadsPerTurn != 3 || report.Budget.SkillReferenceLoadsPerTurn != 4 {
		t.Fatalf("unexpected budget in JSON: %+v", report.Budget)
	}
	if len(report.BudgetWarnings) != 2 {
		t.Fatalf("expected two budget warnings, got %+v", report.BudgetWarnings)
	}
	if report.BudgetWarnings[0].RunID != "run-current" || report.BudgetWarnings[0].TurnID != "turn-1" {
		t.Fatalf("warning should preserve safe run/turn ids: %+v", report.BudgetWarnings[0])
	}
}

func TestSafeTelemetryPathOmitsUnsafeAbsolutePaths(t *testing.T) {
	if got := safeTelemetryPath("/tmp/private/reference.md"); got != "" {
		t.Fatalf("expected unsafe absolute path to be omitted, got %q", got)
	}
	if got := safeTelemetryPath("/home/alice/.codex/skills/plan/references/checklist.md"); got != "skills/plan/references/checklist.md" {
		t.Fatalf("expected skill path to be relativized, got %q", got)
	}
	if got := safeTelemetryPath("../outside.md"); got != "" {
		t.Fatalf("expected parent traversal to be omitted, got %q", got)
	}
	if got := safeTelemetryPath("notes/private.md"); got != "" {
		t.Fatalf("expected arbitrary relative path to be omitted, got %q", got)
	}
}

func writeTelemetryLog(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir telemetry log dir: %v", err)
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write telemetry log: %v", err)
	}
}
