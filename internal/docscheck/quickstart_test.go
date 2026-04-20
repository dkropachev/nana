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
	if strings.Contains(fiveMinuteCommands, "\nnana verify --json\n") {
		t.Fatal("five-minute path must not present a bare nana verify --json as mandatory")
	}
	for _, expected := range []string{
		"if [ -f nana-verify.json ]; then",
		"nana verify --json",
		"No nana-verify.json found; run this project's native checks instead.",
	} {
		if !strings.Contains(fiveMinuteCommands, expected) {
			t.Fatalf("five-minute path should condition profile-backed verification with %q; got:\n%s", expected, fiveMinuteCommands)
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
