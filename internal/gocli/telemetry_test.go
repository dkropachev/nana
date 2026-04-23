package gocli

import (
	"encoding/json"
	"fmt"
	"io"
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

	output, err := captureTelemetryStdout(t, func() error { return Telemetry(cwd, []string{"summary"}) })
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

	output, err := captureTelemetryStdout(t, func() error { return Telemetry(cwd, []string{"summary", "--all", "--json"}) })
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

	output, err := captureTelemetryStdout(t, func() error { return Telemetry(cwd, []string{"summary"}) })
	if err != nil {
		t.Fatalf("Telemetry(summary): %v", err)
	}
	want := "autopilot @ skills/autopilot/RUNTIME.md: 2 (doc=2 reference=0) cache(hit=1 miss=1)"
	if !strings.Contains(output, want) {
		t.Fatalf("expected cache status %q in output:\n%s", want, output)
	}
}

func TestTelemetrySummaryWarnsWhenSkillLoadBudgetExceeded(t *testing.T) {
	cwd := t.TempDir()
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T12:00:00Z","run_id":"run-budget","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md","cache":"miss"}`,
		`{"timestamp":"2026-04-20T12:00:01Z","run_id":"run-budget","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md","cache":"hit"}`,
		`{"timestamp":"2026-04-20T12:00:02Z","run_id":"run-budget","event":"skill_doc_load","skill":"tdd","path":"skills/tdd/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T12:00:03Z","run_id":"run-budget","event":"skill_doc_load","skill":"build-fix","path":"skills/build-fix/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T12:00:04Z","run_id":"run-budget","event":"skill_doc_load","skill":"code-review","path":"skills/code-review/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T12:00:05Z","run_id":"run-budget","event":"skill_reference_load","skill":"plan","path":"skills/plan/references/a.md"}`,
		`{"timestamp":"2026-04-20T12:00:06Z","run_id":"run-budget","event":"skill_reference_load","skill":"plan","path":"skills/plan/references/b.md"}`,
		`{"timestamp":"2026-04-20T12:00:07Z","run_id":"run-budget","event":"skill_reference_load","skill":"plan","path":"skills/plan/references/c.md"}`,
		`{"timestamp":"2026-04-20T12:00:08Z","run_id":"run-budget","event":"skill_reference_load","skill":"plan","path":"skills/plan/references/d.md"}`,
		`{"timestamp":"2026-04-20T12:00:09Z","run_id":"run-budget","event":"skill_reference_load","skill":"plan","path":"skills/plan/references/e.md"}`,
		`{"timestamp":"2026-04-20T12:00:10Z","run_id":"run-budget","event":"skill_reference_load","skill":"plan","path":"skills/plan/references/f.md"}`,
	})

	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "run-budget")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")

	output, err := captureTelemetryStdout(t, func() error { return Telemetry(cwd, []string{"summary"}) })
	if err != nil {
		t.Fatalf("Telemetry(summary): %v", err)
	}
	for _, want := range []string{
		"Skill/reference budget:",
		"docs: 4/3 unique files",
		"references: 6/5 unique files",
		"total: 10/8 unique files",
		"warning: skill runtime docs loaded 4 unique files (budget 3)",
		"warning: skill references loaded 6 unique files (budget 5)",
		"warning: skill/reference context loaded 10 unique files (budget 8)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected budget output %q in:\n%s", want, output)
		}
	}

	jsonOutput, err := captureTelemetryStdout(t, func() error { return Telemetry(cwd, []string{"summary", "--json"}) })
	if err != nil {
		t.Fatalf("Telemetry(summary --json): %v", err)
	}
	var report telemetrySummaryReport
	if err := json.Unmarshal([]byte(jsonOutput), &report); err != nil {
		t.Fatalf("unmarshal telemetry JSON: %v\n%s", err, jsonOutput)
	}
	if report.SkillBudget.DocFiles != 4 || report.SkillBudget.ReferenceFiles != 6 || report.SkillBudget.TotalFiles != 10 {
		t.Fatalf("unexpected skill budget counts: %+v", report.SkillBudget)
	}
	if len(report.SkillBudget.Warnings) != 3 {
		t.Fatalf("expected 3 budget warnings, got %+v", report.SkillBudget.Warnings)
	}
}

