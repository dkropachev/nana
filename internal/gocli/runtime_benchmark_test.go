package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func BenchmarkResolveCLIInvocation(b *testing.B) {
	cases := [][]string{
		nil,
		{"--high", "--model", "gpt-5.4"},
		{"launch", "--xhigh", "--", "implement the task"},
		{"exec", "--json", "status"},
		{"work", "status", "--global-last"},
		{"work-local", "status", "--run-id", "lw-123"},
		{"review", "https://github.com/acme/widget/pull/77"},
		{"explore", "--prompt", "find symbol"},
	}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		got := ResolveCLIInvocation(cases[i%len(cases)])
		if got.Command == "" {
			b.Fatal("empty command")
		}
	}
}

func BenchmarkWorkRunIndexRead(b *testing.B) {
	home := b.TempDir()
	b.Setenv("HOME", home)
	seedWorkRunIndex(b, "local", 100)

	b.Run("by-run-id", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			entry, err := readWorkRunIndex("local-run-050")
			if err != nil {
				b.Fatal(err)
			}
			if entry.RunID == "" {
				b.Fatal("empty indexed run")
			}
		}
	})

	b.Run("latest-backend", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			entry, err := latestWorkRunIndex("local")
			if err != nil {
				b.Fatal(err)
			}
			if entry.RunID != "local-run-099" {
				b.Fatalf("latest run = %q", entry.RunID)
			}
		}
	})

	b.Run("latest-any", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			entry, err := latestAnyWorkRunIndex()
			if err != nil {
				b.Fatal(err)
			}
			if entry.RunID == "" {
				b.Fatal("empty latest run")
			}
		}
	})
}

func BenchmarkGithubRunLookupFromIndex(b *testing.B) {
	home := b.TempDir()
	b.Setenv("HOME", home)

	managedRepoRoot := filepath.Join(home, ".nana", "work", "repos", "acme", "widget")
	runID := "gh-bench-run"
	runDir := filepath.Join(managedRepoRoot, "runs", runID)
	sandboxPath := filepath.Join(managedRepoRoot, "sandboxes", "issue-42-"+runID)
	repoCheckoutPath := filepath.Join(sandboxPath, "repo")
	if err := os.MkdirAll(repoCheckoutPath, 0o755); err != nil {
		b.Fatalf("mkdir repo checkout: %v", err)
	}
	manifest := githubWorkManifest{
		Version:         3,
		RunID:           runID,
		CreatedAt:       "2026-04-11T12:00:00Z",
		UpdatedAt:       "2026-04-11T12:00:00Z",
		RepoSlug:        "acme/widget",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ManagedRepoRoot: managedRepoRoot,
		SandboxID:       "issue-42-" + runID,
		SandboxPath:     sandboxPath,
		SandboxRepoPath: repoCheckoutPath,
		TargetKind:      "issue",
		TargetNumber:    42,
		TargetURL:       "https://github.com/acme/widget/issues/42",
	}
	manifestPath := filepath.Join(runDir, "manifest.json")
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		b.Fatalf("write manifest: %v", err)
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		b.Fatalf("index manifest: %v", err)
	}

	b.Run("explicit-run-id", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			path, root, err := resolveGithubRunManifestPath(runID, false)
			if err != nil {
				b.Fatal(err)
			}
			if path != manifestPath || root != managedRepoRoot {
				b.Fatalf("resolved manifest=%q root=%q", path, root)
			}
		}
	})

	b.Run("latest-indexed-run", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			path, root, err := resolveGithubRunManifestPath("", true)
			if err != nil {
				b.Fatal(err)
			}
			if path != manifestPath || root != managedRepoRoot {
				b.Fatalf("resolved manifest=%q root=%q", path, root)
			}
		}
	})
}

func BenchmarkDetectGithubVerificationPlan(b *testing.B) {
	repo := b.TempDir()
	writeBenchmarkRepoFile(b, repo, "Makefile", "lint:\n\t@true\nbuild:\n\t@true\ntest-unit:\n\t@true\ntest-integration:\n\t@true\nbenchmark:\n\t@true\n")
	writeBenchmarkRepoFile(b, repo, "go.mod", "module example.com/nana-benchmark\n\ngo 1.24\n")
	writeBenchmarkRepoFile(b, repo, "cmd/nana/main.go", "package main\n\nfunc main() {}\n")

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		plan := detectGithubVerificationPlan(repo)
		if len(plan.Benchmarks) != 1 || plan.Benchmarks[0] != "make benchmark" {
			b.Fatalf("unexpected benchmarks: %#v", plan.Benchmarks)
		}
		if plan.PlanFingerprint == "" {
			b.Fatal("empty fingerprint")
		}
	}
}

func seedWorkRunIndex(b *testing.B, backend string, count int) {
	b.Helper()
	store, err := openLocalWorkDB()
	if err != nil {
		b.Fatalf("open db: %v", err)
	}
	defer store.Close()
	tx, err := store.db.Begin()
	if err != nil {
		b.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()
	baseTime := time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
	for i := 0; i < count; i++ {
		entry := workRunIndexEntry{
			RunID:        fmt.Sprintf("%s-run-%03d", backend, i),
			Backend:      backend,
			RepoKey:      "acme/widget",
			RepoRoot:     filepath.Join(b.TempDir(), "repo"),
			RepoName:     "widget",
			ManifestPath: filepath.Join(b.TempDir(), "manifest.json"),
			UpdatedAt:    baseTime.Add(time.Duration(i) * time.Second).Format(time.RFC3339),
			TargetKind:   backend,
		}
		if err := writeWorkRunIndexTx(tx, entry); err != nil {
			b.Fatalf("seed run index: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("commit seed index: %v", err)
	}
}

func writeBenchmarkRepoFile(b *testing.B, repo string, name string, content string) {
	b.Helper()
	path := filepath.Join(repo, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.Fatalf("mkdir %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		b.Fatalf("write %s: %v", name, err)
	}
}
