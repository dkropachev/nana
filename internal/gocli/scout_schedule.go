package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	scoutScheduleAlways       = "always"
	scoutScheduleDaily        = "daily"
	scoutScheduleWeekly       = "weekly"
	scoutScheduleWhenResolved = "when_resolved"
)

type scoutScheduleState struct {
	Version             int    `json:"version"`
	LastSuccessfulRunAt string `json:"last_successful_run_at,omitempty"`
	UpdatedAt           string `json:"updated_at,omitempty"`
}

type scoutScheduleDecision struct {
	Due    bool
	Reason string
}

func effectiveScoutSchedule(policy scoutPolicy) string {
	normalized := normalizeScoutSchedule(policy.Schedule)
	if normalized == "" {
		return scoutScheduleWhenResolved
	}
	return normalized
}

func normalizeScoutSchedule(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case scoutScheduleAlways:
		return scoutScheduleAlways
	case scoutScheduleDaily:
		return scoutScheduleDaily
	case scoutScheduleWeekly:
		return scoutScheduleWeekly
	case scoutScheduleWhenResolved, "when-resolved", "resolved":
		return scoutScheduleWhenResolved
	default:
		return ""
	}
}

func parseRepoScoutSchedule(value string) (string, error) {
	normalized := normalizeScoutSchedule(value)
	if normalized == "" {
		return "", fmt.Errorf("invalid scout schedule %q. Expected always, daily, weekly, or when-resolved", value)
	}
	return normalized, nil
}

func scoutScheduleStatePath(repoPath string, role string) string {
	return filepath.Join(repoScoutPolicyDir(repoPath), scoutRoleSpecFor(role).ConfigKey+"-schedule-state.json")
}

func readScoutScheduleState(repoPath string, role string) (scoutScheduleState, string, error) {
	path := scoutScheduleStatePath(repoPath, role)
	state := scoutScheduleState{Version: 1}
	if err := readGithubJSON(path, &state); err != nil {
		if os.IsNotExist(err) {
			return state, path, nil
		}
		return scoutScheduleState{}, "", err
	}
	state.Version = 1
	return state, path, nil
}

func writeScoutScheduleState(path string, state scoutScheduleState) error {
	state.Version = 1
	return writeGithubJSON(path, state)
}

func recordSuccessfulScoutRun(repoPath string, role string, when time.Time) error {
	state, path, err := readScoutScheduleState(repoPath, role)
	if err != nil {
		return err
	}
	timestamp := when.UTC().Format(time.RFC3339)
	state.LastSuccessfulRunAt = timestamp
	state.UpdatedAt = timestamp
	return writeScoutScheduleState(path, state)
}

func scoutScheduleDecisionForRole(repoPath string, repoSlug string, role string, policy scoutPolicy, now time.Time) (scoutScheduleDecision, error) {
	schedule := effectiveScoutSchedule(policy)
	if schedule == scoutScheduleAlways {
		return scoutScheduleDecision{Due: true}, nil
	}
	if schedule == scoutScheduleWhenResolved {
		outstanding, err := scoutOutstandingCount(repoPath, repoSlug, role, policy)
		if err != nil {
			return scoutScheduleDecision{}, err
		}
		if outstanding == 0 {
			return scoutScheduleDecision{Due: true}, nil
		}
		return scoutScheduleDecision{
			Due:    false,
			Reason: fmt.Sprintf("waiting for %d previously reported %s item(s) to be fixed or dropped", outstanding, scoutIssueHeading(role)),
		}, nil
	}
	state, _, err := readScoutScheduleState(repoPath, role)
	if err != nil {
		return scoutScheduleDecision{}, err
	}
	lastSuccess, ok := parseScoutScheduleTime(state.LastSuccessfulRunAt)
	if !ok {
		return scoutScheduleDecision{Due: true}, nil
	}
	switch schedule {
	case scoutScheduleDaily:
		return scoutIntervalScheduleDecision(lastSuccess, now, 24*time.Hour, "daily")
	case scoutScheduleWeekly:
		return scoutIntervalScheduleDecision(lastSuccess, now, 7*24*time.Hour, "weekly")
	default:
		return scoutScheduleDecision{Due: true}, nil
	}
}

func scoutIntervalScheduleDecision(lastSuccess time.Time, now time.Time, interval time.Duration, label string) (scoutScheduleDecision, error) {
	nextDue := lastSuccess.Add(interval)
	if !now.Before(nextDue) {
		return scoutScheduleDecision{Due: true}, nil
	}
	return scoutScheduleDecision{
		Due:    false,
		Reason: fmt.Sprintf("%s schedule not due until %s", label, nextDue.UTC().Format(time.RFC3339)),
	}, nil
}

func parseScoutScheduleTime(value string) (time.Time, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func scoutOutstandingCount(repoPath string, repoSlug string, role string, policy scoutPolicy) (int, error) {
	switch normalizeScoutDestination(policy.IssueDestination) {
	case improvementDestinationTarget, improvementDestinationFork:
		targetRepo, err := resolveScoutIssueTargetRepo(repoSlug, policy, role)
		if err != nil {
			return 0, err
		}
		if !validRepoSlug(targetRepo) {
			return 0, nil
		}
		apiBaseURL := strings.TrimSpace(os.Getenv("GITHUB_API_URL"))
		if apiBaseURL == "" {
			apiBaseURL = "https://api.github.com"
		}
		token, err := resolveGithubToken()
		if err != nil {
			return 0, err
		}
		return countOpenScoutIssues(apiBaseURL, token, targetRepo, role)
	default:
		return countOutstandingLocalScoutItems(repoPath, repoSlug, role)
	}
}

func localScoutPickupStatusIsResolved(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "dismissed", "deleted":
		return true
	default:
		return false
	}
}

func resolveScoutIssueTargetRepo(repoSlug string, policy scoutPolicy, role string) (string, error) {
	destination := normalizeScoutDestination(policy.IssueDestination)
	switch destination {
	case improvementDestinationFork:
		targetRepo := strings.TrimSpace(policy.ForkRepo)
		if validRepoSlug(targetRepo) {
			return targetRepo, nil
		}
		apiBaseURL := strings.TrimSpace(os.Getenv("GITHUB_API_URL"))
		if apiBaseURL == "" {
			apiBaseURL = "https://api.github.com"
		}
		token, err := resolveGithubToken()
		if err != nil {
			return "", err
		}
		var viewer struct {
			Login string `json:"login"`
		}
		if err := githubAPIGetJSON(apiBaseURL, token, "/user", &viewer); err != nil {
			return "", err
		}
		parts := strings.Split(repoSlug, "/")
		if len(parts) != 2 || strings.TrimSpace(viewer.Login) == "" {
			return "", fmt.Errorf("cannot infer fork repo for %s; set fork_repo in %s policy", repoSlug, role)
		}
		return viewer.Login + "/" + parts[1], nil
	case improvementDestinationTarget:
		return repoSlug, nil
	default:
		return "", nil
	}
}
