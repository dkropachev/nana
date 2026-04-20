package gocli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestArtifactsListGroupsRepoArtifacts(t *testing.T) {
	repo := makeArtifactFixture(t)

	output, err := captureStdout(t, func() error {
		return Artifacts(repo, []string{"list"})
	})
	if err != nil {
		t.Fatalf("Artifacts(list): %v", err)
	}

	expected := []string{
		"NANA artifacts in " + filepath.Join(repo, ".nana"),
		"notes (1)",
		"project-memory (1)",
		"context (1)",
		"interviews (1)",
		"specs (2)",
		"plans (1)",
		"logs (1)",
		"state (2)",
		"enhancements (1)",
		"artifacts (1)",
		".nana/notepad.md — Run Notes",
		".nana/context/artifact-index-20260420T000000Z.md — Context Snapshot",
		".nana/interviews/artifact-index-20260420T010000Z.md — Interview Transcript",
		".nana/specs/deep-interview-artifact-index.md — Artifact Index Spec",
		".nana/specs/autoresearch-artifact-index/mission.md — Mission Draft",
		".nana/plans/prd-artifacts.md — PRD: Artifact Index",
		".nana/logs/investigate/investigate-100 — run investigate-100; status completed",
		".nana/enhancements/enhance-1700000000000000000 — 2 proposals: Add an artifact index",
		".nana/artifacts/gemini-artifact-index-2026-04-20T00-00-00Z.md — gemini advisor artifact",
	}
	for _, snippet := range expected {
		if !strings.Contains(output, snippet) {
			t.Fatalf("expected artifacts output to contain %q, got:\n%s", snippet, output)
		}
	}
}

func TestArtifactsListJSONIncludesModeAndSummary(t *testing.T) {
	repo := makeArtifactFixture(t)

	output, err := captureStdout(t, func() error {
		return Artifacts(repo, []string{"list", "--json"})
	})
	if err != nil {
		t.Fatalf("Artifacts(list --json): %v", err)
	}

	var index nanaArtifactIndex
	if err := json.Unmarshal([]byte(output), &index); err != nil {
		t.Fatalf("unmarshal artifacts json: %v\n%s", err, output)
	}
	if index.RepoRoot != repo || index.NanaDir != filepath.Join(repo, ".nana") {
		t.Fatalf("unexpected index roots: %+v", index)
	}
	enhancement := findArtifact(index.Artifacts, ".nana/enhancements/enhance-1700000000000000000")
	if enhancement == nil {
		t.Fatalf("missing enhancement artifact in %+v", index.Artifacts)
	}
	if enhancement.Type != "enhancements" || enhancement.Mode != "enhance" {
		t.Fatalf("unexpected enhancement type/mode: %+v", enhancement)
	}
	if enhancement.Timestamp != "2026-04-20T01:02:03Z" || !strings.Contains(enhancement.Summary, "2 proposals: Add an artifact index") {
		t.Fatalf("unexpected enhancement metadata: %+v", enhancement)
	}

	contextSnapshot := findArtifact(index.Artifacts, ".nana/context/artifact-index-20260420T000000Z.md")
	if contextSnapshot == nil {
		t.Fatalf("missing context snapshot artifact in %+v", index.Artifacts)
	}
	if contextSnapshot.Type != "context" || contextSnapshot.Mode != "context" || contextSnapshot.Summary != "Context Snapshot" {
		t.Fatalf("unexpected context snapshot metadata: %+v", contextSnapshot)
	}

	interview := findArtifact(index.Artifacts, ".nana/interviews/artifact-index-20260420T010000Z.md")
	if interview == nil {
		t.Fatalf("missing interview artifact in %+v", index.Artifacts)
	}
	if interview.Type != "interviews" || interview.Mode != "deep-interview" || interview.Summary != "Interview Transcript" {
		t.Fatalf("unexpected interview metadata: %+v", interview)
	}

	spec := findArtifact(index.Artifacts, ".nana/specs/deep-interview-artifact-index.md")
	if spec == nil {
		t.Fatalf("missing spec artifact in %+v", index.Artifacts)
	}
	if spec.Type != "specs" || spec.Mode != "deep-interview" || spec.Summary != "Artifact Index Spec" {
		t.Fatalf("unexpected spec metadata: %+v", spec)
	}

	autoresearchMission := findArtifact(index.Artifacts, ".nana/specs/autoresearch-artifact-index/mission.md")
	if autoresearchMission == nil {
		t.Fatalf("missing nested autoresearch spec artifact in %+v", index.Artifacts)
	}
	if autoresearchMission.Type != "specs" || autoresearchMission.Mode != "autoresearch" || autoresearchMission.Summary != "Mission Draft" {
		t.Fatalf("unexpected nested spec metadata: %+v", autoresearchMission)
	}

	logs := findArtifact(index.Artifacts, ".nana/logs/investigate/investigate-100")
	if logs == nil {
		t.Fatalf("missing log artifact in %+v", index.Artifacts)
	}
	if logs.Mode != "investigate" || !strings.Contains(logs.Summary, "run investigate-100; status completed") {
		t.Fatalf("unexpected log metadata: %+v", logs)
	}
}