func TestTelemetrySummarySkillBudgetDedupesSharedPathsAcrossSkillLabels(t *testing.T) {
	cwd := t.TempDir()
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T12:30:00Z","run_id":"run-dedupe","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T12:30:01Z","run_id":"run-dedupe","event":"skill_doc_load","path":"skills/plan/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T12:30:02Z","run_id":"run-dedupe","event":"skill_reference_load","skill":"plan","path":"skills/plan/references/checklist.md"}`,
		`{"timestamp":"2026-04-20T12:30:03Z","run_id":"run-dedupe","event":"skill_reference_load","skill":"renamed-plan","path":"skills/plan/references/checklist.md"}`,
	})

	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "run-dedupe")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")

	output, err := captureTelemetryStdout(t, func() error { return Telemetry(cwd, []string{"summary"}) })
	if err != nil {
		t.Fatalf("Telemetry(summary): %v", err)
	}
	for _, want := range []string{
		"Skill/reference budget:",
		"docs: 1/3 unique files",
		"references: 1/5 unique files",
		"total: 2/8 unique files",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected deduped budget output %q in:\n%s", want, output)
		}
	}

	jsonOutput, err := captureTelemetryStdout(t, func() error { return Telemetry(cwd, []string{"summary", "--json"}) })
	if err != nil {
		t.Fatalf("Telemetry(summary --json): %v", err)
	}
	var report telemetrySummaryReport
	if err := json.Unmarshal([]byte(jsonOutput), &report); err != nil {
		t.Fatalf("unmarshal telemetry JSON: %v\n%s", err, jsonOutput)
	}
	if report.SkillBudget.DocFiles != 1 || report.SkillBudget.ReferenceFiles != 1 || report.SkillBudget.TotalFiles != 2 {
		t.Fatalf("unexpected deduped skill budget counts: %+v", report.SkillBudget)
	}
}

func TestTelemetrySummarySkillBudgetDedupesSharedPathsAcrossLoadTypes(t *testing.T) {
	cwd := t.TempDir()
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T12:45:00Z","run_id":"run-shared-path","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T12:45:01Z","run_id":"run-shared-path","event":"skill_reference_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
	})

	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "run-shared-path")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")

	output, err := captureTelemetryStdout(t, func() error { return Telemetry(cwd, []string{"summary"}) })
	if err != nil {
		t.Fatalf("Telemetry(summary): %v", err)
	}
	for _, want := range []string{
		"Skill/reference budget:",
		"docs: 1/3 unique files",
		"references: 1/5 unique files",
		"total: 1/8 unique files",
		"warnings: (none)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected shared-path budget output %q in:\n%s", want, output)
		}
	}

	jsonOutput, err := captureTelemetryStdout(t, func() error { return Telemetry(cwd, []string{"summary", "--json"}) })
	if err != nil {
		t.Fatalf("Telemetry(summary --json): %v", err)
	}
	var report telemetrySummaryReport
	if err := json.Unmarshal([]byte(jsonOutput), &report); err != nil {
		t.Fatalf("unmarshal telemetry JSON: %v\n%s", err, jsonOutput)
	}
	if report.SkillBudget.DocFiles != 1 || report.SkillBudget.ReferenceFiles != 1 || report.SkillBudget.TotalFiles != 1 {
		t.Fatalf("unexpected shared-path skill budget counts: %+v", report.SkillBudget)
	}
	if len(report.SkillBudget.Warnings) != 0 {
		t.Fatalf("expected no budget warnings for shared-path union count, got %+v", report.SkillBudget.Warnings)
	}
}

func TestBuildTelemetrySummaryFiltersSkillBudgetByTurn(t *testing.T) {
	cwd := t.TempDir()
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T13:00:00Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:00:01Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"tdd","path":"skills/tdd/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:00:02Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"build-fix","path":"skills/build-fix/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:00:03Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"code-review","path":"skills/code-review/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:05:00Z","run_id":"run-turn","turn_id":"turn-b","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
	})

	reportA, err := buildTelemetrySummary(telemetryOptions{
		View:   "summary",
		RunID:  "run-turn",
		TurnID: "turn-a",
		Log:    logPath,
		CWD:    cwd,
	})
	if err != nil {
		t.Fatalf("buildTelemetrySummary(turn-a): %v", err)
	}
	if !reportA.Scope.FilteredByTurn || reportA.Scope.TurnID != "turn-a" {
		t.Fatalf("expected turn-a scope, got %+v", reportA.Scope)
	}
	if reportA.SkillBudget.DocFiles != 4 || reportA.SkillBudget.TotalFiles != 4 {
		t.Fatalf("unexpected turn-a skill budget counts: %+v", reportA.SkillBudget)
	}
	if len(reportA.SkillBudget.Warnings) != 1 || reportA.SkillBudget.Warnings[0].Budget != "skill_doc_files" {
		t.Fatalf("expected one doc-file warning for turn-a, got %+v", reportA.SkillBudget.Warnings)
	}

	reportB, err := buildTelemetrySummary(telemetryOptions{
		View:   "summary",
		RunID:  "run-turn",
		TurnID: "turn-b",
		Log:    logPath,
		CWD:    cwd,
	})
	if err != nil {
		t.Fatalf("buildTelemetrySummary(turn-b): %v", err)
	}
	if reportB.SkillBudget.DocFiles != 1 || reportB.SkillBudget.TotalFiles != 1 {
		t.Fatalf("unexpected turn-b skill budget counts: %+v", reportB.SkillBudget)
	}
	if len(reportB.SkillBudget.Warnings) != 0 {
		t.Fatalf("expected no turn-b warnings, got %+v", reportB.SkillBudget.Warnings)
	}
}

