package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const RepoScoutHelp = `nana repo scout - Manage scout startup policies

Usage:
  nana repo scout enable [--repo <path>] [--role improvement|enhancement|ui|both|all] [--mode auto|manual] [--schedule always|daily|weekly|when-resolved] [--issue-destination local|repo|fork] [--fork-repo <owner/repo>] [--labels <a,b>] [--session-limit <1-6>]

Behavior:
  - writes scout policy to Nana-managed runtime state outside the source checkout
  - preserves existing policy fields unless the matching flag is supplied
  - default role is both (improvement + enhancement); use --role ui or --role all to manage ui-scout
  - default new-policy mode is auto; default schedule is when-resolved; default new-policy issue destination is local
  - schedule controls when startup reruns the scout: always, daily, weekly, or after all reported issues are fixed or dropped
  - ui-scout accepts a session-limit that caps parallel page-audit sessions
`

type repoScoutEnableOptions struct {
	RepoPath         string
	Roles            []string
	Mode             string
	ModeSet          bool
	Schedule         string
	ScheduleSet      bool
	IssueDestination string
	DestinationSet   bool
	ForkRepo         string
	ForkRepoSet      bool
	Labels           []string
	LabelsSet        bool
	SessionLimit     int
	SessionLimitSet  bool
	GithubPolicyPath bool
}

func repoScout(cwd string, args []string) error {
	if len(args) == 0 || isHelpToken(args[0]) {
		fmt.Fprint(os.Stdout, RepoScoutHelp)
		return nil
	}
	switch args[0] {
	case "enable":
		if len(args) > 1 && isHelpToken(args[1]) {
			fmt.Fprint(os.Stdout, RepoScoutHelp)
			return nil
		}
		options, err := parseRepoScoutEnableArgs(args[1:])
		if err != nil {
			return err
		}
		return repoScoutEnable(cwd, options)
	default:
		return fmt.Errorf("Unknown repo scout subcommand: %s\n\n%s", args[0], RepoHelp)
	}
}

func parseRepoScoutEnableArgs(args []string) (repoScoutEnableOptions, error) {
	options := repoScoutEnableOptions{
		Roles: scoutDefaultRepoRoles(),
	}
	for index := 0; index < len(args); index++ {
		token := args[index]
		switch {
		case token == "--repo":
			value, err := requireRepoScoutValue(args, index, token)
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			options.RepoPath = value
			index++
		case strings.HasPrefix(token, "--repo="):
			options.RepoPath = strings.TrimSpace(strings.TrimPrefix(token, "--repo="))
		case token == "--role":
			value, err := requireRepoScoutValue(args, index, token)
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			roles, err := parseRepoScoutRoles(value)
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			options.Roles = roles
			index++
		case strings.HasPrefix(token, "--role="):
			roles, err := parseRepoScoutRoles(strings.TrimPrefix(token, "--role="))
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			options.Roles = roles
		case token == "--mode":
			value, err := requireRepoScoutValue(args, index, token)
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			parsed, err := parseRepoScoutMode(value)
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			options.Mode = parsed
			options.ModeSet = true
			index++
		case strings.HasPrefix(token, "--mode="):
			parsed, err := parseRepoScoutMode(strings.TrimPrefix(token, "--mode="))
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			options.Mode = parsed
			options.ModeSet = true
		case token == "--schedule":
			value, err := requireRepoScoutValue(args, index, token)
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			parsed, err := parseRepoScoutSchedule(value)
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			options.Schedule = parsed
			options.ScheduleSet = true
			index++
		case strings.HasPrefix(token, "--schedule="):
			parsed, err := parseRepoScoutSchedule(strings.TrimPrefix(token, "--schedule="))
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			options.Schedule = parsed
			options.ScheduleSet = true
		case token == "--issue-destination":
			value, err := requireRepoScoutValue(args, index, token)
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			parsed, err := parseRepoScoutIssueDestination(value)
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			options.IssueDestination = parsed
			options.DestinationSet = true
			index++
		case strings.HasPrefix(token, "--issue-destination="):
			parsed, err := parseRepoScoutIssueDestination(strings.TrimPrefix(token, "--issue-destination="))
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			options.IssueDestination = parsed
			options.DestinationSet = true
		case token == "--fork-repo":
			value, err := requireRepoScoutValue(args, index, token)
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			options.ForkRepo = strings.TrimSpace(value)
			options.ForkRepoSet = true
			index++
		case strings.HasPrefix(token, "--fork-repo="):
			options.ForkRepo = strings.TrimSpace(strings.TrimPrefix(token, "--fork-repo="))
			options.ForkRepoSet = true
		case token == "--labels":
			value, err := requireRepoScoutValue(args, index, token)
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			options.Labels = parseRepoScoutLabels(value)
			options.LabelsSet = true
			index++
		case strings.HasPrefix(token, "--labels="):
			options.Labels = parseRepoScoutLabels(strings.TrimPrefix(token, "--labels="))
			options.LabelsSet = true
		case token == "--session-limit":
			value, err := requireRepoScoutValue(args, index, token)
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			parsed, err := parseRepoScoutSessionLimit(value)
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			options.SessionLimit = parsed
			options.SessionLimitSet = true
			index++
		case strings.HasPrefix(token, "--session-limit="):
			parsed, err := parseRepoScoutSessionLimit(strings.TrimPrefix(token, "--session-limit="))
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			options.SessionLimit = parsed
			options.SessionLimitSet = true
		case token == "--github":
			options.GithubPolicyPath = true
		default:
			return repoScoutEnableOptions{}, fmt.Errorf("Unknown repo scout enable option: %s\n\n%s", token, RepoHelp)
		}
	}
	if options.DestinationSet && options.IssueDestination == improvementDestinationFork && !validRepoSlug(options.ForkRepo) {
		return repoScoutEnableOptions{}, fmt.Errorf("--issue-destination fork requires --fork-repo <owner/repo>")
	}
	if strings.TrimSpace(options.ForkRepo) != "" && !validRepoSlug(options.ForkRepo) {
		return repoScoutEnableOptions{}, fmt.Errorf("Invalid --fork-repo value %q. Expected owner/repo.", options.ForkRepo)
	}
	if options.SessionLimitSet && !supportsSessionLimitForAnyScoutRole(options.Roles) {
		return repoScoutEnableOptions{}, fmt.Errorf("--session-limit only applies when --role includes ui")
	}
	return options, nil
}

