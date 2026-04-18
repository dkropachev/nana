package gocli

import (
	"strings"
	"testing"
)

func TestSparkShellHelpDocumentsSummaryControls(t *testing.T) {
	output, err := captureStdout(t, func() error {
		return SparkShell("", t.TempDir(), []string{"--help"})
	})
	if err != nil {
		t.Fatalf("SparkShell(--help): %v", err)
	}

	expectedSnippets := []string{
		"Usage: nana sparkshell <command> [args...]",
		"raw-vs-summary behavior",
		"stdout+stderr is emitted raw when visible output is <= NANA_SPARKSHELL_LINES (default 12)",
		"Output above that threshold is summarized with codex exec using low reasoning",
		"summary unavailable",
		"NANA_SPARKSHELL_SUMMARY_TIMEOUT_MS",
		"NANA_SPARKSHELL_MODEL",
		"NANA_DEFAULT_SPARK_MODEL / NANA_SPARK_MODEL",
		"NANA_SPARKSHELL_FALLBACK_MODEL",
		"NANA_DEFAULT_FRONTIER_MODEL",
		"NANA_SPARKSHELL_SUMMARY_MAX_LINES",
		"NANA_SPARKSHELL_SUMMARY_MAX_BYTES",
	}
	for _, snippet := range expectedSnippets {
		if !strings.Contains(output, snippet) {
			t.Fatalf("expected help to contain %q, got %q", snippet, output)
		}
	}
}
