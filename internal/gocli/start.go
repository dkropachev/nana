package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const StartHelp = `nana start - Run repo automation or scout startup

Usage:
  Automation mode:
    nana start [--parallel <n>] [--per-repo-workers <n>] [--max-open-prs <n>] [--once|--cycles <n>|--forever] [--interval <duration>] [--no-ui] [--ui-api-port <port>] [--ui-web-port <port>] [-- codex-args...]

  Scout mode:
    nana start [owner/repo|github-url] [--repo <path>] [--focus <ux,perf>] [--from-file <proposals.json>] [--dry-run] [--local-only] [--once|--cycles <n>|--forever] [--interval <duration>] [-- codex-args...]

  nana start help

Mode selection:
  - automation mode runs onboarded GitHub repo automation
  - scout mode runs policy-backed improvement/enhancement/ui scout startup
  - scout mode is selected by scout flags, positional scout targets, or path-like --repo values
  - each run prints the selected [start] Mode line before execution begins

Examples:
  nana start --once
  nana start --parallel 4 --interval 10s
  nana start --repo . --from-file proposals.json --once

Automation mode behavior:
  - with no options, loops indefinitely until interrupted
  - starts the per-user Nana service control socket used by stateful nana CLI commands
  - scans onboarded GitHub repos under ~/.nana/work/repos
  - skips repos where repo-mode is disabled/local or issue-pick is manual
  - blocks repo automation early when gh auth or managed-source SSH origin preflight fails
  - --parallel limits total workers across all selected repos (default: 10)
  - all selected repos share one automation worker queue
  - --per-repo-workers is accepted as a deprecated alias for --parallel
  - --interval controls the target time between cycle starts in forever mode
  - service and implementation tasks all consume the shared worker budget
  - triages issues before implementation pickup and persists triage results locally
  - runs supported scouts from the managed source checkout when scout policies exist
  - scout policy reads from managed source checkouts participate in the shared repo read/write lock model
  - forwards PRs when pr-forward is auto: fork creates upstream PRs; repo attempts merge
  - launches loopback UI services by default: REST API + assistant workspace
  - open the printed [start-ui] Web URL for the assistant workspace; see docs/start-ui.html
  - use --once or --cycles <n> for bounded runs

Scout mode behavior:
  - detects scout support from repo policy files
  - runs improvement-scout, enhancement-scout, and/or ui-scout when their policies exist
  - scout policy reads, artifact writes, and auto-mode source mutations participate in the shared repo lock model
  - local repos keep scout artifacts under .nana/improvements/, .nana/enhancements/, or .nana/ui-findings/
  - GitHub targets follow their scout policy issue_destination
`

const startDefaultGlobalParallel = 10

