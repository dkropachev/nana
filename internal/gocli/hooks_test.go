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
	if !strings.Contains(output, "Hooks are enabled by default. Disable with NANA_HOOK_PLUGINS=0.") {
		t.Fatalf("unexpected help output: %q", output)
	}
}

func TestHooksInitAndStatus(t *testing.T) {
	cwd := t.TempDir()
	initOutput, err := captureStdout(t, func() error { return Hooks(cwd, t.TempDir(), []string{"init"}) })
	if err != nil {
		t.Fatalf("Hooks(init): %v", err)
	}
	if !strings.Contains(initOutput, "Hooks are enabled by default. Disable with NANA_HOOK_PLUGINS=0.") {
		t.Fatalf("unexpected init output: %q", initOutput)
	}
	if _, err := os.Stat(sampleHookPath(cwd)); err != nil {
		t.Fatalf("missing scaffold: %v", err)
	}

	statusOutput, err := captureStdout(t, func() error { return Hooks(cwd, t.TempDir(), []string{"status"}) })
	if err != nil {
		t.Fatalf("Hooks(status): %v", err)
	}
	if !strings.Contains(statusOutput, "Hooks enabled: yes") {
		t.Fatalf("unexpected enabled status output: %q", statusOutput)
	}

	t.Setenv("NANA_HOOK_PLUGINS", "0")
	disabledOutput, err := captureStdout(t, func() error { return Hooks(cwd, t.TempDir(), []string{"status"}) })
	if err != nil {
		t.Fatalf("Hooks(status disabled): %v", err)
	}
	if !strings.Contains(disabledOutput, "Hooks enabled: no (disabled with NANA_HOOK_PLUGINS=0)") {
		t.Fatalf("unexpected disabled status output: %q", disabledOutput)
	}
}

func TestHooksValidate(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".nana", "hooks"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	validPlugin := filepath.Join(cwd, ".nana", "hooks", "valid.sh")
	legacyPlugin := filepath.Join(cwd, ".nana", "hooks", "legacy.mjs")
	if err := os.WriteFile(validPlugin, []byte("#!/bin/sh\nprintf '%s\\n' '{\"ok\":true,\"reason\":\"ok\"}'\n"), 0o755); err != nil {
		t.Fatalf("write valid plugin: %v", err)
	}
	if err := os.WriteFile(legacyPlugin, []byte("export const nope = true;\n"), 0o644); err != nil {
		t.Fatalf("write legacy plugin: %v", err)
	}

	output, err := captureStdout(t, func() error { return Hooks(cwd, t.TempDir(), []string{"validate"}) })
	if err == nil {
		t.Fatalf("expected validation failure")
	}
	if !strings.Contains(output, "✓ valid.sh") || !strings.Contains(output, "✗ legacy.mjs") {
		t.Fatalf("unexpected validate output: %q", output)
	}
}
