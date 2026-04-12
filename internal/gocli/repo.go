package gocli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const RepoHelp = `nana repo - Repository onboarding and verification-plan inspection

Usage:
  nana repo onboard [--repo <path>] [--json]
  nana repo onboard <owner/repo> [--repo-mode local|fork|repo] [--issue-pick manual|label|auto] [--pr-forward approve|auto]
  nana repo config <owner/repo> [--repo-mode local|fork|repo] [--issue-pick manual|label|auto] [--pr-forward approve|auto]
  nana repo defaults set [--repo-mode local|fork|repo] [--issue-pick manual|label|auto] [--pr-forward approve|auto]
  nana repo defaults show [--json]
  nana repo explain <owner/repo> [--json]
  nana repo help

Notes:
  - Usually you do not need to run local onboarding manually; Nana performs onboarding automatically when workflows such as ` + "`nana work start --task ...`" + ` begin.
  - Run local onboarding manually when you want to inspect the detected lint/unit/integration/benchmark split before a long run, after changing Makefile/build scripts, or when a warning suggests the repo should split unit/integration/benchmark targets more cleanly.
  - Use repo config/explain for GitHub automation settings consumed by ` + "`nana start`" + `.
`

func Repo(cwd string, args []string) error {
	if len(args) == 0 || isHelpToken(args[0]) {
		fmt.Fprint(os.Stdout, RepoHelp)
		return nil
	}

	switch args[0] {
	case "onboard":
		if len(args) > 1 && validRepoSlug(args[1]) {
			return repoGithubOnboard(args[1:])
		}
		repoPath := ""
		jsonOutput := false
		for index := 1; index < len(args); index++ {
			token := args[index]
			switch {
			case token == "--repo":
				if index+1 >= len(args) {
					return fmt.Errorf("Missing value after --repo.\n%s", RepoHelp)
				}
				repoPath = args[index+1]
				index++
			case strings.HasPrefix(token, "--repo="):
				repoPath = strings.TrimSpace(strings.TrimPrefix(token, "--repo="))
			case token == "--json":
				jsonOutput = true
			default:
				return fmt.Errorf("Unknown repo onboard option: %s\n\n%s", token, RepoHelp)
			}
		}
		return repoOnboard(cwd, repoPath, jsonOutput)
	case "config":
		if len(args) < 2 || !validRepoSlug(args[1]) {
			return fmt.Errorf("Usage: nana repo config <owner/repo> [--repo-mode local|fork|repo] [--issue-pick manual|label|auto] [--pr-forward approve|auto]\n\n%s", RepoHelp)
		}
		return githubDefaultsSet(args[1:])
	case "defaults":
		return repoAutomationDefaults(args[1:])
	case "explain":
		return repoExplain(args[1:])
	default:
		return fmt.Errorf("Unknown repo subcommand: %s\n\n%s", args[0], RepoHelp)
	}
}

type repoAutomationDefaultsConfig struct {
	Version        int    `json:"version"`
	RepoMode       string `json:"repo_mode,omitempty"`
	IssuePickMode  string `json:"issue_pick_mode,omitempty"`
	PRForwardMode  string `json:"pr_forward_mode,omitempty"`
	ForkIssuesMode string `json:"fork_issues_mode,omitempty"`
	ImplementMode  string `json:"implement_mode,omitempty"`
	PublishTarget  string `json:"publish_target,omitempty"`
	UpdatedAt      string `json:"updated_at"`
}

