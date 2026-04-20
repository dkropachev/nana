//go:build unix

package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBuildNanaArtifactIndexSkipsNonRegularArtifactCandidatesWithoutBlocking(t *testing.T) {
	repo := t.TempDir()
	fifoRels := []string{
		".nana/notepad.md",
		".nana/project-memory.json",
		".nana/plans/fifo.md",
		".nana/logs/investigate/run/manifest.json",
		".nana/state/fifo.json",
		".nana/artifacts/fifo.md",
		".nana/enhancements/enhance-fifo/proposals.json",
	}
	for _, rel := range fifoRels {
		makeArtifactFIFO(t, repo, rel)
	}
	writeArtifactFile(t, repo, ".nana/context/regular.md", "# Regular Context\n\nDetails.\n")

	type result struct {
		index nanaArtifactIndex
		err   error
	}
	done := make(chan result, 1)
	go func() {
		index, err := buildNanaArtifactIndex(repo)
		done <- result{index: index, err: err}
	}()

	var index nanaArtifactIndex
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("buildNanaArtifactIndex: %v", result.err)
		}
		index = result.index
	case <-time.After(2 * time.Second):
		unblockArtifactFIFOs(t, repo, fifoRels)
		select {
		case result := <-done:
			t.Fatalf("buildNanaArtifactIndex blocked on non-regular artifact candidate before returning index=%+v err=%v", result.index, result.err)
		case <-time.After(500 * time.Millisecond):
			t.Fatal("buildNanaArtifactIndex blocked on non-regular artifact candidate")
		}
	}

	if artifact := findArtifact(index.Artifacts, ".nana/context/regular.md"); artifact == nil || artifact.Summary != "Regular Context" {
		t.Fatalf("expected regular context artifact to be indexed, got %+v", index.Artifacts)
	}
	for _, rel := range fifoRels {
		if artifact := findArtifact(index.Artifacts, filepath.ToSlash(rel)); artifact != nil {
			t.Fatalf("expected FIFO artifact candidate %s to be skipped, got %+v", rel, artifact)
		}
	}
	if scout := findArtifact(index.Artifacts, ".nana/enhancements/enhance-fifo"); scout == nil {
		t.Fatalf("expected scout artifact directory to remain indexed, got %+v", index.Artifacts)
	} else if scout.Files != 0 || scout.Summary != "" {
		t.Fatalf("expected scout artifact directory to ignore FIFO metadata, got %+v", scout)
	}
}

func TestBuildNanaArtifactIndexSkipsSymlinkArtifactCandidates(t *testing.T) {
	repo := t.TempDir()
	external := t.TempDir()
	symlinkRels := []string{
		".nana/notepad.md",
		".nana/project-memory.json",
		".nana/context/leak.md",
		".nana/plans/leak.md",
		".nana/state/leak.json",
		".nana/artifacts/leak.md",
		".nana/logs/investigate/run/manifest.json",
		".nana/enhancements/enhance-leak/proposals.json",
	}
	for index, rel := range symlinkRels {
		target := filepath.Join(external, fmt.Sprintf("target-%d%s", index, filepath.Ext(rel)))
		content := "# EXTERNAL SECRET\n\nThis must not be indexed.\n"
		if strings.EqualFold(filepath.Ext(rel), ".json") {
			content = `{"secret":"EXTERNAL SECRET","generated_at":"2026-04-20T00:00:00Z","proposals":[{"title":"EXTERNAL SECRET"}]}`
		}
		symlinkArtifactFile(t, repo, rel, target, content)
	}
	writeArtifactFile(t, repo, ".nana/context/regular.md", "# Regular Context\n\nDetails.\n")

	index, err := buildNanaArtifactIndex(repo)
	if err != nil {
		t.Fatalf("buildNanaArtifactIndex: %v", err)
	}

	if artifact := findArtifact(index.Artifacts, ".nana/context/regular.md"); artifact == nil || artifact.Summary != "Regular Context" {
		t.Fatalf("expected regular context artifact to be indexed, got %+v", index.Artifacts)
	}
	for _, rel := range symlinkRels {
		if artifact := findArtifact(index.Artifacts, filepath.ToSlash(rel)); artifact != nil {
			t.Fatalf("expected symlink artifact candidate %s to be skipped, got %+v", rel, artifact)
		}
	}
	if scout := findArtifact(index.Artifacts, ".nana/enhancements/enhance-leak"); scout == nil {
		t.Fatalf("expected scout artifact directory to remain indexed, got %+v", index.Artifacts)
	} else if scout.Files != 0 || scout.Summary != "" {
		t.Fatalf("expected scout artifact directory to ignore symlink metadata, got %+v", scout)
	}
	for _, artifact := range index.Artifacts {
		if strings.Contains(artifact.Summary, "EXTERNAL SECRET") {
			t.Fatalf("expected summaries not to include external symlink target content, got %+v", artifact)
		}
	}
}

func TestArtifactFileExistsRequiresRegularFiles(t *testing.T) {
	repo := t.TempDir()
	fifoRel := ".nana/logs/investigate/run/manifest.json"
	makeArtifactFIFO(t, repo, fifoRel)
	if artifactFileExists(filepath.Join(repo, filepath.FromSlash(fifoRel))) {
		t.Fatal("expected artifactFileExists to reject FIFO manifest")
	}

	external := t.TempDir()
	symlinkRel := ".nana/state/session.json"
	symlinkArtifactFile(t, repo, symlinkRel, filepath.Join(external, "session.json"), `{"session_id":"external"}`)
	if artifactFileExists(filepath.Join(repo, filepath.FromSlash(symlinkRel))) {
		t.Fatal("expected artifactFileExists to reject symlink to regular file")
	}
}

func makeArtifactFIFO(t *testing.T, repo string, rel string) {
	t.Helper()
	path := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo %s: %v", rel, err)
	}
}

func symlinkArtifactFile(t *testing.T, repo string, rel string, target string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir target for %s: %v", rel, err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatalf("write target for %s: %v", rel, err)
	}
	link := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatalf("mkdir symlink parent %s: %v", rel, err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", rel, target, err)
	}
}

func unblockArtifactFIFOs(t *testing.T, repo string, rels []string) {
	t.Helper()
	for _, rel := range rels {
		path := filepath.Join(repo, filepath.FromSlash(rel))
		fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			continue
		}
		_, _ = syscall.Write(fd, []byte("\n"))
		_ = syscall.Close(fd)
	}
}
