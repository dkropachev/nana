package gocli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	startTaskKindPreflight       = "preflight"
	startRepoPreflightTaskTarget = "repo"
)

var githubAutomationRepoPreflight = preflightGithubAutomationRepo

func githubCLIAuthStatus(apiBaseURL string) (string, string, error) {
	host := githubCLIHostForAPIBase(strings.TrimSpace(apiBaseURL))
	path, err := exec.LookPath("gh")
	if err != nil {
		if host != "" {
			return "", host, fmt.Errorf("GitHub auth required for %s. Install `gh` and run `gh auth login --hostname %s`.", host, host)
		}
		return "", host, fmt.Errorf("GitHub auth required. Install `gh` and run `gh auth login`.")
	}
	args := []string{"auth", "status"}
	if host != "" {
		args = append(args, "--hostname", host)
	}
	output, err := exec.Command(path, args...).CombinedOutput()
	if err != nil {
		if host != "" {
			return "", host, fmt.Errorf("GitHub auth required for %s. Run `gh auth login --hostname %s` or fix `gh auth status --hostname %s`: %s", host, host, host, strings.TrimSpace(string(output)))
		}
		return "", host, fmt.Errorf("GitHub auth required. Run `gh auth login` or fix `gh auth status`: %s", strings.TrimSpace(string(output)))
	}
	return path, host, nil
}

func loadGithubManagedRepoMetadataForSlug(repoSlug string) *githubManagedRepoMetadata {
	repoSlug = strings.TrimSpace(repoSlug)
	if repoSlug == "" {
		return nil
	}
	metadata := &githubManagedRepoMetadata{RepoSlug: repoSlug}
	if err := readGithubJSON(githubManagedPaths(repoSlug).RepoMetaPath, metadata); err == nil {
		normalizeGithubManagedRepoMetadata(metadata)
	}
	if strings.TrimSpace(metadata.RepoSlug) == "" {
		metadata.RepoSlug = repoSlug
	}
	if strings.TrimSpace(metadata.CanonicalOriginURL) == "" {
		metadata.CanonicalOriginURL = canonicalGithubSSHRemote(repoSlug, metadata.HTMLURL)
	}
	return metadata
}

func validateGithubManagedOrigin(repoPath string, repoMeta *githubManagedRepoMetadata) error {
	canonical := githubManagedCanonicalOriginURL(repoMeta)
	if canonical == "" {
		return fmt.Errorf("managed source checkout %s is missing a canonical origin", repoPath)
	}
	currentOrigin, err := githubGitOutput(repoPath, "remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("managed source checkout %s is missing origin; expected immutable %s", repoPath, canonical)
	}
	current := strings.TrimSpace(currentOrigin)
	if current != canonical {
		return fmt.Errorf("managed source checkout %s has origin %s; expected immutable %s", repoPath, current, canonical)
	}
	pushOrigin, err := githubGitOutput(repoPath, "remote", "get-url", "--push", "origin")
	if err != nil {
		return fmt.Errorf("managed source checkout %s is missing push origin; expected immutable %s", repoPath, canonical)
	}
	if strings.TrimSpace(pushOrigin) != canonical {
		return fmt.Errorf("managed source checkout %s has push origin %s; expected immutable %s", repoPath, strings.TrimSpace(pushOrigin), canonical)
	}
	return nil
}

func preflightGithubAutomationRepo(repoSlug string, repairOrigin bool) error {
	repoSlug = strings.TrimSpace(repoSlug)
	if repoSlug == "" {
		return fmt.Errorf("missing repo slug")
	}
	if _, _, err := githubCLIAuthStatus(strings.TrimSpace(os.Getenv("GITHUB_API_URL"))); err != nil {
		return err
	}
	paths := githubManagedPaths(repoSlug)
	if _, err := os.Stat(paths.SourcePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("managed source checkout missing at %s; onboard the repo again or refresh the clone before automation can run", paths.SourcePath)
		}
		return err
	}
	metadata := loadGithubManagedRepoMetadataForSlug(repoSlug)
	if repairOrigin {
		if err := ensureGithubManagedOrigin(paths.SourcePath, metadata); err != nil {
			return err
		}
	} else if err := validateGithubManagedOrigin(paths.SourcePath, metadata); err != nil {
		return err
	}
	return githubManagedOriginPreflight(paths.SourcePath, metadata)
}

func startRepoPreflightTaskID() string {
	return startServiceTaskKey(startTaskKindPreflight, startRepoPreflightTaskTarget)
}

func clearStartRepoAutomationPreflight(repoSlug string) error {
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	state, err := readStartWorkStateUnlocked(repoSlug)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	taskID := startRepoPreflightTaskID()
	if _, ok := state.ServiceTasks[taskID]; !ok {
		return nil
	}
	delete(state.ServiceTasks, taskID)
	state.UpdatedAt = ISOTimeNow()
	return writeStartWorkStateUnlocked(*state)
}

func recordStartRepoAutomationPreflightFailure(repoSlug string, preflightErr error) error {
	if preflightErr == nil {
		return clearStartRepoAutomationPreflight(repoSlug)
	}
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	state, err := ensureStartWorkStateUnlocked(repoSlug)
	if err != nil {
		return err
	}
	taskID := startRepoPreflightTaskID()
	message := strings.TrimSpace(preflightErr.Error())
	if existing, ok := state.ServiceTasks[taskID]; ok && existing.Status == startWorkServiceTaskFailed && strings.TrimSpace(existing.LastError) == message {
		return nil
	}
	now := ISOTimeNow()
	attempts := 1
	if existing, ok := state.ServiceTasks[taskID]; ok && existing.Attempts > 0 {
		attempts = existing.Attempts + 1
	}
	state.ServiceTasks[taskID] = startWorkServiceTask{
		ID:            taskID,
		Kind:          startTaskKindPreflight,
		Queue:         startTaskQueueService,
		Status:        startWorkServiceTaskFailed,
		Attempts:      attempts,
		LastError:     message,
		ResultSummary: "blocked",
		UpdatedAt:     now,
		CompletedAt:   now,
	}
	state.UpdatedAt = now
	return writeStartWorkStateUnlocked(*state)
}