func repoGithubOnboard(args []string) error {
	if len(args) == 1 {
		defaults, _ := readRepoAutomationDefaults()
		if defaults != nil {
			patched := []string{args[0]}
			if defaults.RepoMode != "" {
				patched = append(patched, "--repo-mode", defaults.RepoMode)
			}
			if defaults.IssuePickMode != "" {
				patched = append(patched, "--issue-pick", defaults.IssuePickMode)
			}
			if defaults.PRForwardMode != "" {
				patched = append(patched, "--pr-forward", defaults.PRForwardMode)
			}
			if defaults.ForkIssuesMode != "" {
				patched = append(patched, "--fork-issues", defaults.ForkIssuesMode)
			}
			if defaults.ImplementMode != "" {
				patched = append(patched, "--implement", defaults.ImplementMode)
			}
			if defaults.PublishTarget != "" {
				patched = append(patched, "--publish", defaults.PublishTarget)
			}
			return githubDefaultsSet(patched)
		}
	}
	return githubDefaultsSet(args)
}

func repoAutomationDefaults(args []string) error {
	if len(args) == 0 || isHelpToken(args[0]) {
		return fmt.Errorf("Usage: nana repo defaults set|show [--json]\n\n%s", RepoHelp)
	}
	switch args[0] {
	case "set":
		return repoAutomationDefaultsSet(args[1:])
	case "show":
		jsonOutput := len(args) > 1 && args[1] == "--json"
		return repoAutomationDefaultsShow(jsonOutput)
	default:
		return fmt.Errorf("Unknown repo defaults subcommand: %s\n\n%s", args[0], RepoHelp)
	}
}

