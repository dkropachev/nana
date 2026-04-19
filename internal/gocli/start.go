package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const StartHelp = `nana start - Run automation for onboarded repositories

Usage:
  nana start [--repo <owner/repo>] [--parallel <n>] [--per-repo-workers <n>] [--max-open-prs <n>] [--once|--cycles <n>|--forever] [--interval <duration>] [--no-ui] [--ui-api-port <port>] [--ui-web-port <port>] [-- codex-args...]
  nana start help

Behavior:
  - with no options, loops indefinitely until interrupted
  - scans onboarded GitHub repos under ~/.nana/work/repos
  - skips repos where repo-mode is local or issue-pick is manual
  - --parallel limits how many repos may be active at once
  - --per-repo-workers limits how many workers a repo may use at once
  - --interval controls the target time between cycle starts in forever mode
  - uses separate per-repo service and implementation queues that share the repo worker budget
  - triages issues before implementation pickup and persists triage results locally
  - runs supported scouts from the managed source checkout when scout policies exist
  - forwards PRs when pr-forward is auto: fork creates upstream PRs; repo attempts merge
  - launches loopback UI services by default: REST API + web console
  - use --once or --cycles <n> for bounded runs
`

const startDefaultGlobalParallel = 3

type startOptions struct {
	RepoSlug       string
	Parallel       int
	PerRepoWorkers int
	MaxOpenPR      int
	Cycles         int
	Forever        bool
	Interval       time.Duration
	NoUI           bool
	UIAPIPort      int
	UIWebPort      int
	CodexArgs      []string
}

type startRuntimeOptions struct {
	Cycles   int
	Forever  bool
	Interval time.Duration
}

var startRunStartWork = startWorkStart
var startPromoteStartWork = startWorkPromote
var startRunScoutStart = runScoutStart
var startRunLocalScoutPickup = runLocalScoutDiscoveredItems
var startRunRepoCycle = runStartRepoCycle
var startLoopNow = time.Now
var startLoopSleep = time.Sleep
var startLoopContinue = func() bool { return true }

func Start(cwd string, args []string) error {
	if len(args) > 0 && isHelpToken(args[0]) {
		fmt.Fprint(os.Stdout, StartHelp)
		return nil
	}
	cleanArgs, runtime, err := parseStartRuntimeArgs(args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		runtime.Forever = true
	}
	uiOptions, err := parseStartUIOptions(cleanArgs)
	if err != nil {
		return err
	}
	var ui *startUISupervisor
	if !uiOptions.NoUI {
		ui, err = launchStartUISupervisor(cwd, uiOptions)
		if err != nil {
			return err
		}
		defer ui.Close()
	}
	if startShouldRunScouts(cwd, cleanArgs) {
		return runStartLoop(runtime, func() error {
			return StartScouts(cwd, cleanArgs)
		})
	}
	options, err := parseStartArgs(cleanArgs)
	if err != nil {
		return err
	}
	options.Cycles = runtime.Cycles
	options.Forever = runtime.Forever
	options.Interval = runtime.Interval
	return runStartLoop(runtime, func() error {
		repos, err := resolveStartRepos(options.RepoSlug)
		if err != nil {
			return err
		}
		if len(repos) == 0 {
			fmt.Fprintln(os.Stdout, "[start] No onboarded repos with repo-mode fork/repo and issue-pick automation enabled.")
			return nil
		}
		fmt.Fprintf(os.Stdout, "[start] Repos selected: %s\n", strings.Join(repos, ", "))
		return runStartRepoCycles(cwd, repos, options)
	})
}

