package docscheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuickstartMakesProfileBackedVerifyConditional(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	content, err := os.ReadFile(filepath.Join(repoRoot, "docs", "quickstart.md"))
	if err != nil {
		t.Fatalf("read docs/quickstart.md: %v", err)
	}

	fiveMinuteCommands := fencedBlockAfterHeading(t, string(content), "## Five-minute path", "bash")
	for _, expected := range []string{
		"nana repo onboard --repo .",
		"nana verify --json",
	} {
		if !strings.Contains(fiveMinuteCommands, expected) {
			t.Fatalf("five-minute path should include onboarded verification guidance %q; got:\n%s", expected, fiveMinuteCommands)
		}
	}
}

func fencedBlockAfterHeading(t *testing.T, content string, heading string, language string) string {
	t.Helper()
	headingStart := strings.Index(content, heading)
	if headingStart < 0 {
		t.Fatalf("missing heading %q", heading)
	}
	fenceStart := strings.Index(content[headingStart:], "```"+language+"\n")
	if fenceStart < 0 {
		t.Fatalf("missing %s fenced block after %q", language, heading)
	}
	blockStart := headingStart + fenceStart + len("```"+language+"\n")
	blockEnd := strings.Index(content[blockStart:], "\n```")
	if blockEnd < 0 {
		t.Fatalf("missing closing fence for %s block after %q", language, heading)
	}
	return "\n" + content[blockStart:blockStart+blockEnd] + "\n"
}