func TestTelemetrySummaryExposesTurnScopedFilteringViaCLIFlag(t *testing.T) {
	cwd := t.TempDir()
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T13:00:00Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:00:01Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"tdd","path":"skills/tdd/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:00:02Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"build-fix","path":"skills/build-fix/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:00:03Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"code-review","path":"skills/code-review/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:05:00Z","run_id":"run-turn","turn_id":"turn-b","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
	})

	t.Setenv("NANA_CONTEXT_TELEMETRY_LOG", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_TURN_ID", "")
	t.Setenv("NANA_TURN_ID", "")
	t.Setenv("CODEX_TURN_ID", "")

	output, err := captureTelemetryStdout(t, func() error {
		return Telemetry(cwd, []string{"summary", "--run-id", "run-turn", "--turn-id", "turn-b"})
	})
	if err != nil {
		t.Fatalf("Telemetry(summary --run-id --turn-id): %v", err)
	}
	for _, want := range []string{
		"Scope: turn_id=turn-b within run_id=run-turn",
		"docs: 1/3 unique files",
		"references: 0/5 unique files",
		"total: 1/8 unique files",
		"warnings: (none)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected %q in telemetry output:\n%s", want, output)
		}
	}
	if strings.Contains(output, "warning: skill runtime docs loaded 4 unique files (budget 3)") {
		t.Fatalf("turn-scoped CLI summary should not include sibling-turn warning:\n%s", output)
	}
}

func TestTelemetrySummaryJSONFiltersShellCompactionEventsByTurn(t *testing.T) {
	cwd := t.TempDir()
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T14:00:00Z","run_id":"run-turn","turn_id":"turn-a","event":"shell_output_compaction","command_name":"go","argument_count":2,"captured_bytes":1200,"stdout_bytes":1000,"stderr_bytes":200,"stdout_lines":40,"stderr_lines":5,"summary_bytes":120,"summary_lines":4,"summarized":true}`,
		`{"timestamp":"2026-04-20T14:01:00Z","run_id":"run-turn","turn_id":"turn-a","event":"shell_output_compaction_failed","command_name":"pytest","argument_count":1,"captured_bytes":300,"stdout_bytes":200,"stderr_bytes":100,"stdout_lines":8,"stderr_lines":2,"error":"codex_timeout","summarized":false}`,
		`{"timestamp":"2026-04-20T14:02:00Z","run_id":"run-turn","turn_id":"turn-b","event":"shell_output_compaction","command_name":"bash","argument_count":1,"captured_bytes":900,"stdout_bytes":900,"stderr_bytes":0,"stdout_lines":18,"stderr_lines":0,"summary_bytes":90,"summary_lines":3,"summarized":true}`,
		`{"timestamp":"2026-04-20T14:03:00Z","run_id":"other-run","turn_id":"turn-a","event":"shell_output_compaction","command_name":"node","argument_count":1,"captured_bytes":700,"stdout_bytes":500,"stderr_bytes":200,"stdout_lines":10,"stderr_lines":4,"summary_bytes":70,"summary_lines":2,"summarized":true}`,
	})

	t.Setenv("NANA_CONTEXT_TELEMETRY_LOG", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_TURN_ID", "")
	t.Setenv("NANA_TURN_ID", "")
	t.Setenv("CODEX_TURN_ID", "")

	output, err := captureTelemetryStdout(t, func() error {
		return Telemetry(cwd, []string{"summary", "--run-id", "run-turn", "--turn-id", "turn-a", "--json"})
	})
	if err != nil {
		t.Fatalf("Telemetry(summary --run-id --turn-id --json): %v", err)
	}

	var report telemetrySummaryReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("unmarshal telemetry JSON: %v\n%s", err, output)
	}
	if !report.Scope.FilteredByRun || report.Scope.RunID != "run-turn" || !report.Scope.FilteredByTurn || report.Scope.TurnID != "turn-a" {
		t.Fatalf("unexpected scope: %+v", report.Scope)
	}
	if report.EventsScanned != 4 || report.EventsMatched != 2 || report.EventsIgnored != 2 || report.InvalidLines != 0 {
		t.Fatalf("unexpected event counts: %+v", report)
	}
	if report.Shell.Compactions != 2 || report.Shell.Failed != 1 {
		t.Fatalf("unexpected shell compaction totals: %+v", report.Shell)
	}
	if report.Shell.CapturedBytes != 1500 || report.Shell.StdoutBytes != 1200 || report.Shell.StderrBytes != 300 {
		t.Fatalf("unexpected shell byte totals: %+v", report.Shell)
	}
	if report.Shell.StdoutLines != 48 || report.Shell.StderrLines != 7 || report.Shell.SummaryBytes != 120 || report.Shell.SummaryLines != 4 {
		t.Fatalf("unexpected shell line/summary totals: %+v", report.Shell)
	}
	if len(report.Shell.Commands) != 2 {
		t.Fatalf("expected two turn-scoped shell commands, got %+v", report.Shell.Commands)
	}
	if report.Shell.Commands[0].Command != "go" || report.Shell.Commands[0].Count != 1 || report.Shell.Commands[0].Failed != 0 {
		t.Fatalf("unexpected first command summary: %+v", report.Shell.Commands[0])
	}
	if report.Shell.Commands[1].Command != "pytest" || report.Shell.Commands[1].Count != 1 || report.Shell.Commands[1].Failed != 1 {
		t.Fatalf("unexpected second command summary: %+v", report.Shell.Commands[1])
	}
}

