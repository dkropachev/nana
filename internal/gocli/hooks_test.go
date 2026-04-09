package gocli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHooksHelp(t *testing.T) {
	output, err := captureStdout(t, func() error { return Hooks(t.TempDir(), t.TempDir(), []string{"--help"}) })
	if err != nil {
		t.Fatalf("Hooks(help): %v", err)
	}
	if !strings.Contains(output, "Plugins are enabled by default. Disable with NANA_HOOK_PLUGINS=0.") {
		t.Fatalf("unexpected help output: %q", output)
	}
}

func TestHooksInitAndStatus(t *testing.T) {
	cwd := t.TempDir()
	initOutput, err := captureStdout(t, func() error { return Hooks(cwd, t.TempDir(), []string{"init"}) })
	if err != nil {
		t.Fatalf("Hooks(init): %v", err)
	}
	if !strings.Contains(initOutput, "Plugins are enabled by default. Disable with NANA_HOOK_PLUGINS=0.") {
		t.Fatalf("unexpected init output: %q", initOutput)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".nana", "hooks", "sample-plugin.mjs")); err != nil {
		t.Fatalf("missing scaffold: %v", err)
	}

	statusOutput, err := captureStdout(t, func() error { return Hooks(cwd, t.TempDir(), []string{"status"}) })
	if err != nil {
		t.Fatalf("Hooks(status): %v", err)
	}
	if !strings.Contains(statusOutput, "Plugins enabled: yes") {
		t.Fatalf("unexpected enabled status output: %q", statusOutput)
	}

	t.Setenv("NANA_HOOK_PLUGINS", "0")
	disabledOutput, err := captureStdout(t, func() error { return Hooks(cwd, t.TempDir(), []string{"status"}) })
	if err != nil {
		t.Fatalf("Hooks(status disabled): %v", err)
	}
	if !strings.Contains(disabledOutput, "Plugins enabled: no (disabled with NANA_HOOK_PLUGINS=0)") {
		t.Fatalf("unexpected disabled status output: %q", disabledOutput)
	}
}

func TestHooksValidate(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".nana", "hooks"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	validPlugin := filepath.Join(cwd, ".nana", "hooks", "valid.mjs")
	invalidPlugin := filepath.Join(cwd, ".nana", "hooks", "invalid.mjs")
	if err := os.WriteFile(validPlugin, []byte("export async function onHookEvent(event, sdk) {}\n"), 0o644); err != nil {
		t.Fatalf("write valid plugin: %v", err)
	}
	if err := os.WriteFile(invalidPlugin, []byte("export const nope = true;\n"), 0o644); err != nil {
		t.Fatalf("write invalid plugin: %v", err)
	}

	output, err := captureStdout(t, func() error { return Hooks(cwd, t.TempDir(), []string{"validate"}) })
	if err == nil {
		t.Fatalf("expected validation failure")
	}
	if !strings.Contains(output, "✓ valid.mjs") || !strings.Contains(output, "✗ invalid.mjs") {
		t.Fatalf("unexpected validate output: %q", output)
	}
}