func TestArtifactsListJSONUsesEmptyArrayForExistingEmptyNanaDir(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".nana"), 0o755); err != nil {
		t.Fatalf("mkdir .nana: %v", err)
	}

	output, err := captureStdout(t, func() error {
		return Artifacts(repo, []string{"list", "--json"})
	})
	if err != nil {
		t.Fatalf("Artifacts(list --json): %v", err)
	}

	var raw struct {
		Artifacts json.RawMessage `json:"artifacts"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		t.Fatalf("unmarshal artifacts json: %v\n%s", err, output)
	}
	if string(raw.Artifacts) != "[]" {
		t.Fatalf("expected artifacts to be encoded as [], got %s in:\n%s", raw.Artifacts, output)
	}

	var index nanaArtifactIndex
	if err := json.Unmarshal([]byte(output), &index); err != nil {
		t.Fatalf("unmarshal artifact index: %v\n%s", err, output)
	}
	if index.Artifacts == nil || len(index.Artifacts) != 0 {
		t.Fatalf("expected a non-nil empty artifacts slice, got %#v", index.Artifacts)
	}
}

func TestArtifactsListDoesNotTreatRepoValueAsHelp(t *testing.T) {
	parent := t.TempDir()
	for _, repoName := range []string{"help", "-h", "--help"} {
		repo := filepath.Join(parent, repoName)
		writeArtifactFile(t, repo, ".nana/notepad.md", "# Repo "+repoName+"\n\nDetails.\n")

		output, err := captureStdout(t, func() error {
			return Artifacts(parent, []string{"list", "--repo", repoName, "--json"})
		})
		if err != nil {
			t.Fatalf("Artifacts(list --repo %s --json): %v", repoName, err)
		}

		var index nanaArtifactIndex
		if err := json.Unmarshal([]byte(output), &index); err != nil {
			t.Fatalf("expected JSON output for --repo %s, got unmarshal error: %v\n%s", repoName, err, output)
		}
		if index.RepoRoot != repo || index.NanaDir != filepath.Join(repo, ".nana") {
			t.Fatalf("unexpected roots for --repo %s: %+v", repoName, index)
		}
		if artifact := findArtifact(index.Artifacts, ".nana/notepad.md"); artifact == nil || artifact.Summary != "Repo "+repoName {
			t.Fatalf("expected notepad artifact for --repo %s, got %+v", repoName, index.Artifacts)
		}
	}
}

func TestArtifactsListRepoEqualsHelpDoesNotPrintHelp(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "help")
	writeArtifactFile(t, repo, ".nana/notepad.md", "# Repo help\n")

	output, err := captureStdout(t, func() error {
		return Artifacts(parent, []string{"list", "--repo=help", "--json"})
	})
	if err != nil {
		t.Fatalf("Artifacts(list --repo=help --json): %v", err)
	}

	var index nanaArtifactIndex
	if err := json.Unmarshal([]byte(output), &index); err != nil {
		t.Fatalf("expected JSON output for --repo=help, got unmarshal error: %v\n%s", err, output)
	}
	if index.RepoRoot != repo {
		t.Fatalf("expected --repo=help to resolve %s, got %+v", repo, index)
	}
}

func TestTruncateArtifactSummaryPreservesUTF8WhenMultibyteRuneCrossesLimit(t *testing.T) {
	value := strings.Repeat("a", 118) + "🙂" + "suffix"

	summary := truncateArtifactSummary(value)

	if !utf8.ValidString(summary) {
		t.Fatalf("expected valid UTF-8 summary, got %q", summary)
	}
	if strings.ContainsRune(summary, utf8.RuneError) {
		t.Fatalf("expected summary not to contain replacement characters, got %q", summary)
	}
	if !strings.HasSuffix(summary, "…") {
		t.Fatalf("expected summary to be truncated with ellipsis, got %q", summary)
	}
}

func makeArtifactFixture(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	writeArtifactFile(t, repo, ".nana/notepad.md", "# Run Notes\n\nRemember the handoff.\n")
	writeArtifactFile(t, repo, ".nana/project-memory.json", `{"decisions":[],"updated_at":"2026-04-20T00:00:00Z"}`)
	writeArtifactFile(t, repo, ".nana/context/artifact-index-20260420T000000Z.md", "# Context Snapshot\n\nGrounding details.\n")
	writeArtifactFile(t, repo, ".nana/interviews/artifact-index-20260420T010000Z.md", "# Interview Transcript\n\nQuestion and answer summary.\n")
	writeArtifactFile(t, repo, ".nana/specs/deep-interview-artifact-index.md", "# Artifact Index Spec\n\nExecution-ready brief.\n")
	writeArtifactFile(t, repo, ".nana/specs/autoresearch-artifact-index/mission.md", "# Mission Draft\n\nResearch mission.\n")
	writeArtifactFile(t, repo, ".nana/plans/prd-artifacts.md", "# PRD: Artifact Index\n\nDetails.\n")
	writeArtifactFile(t, repo, ".nana/logs/investigate/investigate-100/manifest.json", `{"run_id":"investigate-100","status":"completed","query":"Why is artifact navigation hard?","created_at":"2026-04-19T00:00:00Z","updated_at":"2026-04-20T00:00:00Z"}`)
	writeArtifactFile(t, repo, ".nana/logs/investigate/investigate-100/round-1.log", "ok\n")
	writeArtifactFile(t, repo, ".nana/state/session.json", `{"session_id":"sess-1"}`)
	writeArtifactFile(t, repo, ".nana/state/sessions/sess-1/team-state.json", `{"active":true,"current_phase":"team-exec"}`)
	writeArtifactFile(t, repo, ".nana/enhancements/enhance-1700000000000000000/proposals.json", `{"version":1,"repo":"demo","generated_at":"2026-04-20T01:02:03Z","proposals":[{"title":"Add an artifact index","summary":"Make durable outputs easy to revisit."},{"title":"Second proposal","summary":"Another grounded idea."}]}`)
	writeArtifactFile(t, repo, ".nana/enhancements/enhance-1700000000000000000/raw-output.txt", "raw\n")
	writeArtifactFile(t, repo, ".nana/artifacts/gemini-artifact-index-2026-04-20T00-00-00Z.md", "# gemini advisor artifact\n\nSummary.\n")
	return repo
}

func writeArtifactFile(t *testing.T, repo string, rel string, content string) {
	t.Helper()
	path := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func findArtifact(artifacts []nanaArtifact, path string) *nanaArtifact {
	for index := range artifacts {
		if artifacts[index].Path == path {
			return &artifacts[index]
		}
	}
	return nil
}