func runStartRepoCycle(cwd string, repoSlug string, options startOptions) error {
	settings, _ := readGithubRepoSettings(githubRepoSettingsPath(repoSlug))
	forkMode := "manual"
	implementMode := "manual"
	repoMode := resolvedGithubRepoMode(settings)
	issuePickMode := resolvedGithubIssuePickMode(settings)
	prForwardMode := resolvedGithubPRForwardMode(settings)
	publishTarget := repoModeToPublishTarget(repoMode)
	if settings != nil {
		forkMode = defaultString(normalizeGithubAutomationMode(settings.ForkIssuesMode), issuePickModeToAutomationMode(issuePickMode))
		implementMode = defaultString(normalizeGithubAutomationMode(settings.ImplementMode), issuePickModeToAutomationMode(issuePickMode))
	}
	fmt.Fprintf(os.Stdout, "[start] %s: repo-mode=%s issue-pick=%s pr-forward=%s\n", repoSlug, repoMode, issuePickMode, prForwardMode)
	if repoMode == "fork" && prForwardMode == "auto" {
		if _, err := os.Stat(startWorkStatePath(repoSlug)); err == nil {
			if err := startPromoteStartWork(startWorkOptions{RepoSlug: repoSlug}); err != nil {
				return err
			}
		}
	}
	if repoMode == "local" || issuePickMode == "manual" {
		return nil
	}
	workOptions := startWorkOptions{
		RepoSlug:       repoSlug,
		Parallel:       options.PerRepoWorkers,
		MaxOpenPR:      options.MaxOpenPR,
		ForkIssuesMode: forkMode,
		ImplementMode:  implementMode,
		PublishTarget:  publishTarget,
		RepoMode:       repoMode,
		IssuePickMode:  issuePickMode,
		PRForwardMode:  prForwardMode,
		CodexArgs:      options.CodexArgs,
	}
	return runStartRepoSchedulerCycle(cwd, repoSlug, workOptions, options)
}

func startRepoHasScoutPolicies(repoSlug string) bool {
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	if info, err := os.Stat(sourcePath); err == nil && info.IsDir() {
		if len(supportedScoutRoles(sourcePath)) > 0 {
			return true
		}
	}
	repoPath, checkoutErr := ensureImproveGithubCheckout(repoSlug)
	if checkoutErr != nil {
		fmt.Fprintf(os.Stdout, "[start] %s: scout policy check skipped: %v\n", repoSlug, checkoutErr)
		return false
	}
	return len(supportedScoutRoles(repoPath)) > 0
}

func startShouldRunScouts(cwd string, args []string) bool {
	if len(args) == 0 {
		return len(supportedScoutRoles(cwd)) > 0
	}
	for index := 0; index < len(args); index++ {
		token := args[index]
		if token == "--" {
			break
		}
		switch {
		case token == "--from-file", strings.HasPrefix(token, "--from-file="), token == "--focus", strings.HasPrefix(token, "--focus="), token == "--dry-run", token == "--local-only":
			return true
		case token == "--no-ui", token == "--ui-api-port", token == "--ui-web-port":
			if token == "--ui-api-port" || token == "--ui-web-port" {
				index++
			}
			continue
		case strings.HasPrefix(token, "--ui-api-port="), strings.HasPrefix(token, "--ui-web-port="):
			continue
		case token == "--repo":
			if index+1 < len(args) && startRepoValueLooksLikePath(args[index+1]) {
				return true
			}
			index++
		case strings.HasPrefix(token, "--repo="):
			if startRepoValueLooksLikePath(strings.TrimPrefix(token, "--repo=")) {
				return true
			}
		case token == "--parallel", token == "--per-repo-workers", token == "--max-open-prs", token == "--cycles":
			index++
		case strings.HasPrefix(token, "--parallel="), strings.HasPrefix(token, "--per-repo-workers="), strings.HasPrefix(token, "--max-open-prs="), strings.HasPrefix(token, "--cycles="):
			continue
		case strings.HasPrefix(token, "-"):
			continue
		default:
			return true
		}
	}
	return false
}

func parseStartRuntimeArgs(args []string) ([]string, startRuntimeOptions, error) {
	runtime := startRuntimeOptions{Cycles: 1, Interval: time.Minute}
	clean := []string{}
	foreverSet := false
	finiteSet := false
	intervalSet := false
	for index := 0; index < len(args); index++ {
		token := args[index]
		if token == "--" {
			clean = append(clean, args[index:]...)
			break
		}
		switch {
		case token == "--forever" || token == "--loop":
			runtime.Forever = true
			foreverSet = true
		case token == "--once":
			runtime.Cycles = 1
			finiteSet = true
		case token == "--cycles":
			value, err := requireStartFlagValue(args, index, token)
			if err != nil {
				return nil, startRuntimeOptions{}, err
			}
			parsed, err := parsePositiveInt(value, token)
			if err != nil {
				return nil, startRuntimeOptions{}, err
			}
			runtime.Cycles = parsed
			finiteSet = true
			index++
		case strings.HasPrefix(token, "--cycles="):
			parsed, err := parsePositiveInt(strings.TrimPrefix(token, "--cycles="), "--cycles")
			if err != nil {
				return nil, startRuntimeOptions{}, err
			}
			runtime.Cycles = parsed
			finiteSet = true
		case token == "--interval":
			value, err := requireStartFlagValue(args, index, token)
			if err != nil {
				return nil, startRuntimeOptions{}, err
			}
			parsed, err := parseStartInterval(value)
			if err != nil {
				return nil, startRuntimeOptions{}, err
			}
			runtime.Interval = parsed
			intervalSet = true
			index++
		case strings.HasPrefix(token, "--interval="):
			parsed, err := parseStartInterval(strings.TrimPrefix(token, "--interval="))
			if err != nil {
				return nil, startRuntimeOptions{}, err
			}
			runtime.Interval = parsed
			intervalSet = true
		default:
			clean = append(clean, token)
		}
	}
	if foreverSet && finiteSet {
		return nil, startRuntimeOptions{}, fmt.Errorf("Use either --forever/--loop or --once/--cycles, not both.")
	}
	if intervalSet && !finiteSet {
		runtime.Forever = true
	}
	return clean, runtime, nil
}

