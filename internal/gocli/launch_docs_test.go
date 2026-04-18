package gocli

import (
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchDocsPreferSafeDefaultsOverMadmax(t *testing.T) {
	repoRoot := repoRootFromCaller(t)
	testCases := []struct {
		name      string
		relPath   string
		required  []string
		forbidden []string
	}{
		{
			name:    "readme",
			relPath: "README.md",
			required: []string{
				"If you want the default NANA experience, install and verify the CLI first:",
				"curl -fsSL -o nana.tar.gz \"https://github.com/Yeachan-Heo/nana/releases/latest/download/nana-${NANA_TARGET}.tar.gz\"",
				"sudo install -m 0755 nana nana-runtime nana-explore-harness nana-sparkshell /usr/local/bin/",
				"Start your first NANA session from a project:",
				"```bash\nnana\n```",
				"`nana --madmax` only in trusted environments because it bypasses approvals and sandboxing.",
			},
			forbidden: []string{
				"curl -L -o nana <release-binary-url>",
				"tar -xzf nana.tar.gz nana",
				"sudo install -m 0755 nana /usr/local/bin/nana",
				"Launch NANA the recommended way:\n\n```bash\nnana --madmax --high\n```",
				"Launch with `nana --madmax --high` for thorough work",
			},
		},
		{
			name:    "getting started",
			relPath: "docs/getting-started.html",
			required: []string{
				"nana                   # standard launch with configured defaults",
				"nana --high            # optional one-off reasoning override",
				"nana --madmax          # trusted environments only",
				"bypasses approvals and sandboxing",
			},
			forbidden: []string{
				"nana --madmax --high   # recommended first session",
				"Start with <code>nana --madmax --high</code> for a strong first session",
			},
		},
		{
			name:    "homepage",
			relPath: "docs/index.html",
			required: []string{
				"<code>nana</code> with the configured defaults",
				"<code>nana --high</code> for a one-off reasoning override",
				"<code>nana --madmax</code> for trusted environments only",
				"bypasses approvals and sandboxing",
			},
			forbidden: []string{
				"Start with <code>nana --madmax --high</code> for a strong first session",
				"<li><code>nana --madmax --high</code> (recommended first session)",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join(repoRoot, tc.relPath))
			if err != nil {
				t.Fatalf("read %s: %v", tc.relPath, err)
			}
			text := string(content)
			for _, needle := range tc.required {
				if !strings.Contains(text, needle) {
					t.Fatalf("expected %s to contain %q", tc.relPath, needle)
				}
			}
			for _, needle := range tc.forbidden {
				if strings.Contains(text, needle) {
					t.Fatalf("expected %s to avoid %q", tc.relPath, needle)
				}
			}
		})
	}
}

func TestPublicInstallSnippetsInstallReleaseCompanionBinaries(t *testing.T) {
	repoRoot := repoRootFromCaller(t)
	testCases := []struct {
		name    string
		relPath string
	}{
		{name: "readme", relPath: "README.md"},
		{name: "getting started", relPath: "docs/getting-started.html"},
		{name: "homepage", relPath: "docs/index.html"},
	}

	required := []string{
		"curl -fsSL -o nana.tar.gz \"https://github.com/Yeachan-Heo/nana/releases/latest/download/nana-${NANA_TARGET}.tar.gz\"",
		"tar -xzf nana.tar.gz",
		"sudo mkdir -p /usr/local/bin",
		"sudo install -m 0755 nana nana-runtime nana-explore-harness nana-sparkshell /usr/local/bin/",
		"rm -f nana nana-runtime nana-explore-harness nana-sparkshell nana.tar.gz",
	}
	forbidden := []string{
		"curl -L -o nana <release-binary-url>",
		"chmod +x nana",
		"sudo mv nana /usr/local/bin/nana",
		"tar -xzf nana.tar.gz nana",
		"sudo install -m 0755 nana /usr/local/bin/nana",
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join(repoRoot, tc.relPath))
			if err != nil {
				t.Fatalf("read %s: %v", tc.relPath, err)
			}

			text := html.UnescapeString(string(content))
			for _, needle := range required {
				if !strings.Contains(text, needle) {
					t.Fatalf("expected %s install snippet to contain %q", tc.relPath, needle)
				}
			}
			for _, needle := range forbidden {
				if strings.Contains(text, needle) {
					t.Fatalf("expected %s install snippet to avoid companion-dropping command %q", tc.relPath, needle)
				}
			}
		})
	}
}
