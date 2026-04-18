package docscheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestInstallDocsTrackCurrentVersionAndManifest(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	versionRaw, err := os.ReadFile(filepath.Join(repoRoot, "VERSION"))
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	currentVersion := strings.TrimPrefix(strings.TrimSpace(string(versionRaw)), "v")
	if currentVersion == "" {
		t.Fatal("VERSION is empty")
	}
	manifestURL := "https://github.com/dkropachev/nana/releases/download/v" + currentVersion + "/native-release-manifest.json"
	expected := map[string][]string{
		"README.md": {
			"NANA_VERSION=" + currentVersion,
			"native-release-manifest.json",
			"https://github.com/dkropachev/nana/releases/download/v${NANA_VERSION}",
		},
		"docs/index.html": {
			"NANA_VERSION=" + currentVersion,
			"native-release-manifest.json",
			manifestURL,
			"What's New in " + currentVersion,
		},
		"docs/getting-started.html": {
			"NANA_VERSION=" + currentVersion,
			"native-release-manifest.json",
			manifestURL,
		},
	}
	for rel, needles := range expected {
		content, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		text := string(content)
		for _, needle := range needles {
			if !strings.Contains(text, needle) {
				t.Fatalf("expected %s to contain %q", rel, needle)
			}
		}
		for _, stale := range []string{"<release-binary-url>", "&lt;release-binary-url&gt;", "What's New in 0.6.0", "whats-new-060"} {
			if strings.Contains(text, stale) {
				t.Fatalf("%s still contains stale install/release text %q", rel, stale)
			}
		}
	}
}

func TestEvergreenDocsDoNotContainVersionedInstallTextOutsideSyncedBlocks(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	semverPattern := regexp.MustCompile(`\bv?\d+\.\d+\.\d+\b`)
	for _, rel := range []string{"README.md", "docs/index.html", "docs/getting-started.html"} {
		content, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		text := stripMarkedDocsBlock(string(content), "<!-- NANA:INSTALL:START -->", "<!-- NANA:INSTALL:END -->")
		text = stripMarkedDocsBlock(text, "<!-- NANA:RELEASE:START -->", "<!-- NANA:RELEASE:END -->")
		for _, match := range semverPattern.FindAllStringIndex(text, -1) {
			if match[1] < len(text) && text[match[1]] == '.' {
				continue
			}
			line := 1 + strings.Count(text[:match[0]], "\n")
			t.Fatalf("%s:%d contains versioned install/release text outside synced docs blocks: %q", rel, line, text[match[0]:match[1]])
		}
	}
}

func TestPublicDocsUsePlainNanaAsCanonicalFirstLaunch(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	expected := map[string]string{
		"README.md":                 "Then start the first session:\n\n```bash\nnana\n```",
		"docs/index.html":           "<li><code>nana</code></li>",
		"docs/getting-started.html": "<pre><code>nana</code></pre>",
	}
	staleLaunches := []string{
		"nana --madmax --high",
		"nana --xhigh --madmax",
		"nana --madmax --xhigh",
	}

	for rel, canonical := range expected {
		content, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		text := string(content)
		if !strings.Contains(text, canonical) {
			t.Fatalf("expected %s to contain canonical first launch %q", rel, canonical)
		}
		for _, stale := range staleLaunches {
			if strings.Contains(text, stale) {
				t.Fatalf("%s still contains stale first-launch command %q", rel, stale)
			}
		}
	}
}

func stripMarkedDocsBlock(content string, startMarker string, endMarker string) string {
	for {
		start := strings.Index(content, startMarker)
		if start < 0 {
			return content
		}
		afterStart := start + len(startMarker)
		relEnd := strings.Index(content[afterStart:], endMarker)
		if relEnd < 0 {
			return content
		}
		end := afterStart + relEnd + len(endMarker)
		content = content[:start] + content[end:]
	}
}