func TestTelemetrySummaryDoesNotLabelExplicitTurnAsCurrentWhenRunComesFromEnvironment(t *testing.T) {
	cwd := t.TempDir()
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T13:00:00Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:05:00Z","run_id":"run-turn","turn_id":"turn-b","event":"skill_doc_load","skill":"tdd","path":"skills/tdd/RUNTIME.md"}`,
	})

	t.Setenv("NANA_CONTEXT_TELEMETRY_LOG", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "run-turn")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_TURN_ID", "turn-a")
	t.Setenv("NANA_TURN_ID", "")
	t.Setenv("CODEX_TURN_ID", "")

	output, err := captureTelemetryStdout(t, func() error {
		return Telemetry(cwd, []string{"summary", "--turn-id", "turn-b"})
	})
	if err != nil {
		t.Fatalf("Telemetry(summary --turn-id): %v", err)
	}
	if !strings.Contains(output, "Scope: turn_id=turn-b within run_id=run-turn") {
		t.Fatalf("expected explicit turn label in telemetry output:\n%s", output)
	}
	if strings.Contains(output, "Scope: current turn_id=turn-b within run_id=run-turn") {
		t.Fatalf("explicit turn should not be labeled current:\n%s", output)
	}
}

func TestTelemetrySummaryDoesNotInheritEnvironmentTurnForExplicitRunID(t *testing.T) {
	cwd := t.TempDir()
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T13:00:00Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:00:01Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"tdd","path":"skills/tdd/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:00:02Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"build-fix","path":"skills/build-fix/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:00:03Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"code-review","path":"skills/code-review/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:05:00Z","run_id":"run-turn","turn_id":"turn-b","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
	})

	t.Setenv("NANA_CONTEXT_TELEMETRY_LOG", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_TURN_ID", "turn-b")
	t.Setenv("NANA_TURN_ID", "")
	t.Setenv("CODEX_TURN_ID", "")

	output, err := captureTelemetryStdout(t, func() error {
		return Telemetry(cwd, []string{"summary", "--run-id", "run-turn"})
	})
	if err != nil {
		t.Fatalf("Telemetry(summary --run-id): %v", err)
	}
	for _, want := range []string{
		"Scope: run_id=run-turn",
		"Events: scanned=5 matched=5 ignored=0 invalid=0",
		"docs: 4/3 unique files",
		"references: 0/5 unique files",
		"total: 4/8 unique files",
		"warning: skill runtime docs loaded 4 unique files (budget 3)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected %q in telemetry output:\n%s", want, output)
		}
	}
	if strings.Contains(output, "Scope: turn_id=turn-b within run_id=run-turn") {
		t.Fatalf("explicit run summary should not inherit the environment turn:\n%s", output)
	}
}

