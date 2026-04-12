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
  nana repo scout enable [--repo <path>] [--role improvement|enhancement|both] [--mode auto|manual] [--issue-destination local|repo|fork] [--fork-repo <owner/repo>] [--labels <a,b>] [--max-issues <1-50>] [--github]

Behavior:
  - writes .nana/improvement-policy.json and/or .nana/enhancement-policy.json by default
  - use --github to write .github/nana-improvement-policy.json and/or .github/nana-enhancement-policy.json
  - preserves existing policy fields unless the matching flag is supplied
  - default role is both; default new-policy mode is auto; default new-policy issue destination is local
`

type repoScoutEnableOptions struct {
	RepoPath         string
	Roles            []string
	Mode             string
	ModeSet          bool
	IssueDestination string
	DestinationSet   bool
	ForkRepo         string
	ForkRepoSet      bool
	Labels           []string
	LabelsSet        bool
	MaxIssues        int
	MaxIssuesSet     bool
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
		Roles: []string{improvementScoutRole, enhancementScoutRole},
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
		case token == "--max-issues":
			value, err := requireRepoScoutValue(args, index, token)
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			parsed, err := parseRepoScoutMaxIssues(value)
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			options.MaxIssues = parsed
			options.MaxIssuesSet = true
			index++
		case strings.HasPrefix(token, "--max-issues="):
			parsed, err := parseRepoScoutMaxIssues(strings.TrimPrefix(token, "--max-issues="))
			if err != nil {
				return repoScoutEnableOptions{}, err
			}
			options.MaxIssues = parsed
			options.MaxIssuesSet = true
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
		policy := improvementPolicy{}
		_ = readGithubJSON(path, &policy)
		policy.Version = 1
		if options.ModeSet || strings.TrimSpace(policy.Mode) == "" {
			policy.Mode = defaultString(options.Mode, "auto")
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
		if options.MaxIssuesSet {
			policy.MaxIssues = options.MaxIssues
		}
		if err := writeGithubJSON(path, policy); err != nil {
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
	if githubPolicyPath {
		if role == enhancementScoutRole {
			return filepath.Join(repoPath, ".github", "nana-enhancement-policy.json")
		}
		return filepath.Join(repoPath, ".github", "nana-improvement-policy.json")
	}
	if role == enhancementScoutRole {
		return filepath.Join(repoPath, ".nana", "enhancement-policy.json")
	}
	return filepath.Join(repoPath, ".nana", "improvement-policy.json")
}

func requireRepoScoutValue(args []string, index int, flag string) (string, error) {
	if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
		return "", fmt.Errorf("Missing value after %s.\n%s", flag, RepoHelp)
	}
	return strings.TrimSpace(args[index+1]), nil
}

func parseRepoScoutRoles(value string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "both", "all":
		return []string{improvementScoutRole, enhancementScoutRole}, nil
	case "improvement", "improvements", improvementScoutRole:
		return []string{improvementScoutRole}, nil
	case "enhancement", "enhancements", enhancementScoutRole:
		return []string{enhancementScoutRole}, nil
	default:
		return nil, fmt.Errorf("Invalid --role value %q. Expected improvement, enhancement, or both.", value)
	}
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

func parseRepoScoutMaxIssues(value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 1 || parsed > maxScoutIssueCap {
		return 0, fmt.Errorf("Invalid --max-issues value %q. Expected an integer from 1 to %d.", value, maxScoutIssueCap)
	}
	return parsed, nil
}