type startOptions struct {
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
var startRunRepoCyclesBatch = runStartRepoCyclesSharedWorkers
var startRecoverManagedPromptSteps = recoverStaleManagedPromptSteps
var startLaunchLocalWorkDBProxySupervisor = launchLocalWorkDBProxySupervisor
var startLaunchNanaServiceSupervisor = launchNanaServiceSupervisor
var startLoopNow = time.Now
var startLoopSleep = time.Sleep
var startLoopContinue = func() bool { return true }

type startExecutionMode string

const (
	startExecutionModeAutomation startExecutionMode = "automation"
	startExecutionModeScout      startExecutionMode = "scout"
)

func Start(cwd string, args []string) error {
	if len(args) > 0 && isHelpToken(args[0]) {
		fmt.Fprint(os.Stdout, StartHelp)
		return nil
	}
	if len(args) > 0 && args[0] == "__recover-triage" {
		return recoverStartWorkIssueTriage(args[1:])
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
	mode, err := startExecutionModeForArgs(cwd, cleanArgs)
	if err != nil {
		return err
	}
	var runOnce func() error
	if mode == startExecutionModeScout {
		scoutOptions, err := parseScoutArgs(cleanArgs, ScoutStartHelp, "start")
		if err != nil {
			return err
		}
		runOnce = func() error {
			return startRunScoutStart(cwd, scoutOptions)
		}
	} else {
		options, err := parseStartArgs(cleanArgs)
		if err != nil {
			return err
		}
		options.Cycles = runtime.Cycles
		options.Forever = runtime.Forever
		options.Interval = runtime.Interval
		runOnce = func() error {
			repos, err := resolveStartRepos()
			if err != nil {
				return err
			}
			if len(repos) == 0 {
				fmt.Fprintln(os.Stdout, "[start] No onboarded repos with development enabled and issue-pick automation active.")
				return nil
			}
			fmt.Fprintf(os.Stdout, "[start] Repos selected: %s\n", strings.Join(repos, ", "))
			return startRunRepoCyclesBatch(cwd, repos, options)
		}
	}
	printStartModeBanner(mode)
	dbProxy, err := startLaunchLocalWorkDBProxySupervisor()
	if err != nil {
		return err
	}
	defer dbProxy.Close()
	service, err := startLaunchNanaServiceSupervisor()
	if err != nil {
		return err
	}
	defer service.Close()
	var ui *startUISupervisor
	if !uiOptions.NoUI {
		ui, err = launchStartUISupervisor(cwd, uiOptions)
		if err != nil {
			return err
		}
		defer ui.Close()
	}
	return runStartLoop(runtime, runOnce)
}

func startExecutionModeForArgs(cwd string, args []string) (startExecutionMode, error) {
	if startShouldRunScouts(cwd, args) {
		return startExecutionModeScout, nil
	}
	if len(args) == 0 {
		if info, err := os.Stat(cwd); err == nil && info.IsDir() {
			roles, lockErr := supportedScoutRolesWithReadLock(cwd, repoAccessLockOwner{
				Backend: "start",
				RunID:   sanitizePathToken(filepath.Base(cwd)),
				Purpose: "mode-detect",
				Label:   "start-mode-detect",
			})
			if lockErr != nil {
				return "", lockErr
			}
			if len(roles) > 0 {
				if repos, err := resolveStartRepos(); err == nil && len(repos) == 0 {
					return startExecutionModeScout, nil
				}
			}
		}
	}
	return startExecutionModeAutomation, nil
}

func printStartModeBanner(mode startExecutionMode) {
	switch mode {
	case startExecutionModeScout:
		fmt.Fprintln(os.Stdout, "[start] Mode: scout (policy-backed scout startup).")
	default:
		fmt.Fprintln(os.Stdout, "[start] Mode: automation (onboarded repo automation).")
	}
}

type preparedStartRepoCycle struct {
	repoSlug    string
	workOptions startWorkOptions
}

func prepareStartRepoCycle(repoSlug string, options startOptions) (*preparedStartRepoCycle, error) {
	settings, _ := readGithubRepoSettings(githubRepoSettingsPath(repoSlug))
	if cleaned, manifests, err := cleanupStaleLocalWorkRunsForRepoDetailed(githubManagedPaths(repoSlug).SourcePath, options.CodexArgs); err != nil {
		fmt.Fprintf(os.Stdout, "[start] %s: stale local work cleanup skipped: %v\n", repoSlug, err)
	} else if cleaned > 0 {
		if state, stateErr := readStartWorkState(repoSlug); stateErr == nil {
			resumed, requeued, _, updated, recoverErr := recoverStartWorkScoutJobsFromStaleManifests(repoSlug, state, manifests, options.CodexArgs)
			if recoverErr != nil {
				fmt.Fprintf(os.Stdout, "[start] %s: stale scout recovery skipped: %v\n", repoSlug, recoverErr)
			} else if updated {
				state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
				if writeErr := writeStartWorkStatePreservingPlannedItems(*state); writeErr != nil {
					fmt.Fprintf(os.Stdout, "[start] %s: failed to persist stale scout recovery: %v\n", repoSlug, writeErr)
				}
				fmt.Fprintf(os.Stdout, "[start] %s: recovered stale scout jobs resumed=%d requeued=%d\n", repoSlug, resumed, requeued)
			}
		} else if !os.IsNotExist(stateErr) {
			fmt.Fprintf(os.Stdout, "[start] %s: stale scout recovery skipped: %v\n", repoSlug, stateErr)
		}
		fmt.Fprintf(os.Stdout, "[start] %s: cleaned stale local work runs=%d\n", repoSlug, cleaned)
	}
	if lifecycle, err := maintainDismissedItemLifecycleForRepo(repoSlug, settings, time.Now().UTC()); err != nil {
		fmt.Fprintf(os.Stdout, "[start] %s: dismissed-item retention skipped: %v\n", repoSlug, err)
	} else if lifecycle.total() > 0 {
		for _, action := range lifecycle.actionMessages() {
			fmt.Fprintf(os.Stdout, "[start] %s: %s\n", repoSlug, action)
		}
	}
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
	if !githubRepoModeAllowsDevelopment(repoMode) || issuePickMode == "manual" {
		if err := clearStartRepoAutomationPreflight(repoSlug); err != nil {
			fmt.Fprintf(os.Stdout, "[start] %s: failed to clear automation preflight state: %v\n", repoSlug, err)
		}
		return nil, nil
	}
	if err := githubAutomationRepoPreflight(repoSlug, true); err != nil {
		if stateErr := recordStartRepoAutomationPreflightFailure(repoSlug, err); stateErr != nil {
			fmt.Fprintf(os.Stdout, "[start] %s: failed to persist automation preflight blocker: %v\n", repoSlug, stateErr)
		}
		fmt.Fprintf(os.Stdout, "[start] %s: automation preflight blocked: %v\n", repoSlug, err)
		return nil, nil
	}
	if err := clearStartRepoAutomationPreflight(repoSlug); err != nil {
		fmt.Fprintf(os.Stdout, "[start] %s: failed to clear automation preflight state: %v\n", repoSlug, err)
	}
	if repoMode == "fork" && prForwardMode == "auto" {
		if _, err := os.Stat(startWorkStatePath(repoSlug)); err == nil {
			if err := startPromoteStartWork(startWorkOptions{RepoSlug: repoSlug}); err != nil {
				return nil, err
			}
		}
	}
	return &preparedStartRepoCycle{
		repoSlug: repoSlug,
		workOptions: startWorkOptions{
			RepoSlug:       repoSlug,
			Parallel:       options.Parallel,
			MaxOpenPR:      options.MaxOpenPR,
			ForkIssuesMode: forkMode,
			ImplementMode:  implementMode,
			PublishTarget:  publishTarget,
			RepoMode:       repoMode,
			IssuePickMode:  issuePickMode,
			PRForwardMode:  prForwardMode,
			CodexArgs:      options.CodexArgs,
		},
	}, nil
}

func finalizeStartRepoCycle(repoSlug string, options startOptions) error {
	if _, err := syncGithubWorkItems(workItemSyncCommandOptions{
		RepoSlug:  repoSlug,
		Limit:     50,
		AutoRun:   true,
		CodexArgs: options.CodexArgs,
	}); err != nil {
		fmt.Fprintf(os.Stdout, "[start] %s: work item sync skipped: %v\n", repoSlug, err)
	}
	if started, err := dispatchQueuedWorkItems(repoSlug, options.CodexArgs); err != nil {
		fmt.Fprintf(os.Stdout, "[start] %s: work item dispatch skipped: %v\n", repoSlug, err)
	} else if started > 0 {
		fmt.Fprintf(os.Stdout, "[start] %s: auto-started work items=%d\n", repoSlug, started)
	}
	return nil
}

func runStartRepoCycle(cwd string, repoSlug string, options startOptions) error {
	prepared, err := prepareStartRepoCycle(repoSlug, options)
	if err != nil {
		return err
	}
	if prepared == nil {
		return nil
	}
	if err := runStartRepoSchedulerCycle(cwd, repoSlug, prepared.workOptions, options); err != nil {
		return err
	}
	return finalizeStartRepoCycle(repoSlug, options)
}

func startRepoHasScoutPolicies(repoSlug string) bool {
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	if info, err := os.Stat(sourcePath); err == nil && info.IsDir() {
		roles := []string{}
		if lockErr := withSourceReadLock(sourcePath, repoAccessLockOwner{
			Backend: "start",
			RunID:   sanitizePathToken(repoSlug),
			Purpose: "scout-policy-check",
			Label:   "start-scout-policy-check",
		}, func() error {
			roles = supportedScoutRoles(sourcePath)
			return nil
		}); lockErr != nil {
			fmt.Fprintf(os.Stdout, "[start] %s: scout policy check skipped: %v\n", repoSlug, lockErr)
			return false
		}
		if len(roles) > 0 {
			return true
		}
	}
	repoPath, checkoutErr := ensureImproveGithubCheckout(repoSlug)
	if checkoutErr != nil {
		fmt.Fprintf(os.Stdout, "[start] %s: scout policy check skipped: %v\n", repoSlug, checkoutErr)
		return false
	}
	roles, lockErr := supportedScoutRolesWithReadLock(repoPath, repoAccessLockOwner{
		Backend: "start",
		RunID:   sanitizePathToken(repoSlug),
		Purpose: "scout-policy-check",
		Label:   "start-scout-policy-check",
	})
	if lockErr != nil {
		fmt.Fprintf(os.Stdout, "[start] %s: scout policy check skipped: %v\n", repoSlug, lockErr)
		return false
	}
	return len(roles) > 0
}

func startShouldRunScouts(cwd string, args []string) bool {
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
		if err := startRecoverManagedPromptSteps(); err != nil {
			if !runtime.Forever {
				return err
			}
			fmt.Fprintf(os.Stdout, "[start] Managed prompt recovery failed: %v\n", err)
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
		PerRepoWorkers: startDefaultGlobalParallel,
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
			options.PerRepoWorkers = parsed
			index++
		case strings.HasPrefix(token, "--parallel="):
			parsed, err := parsePositiveInt(strings.TrimPrefix(token, "--parallel="), "--parallel")
			if err != nil {
				return startOptions{}, err
			}
			options.Parallel = parsed
			options.PerRepoWorkers = parsed
		case token == "--per-repo-workers":
			value, err := requireStartFlagValue(parseArgs, index, token)
			if err != nil {
				return startOptions{}, err
			}
			parsed, err := parsePositiveInt(value, token)
			if err != nil {
				return startOptions{}, err
			}
			options.Parallel = parsed
			options.PerRepoWorkers = parsed
			index++
		case strings.HasPrefix(token, "--per-repo-workers="):
			parsed, err := parsePositiveInt(strings.TrimPrefix(token, "--per-repo-workers="), "--per-repo-workers")
			if err != nil {
				return startOptions{}, err
			}
			options.Parallel = parsed
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

func resolveStartRepos() ([]string, error) {
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
	root := githubWorkReposRoot()
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	}
	repos := []string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root {
			rel, relErr := filepath.Rel(root, path)
			if relErr == nil {
				depth := strings.Count(filepath.ToSlash(rel), "/") + 1
				if entry.IsDir() {
					if depth > 2 {
						return filepath.SkipDir
					}
				} else if depth > 3 {
					return nil
				}
			}
		}
		if entry.IsDir() || entry.Name() != "settings.json" {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		repoSlug := filepath.ToSlash(rel)
		if validRepoSlug(repoSlug) {
			repos = append(repos, repoSlug)
		}
		return nil
	})
	return uniqueStrings(repos), err
}

func githubRepoAutomationEnabled(settings *githubRepoSettings) bool {
	if settings == nil {
		return false
	}
	repoMode := resolvedGithubRepoMode(settings)
	issuePickMode := resolvedGithubIssuePickMode(settings)
	return githubRepoModeAllowsDevelopment(repoMode) && issuePickMode != "manual"
}

func githubRepoModeAllowsDevelopment(repoMode string) bool {
	switch normalizeGithubRepoMode(repoMode) {
	case "local", "fork", "repo":
		return true
	default:
		return false
	}
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