func TestTelemetrySummaryUsesCurrentTurnIDFromEnvironment(t *testing.T) {
	cwd := t.TempDir()
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T13:00:00Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:00:01Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"tdd","path":"skills/tdd/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:00:02Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"build-fix","path":"skills/build-fix/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:00:03Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"code-review","path":"skills/code-review/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:05:00Z","run_id":"run-turn","turn_id":"turn-b","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
	})

	t.Setenv("NANA_CONTEXT_TELEMETRY_LOG", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "run-turn")
	t.Setenv("NANA_CONTEXT_TELEMETRY_TURN_ID", "turn-b")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")
	t.Setenv("NANA_TURN_ID", "")
	t.Setenv("CODEX_TURN_ID", "")

	output, err := captureTelemetryStdout(t, func() error {
		return Telemetry(cwd, []string{"summary"})
	})
	if err != nil {
		t.Fatalf("Telemetry(summary): %v", err)
	}
	for _, want := range []string{
		"Scope: current turn_id=turn-b within run_id=run-turn",
		"docs: 1/3 unique files",
		"references: 0/5 unique files",
		"total: 1/8 unique files",
		"warnings: (none)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected %q in telemetry output:\n%s", want, output)
		}
	}
	if strings.Contains(output, "warning: skill runtime docs loaded 4 unique files (budget 3)") {
		t.Fatalf("turn-scoped env summary should not include sibling-turn warning:\n%s", output)
	}
}

func TestTelemetrySummaryRejectsTurnScopedFilteringWithoutRunScope(t *testing.T) {
	cwd := t.TempDir()

	t.Setenv("NANA_CONTEXT_TELEMETRY_LOG", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_TURN_ID", "")
	t.Setenv("NANA_TURN_ID", "")
	t.Setenv("CODEX_TURN_ID", "")

	err := Telemetry(cwd, []string{"summary", "--turn-id", "turn-b"})
	if err == nil {
		t.Fatal("expected telemetry summary to reject --turn-id without a run scope")
	}
	if !strings.Contains(err.Error(), "--turn-id requires --run-id or a current run id in the environment") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCurrentSkillContextBudgetReportWithScopeUsesCachedOffsetForAppends(t *testing.T) {
	cwd := t.TempDir()
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T13:00:00Z","run_id":"run-budget","turn_id":"turn-a","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:00:01Z","run_id":"run-budget","turn_id":"turn-a","event":"skill_doc_load","skill":"tdd","path":"skills/tdd/RUNTIME.md"}`,
	})

	reportA, ok := currentSkillContextBudgetReportWithScope(cwd, contextTelemetryScope{RunID: "run-budget", TurnID: "turn-a"})
	if !ok {
		t.Fatal("expected turn-a skill context budget report")
	}
	if reportA.SkillBudget.DocFiles != 2 || reportA.SkillBudget.TotalFiles != 2 {
		t.Fatalf("unexpected turn-a skill budget counts: %+v", reportA.SkillBudget)
	}

	infoBeforeAppend, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat telemetry log before append: %v", err)
	}
	appendTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T13:05:00Z","run_id":"run-budget","turn_id":"turn-a","event":"skill_doc_load","skill":"build-fix","path":"skills/build-fix/RUNTIME.md"}`,
	})

	originalOpen := openTelemetrySkillBudgetLog
	defer func() {
		openTelemetrySkillBudgetLog = originalOpen
	}()

	tracked := &trackedTelemetryBudgetLogOpen{}
	openTelemetrySkillBudgetLog = func(path string) (telemetryBudgetLogReadSeeker, error) {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		return &trackedTelemetryBudgetLogFile{
			File:    file,
			tracker: tracked,
		}, nil
	}

	reportB, ok := currentSkillContextBudgetReportWithScope(cwd, contextTelemetryScope{RunID: "run-budget", TurnID: "turn-a"})
	if !ok {
		t.Fatal("expected turn-a skill context budget report after append")
	}
	if reportB.SkillBudget.DocFiles != 3 || reportB.SkillBudget.TotalFiles != 3 {
		t.Fatalf("unexpected turn-a skill budget counts after append: %+v", reportB.SkillBudget)
	}
	if tracked.seekCalls == 0 {
		t.Fatal("expected cached telemetry reader to seek to the append offset")
	}
	if tracked.firstSeekWhence != io.SeekStart || tracked.firstSeekOffset != infoBeforeAppend.Size() {
		t.Fatalf("expected first cache seek to start at offset %d, got whence=%d offset=%d", infoBeforeAppend.Size(), tracked.firstSeekWhence, tracked.firstSeekOffset)
	}

	infoAfterAppend, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat telemetry log after append: %v", err)
	}
	scope := telemetryScope{RunID: "run-budget", TurnID: "turn-a", FilteredByRun: true, FilteredByTurn: true}
	cache := readTelemetrySkillBudgetCache(telemetrySkillBudgetCachePathForScope(cwd, logPath, scope))
	if cache.Offset != infoAfterAppend.Size() {
		t.Fatalf("expected cache offset %d after append, got %+v", infoAfterAppend.Size(), cache)
	}
}