func parseStartInterval(value string) (time.Duration, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return 0, fmt.Errorf("Invalid --interval value %q. Expected a positive duration such as 30s or 2m.", value)
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds <= 0 {
			return 0, fmt.Errorf("Invalid --interval value %q. Expected a positive duration.", value)
		}
		return time.Duration(seconds) * time.Second, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("Invalid --interval value %q. Expected a positive duration such as 30s or 2m.", value)
	}
	return parsed, nil
}

func runStartLoop(runtime startRuntimeOptions, runOnce func() error) error {
	if runtime.Cycles <= 0 {
		runtime.Cycles = 1
	}
	if runtime.Interval <= 0 {
		runtime.Interval = time.Minute
	}
	for cycle := 1; ; cycle++ {
		cycleStartedAt := startLoopNow()
		if runtime.Forever {
			fmt.Fprintf(os.Stdout, "[start] Cycle %d/forever.\n", cycle)
		} else if runtime.Cycles > 1 {
			fmt.Fprintf(os.Stdout, "[start] Cycle %d/%d.\n", cycle, runtime.Cycles)
		}
		if err := runOnce(); err != nil {
			if !runtime.Forever {
				return err
			}
			fmt.Fprintf(os.Stdout, "[start] Cycle %d failed: %v\n", cycle, err)
		}
		if !runtime.Forever && cycle >= runtime.Cycles {
			return nil
		}
		if runtime.Forever && !startLoopContinue() {
			return nil
		}
		if runtime.Forever {
			remaining := runtime.Interval - startLoopNow().Sub(cycleStartedAt)
			if remaining > 0 {
				startLoopSleep(remaining)
			}
		}
	}
}

func startRepoValueLooksLikePath(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if info, err := os.Stat(trimmed); err == nil && info.IsDir() {
		return true
	}
	if strings.HasPrefix(trimmed, ".") || filepath.IsAbs(trimmed) {
		return true
	}
	return false
}