func repoAutomationDefaultsSet(args []string) error {
	existing, _ := readRepoAutomationDefaults()
	config := repoAutomationDefaultsConfig{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	if existing != nil {
		config = *existing
		config.Version = 1
		config.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	for index := 0; index < len(args); index++ {
		token := args[index]
		switch {
		case token == "--repo-mode":
			value, err := requireRepoDefaultsValue(args, index, token)
			if err != nil {
				return err
			}
			parsed, err := parseGithubRepoMode(value, token)
			if err != nil {
				return err
			}
			config.RepoMode = parsed
			config.PublishTarget = repoModeToPublishTarget(parsed)
			index++
		case strings.HasPrefix(token, "--repo-mode="):
			parsed, err := parseGithubRepoMode(strings.TrimPrefix(token, "--repo-mode="), "--repo-mode")
			if err != nil {
				return err
			}
			config.RepoMode = parsed
			config.PublishTarget = repoModeToPublishTarget(parsed)
		case token == "--issue-pick":
			value, err := requireRepoDefaultsValue(args, index, token)
			if err != nil {
				return err
			}
			parsed, err := parseGithubIssuePickMode(value, token)
			if err != nil {
				return err
			}
			config.IssuePickMode = parsed
			config.ForkIssuesMode = issuePickModeToAutomationMode(parsed)
			config.ImplementMode = issuePickModeToAutomationMode(parsed)
			index++
		case strings.HasPrefix(token, "--issue-pick="):
			parsed, err := parseGithubIssuePickMode(strings.TrimPrefix(token, "--issue-pick="), "--issue-pick")
			if err != nil {
				return err
			}
			config.IssuePickMode = parsed
			config.ForkIssuesMode = issuePickModeToAutomationMode(parsed)
			config.ImplementMode = issuePickModeToAutomationMode(parsed)
		case token == "--pr-forward":
			value, err := requireRepoDefaultsValue(args, index, token)
			if err != nil {
				return err
			}
			parsed, err := parseGithubPRForwardMode(value, token)
			if err != nil {
				return err
			}
			config.PRForwardMode = parsed
			index++
		case strings.HasPrefix(token, "--pr-forward="):
			parsed, err := parseGithubPRForwardMode(strings.TrimPrefix(token, "--pr-forward="), "--pr-forward")
			if err != nil {
				return err
			}
			config.PRForwardMode = parsed
		case token == "--fork-issues":
			value, err := requireRepoDefaultsValue(args, index, token)
			if err != nil {
				return err
			}
			parsed, err := parseGithubAutomationMode(value, token)
			if err != nil {
				return err
			}
			config.ForkIssuesMode = parsed
			index++
		case strings.HasPrefix(token, "--fork-issues="):
			parsed, err := parseGithubAutomationMode(strings.TrimPrefix(token, "--fork-issues="), "--fork-issues")
			if err != nil {
				return err
			}
			config.ForkIssuesMode = parsed
		case token == "--implement":
			value, err := requireRepoDefaultsValue(args, index, token)
			if err != nil {
				return err
			}
			parsed, err := parseGithubAutomationMode(value, token)
			if err != nil {
				return err
			}
			config.ImplementMode = parsed
			index++
		case strings.HasPrefix(token, "--implement="):
			parsed, err := parseGithubAutomationMode(strings.TrimPrefix(token, "--implement="), "--implement")
			if err != nil {
				return err
			}
			config.ImplementMode = parsed
		case token == "--publish":
			value, err := requireRepoDefaultsValue(args, index, token)
			if err != nil {
				return err
			}
			parsed, err := parseGithubPublishTarget(value, token)
			if err != nil {
				return err
			}
			config.PublishTarget = parsed
			index++
		case strings.HasPrefix(token, "--publish="):
			parsed, err := parseGithubPublishTarget(strings.TrimPrefix(token, "--publish="), "--publish")
			if err != nil {
				return err
			}
			config.PublishTarget = parsed
		default:
			return fmt.Errorf("Unknown repo defaults set option: %s\n\n%s", token, RepoHelp)
		}
	}
	if config.RepoMode == "" && config.IssuePickMode == "" && config.PRForwardMode == "" && config.ForkIssuesMode == "" && config.ImplementMode == "" && config.PublishTarget == "" {
		return fmt.Errorf("Specify at least one repo automation default.\n%s", RepoHelp)
	}
	if err := writeGithubJSON(repoAutomationDefaultsPath(), config); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "[repo] Saved manual onboard defaults: repo-mode=%s issue-pick=%s pr-forward=%s\n", defaultString(config.RepoMode, "local"), defaultString(config.IssuePickMode, "manual"), defaultString(config.PRForwardMode, "approve"))
	fmt.Fprintf(os.Stdout, "[repo] Defaults path: %s\n", repoAutomationDefaultsPath())
	return nil
}

func repoAutomationDefaultsShow(jsonOutput bool) error {
	config, _ := readRepoAutomationDefaults()
	if config == nil {
		config = &repoAutomationDefaultsConfig{Version: 1}
	}
	if jsonOutput {
		content, err := json.MarshalIndent(config, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "%s\n", string(content))
		return nil
	}
	fmt.Fprintf(os.Stdout, "[repo] Manual onboard defaults: repo-mode=%s issue-pick=%s pr-forward=%s\n", defaultString(config.RepoMode, "local"), defaultString(config.IssuePickMode, "manual"), defaultString(config.PRForwardMode, "approve"))
	fmt.Fprintf(os.Stdout, "[repo] Defaults path: %s\n", repoAutomationDefaultsPath())
	return nil
}

func requireRepoDefaultsValue(args []string, index int, flag string) (string, error) {
	if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
		return "", fmt.Errorf("Missing value after %s.\n%s", flag, RepoHelp)
	}
	return args[index+1], nil
}