func TestCurrentSkillContextBudgetReportWithScopeCachesOnlyRequestedTurn(t *testing.T) {
	cwd := t.TempDir()
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	lines := []string{}
	for index := 0; index < 25; index++ {
		lines = append(lines, fmt.Sprintf(
			`{"timestamp":"2026-04-20T12:00:%02dZ","run_id":"run-history-%d","turn_id":"turn-history-%d","event":"skill_doc_load","skill":"history-%d","path":"skills/history-%d/RUNTIME.md"}`,
			index, index, index, index, index,
		))
	}
	lines = append(lines,
		`{"timestamp":"2026-04-20T12:05:00Z","run_id":"run-current","turn_id":"turn-current","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
	)
	writeTelemetryLog(t, logPath, lines)

	legacyCachePath := telemetrySkillBudgetCachePath(cwd, logPath)
	if err := writeTelemetrySkillBudgetCache(legacyCachePath, telemetrySkillBudgetCache{
		LogPath: filepath.Clean(logPath),
		Runs: map[string][]telemetrySkillSummary{
			"run-history-legacy": {{Skill: "legacy", Path: "skills/legacy/RUNTIME.md", Count: 1, DocLoads: 1}},
		},
	}); err != nil {
		t.Fatalf("write legacy telemetry skill budget cache: %v", err)
	}

	report, ok := currentSkillContextBudgetReportWithScope(cwd, contextTelemetryScope{RunID: "run-current", TurnID: "turn-current"})
	if !ok {
		t.Fatal("expected current turn skill context budget report")
	}
	if report.SkillBudget.DocFiles != 1 || report.SkillBudget.TotalFiles != 1 {
		t.Fatalf("unexpected current turn skill budget counts: %+v", report.SkillBudget)
	}
	if _, err := os.Stat(legacyCachePath); !os.IsNotExist(err) {
		t.Fatalf("expected legacy all-scope cache to be removed, stat err=%v", err)
	}

	scope := telemetryScope{RunID: "run-current", TurnID: "turn-current", FilteredByRun: true, FilteredByTurn: true}
	cachePath := telemetrySkillBudgetCachePathForScope(cwd, logPath, scope)
	cache := readTelemetrySkillBudgetCache(cachePath)
	if len(cache.Runs) != 0 {
		t.Fatalf("turn-scoped launch cache should not retain run buckets, got %+v", cache.Runs)
	}
	if len(cache.Turns) != 1 || len(cache.Turns["run-current"]) != 1 || len(cache.Turns["run-current"]["turn-current"]) != 1 {
		t.Fatalf("turn-scoped launch cache should retain only the requested turn, got %+v", cache.Turns)
	}
	raw, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("read scoped telemetry skill budget cache: %v", err)
	}
	if strings.Contains(string(raw), "run-history-") || strings.Contains(string(raw), "turn-history-") {
		t.Fatalf("scoped launch cache retained historical buckets: %s", raw)
	}
}

func TestCurrentSkillContextBudgetReportWithScopeResetsCacheAfterLogRewrite(t *testing.T) {
	cwd := t.TempDir()
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T13:00:00Z","run_id":"run-budget","turn_id":"turn-old","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:00:01Z","run_id":"run-budget","turn_id":"turn-old","event":"skill_doc_load","skill":"tdd","path":"skills/tdd/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:00:02Z","run_id":"run-budget","turn_id":"turn-old","event":"skill_doc_load","skill":"build-fix","path":"skills/build-fix/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:00:03Z","run_id":"run-budget","turn_id":"turn-old","event":"skill_doc_load","skill":"code-review","path":"skills/code-review/RUNTIME.md"}`,
	})

	reportOld, ok := currentSkillContextBudgetReportWithScope(cwd, contextTelemetryScope{RunID: "run-budget", TurnID: "turn-old"})
	if !ok {
		t.Fatal("expected turn-old skill context budget report")
	}
	if reportOld.SkillBudget.DocFiles != 4 {
		t.Fatalf("unexpected turn-old skill budget counts: %+v", reportOld.SkillBudget)
	}

	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T13:10:00Z","run_id":"run-budget","turn_id":"turn-new","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
	})

	reportNew, ok := currentSkillContextBudgetReportWithScope(cwd, contextTelemetryScope{RunID: "run-budget", TurnID: "turn-new"})
	if !ok {
		t.Fatal("expected turn-new skill context budget report")
	}
	if reportNew.SkillBudget.DocFiles != 1 || reportNew.SkillBudget.TotalFiles != 1 {
		t.Fatalf("unexpected turn-new skill budget counts after rewrite: %+v", reportNew.SkillBudget)
	}
	if len(reportNew.SkillBudget.Warnings) != 0 {
		t.Fatalf("expected no warnings after rewrite reset, got %+v", reportNew.SkillBudget.Warnings)
	}

	newScope := telemetryScope{RunID: "run-budget", TurnID: "turn-new", FilteredByRun: true, FilteredByTurn: true}
	cache := readTelemetrySkillBudgetCache(telemetrySkillBudgetCachePathForScope(cwd, logPath, newScope))
	oldRows := telemetrySkillBudgetCacheSkillLoads(cache, telemetryScope{
		RunID:          "run-budget",
		TurnID:         "turn-old",
		FilteredByRun:  true,
		FilteredByTurn: true,
	})
	if len(oldRows) != 0 {
		t.Fatalf("expected rewritten log cache to discard stale turn-old rows, got %+v", oldRows)
	}
}

