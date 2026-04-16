package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalInstallScriptUsesVersionedManifest(t *testing.T) {
	script := canonicalInstallScript("1.2.3", "example/nana")

	for _, want := range []string{
		"NANA_VERSION=1.2.3",
		"https://github.com/example/nana/releases/download/v${NANA_VERSION}",
		"native-release-manifest.json",
		"nana-${nana_target}.tar.gz",
		"grep -q",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("expected install script to contain %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "<release-binary-url>") {
		t.Fatalf("install script still contains placeholder URL:\n%s", script)
	}
}

func TestCanonicalInstallScriptEnablesFailFastMode(t *testing.T) {
	script := canonicalInstallScript("1.2.3", "example/nana")

	if !strings.HasPrefix(script, "set -euo pipefail\n") {
		t.Fatalf("expected install script to start with fail-fast shell options:\n%s", script)
	}
}

func TestCanonicalInstallScriptExitsWhenManifestDoesNotListArchive(t *testing.T) {
	script := canonicalInstallScript("1.2.3", "example/nana")

	for _, want := range []string{
		`if ! grep -q "\"archive\": \"${nana_archive}\"" native-release-manifest.json; then`,
		`echo "Release manifest does not list ${nana_archive}" >&2`,
		`exit 1`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("expected install script to contain %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, `grep -q "\"archive\": \"nana-${nana_target}.tar.gz\"" native-release-manifest.json
curl -fsSL`) {
		t.Fatalf("manifest grep is still unguarded before archive download:\n%s", script)
	}
}

func TestCanonicalInstallScriptVerifiesDownloadedChecksumBeforeExtraction(t *testing.T) {
	script := canonicalInstallScript("1.2.3", "example/nana")

	for _, want := range []string{
		`expected_sha256="$(`,
		`"sha256":`,
		`actual_sha256="`,
		`if [ "$actual_sha256" != "$expected_sha256" ]; then`,
		`echo "Checksum mismatch for ${nana_archive}" >&2`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("expected install script to contain %q:\n%s", want, script)
		}
	}
	assertOrder(t, script, `if [ "$actual_sha256" != "$expected_sha256" ]; then`, `tar -xzf "${nana_archive}" nana`)
}

func TestSyncInstallDocsUpdatesMarkedDocs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "VERSION"), "1.2.3\n")
	writeFile(t, filepath.Join(root, "CHANGELOG.md"), strings.Join([]string{
		"# Changelog",
		"",
		"## [Unreleased]",
		"",
		"## [1.2.3] - 2026-04-13",
		"",
		"### Fixed",
		"- **First current fix** — keeps `nana` install guidance current. (PR [#1](https://example.invalid/1))",
		"- **Second current fix** — uses the release manifest.",
		"",
	}, "\n"))
	writeFile(t, filepath.Join(root, "README.md"), "# nana\n\n"+installStartMarker+"\nstale\n"+installEndMarker+"\n")
	writeFile(t, filepath.Join(root, "docs", "index.html"), "<main>\n"+installStartMarker+"\nstale\n"+installEndMarker+"\n"+releaseStartMarker+"\nstale\n"+releaseEndMarker+"\n</main>\n")
	writeFile(t, filepath.Join(root, "docs", "getting-started.html"), "<main>\n"+installStartMarker+"\nstale\n"+installEndMarker+"\n</main>\n")

	if err := syncInstallDocs(root, "example/nana", false); err != nil {
		t.Fatalf("syncInstallDocs: %v", err)
	}

	readme := readFile(t, filepath.Join(root, "README.md"))
	index := readFile(t, filepath.Join(root, "docs", "index.html"))
	gettingStarted := readFile(t, filepath.Join(root, "docs", "getting-started.html"))

	for path, content := range map[string]string{
		"README.md":                 readme,
		"docs/index.html":           index,
		"docs/getting-started.html": gettingStarted,
	} {
		for _, want := range []string{"NANA_VERSION=1.2.3", "native-release-manifest.json", "example/nana/releases/download"} {
			if !strings.Contains(content, want) {
				t.Fatalf("expected %s to contain %q:\n%s", path, want, content)
			}
		}
		if strings.Contains(content, "stale") {
			t.Fatalf("expected %s stale marker body to be replaced:\n%s", path, content)
		}
	}

	for _, want := range []string{
		`What's New in 1.2.3`,
		`First current fix`,
		`keeps <code>nana</code> install guidance current.`,
		`https://github.com/example/nana/releases/download/v1.2.3/native-release-manifest.json`,
	} {
		if !strings.Contains(index, want) {
			t.Fatalf("expected docs/index.html to contain %q:\n%s", want, index)
		}
	}

	if err := syncInstallDocs(root, "example/nana", true); err != nil {
		t.Fatalf("syncInstallDocs --check after sync: %v", err)
	}
}

func TestSyncInstallDocsCheckDetectsDrift(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "VERSION"), "1.2.3\n")
	writeFile(t, filepath.Join(root, "CHANGELOG.md"), `# Changelog

## [1.2.3] - 2026-04-13

- **Current fix** — release notes.
`)
	writeFile(t, filepath.Join(root, "README.md"), "# nana\n\n"+installStartMarker+"\nstale\n"+installEndMarker+"\n")
	writeFile(t, filepath.Join(root, "docs", "index.html"), "<main>\n"+installStartMarker+"\nstale\n"+installEndMarker+"\n"+releaseStartMarker+"\nstale\n"+releaseEndMarker+"\n</main>\n")
	writeFile(t, filepath.Join(root, "docs", "getting-started.html"), "<main>\n"+installStartMarker+"\nstale\n"+installEndMarker+"\n</main>\n")

	err := syncInstallDocs(root, "example/nana", true)
	if err == nil {
		t.Fatal("expected check mode to detect drift")
	}
	if !strings.Contains(err.Error(), "README.md") || !strings.Contains(err.Error(), "docs/index.html") || !strings.Contains(err.Error(), "docs/getting-started.html") {
		t.Fatalf("expected drift error to list docs, got %v", err)
	}
}

func assertOrder(t *testing.T, text string, first string, second string) {
	t.Helper()
	firstIndex := strings.Index(text, first)
	if firstIndex < 0 {
		t.Fatalf("expected text to contain %q:\n%s", first, text)
	}
	secondIndex := strings.Index(text, second)
	if secondIndex < 0 {
		t.Fatalf("expected text to contain %q:\n%s", second, text)
	}
	if firstIndex >= secondIndex {
		t.Fatalf("expected %q to appear before %q:\n%s", first, second, text)
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}
