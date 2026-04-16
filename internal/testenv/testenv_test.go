package testenv

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSanitizedEntriesScrubNanaAndGitHubState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "isolated")
	entries, err := sanitizedEntries([]string{
		"APPDATA=/host/appdata",
		"CODEX_HOME=/host/codex",
		"CUSTOM=keep",
		"GH_TOKEN=secret",
		"GITHUB_TOKEN=secret",
		"GOCACHE=/host/go-build",
		"GOMODCACHE=/host/pkg/mod",
		"GOPATH=/host/go",
		"GIT_AUTHOR_NAME=host",
		"HOME=/host/home",
		"LOCALAPPDATA=/host/localappdata",
		"NANA_TEAM_STATE_ROOT=/host/state",
		"PATH=/usr/bin",
		"USERPROFILE=/host/profile",
		"XDG_CONFIG_HOME=/host/xdg-config",
	}, root)
	if err != nil {
		t.Fatalf("sanitizedEntries() error = %v", err)
	}

	env := map[string]string{}
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("unexpected env entry %q", entry)
		}
		env[key] = value
	}

	home := filepath.Join(root, "home")
	if env["HOME"] != home {
		t.Fatalf("HOME = %q, want %q", env["HOME"], home)
	}
	if env["CODEX_HOME"] != filepath.Join(home, ".nana", "codex-home") {
		t.Fatalf("CODEX_HOME = %q", env["CODEX_HOME"])
	}
	if env["GOCACHE"] != "/host/go-build" {
		t.Fatalf("GOCACHE = %q, want /host/go-build", env["GOCACHE"])
	}
	if env["GOMODCACHE"] != "/host/pkg/mod" {
		t.Fatalf("GOMODCACHE = %q, want /host/pkg/mod", env["GOMODCACHE"])
	}
	if env["GOPATH"] != "/host/go" {
		t.Fatalf("GOPATH = %q, want /host/go", env["GOPATH"])
	}
	if env["XDG_CONFIG_HOME"] != filepath.Join(root, "xdg", "config") {
		t.Fatalf("XDG_CONFIG_HOME = %q", env["XDG_CONFIG_HOME"])
	}
	if env["GIT_CONFIG_NOSYSTEM"] != "1" {
		t.Fatalf("GIT_CONFIG_NOSYSTEM = %q, want 1", env["GIT_CONFIG_NOSYSTEM"])
	}
	if env["CUSTOM"] != "keep" {
		t.Fatalf("CUSTOM = %q, want keep", env["CUSTOM"])
	}
	if env["PATH"] != "/usr/bin" {
		t.Fatalf("PATH = %q, want /usr/bin", env["PATH"])
	}
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GIT_AUTHOR_NAME", "NANA_TEAM_STATE_ROOT"} {
		if _, ok := env[key]; ok {
			t.Fatalf("expected %s to be scrubbed", key)
		}
	}
	if runtime.GOOS == "windows" {
		if env["USERPROFILE"] != home {
			t.Fatalf("USERPROFILE = %q, want %q", env["USERPROFILE"], home)
		}
	}
}