func TestCurrentSkillContextBudgetReportWithScopeResetsCacheAfterLargerLogRewrite(t *testing.T) {
	cwd := t.TempDir()
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T13:00:00Z","run_id":"run-budget","turn_id":"turn-old","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
	})

	reportOld, ok := currentSkillContextBudgetReportWithScope(cwd, contextTelemetryScope{RunID: "run-budget", TurnID: "turn-old"})
	if !ok {
		t.Fatal("expected turn-old skill context budget report")
	}
	if reportOld.SkillBudget.DocFiles != 1 || reportOld.SkillBudget.TotalFiles != 1 {
		t.Fatalf("unexpected turn-old skill budget counts: %+v", reportOld.SkillBudget)
	}

	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T13:10:00Z","run_id":"run-budget","turn_id":"turn-new","event":"skill_doc_load","skill":"tdd","path":"skills/tdd/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:10:01Z","run_id":"run-budget","turn_id":"turn-new","event":"skill_doc_load","skill":"build-fix","path":"skills/build-fix/RUNTIME.md"}`,
	})

	reportNew, ok := currentSkillContextBudgetReportWithScope(cwd, contextTelemetryScope{RunID: "run-budget", TurnID: "turn-new"})
	if !ok {
		t.Fatal("expected turn-new skill context budget report")
	}
	if reportNew.SkillBudget.DocFiles != 2 || reportNew.SkillBudget.TotalFiles != 2 {
		t.Fatalf("unexpected turn-new skill budget counts after larger rewrite: %+v", reportNew.SkillBudget)
	}
	if len(reportNew.SkillBudget.Warnings) != 0 {
		t.Fatalf("expected no warnings after larger rewrite reset, got %+v", reportNew.SkillBudget.Warnings)
	}

	newScope := telemetryScope{RunID: "run-budget", TurnID: "turn-new", FilteredByRun: true, FilteredByTurn: true}
	cache := readTelemetrySkillBudgetCache(telemetrySkillBudgetCachePathForScope(cwd, logPath, newScope))
	oldRows := telemetrySkillBudgetCacheSkillLoads(cache, telemetryScope{
		RunID:          "run-budget",
		TurnID:         "turn-old",
		FilteredByRun:  true,
		FilteredByTurn: true,
	})
	if len(oldRows) != 0 {
		t.Fatalf("expected larger rewritten log cache to discard stale turn-old rows, got %+v", oldRows)
	}
}