func readRepoAutomationDefaults() (*repoAutomationDefaultsConfig, error) {
	var config repoAutomationDefaultsConfig
	if err := readGithubJSON(repoAutomationDefaultsPath(), &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func repoAutomationDefaultsPath() string {
	return filepath.Join(githubNanaHome(), "repo-defaults.json")
}

func repoOnboard(cwd string, repoPath string, jsonOutput bool) error {
	repoRoot, err := resolveLocalWorkRepoRoot(cwd, repoPath)
	if err != nil {
		return err
	}
	plan := detectGithubVerificationPlan(repoRoot)
	considerations := inferGithubInitialRepoConsiderations(repoRoot, filepath.Base(repoRoot), plan)
	repoSlug := inferGithubRepoSlugFromRepo(repoRoot)
	profile, profilePath, err := refreshGithubRepoProfile(repoSlug, repoRoot, plan, considerations.Considerations, time.Now().UTC())
	if err != nil {
		return err
	}
	if jsonOutput {
		payload := map[string]any{
			"repo_root":                repoRoot,
			"repo_slug":                repoSlug,
			"verification_plan":        plan,
			"suggested_considerations": considerations.Considerations,
			"repo_profile_path":        profilePath,
			"repo_profile":             profile,
		}
		content, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "%s\n", string(content))
		return nil
	}

	fmt.Fprintf(os.Stdout, "[repo] Onboarding %s\n", repoRoot)
	if profile != nil {
		fmt.Fprintf(os.Stdout, "[repo] Repo profile fingerprint: %s\n", profile.Fingerprint)
		if profilePath != "" {
			fmt.Fprintf(os.Stdout, "[repo] Repo profile path: %s\n", profilePath)
		}
	}
	fmt.Fprintf(
		os.Stdout,
		"[repo] Verification plan: lint=%d compile=%d unit=%d integration=%d benchmark=%d\n",
		len(plan.Lint),
		len(plan.Compile),
		len(plan.Unit),
		len(plan.Integration),
		len(plan.Benchmarks),
	)
	fmt.Fprintf(os.Stdout, "[repo] Suggested considerations: %s\n", joinOrNone(considerations.Considerations))
	printRepoPlanSection("Lint", plan.Lint)
	printRepoPlanSection("Compile", plan.Compile)
	printRepoPlanSection("Unit", plan.Unit)
	printRepoPlanSection("Integration", plan.Integration)
	printRepoPlanSection("Benchmark", plan.Benchmarks)
	if len(plan.Warnings) == 0 {
		fmt.Fprintln(os.Stdout, "[repo] Warnings: (none)")
	} else {
		for _, warning := range plan.Warnings {
			fmt.Fprintf(os.Stdout, "[repo] Warning: %s\n", warning)
		}
	}
	if profile != nil {
		for _, warning := range profile.Warnings {
			fmt.Fprintf(os.Stdout, "[repo] Profile warning: %s\n", warning)
		}
	}
	return nil
}

func repoExplain(args []string) error {
	jsonOutput := false
	repoSlug := ""
	for _, token := range args {
		switch token {
		case "--json":
			jsonOutput = true
		default:
			if repoSlug != "" {
				return fmt.Errorf("Usage: nana repo explain <owner/repo> [--json]\n\n%s", RepoHelp)
			}
			repoSlug = strings.TrimSpace(token)
		}
	}
	if !validRepoSlug(repoSlug) {
		return fmt.Errorf("Usage: nana repo explain <owner/repo> [--json]\n\n%s", RepoHelp)
	}
	settings, _ := readGithubRepoSettings(githubRepoSettingsPath(repoSlug))
	repoMode := resolvedGithubRepoMode(settings)
	issuePickMode := resolvedGithubIssuePickMode(settings)
	prForwardMode := resolvedGithubPRForwardMode(settings)
	forkMode := "manual"
	implementMode := "manual"
	publishTarget := repoModeToPublishTarget(repoMode)
	if settings != nil {
		forkMode = defaultString(normalizeGithubAutomationMode(settings.ForkIssuesMode), issuePickModeToAutomationMode(issuePickMode))
		implementMode = defaultString(normalizeGithubAutomationMode(settings.ImplementMode), issuePickModeToAutomationMode(issuePickMode))
		publishTarget = defaultString(normalizeGithubPublishTarget(settings.PublishTarget), publishTarget)
	}
	state, _ := readStartWorkState(repoSlug)
	payload := map[string]any{
		"repo":                       repoSlug,
		"settings_path":              githubRepoSettingsPath(repoSlug),
		"state_path":                 startWorkStatePath(repoSlug),
		"repo_mode":                  repoMode,
		"issue_pick_mode":            issuePickMode,
		"pr_forward_mode":            prForwardMode,
		"fork_issues_mode":           forkMode,
		"implement_mode":             implementMode,
		"publish_target":             publishTarget,
		"start_command":              "nana start",
		"labeled_fork_labels":        []string{"nana"},
		"labeled_implement_labels":   []string{"nana"},
		"open_pr_cap":                startWorkDefaultOpenPRCap,
		"parallel_limit":             startWorkDefaultParallel,
		"is_enabled_for_start":       githubRepoAutomationEnabled(settings),
		"start_promotes_before_work": true,
	}
	if state != nil {
		promoted, reused, activeSkips := startWorkPromotionCounts(state)
		payload["fork_repo"] = state.ForkRepo
		payload["tracked_issues"] = len(state.Issues)
		payload["last_run"] = state.LastRun
		payload["promotion_summary"] = map[string]int{"promoted": promoted, "reused": reused, "active_skips": activeSkips}
		if len(state.PromotionSkips) > 0 {
			payload["promotion_skips"] = state.PromotionSkips
		}
	}
	if jsonOutput {
		content, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "%s\n", string(content))
		return nil
	}
	fmt.Fprintf(os.Stdout, "[repo] Repo: %s\n", repoSlug)
	fmt.Fprintf(os.Stdout, "[repo] Settings path: %s\n", githubRepoSettingsPath(repoSlug))
	fmt.Fprintf(os.Stdout, "[repo] Start state path: %s\n", startWorkStatePath(repoSlug))
	fmt.Fprintf(os.Stdout, "[repo] repo-mode: %s\n", repoMode)
	fmt.Fprintf(os.Stdout, "[repo] issue-pick: %s\n", issuePickMode)
	fmt.Fprintf(os.Stdout, "[repo] pr-forward: %s\n", prForwardMode)
	fmt.Fprintf(os.Stdout, "[repo] fork-issues: %s\n", forkMode)
	fmt.Fprintf(os.Stdout, "[repo] implement: %s\n", implementMode)
	fmt.Fprintf(os.Stdout, "[repo] publish: %s\n", publishTarget)
	fmt.Fprintf(os.Stdout, "[repo] Start participation: %t\n", githubRepoAutomationEnabled(settings))
	fmt.Fprintln(os.Stdout, "[repo] `nana start` mirrors eligible issues, starts eligible workers, and forwards PRs when pr-forward is auto.")
	fmt.Fprintln(os.Stdout, "[repo] label issue-pick mode requires the single opt-in label: nana")
	fmt.Fprintf(os.Stdout, "[repo] Defaults: parallel=%d open_fork_pr_cap=%d\n", startWorkDefaultParallel, startWorkDefaultOpenPRCap)
	if state != nil {
		promoted, reused, activeSkips := startWorkPromotionCounts(state)
		fmt.Fprintf(os.Stdout, "[repo] Fork repo: %s\n", defaultString(state.ForkRepo, "(none)"))
		fmt.Fprintf(os.Stdout, "[repo] Tracked issues: %d\n", len(state.Issues))
		fmt.Fprintf(os.Stdout, "[repo] Forwarding: promoted=%d reused=%d active_skips=%d\n", promoted, reused, activeSkips)
		if len(state.PromotionSkips) > 0 {
			reasons := []string{}
			for _, skipped := range state.PromotionSkips {
				reasons = append(reasons, fmt.Sprintf("fork PR #%d: %s", skipped.ForkPRNumber, skipped.Reason))
			}
			slices.Sort(reasons)
			fmt.Fprintf(os.Stdout, "[repo] Forward skips: %s\n", strings.Join(reasons, "; "))
		}
	}

	return nil
}

func printRepoPlanSection(label string, commands []string) {
	if len(commands) == 0 {
		fmt.Fprintf(os.Stdout, "[repo] %s: (none)\n", label)
		return
	}
	fmt.Fprintf(os.Stdout, "[repo] %s: %s\n", label, strings.Join(commands, " | "))
}
