package gocli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveContextTelemetryLogPathUsesNearestNanaRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nana", "logs"), 0o755); err != nil {
		t.Fatalf("mkdir .nana/logs: %v", err)
	}
	nested := filepath.Join(root, "cmd", "pkg")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	t.Setenv("NANA_CONTEXT_TELEMETRY_LOG", "")

	got := resolveContextTelemetryLogPath(nested)
	want := filepath.Join(root, ".nana", "logs", "context-telemetry.ndjson")
	if got != want {
		t.Fatalf("unexpected telemetry path:\n got: %s\nwant: %s", got, want)
	}
}

func TestResolveContextTelemetryLogPathHonorsExplicitEnv(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "telemetry.ndjson")
	t.Setenv("NANA_CONTEXT_TELEMETRY_LOG", explicit)

	if got := resolveContextTelemetryLogPath(t.TempDir()); got != explicit {
		t.Fatalf("expected explicit telemetry path %s, got %s", explicit, got)
	}
}