func repoScoutEnable(cwd string, options repoScoutEnableOptions) error {
	repoPath, err := resolveRepoScoutPolicyPath(cwd, options.RepoPath)
	if err != nil {
		return err
	}
	written := []string{}
	for _, role := range options.Roles {
		path := repoScoutPolicyPath(repoPath, role, options.GithubPolicyPath)
		if err := withSourceWriteLock(repoPath, repoAccessLockOwner{
			Backend: "repo-scout",
			RunID:   sanitizePathToken(filepath.Base(repoPath)),
			Purpose: "enable-" + sanitizePathToken(role),
			Label:   "repo-scout-enable",
		}, func() error {
			policy := readScoutPolicy(repoPath, role)
			policy.Version = 1
			if options.ModeSet || strings.TrimSpace(policy.Mode) == "" {
				policy.Mode = defaultString(options.Mode, "auto")
			}
			if options.ScheduleSet || strings.TrimSpace(policy.Schedule) == "" {
				policy.Schedule = defaultString(options.Schedule, scoutScheduleWhenResolved)
			}
			if options.DestinationSet || strings.TrimSpace(policy.IssueDestination) == "" {
				policy.IssueDestination = defaultString(options.IssueDestination, improvementDestinationLocal)
			}
			if options.ForkRepoSet {
				policy.ForkRepo = options.ForkRepo
			}
			if options.LabelsSet {
				policy.Labels = normalizeScoutLabels(options.Labels, role)
			}
			if options.SessionLimitSet && scoutRoleSupportsSessionLimit(role) {
				policy.SessionLimit = options.SessionLimit
			}
			if err := writeGithubJSON(path, policy); err != nil {
				return err
			}
			for _, legacyPath := range repoScoutLegacyPolicyPaths(repoPath, role) {
				if _, err := removePathIfExists(legacyPath); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		written = append(written, path)
	}
	for _, path := range written {
		fmt.Fprintf(os.Stdout, "[repo] Wrote scout policy: %s\n", path)
	}
	fmt.Fprintln(os.Stdout, "[repo] `nana start` will run supported scout startup automation in this repo.")
	return nil
}

func resolveRepoScoutPolicyPath(cwd string, repoPath string) (string, error) {
	target := cwd
	if strings.TrimSpace(repoPath) != "" {
		if filepath.IsAbs(repoPath) {
			target = repoPath
		} else {
			target = filepath.Join(cwd, repoPath)
		}
	}
	absolute, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repo scout policy path must be a directory: %s", absolute)
	}
	return absolute, nil
}

func repoScoutPolicyPath(repoPath string, role string, githubPolicyPath bool) string {
	_ = githubPolicyPath
	return filepath.Join(repoScoutPolicyDir(repoPath), scoutPolicyFileName(role))
}

func repoScoutPolicyDir(repoPath string) string {
	repoPath = filepath.Clean(strings.TrimSpace(repoPath))
	if repoPath == "" {
		return ""
	}
	if filepath.Base(repoPath) == "source" {
		managedRoot := filepath.Dir(repoPath)
		if rel, err := filepath.Rel(githubWorkReposRoot(), managedRoot); err == nil && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." && validRepoSlug(filepath.ToSlash(rel)) {
			return filepath.Join(managedRoot, "scouts")
		}
	}
	return filepath.Join(localWorkRepoDir(repoPath), "scouts")
}

func repoScoutPolicyFileName(role string) string { return scoutPolicyFileName(role) }

func repoScoutLegacyPolicyPaths(repoPath string, role string) []string {
	return scoutLegacyPolicyPaths(repoPath, role)
}

func repoScoutReadPaths(repoPath string, role string) []string {
	canonical := repoScoutPolicyPath(repoPath, role, false)
	if fileExists(canonical) {
		return []string{canonical}
	}
	return append([]string{canonical}, repoScoutLegacyPolicyPaths(repoPath, role)...)
}

func repoScoutConfiguredPath(repoPath string, role string) string {
	canonical := repoScoutPolicyPath(repoPath, role, false)
	if fileExists(canonical) {
		return canonical
	}
	for _, legacyPath := range repoScoutLegacyPolicyPaths(repoPath, role) {
		if fileExists(legacyPath) {
			return legacyPath
		}
	}
	return canonical
}

func requireRepoScoutValue(args []string, index int, flag string) (string, error) {
	if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
		return "", fmt.Errorf("Missing value after %s.\n%s", flag, RepoHelp)
	}
	return strings.TrimSpace(args[index+1]), nil
}