func parseStartArgs(args []string) (startOptions, error) {
	options := startOptions{
		Parallel:       startDefaultGlobalParallel,
		PerRepoWorkers: startWorkDefaultParallel,
		MaxOpenPR:      startWorkDefaultOpenPRCap,
		Cycles:         1,
		UIAPIPort:      startUIDefaultAPIPort,
		UIWebPort:      startUIDefaultWebPort,
	}
	passthroughIndex := len(args)
	for index, token := range args {
		if token == "--" {
			passthroughIndex = index
			break
		}
	}
	parseArgs := args[:passthroughIndex]
	if passthroughIndex < len(args) {
		options.CodexArgs = append([]string{}, args[passthroughIndex+1:]...)
	}
	for index := 0; index < len(parseArgs); index++ {
		token := parseArgs[index]
		switch {
		case token == "--repo":
			value, err := requireStartFlagValue(parseArgs, index, token)
			if err != nil {
				return startOptions{}, err
			}
			if err := validateStartWorkRepoSlug(value); err != nil {
				return startOptions{}, err
			}
			options.RepoSlug = strings.TrimSpace(value)
			index++
		case strings.HasPrefix(token, "--repo="):
			value := strings.TrimSpace(strings.TrimPrefix(token, "--repo="))
			if err := validateStartWorkRepoSlug(value); err != nil {
				return startOptions{}, err
			}
			options.RepoSlug = value
		case token == "--parallel":
			value, err := requireStartFlagValue(parseArgs, index, token)
			if err != nil {
				return startOptions{}, err
			}
			parsed, err := parsePositiveInt(value, token)
			if err != nil {
				return startOptions{}, err
			}
			options.Parallel = parsed
			index++
		case strings.HasPrefix(token, "--parallel="):
			parsed, err := parsePositiveInt(strings.TrimPrefix(token, "--parallel="), "--parallel")
			if err != nil {
				return startOptions{}, err
			}
			options.Parallel = parsed
		case token == "--per-repo-workers":
			value, err := requireStartFlagValue(parseArgs, index, token)
			if err != nil {
				return startOptions{}, err
			}
			parsed, err := parsePositiveInt(value, token)
			if err != nil {
				return startOptions{}, err
			}
			options.PerRepoWorkers = parsed
			index++
		case strings.HasPrefix(token, "--per-repo-workers="):
			parsed, err := parsePositiveInt(strings.TrimPrefix(token, "--per-repo-workers="), "--per-repo-workers")
			if err != nil {
				return startOptions{}, err
			}
			options.PerRepoWorkers = parsed
		case token == "--max-open-prs":
			value, err := requireStartFlagValue(parseArgs, index, token)
			if err != nil {
				return startOptions{}, err
			}
			parsed, err := parsePositiveInt(value, token)
			if err != nil {
				return startOptions{}, err
			}
			options.MaxOpenPR = parsed
			index++
		case strings.HasPrefix(token, "--max-open-prs="):
			parsed, err := parsePositiveInt(strings.TrimPrefix(token, "--max-open-prs="), "--max-open-prs")
			if err != nil {
				return startOptions{}, err
			}
			options.MaxOpenPR = parsed
		case token == "--no-ui":
			options.NoUI = true
		case token == "--ui-api-port":
			value, err := requireStartFlagValue(parseArgs, index, token)
			if err != nil {
				return startOptions{}, err
			}
			parsed, err := parsePositiveInt(value, token)
			if err != nil {
				return startOptions{}, err
			}
			options.UIAPIPort = parsed
			index++
		case strings.HasPrefix(token, "--ui-api-port="):
			parsed, err := parsePositiveInt(strings.TrimPrefix(token, "--ui-api-port="), "--ui-api-port")
			if err != nil {
				return startOptions{}, err
			}
			options.UIAPIPort = parsed
		case token == "--ui-web-port":
			value, err := requireStartFlagValue(parseArgs, index, token)
			if err != nil {
				return startOptions{}, err
			}
			parsed, err := parsePositiveInt(value, token)
			if err != nil {
				return startOptions{}, err
			}
			options.UIWebPort = parsed
			index++
		case strings.HasPrefix(token, "--ui-web-port="):
			parsed, err := parsePositiveInt(strings.TrimPrefix(token, "--ui-web-port="), "--ui-web-port")
			if err != nil {
				return startOptions{}, err
			}
			options.UIWebPort = parsed
		case token == "--cycles":
			value, err := requireStartFlagValue(parseArgs, index, token)
			if err != nil {
				return startOptions{}, err
			}
			parsed, err := parsePositiveInt(value, token)
			if err != nil {
				return startOptions{}, err
			}
			options.Cycles = parsed
			index++
		case strings.HasPrefix(token, "--cycles="):
			parsed, err := parsePositiveInt(strings.TrimPrefix(token, "--cycles="), "--cycles")
			if err != nil {
				return startOptions{}, err
			}
			options.Cycles = parsed
		default:
			return startOptions{}, fmt.Errorf("Unknown start option: %s\n\n%s", token, StartHelp)
		}
	}
	return options, nil
}

func parseStartUIOptions(args []string) (startOptions, error) {
	options := startOptions{
		UIAPIPort: startUIDefaultAPIPort,
		UIWebPort: startUIDefaultWebPort,
	}
	for index := 0; index < len(args); index++ {
		token := args[index]
		if token == "--" {
			break
		}
		switch {
		case token == "--no-ui":
			options.NoUI = true
		case token == "--ui-api-port":
			value, err := requireStartFlagValue(args, index, token)
			if err != nil {
				return startOptions{}, err
			}
			parsed, err := parsePositiveInt(value, token)
			if err != nil {
				return startOptions{}, err
			}
			options.UIAPIPort = parsed
			index++
		case strings.HasPrefix(token, "--ui-api-port="):
			parsed, err := parsePositiveInt(strings.TrimPrefix(token, "--ui-api-port="), "--ui-api-port")
			if err != nil {
				return startOptions{}, err
			}
			options.UIAPIPort = parsed
		case token == "--ui-web-port":
			value, err := requireStartFlagValue(args, index, token)
			if err != nil {
				return startOptions{}, err
			}
			parsed, err := parsePositiveInt(value, token)
			if err != nil {
				return startOptions{}, err
			}
			options.UIWebPort = parsed
			index++
		case strings.HasPrefix(token, "--ui-web-port="):
			parsed, err := parsePositiveInt(strings.TrimPrefix(token, "--ui-web-port="), "--ui-web-port")
			if err != nil {
				return startOptions{}, err
			}
			options.UIWebPort = parsed
		}
	}
	return options, nil
}