func TestCurrentSkillContextBudgetReportWithScopeReplaysLineAfterPartialWriteCompletes(t *testing.T) {
	cwd := t.TempDir()
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T13:00:00Z","run_id":"run-budget","turn_id":"turn-a","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
	})

	reportA, ok := currentSkillContextBudgetReportWithScope(cwd, contextTelemetryScope{RunID: "run-budget", TurnID: "turn-a"})
	if !ok {
		t.Fatal("expected turn-a skill context budget report")
	}
	if reportA.SkillBudget.DocFiles != 1 || reportA.SkillBudget.TotalFiles != 1 {
		t.Fatalf("unexpected turn-a skill budget counts: %+v", reportA.SkillBudget)
	}

	infoBeforePartial, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat telemetry log before partial append: %v", err)
	}
	appendTelemetryRaw(t, logPath, `{"timestamp":"2026-04-20T13:05:00Z","run_id":"run-budget","turn_id":"turn-b","event":"skill_doc_load","skill":"tdd",`)

	reportPartial, ok := currentSkillContextBudgetReportWithScope(cwd, contextTelemetryScope{RunID: "run-budget", TurnID: "turn-b"})
	if !ok {
		t.Fatal("expected turn-b skill context budget report after partial append")
	}
	if reportPartial.SkillBudget.DocFiles != 0 || reportPartial.SkillBudget.TotalFiles != 0 {
		t.Fatalf("partial telemetry line should not be counted yet: %+v", reportPartial.SkillBudget)
	}

	turnBScope := telemetryScope{RunID: "run-budget", TurnID: "turn-b", FilteredByRun: true, FilteredByTurn: true}
	cacheAfterPartial := readTelemetrySkillBudgetCache(telemetrySkillBudgetCachePathForScope(cwd, logPath, turnBScope))
	if cacheAfterPartial.Offset != infoBeforePartial.Size() {
		t.Fatalf("expected cache offset to stay at %d while last line is partial, got %+v", infoBeforePartial.Size(), cacheAfterPartial)
	}

	appendTelemetryRaw(t, logPath, `"path":"skills/tdd/RUNTIME.md"}`+"\n")

	reportComplete, ok := currentSkillContextBudgetReportWithScope(cwd, contextTelemetryScope{RunID: "run-budget", TurnID: "turn-b"})
	if !ok {
		t.Fatal("expected turn-b skill context budget report after partial line completion")
	}
	if reportComplete.SkillBudget.DocFiles != 1 || reportComplete.SkillBudget.TotalFiles != 1 {
		t.Fatalf("completed telemetry line should be replayed and counted: %+v", reportComplete.SkillBudget)
	}

	infoAfterCompletion, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat telemetry log after line completion: %v", err)
	}
	cacheAfterCompletion := readTelemetrySkillBudgetCache(telemetrySkillBudgetCachePathForScope(cwd, logPath, turnBScope))
	if cacheAfterCompletion.Offset != infoAfterCompletion.Size() {
		t.Fatalf("expected cache offset %d after line completion, got %+v", infoAfterCompletion.Size(), cacheAfterCompletion)
	}
}

func TestCurrentSkillContextBudgetAdvisoryBlockDoesNotFallBackToRunWarningsWhenTurnIsClean(t *testing.T) {
	cwd := t.TempDir()
	logPath := filepath.Join(cwd, ".nana", "logs", "context-telemetry.ndjson")
	writeTelemetryLog(t, logPath, []string{
		`{"timestamp":"2026-04-20T13:00:00Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:00:01Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"tdd","path":"skills/tdd/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:00:02Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"build-fix","path":"skills/build-fix/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:00:03Z","run_id":"run-turn","turn_id":"turn-a","event":"skill_doc_load","skill":"code-review","path":"skills/code-review/RUNTIME.md"}`,
		`{"timestamp":"2026-04-20T13:05:00Z","run_id":"run-turn","turn_id":"turn-b","event":"skill_doc_load","skill":"plan","path":"skills/plan/RUNTIME.md"}`,
	})

	t.Setenv("NANA_CONTEXT_TELEMETRY_LOG", "")
	t.Setenv("NANA_CONTEXT_TELEMETRY_RUN_ID", "run-turn")
	t.Setenv("NANA_CONTEXT_TELEMETRY_TURN_ID", "turn-b")
	t.Setenv("NANA_WORK_RUN_ID", "")
	t.Setenv("NANA_RUN_ID", "")
	t.Setenv("NANA_SESSION_ID", "")
	t.Setenv("NANA_TURN_ID", "")
	t.Setenv("CODEX_TURN_ID", "")

	if got := currentSkillContextBudgetAdvisoryBlock(cwd); got != "" {
		t.Fatalf("expected no advisory for clean current turn, got %q", got)
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

func captureTelemetryStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	runErr := fn()
	_ = w.Close()
	os.Stdout = old
	defer r.Close()
	data, _ := io.ReadAll(r)
	return string(data), runErr
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

func appendTelemetryLog(t *testing.T, path string, lines []string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open telemetry log for append: %v", err)
	}
	defer file.Close()
	if _, err := file.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatalf("append telemetry log: %v", err)
	}
}

func appendTelemetryRaw(t *testing.T, path string, content string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open telemetry log for raw append: %v", err)
	}
	defer file.Close()
	if _, err := file.WriteString(content); err != nil {
		t.Fatalf("append raw telemetry log content: %v", err)
	}
}

type trackedTelemetryBudgetLogOpen struct {
	firstSeekOffset int64
	firstSeekWhence int
	seekCalls       int
}

type trackedTelemetryBudgetLogFile struct {
	*os.File
	tracker *trackedTelemetryBudgetLogOpen
}

func (file *trackedTelemetryBudgetLogFile) Seek(offset int64, whence int) (int64, error) {
	if file.tracker != nil {
		if file.tracker.seekCalls == 0 {
			file.tracker.firstSeekOffset = offset
			file.tracker.firstSeekWhence = whence
		}
		file.tracker.seekCalls++
	}
	return file.File.Seek(offset, whence)
}