func parseRepoScoutRoles(value string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "both":
		return scoutDefaultRepoRoles(), nil
	case "all":
		return append([]string{}, supportedScoutRoleOrder...), nil
	case "default":
		return scoutDefaultRepoRoles(), nil
	default:
		for _, role := range supportedScoutRoleOrder {
			if scoutRoleMatchesToken(role, value) {
				return []string{role}, nil
			}
		}
		return nil, fmt.Errorf("Invalid --role value %q. Expected improvement, enhancement, ui, both, or all.", value)
	}
}

func repoScoutHasRole(roles []string, target string) bool {
	return scoutRoleListIncludes(roles, target)
}

func supportsSessionLimitForAnyScoutRole(roles []string) bool {
	for _, role := range roles {
		if scoutRoleSupportsSessionLimit(role) {
			return true
		}
	}
	return false
}

func parseRepoScoutMode(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "auto", "manual":
		return normalized, nil
	default:
		return "", fmt.Errorf("Invalid --mode value %q. Expected auto or manual.", value)
	}
}

func parseRepoScoutIssueDestination(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "local":
		return improvementDestinationLocal, nil
	case "repo", "target":
		return improvementDestinationTarget, nil
	case "fork":
		return improvementDestinationFork, nil
	default:
		return "", fmt.Errorf("Invalid --issue-destination value %q. Expected local, repo, or fork.", value)
	}
}

func parseRepoScoutLabels(value string) []string {
	labels := []string{}
	for _, part := range strings.Split(value, ",") {
		label := strings.ToLower(strings.TrimSpace(part))
		if label != "" {
			labels = append(labels, label)
		}
	}
	return uniqueStrings(labels)
}

func parseRepoScoutSessionLimit(value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 1 || parsed > maxScoutSessionLimit {
		return 0, fmt.Errorf("Invalid --session-limit value %q. Expected an integer from 1 to %d.", value, maxScoutSessionLimit)
	}
	return parsed, nil
}
