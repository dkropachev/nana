package gocli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// githubPreparedManagedSource carries the source-derived artifacts that
// inspection-oriented GitHub flows need after the managed checkout has been
// refreshed and downgraded to a shared read lock.
type githubPreparedManagedSource struct {
	RepoVerificationPlan githubVerificationPlan
	Settings             *githubRepoSettings
	Profile              *githubRepoProfile
	ProfilePath          string
	Policy               githubResolvedWorkPolicy
}

// inspectGithubManagedSource is the shared source-prep path for flows that need
// a stable managed checkout while they compute verification/settings/profile
// artifacts, with an optional tail step that still runs under the read lock.
func inspectGithubManagedSource(paths githubManagedRepoPaths, repoMeta *githubManagedRepoMetadata, owner repoAccessLockOwner, now time.Time, observeReadPhase func(sourcePath string) error, readPhase func(sourcePath string, prepared *githubPreparedManagedSource) error) (githubPreparedManagedSource, error) {
	prepared := githubPreparedManagedSource{}
	err := withSourceWriteThenReadLock(paths.SourcePath, owner,
		func() error {
			return ensureGithubSourceClone(paths, repoMeta)
		},
		func() error {
			if observeReadPhase != nil {
				if err := observeReadPhase(paths.SourcePath); err != nil {
					return err
				}
			}
			prepared.RepoVerificationPlan = detectGithubVerificationPlan(paths.SourcePath)
			if err := writeGithubJSON(paths.RepoVerificationPlanPath, prepared.RepoVerificationPlan); err != nil {
				return err
			}
			trackedFiles := trackedRepoFiles(paths.SourcePath)
			prepared.Settings, _ = readGithubRepoSettings(paths.RepoSettingsPath)
			if prepared.Settings == nil {
				inferred := inferGithubInitialRepoConsiderationsFromFiles(trackedFiles, repoMeta.RepoSlug, prepared.RepoVerificationPlan)
				prepared.Settings = &githubRepoSettings{
					Version:               4,
					DefaultConsiderations: inferred.Considerations,
					DefaultRoleLayout:     "split",
					UpdatedAt:             now.Format(time.RFC3339),
				}
			}
			prepared.Settings.HotPathAPIProfile = inferGithubHotPathProfileFromFiles(trackedFiles, now)
			prepared.Settings.UpdatedAt = now.Format(time.RFC3339)
			if prepared.Settings.DefaultRoleLayout == "" {
				prepared.Settings.DefaultRoleLayout = "split"
			}
			if prepared.Settings.Version == 0 {
				prepared.Settings.Version = 4
			}
			if err := writeGithubJSON(paths.RepoSettingsPath, prepared.Settings); err != nil {
				return err
			}
			var err error
			prepared.Profile, prepared.ProfilePath, err = refreshGithubRepoProfile(repoMeta.RepoSlug, paths.SourcePath, prepared.RepoVerificationPlan, prepared.Settings.DefaultConsiderations, now)
			if err != nil {
				return err
			}
			prepared.Policy, err = resolveGithubWorkPolicy(paths.SourcePath)
			if err != nil {
				return err
			}
			if readPhase != nil {
				return readPhase(paths.SourcePath, &prepared)
			}
			return nil
		},
	)
	return prepared, err
}

// cloneGithubManagedSourceForSandbox is the shared source-prep path for flows
// that only need a stable managed checkout long enough to clone a sandbox from
// it, without the additional inspection/profile work.
func cloneGithubManagedSourceForSandbox(paths githubManagedRepoPaths, repoMeta *githubManagedRepoMetadata, owner repoAccessLockOwner, repoPath string, observeReadPhase func(sourcePath string) error) error {
	return withSourceWriteThenReadLock(paths.SourcePath, owner,
		func() error {
			return ensureGithubSourceClone(paths, repoMeta)
		},
		func() error {
			if observeReadPhase != nil {
				if err := observeReadPhase(paths.SourcePath); err != nil {
					return err
				}
			}
			return cloneGithubSourceToSandbox(paths.SourcePath, repoPath)
		},
	)
}

func githubManagedSourceCheckoutState(repoSlug string) (string, bool, error) {
	sourcePath := strings.TrimSpace(githubManagedPaths(repoSlug).SourcePath)
	if sourcePath == "" {
		return "", false, nil
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return sourcePath, false, nil
		}
		return sourcePath, false, err
	}
	if !info.IsDir() {
		return sourcePath, false, fmt.Errorf("repo %s source checkout is not a directory", repoSlug)
	}
	return sourcePath, true, nil
}

func loadGithubRepository(repoSlug string) (githubRepositoryPayload, error) {
	apiBaseURL := strings.TrimSpace(os.Getenv("GITHUB_API_URL"))
	if apiBaseURL == "" {
		apiBaseURL = "https://api.github.com"
	}
	token, err := resolveGithubToken()
	if err != nil {
		return githubRepositoryPayload{}, err
	}
	var repository githubRepositoryPayload
	if err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s", repoSlug), &repository); err != nil {
		return githubRepositoryPayload{}, err
	}
	return repository, nil
}

func ensureGithubManagedCheckout(repoSlug string, owner repoAccessLockOwner) (string, error) {
	repository, err := loadGithubRepository(repoSlug)
	if err != nil {
		return "", fmt.Errorf("lookup GitHub repository %s: %w", repoSlug, err)
	}
	paths := githubManagedPaths(repoSlug)
	meta, err := ensureGithubManagedRepoMetadata(paths, githubTargetContext{Repository: repository}, time.Now().UTC())
	if err != nil {
		return "", fmt.Errorf("persist managed repo metadata for %s: %w", repoSlug, err)
	}
	if err := withManagedSourceWriteLock(repoSlug, owner, func() error {
		return ensureGithubSourceClone(paths, meta)
	}); err != nil {
		return "", fmt.Errorf("prepare managed source checkout for %s: %w", repoSlug, err)
	}
	return paths.SourcePath, nil
}
