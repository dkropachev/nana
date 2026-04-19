package gocli

import (
	"path/filepath"
	"strings"
	"testing"
)

func createGithubManagedSourceFixture(t *testing.T, home string, repoSlug string) (githubManagedRepoPaths, *githubManagedRepoMetadata) {
	t.Helper()

	parts := strings.SplitN(repoSlug, "/", 2)
	if len(parts) != 2 {
		t.Fatalf("invalid repo slug %q", repoSlug)
	}
	repoOwner := parts[0]
	repoName := parts[1]
	repoHTMLURL := "https://github.com/" + repoSlug
	originBare := filepath.Join(home, "origin.git")
	seedRepo := createLocalWorkRepoAt(t, filepath.Join(home, "seed"))
	runLocalWorkTestGit(t, home, "init", "--bare", originBare)
	runLocalWorkTestGit(t, seedRepo, "remote", "add", "origin", originBare)
	runLocalWorkTestGit(t, seedRepo, "push", "-u", "origin", "main")
	runLocalWorkTestGit(t, "", "--git-dir", originBare, "symbolic-ref", "HEAD", "refs/heads/main")
	configureTestGitInsteadOf(t, canonicalGithubSSHRemote(repoSlug, repoHTMLURL), originBare)

	paths := githubManagedPaths(repoSlug)
	repoMeta := &githubManagedRepoMetadata{
		Version:            2,
		RepoName:           repoName,
		RepoSlug:           repoSlug,
		RepoOwner:          repoOwner,
		CloneURL:           originBare,
		CanonicalOriginURL: canonicalGithubSSHRemote(repoSlug, repoHTMLURL),
		DefaultBranch:      "main",
		HTMLURL:            repoHTMLURL,
		RepoRoot:           paths.RepoRoot,
		SourcePath:         paths.SourcePath,
		UpdatedAt:          ISOTimeNow(),
	}
	normalizeGithubManagedRepoMetadata(repoMeta)

	return paths, repoMeta
}