func resolveStartRepos(repoSlug string) ([]string, error) {
	if strings.TrimSpace(repoSlug) != "" {
		settings, err := readGithubRepoSettings(githubRepoSettingsPath(repoSlug))
		if err != nil {
			return nil, fmt.Errorf("repo %s is not onboarded; run `nana repo onboard %s --repo-mode <local|fork|repo> --issue-pick <manual|label|auto> --pr-forward <approve|auto>`", repoSlug, repoSlug)
		}
		if !githubRepoAutomationEnabled(settings) {
			return nil, nil
		}
		return []string{repoSlug}, nil
	}
	repos, err := listOnboardedGithubRepos()
	if err != nil {
		return nil, err
	}
	selected := []string{}
	for _, repo := range repos {
		settings, _ := readGithubRepoSettings(githubRepoSettingsPath(repo))
		if githubRepoAutomationEnabled(settings) {
			selected = append(selected, repo)
		}
	}
	return selected, nil
}

func listOnboardedGithubRepos() ([]string, error) {
	settingsFiles, err := listGithubRepoSettingsFiles()
	if err != nil {
		return nil, err
	}
	repos := make([]string, 0, len(settingsFiles))
	for _, settingsFile := range settingsFiles {
		repos = append(repos, settingsFile.RepoSlug)
	}
	return uniqueStrings(repos), nil
}

type twoLevelRepoFile struct {
	RepoSlug string
	Path     string
}

func listGithubRepoSettingsFiles() ([]twoLevelRepoFile, error) {
	return listTwoLevelRepoFiles(githubWorkReposRoot(), "settings.json")
}

// listTwoLevelRepoFiles enumerates only <root>/<owner>/<repo>/<fileName>.
// Managed repo directories can contain large source, runs, issues, and .git
// subtrees, so this must stay layout-aware rather than using filepath.WalkDir.
func listTwoLevelRepoFiles(root string, fileName string) ([]twoLevelRepoFile, error) {
	ownerEntries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	files := []twoLevelRepoFile{}
	for _, ownerEntry := range ownerEntries {
		if !ownerEntry.IsDir() {
			continue
		}
		owner := ownerEntry.Name()
		ownerPath := filepath.Join(root, owner)
		repoEntries, err := os.ReadDir(ownerPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, repoEntry := range repoEntries {
			if !repoEntry.IsDir() {
				continue
			}
			repoSlug := filepath.ToSlash(filepath.Join(owner, repoEntry.Name()))
			if !validRepoSlug(repoSlug) {
				continue
			}
			path := filepath.Join(ownerPath, repoEntry.Name(), fileName)
			info, err := os.Stat(path)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
			if info.IsDir() {
				continue
			}
			files = append(files, twoLevelRepoFile{RepoSlug: repoSlug, Path: path})
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].RepoSlug == files[j].RepoSlug {
			return files[i].Path < files[j].Path
		}
		return files[i].RepoSlug < files[j].RepoSlug
	})
	return files, nil
}

func githubRepoAutomationEnabled(settings *githubRepoSettings) bool {
	if settings == nil {
		return false
	}
	repoMode := resolvedGithubRepoMode(settings)
	issuePickMode := resolvedGithubIssuePickMode(settings)
	return repoMode != "local" && issuePickMode != "manual"
}

func requireStartFlagValue(args []string, index int, flag string) (string, error) {
	if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
		return "", fmt.Errorf("Missing value after %s.\n%s", flag, StartHelp)
	}
	return args[index+1], nil
}

func parsePositiveInt(value string, flag string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("Invalid %s value %q. Expected a positive integer.", flag, value)
	}
	return parsed, nil
}
