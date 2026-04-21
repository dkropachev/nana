package gocli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dkropachev/nana/internal/gocliassets"
)

const ImproveHelp = `nana improve - Discover UX and performance improvements

Usage:
  nana improve [owner/repo|github-url] [--repo <path>] [--focus <ux,perf>] [--from-file <proposals.json>] [--dry-run] [--local-only] [-- codex-args...]
  nana improve --resume <run-id>|--last [owner/repo|github-url] [--repo <path>] [-- codex-args...]
  nana improve help

Behavior:
  - runs the improvement-scout role against the selected repo
  - local repos always keep proposals under .nana/improvements/
  - scout policy is stored in Nana-managed runtime state outside the source checkout
  - GitHub policy issue_destination controls publication: local, repo/target, or fork
  - emits every grounded proposal it finds
  - created issues are labeled with improvement-scout, never enhancement

Policy example:
  {"version":1,"issue_destination":"repo","labels":["improvement","ux","perf"]}
  {"version":1,"issue_destination":"fork","fork_repo":"my-user/widget","labels":["improvement"]}
`

const EnhanceHelp = `nana enhance - Discover repo enhancements that help a project move forward

Usage:
  nana enhance [owner/repo|github-url] [--repo <path>] [--focus <ux,perf>] [--from-file <proposals.json>] [--dry-run] [--local-only] [-- codex-args...]
  nana enhance --resume <run-id>|--last [owner/repo|github-url] [--repo <path>] [-- codex-args...]
  nana enhance help

Behavior:
  - runs the enhancement-scout role against the selected repo
  - local repos always keep proposals under .nana/enhancements/
  - scout policy is stored in Nana-managed runtime state outside the source checkout
  - GitHub policy issue_destination controls publication: local, repo/target, or fork
  - emits every grounded proposal it finds
  - created issues are labeled with enhancement-scout

Policy example:
  {"version":1,"issue_destination":"repo","labels":["enhancement"]}
  {"version":1,"issue_destination":"fork","fork_repo":"my-user/widget","labels":["enhancement"]}
`

const UIScoutHelp = `nana ui-scout - Audit UI pages and flows with issue-style findings

Usage:
  nana ui-scout [owner/repo|github-url] [--repo <path>] [--focus <ui,ux,a11y,perf>] [--from-file <findings.json>] [--dry-run] [--local-only] [--session-limit <1-6>] [-- codex-args...]
  nana ui-scout --resume <run-id>|--last [owner/repo|github-url] [--repo <path>] [-- codex-args...]
  nana ui-scout help

Behavior:
  - runs the ui-scout role against the selected repo
  - performs a short preflight before the long audit to detect the best UI surface and audit mode
  - local repos keep findings under .nana/ui-findings/
  - GitHub policy issue_destination controls publication: local, repo/target, or fork
  - emits every grounded finding it finds; --session-limit overrides policy session_limit for this run only
  - prints whether the audit is live-browser or repo-only fallback before the full run starts

Policy example:
  {"version":1,"issue_destination":"local","labels":["ui"],"session_limit":4}
`

const ScoutStartHelp = `nana start - Run supported repo startup automation (scout mode)

Usage:
  nana start [owner/repo|github-url] [--repo <path>] [--focus <ux,perf>] [--from-file <proposals.json>] [--dry-run] [--local-only] [-- codex-args...]
  nana start --resume <run-id>|--last [owner/repo|github-url] [--repo <path>] [-- codex-args...]
  nana start help

Mode:
  - selected when nana start receives scout flags, a positional scout target, or a path-like --repo value
  - prints [start] Mode: scout (policy-backed scout startup). before execution begins

Behavior:
  - detects scout support from repo policy files
  - runs improvement-scout when a managed improvement scout policy exists
  - runs enhancement-scout when a managed enhancement scout policy exists
  - runs ui-scout when a managed UI scout policy exists
  - local repos keep findings under .nana/improvements/, .nana/enhancements/, or .nana/ui-findings/
  - GitHub targets follow their scout policy issue_destination
  - local repos with mode "auto" in every supported scout policy commit generated artifacts to the repo's default branch
  - auto mode requires a clean worktree and a resolvable local default branch
  - exits cleanly when the repo does not declare supported scout policies
`

const (
	improvementDestinationLocal  = "local"
	improvementDestinationTarget = "target"
	improvementDestinationFork   = "fork"

	improvementScoutRole     = "improvement-scout"
	enhancementScoutRole     = "enhancement-scout"
	uiScoutRole              = "ui-scout"
	defaultScoutSessionLimit = 4
	maxScoutSessionLimit     = 6
)

const uiScoutPreflightPrompt = `You are running a short preflight for ui-scout.

Inspect the repository and determine whether a UI audit can run live against a real or mock/demo surface, or whether the audit must fall back to repository-only evidence.

Return only JSON:
{
  "version": 1,
  "browser_ready": true,
  "mode": "live|repo_only|blocked",
  "surface_kind": "app|storybook|demo|mock|unknown",
  "surface_target": "best surface target",
  "reason": "short explanation"
}

Rules:
- Prefer a real app surface first.
- If a real surface is unavailable but a storybook/demo/mock UI exists, return mode "live" and surface_kind accordingly.
- If UI code exists but no live/browser-capable path is evident, return mode "repo_only".
- If no plausible UI surface or UI code exists, return mode "blocked".
- Keep reason concise and concrete.`

type ImproveOptions struct {
	Target          string
	RepoPath        string
	Focus           []string
	FromFile        string
	ResumeRunID     string
	ResumeLast      bool
	DryRun          bool
	LocalOnly       bool
	SessionLimit    int
	CodexArgs       []string
	RateLimitPolicy codexRateLimitPolicy
}

type uiScoutPreflight struct {
	Version       int    `json:"version"`
	GeneratedAt   string `json:"generated_at,omitempty"`
	BrowserReady  bool   `json:"browser_ready"`
	Mode          string `json:"mode,omitempty"`
	SurfaceKind   string `json:"surface_kind,omitempty"`
	SurfaceTarget string `json:"surface_target,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

type scoutRunManifest struct {
	Version      int      `json:"version"`
	RunID        string   `json:"run_id"`
	Role         string   `json:"role"`
	Target       string   `json:"target,omitempty"`
	RepoPath     string   `json:"repo_path"`
	RepoSlug     string   `json:"repo_slug,omitempty"`
	ArtifactDir  string   `json:"artifact_dir"`
	Status       string   `json:"status"`
	Focus        []string `json:"focus,omitempty"`
	DryRun       bool     `json:"dry_run,omitempty"`
	LocalOnly    bool     `json:"local_only,omitempty"`
	SessionLimit int      `json:"session_limit,omitempty"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	CompletedAt  string   `json:"completed_at,omitempty"`
	LastError    string   `json:"last_error,omitempty"`
	PauseReason  string   `json:"pause_reason,omitempty"`
	PauseUntil   string   `json:"pause_until,omitempty"`
}

func Improve(cwd string, args []string) error {
	if len(args) > 0 && isHelpToken(args[0]) || len(args) > 0 && args[0] == "help" {
		fmt.Fprint(os.Stdout, ImproveHelp)
		return nil
	}
	options, err := parseImproveArgs(args)
	if err != nil {
		return err
	}
	return runScout(cwd, options, improvementScoutRole)
}

func Enhance(cwd string, args []string) error {
	if len(args) > 0 && isHelpToken(args[0]) || len(args) > 0 && args[0] == "help" {
		fmt.Fprint(os.Stdout, EnhanceHelp)
		return nil
	}
	options, err := parseScoutArgs(args, EnhanceHelp, "enhance")
	if err != nil {
		return err
	}
	return runScout(cwd, options, enhancementScoutRole)
}

func UIScout(cwd string, args []string) error {
	if len(args) > 0 && isHelpToken(args[0]) || len(args) > 0 && args[0] == "help" {
		fmt.Fprint(os.Stdout, UIScoutHelp)
		return nil
	}
	options, err := parseUIScoutArgs(args)
	if err != nil {
		return err
	}
	return runScout(cwd, options, uiScoutRole)
}

func StartScouts(cwd string, args []string) error {
	if len(args) > 0 && isHelpToken(args[0]) || len(args) > 0 && args[0] == "help" {
		fmt.Fprint(os.Stdout, ScoutStartHelp)
		return nil
	}
	options, err := parseScoutArgs(args, ScoutStartHelp, "start")
	if err != nil {
		return err
	}
	printStartModeBanner(startExecutionModeScout)
	return startRunScoutStart(cwd, options)
}

func parseImproveArgs(args []string) (ImproveOptions, error) {
	return parseScoutArgs(args, ImproveHelp, "improve")
}

func parseUIScoutArgs(args []string) (ImproveOptions, error) {
	return parseScoutArgs(args, UIScoutHelp, "ui-scout")
}

func parseScoutArgs(args []string, help string, command string) (ImproveOptions, error) {
	options := ImproveOptions{Focus: []string{"ux", "perf"}}
	if command == "ui-scout" {
		options.Focus = []string{"ui", "ux", "a11y"}
	}
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		token := args[index]
		if token == "--" {
			options.CodexArgs = append(options.CodexArgs, args[index+1:]...)
			break
		}
		if strings.HasPrefix(token, "-") {
			switch {
			case token == "--repo":
				value, err := requireScoutFlagValue(args, index, token, help)
				if err != nil {
					return ImproveOptions{}, err
				}
				options.RepoPath = value
				index++
			case strings.HasPrefix(token, "--repo="):
				options.RepoPath = strings.TrimSpace(strings.TrimPrefix(token, "--repo="))
			case token == "--focus":
				value, err := requireScoutFlagValue(args, index, token, help)
				if err != nil {
					return ImproveOptions{}, err
				}
				if command == "ui-scout" {
					options.Focus, err = parseUIScoutFocus(value)
				} else {
					options.Focus, err = parseScoutFocus(value, help, command)
				}
				if err != nil {
					return ImproveOptions{}, err
				}
				index++
			case strings.HasPrefix(token, "--focus="):
				var (
					parsed []string
					err    error
				)
				if command == "ui-scout" {
					parsed, err = parseUIScoutFocus(strings.TrimPrefix(token, "--focus="))
				} else {
					parsed, err = parseScoutFocus(strings.TrimPrefix(token, "--focus="), help, command)
				}
				if err != nil {
					return ImproveOptions{}, err
				}
				options.Focus = parsed
			case token == "--from-file":
				value, err := requireScoutFlagValue(args, index, token, help)
				if err != nil {
					return ImproveOptions{}, err
				}
				options.FromFile = value
				index++
			case strings.HasPrefix(token, "--from-file="):
				options.FromFile = strings.TrimSpace(strings.TrimPrefix(token, "--from-file="))
			case token == "--resume":
				value, err := requireScoutFlagValue(args, index, token, help)
				if err != nil {
					return ImproveOptions{}, err
				}
				options.ResumeRunID = strings.TrimSpace(value)
				index++
			case strings.HasPrefix(token, "--resume="):
				options.ResumeRunID = strings.TrimSpace(strings.TrimPrefix(token, "--resume="))
			case token == "--last":
				options.ResumeLast = true
			case token == "--dry-run":
				options.DryRun = true
			case token == "--local-only":
				options.LocalOnly = true
			case token == "--session-limit":
				if command != "ui-scout" {
					return ImproveOptions{}, fmt.Errorf("unknown %s option: %s\n\n%s", command, token, help)
				}
				value, err := requireScoutFlagValue(args, index, token, help)
				if err != nil {
					return ImproveOptions{}, err
				}
				parsed, err := parseScoutSessionLimit(value, command)
				if err != nil {
					return ImproveOptions{}, err
				}
				options.SessionLimit = parsed
				index++
			case strings.HasPrefix(token, "--session-limit="):
				if command != "ui-scout" {
					return ImproveOptions{}, fmt.Errorf("unknown %s option: %s\n\n%s", command, token, help)
				}
				parsed, err := parseScoutSessionLimit(strings.TrimPrefix(token, "--session-limit="), command)
				if err != nil {
					return ImproveOptions{}, err
				}
				options.SessionLimit = parsed
			default:
				return ImproveOptions{}, fmt.Errorf("unknown %s option: %s\n\n%s", command, token, help)
			}
			continue
		}
		positionals = append(positionals, token)
	}
	if len(positionals) > 1 {
		return ImproveOptions{}, fmt.Errorf("nana %s accepts at most one repo target.\n\n%s", command, help)
	}
	if len(positionals) == 1 {
		options.Target = positionals[0]
	}
	if strings.TrimSpace(options.ResumeRunID) != "" && options.ResumeLast {
		return ImproveOptions{}, fmt.Errorf("use either --resume <run-id> or --last for nana %s, not both.\n\n%s", command, help)
	}
	if (strings.TrimSpace(options.ResumeRunID) != "" || options.ResumeLast) && strings.TrimSpace(options.FromFile) != "" {
		return ImproveOptions{}, fmt.Errorf("--from-file cannot be combined with scout resume options.\n\n%s", help)
	}
	return options, nil
}

func requireImproveFlagValue(args []string, index int, flag string) (string, error) {
	return requireScoutFlagValue(args, index, flag, ImproveHelp)
}

func requireScoutFlagValue(args []string, index int, flag string, help string) (string, error) {
	if index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") {
		return "", fmt.Errorf("Missing value after %s.\n\n%s", flag, help)
	}
	return strings.TrimSpace(args[index+1]), nil
}

func parseImproveFocus(value string) ([]string, error) {
	return parseScoutFocus(value, ImproveHelp, "improve")
}

func parseScoutFocus(value string, help string, command string) ([]string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return nil, fmt.Errorf("Missing value after --focus.\n\n%s", help)
	}
	focus := []string{}
	for _, part := range strings.Split(raw, ",") {
		normalized := strings.ToLower(strings.TrimSpace(part))
		switch normalized {
		case "ux", "perf", "performance":
			if normalized == "performance" {
				normalized = "perf"
			}
			focus = append(focus, normalized)
		case "":
		default:
			return nil, fmt.Errorf("invalid %s focus %q. Expected ux, perf, or ux,perf", command, part)
		}
	}
	return uniqueStrings(focus), nil
}

func parseUIScoutFocus(value string) ([]string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return nil, fmt.Errorf("Missing value after --focus.\n\n%s", UIScoutHelp)
	}
	focus := []string{}
	for _, part := range strings.Split(raw, ",") {
		normalized := strings.ToLower(strings.TrimSpace(part))
		switch normalized {
		case "ui", "ux", "a11y", "accessibility", "perf", "performance":
			switch normalized {
			case "accessibility":
				normalized = "a11y"
			case "performance":
				normalized = "perf"
			}
			focus = append(focus, normalized)
		case "":
		default:
			return nil, fmt.Errorf("invalid ui-scout focus %q. Expected ui, ux, a11y, perf, or a comma-separated combination", part)
		}
	}
	return uniqueStrings(focus), nil
}

func parseScoutSessionLimit(value string, command string) (int, error) {
	parsed, err := parsePositiveInt(strings.TrimSpace(value), "--session-limit")
	if err != nil || parsed < 1 || parsed > maxScoutSessionLimit {
		return 0, fmt.Errorf("invalid %s session limit %q. Expected an integer from 1 to %d", command, value, maxScoutSessionLimit)
	}
	return parsed, nil
}

func runScout(cwd string, options ImproveOptions, role string) (err error) {
	repoSlug, githubTarget := normalizeImproveGithubRepo(options.Target)
	repoPath := strings.TrimSpace(options.RepoPath)
	resuming := strings.TrimSpace(options.ResumeRunID) != "" || options.ResumeLast
	ranScout := strings.TrimSpace(options.FromFile) == ""
	if repoPath == "" {
		if strings.TrimSpace(options.Target) != "" && !githubTarget {
			repoPath = options.Target
		} else {
			repoPath = cwd
		}
	}

	if githubTarget {
		repoPath, err = ensureImproveGithubCheckout(repoSlug)
		if err != nil {
			return err
		}
	} else {
		repoPath, err = filepath.Abs(repoPath)
		if err != nil {
			return err
		}
	}
	if info, statErr := os.Stat(repoPath); statErr != nil {
		return statErr
	} else if !info.IsDir() {
		return fmt.Errorf("%s repo path must be a directory: %s", scoutOutputPrefix(role), repoPath)
	}

	var (
		artifactDir string
		manifest    scoutRunManifest
	)
	if resuming {
		artifactDir, err = resolveScoutRunDir(repoPath, role, options)
		if err != nil {
			return err
		}
		manifest, err = readScoutRunManifest(scoutRunManifestPath(artifactDir))
		if err != nil {
			return err
		}
		if manifest.Role != role {
			return fmt.Errorf("scout run %s belongs to %s, not %s", manifest.RunID, manifest.Role, role)
		}
		if manifest.Status == "completed" {
			return fmt.Errorf("scout run %s is already completed", manifest.RunID)
		}
		repoPath = manifest.RepoPath
		repoSlug = manifest.RepoSlug
		options.Target = manifest.Target
		options.Focus = append([]string{}, manifest.Focus...)
		options.DryRun = manifest.DryRun
		options.LocalOnly = manifest.LocalOnly
		options.SessionLimit = manifest.SessionLimit
		_, githubTarget = normalizeImproveGithubRepo(options.Target)
	} else {
		artifactDir, err = prepareLocalScoutArtifactDir(repoPath, role)
		if err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339)
		manifest = scoutRunManifest{
			Version:      1,
			RunID:        filepath.Base(artifactDir),
			Role:         role,
			Target:       options.Target,
			RepoPath:     repoPath,
			RepoSlug:     repoSlug,
			ArtifactDir:  artifactDir,
			Status:       "running",
			Focus:        append([]string{}, options.Focus...),
			DryRun:       options.DryRun,
			LocalOnly:    options.LocalOnly,
			SessionLimit: options.SessionLimit,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
	}
	manifest.Status = "running"
	manifest.LastError = ""
	manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	manifest.CompletedAt = ""
	if err := writeScoutRunManifest(artifactDir, manifest); err != nil {
		return err
	}

	policy, err := readScoutPolicyWithReadLock(repoPath, role, repoAccessLockOwner{
		Backend: "scout",
		RunID:   sanitizePathToken(manifest.RunID),
		Purpose: "policy-read",
		Label:   "scout-policy-read",
	})
	if err != nil {
		return err
	}
	if !githubTarget || options.LocalOnly {
		policy.IssueDestination = improvementDestinationLocal
	}
	if scoutRoleSupportsSessionLimit(role) && options.SessionLimit > 0 {
		policy.SessionLimit = options.SessionLimit
	}
	policy.Labels = normalizeScoutLabels(policy.Labels, role)
	defer func() {
		manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if pauseErr, ok := isCodexRateLimitPauseError(err); ok {
			manifest.Status = "paused"
			manifest.LastError = codexPauseInfoMessage(pauseErr.Info)
			manifest.PauseReason = strings.TrimSpace(pauseErr.Info.Reason)
			manifest.PauseUntil = strings.TrimSpace(pauseErr.Info.RetryAfter)
			manifest.CompletedAt = ""
		} else if err != nil {
			manifest.Status = "failed"
			manifest.LastError = err.Error()
			manifest.PauseReason = ""
			manifest.PauseUntil = ""
			manifest.CompletedAt = manifest.UpdatedAt
		} else {
			manifest.Status = "completed"
			manifest.LastError = ""
			manifest.PauseReason = ""
			manifest.PauseUntil = ""
			manifest.CompletedAt = manifest.UpdatedAt
		}
		if writeErr := writeScoutRunManifest(artifactDir, manifest); writeErr != nil && err == nil {
			err = writeErr
		}
		if err == nil && ranScout {
			if recordErr := recordSuccessfulScoutRun(repoPath, role, time.Now().UTC()); recordErr != nil {
				err = recordErr
			}
		}
	}()

	var preflight *uiScoutPreflight
	rawOutput := []byte{}
	if strings.TrimSpace(options.FromFile) != "" {
		rawOutput, err = os.ReadFile(options.FromFile)
		if err != nil {
			return err
		}
	} else {
		runtime, err := prepareScoutExecutionRuntime(repoPath, artifactDir, role)
		if err != nil {
			return err
		}
		runtime.RateLimitPolicy = codexRateLimitPolicyDefault(options.RateLimitPolicy)
		runtime.OnPause = func(info codexRateLimitPauseInfo) {
			manifest.Status = "paused"
			manifest.LastError = codexPauseInfoMessage(info)
			manifest.PauseReason = strings.TrimSpace(info.Reason)
			manifest.PauseUntil = strings.TrimSpace(info.RetryAfter)
			manifest.UpdatedAt = ISOTimeNow()
			manifest.CompletedAt = ""
			_ = writeScoutRunManifest(artifactDir, manifest)
		}
		runtime.OnResume = func(info codexRateLimitPauseInfo) {
			manifest.Status = "running"
			manifest.LastError = ""
			manifest.PauseReason = ""
			manifest.PauseUntil = ""
			manifest.UpdatedAt = ISOTimeNow()
			_ = writeScoutRunManifest(artifactDir, manifest)
		}
		defer runtime.Cleanup()
		if scoutRoleUsesPreflight(role) {
			if err := readGithubJSON(filepath.Join(artifactDir, "preflight.json"), &preflight); err != nil {
				preflight, err = runUIScoutPreflight(runtime, repoSlug, options.Focus, options.CodexArgs)
				if err != nil {
					return err
				}
				if err := writeGithubJSON(filepath.Join(artifactDir, "preflight.json"), preflight); err != nil {
					return err
				}
			}
			if preflight.Mode == "blocked" {
				return fmt.Errorf("ui-scout preflight could not find a runnable UI surface: %s", defaultString(strings.TrimSpace(preflight.Reason), "no UI surface detected"))
			}
			fmt.Fprintf(os.Stdout, "[ui-scout] Preflight: mode=%s surface=%s target=%s session-limit=%d\n",
				defaultString(strings.TrimSpace(preflight.Mode), "unknown"),
				defaultString(strings.TrimSpace(preflight.SurfaceKind), "unknown"),
				defaultString(strings.TrimSpace(preflight.SurfaceTarget), "(none)"),
				effectiveScoutSessionLimit(policy, role),
			)
			if strings.EqualFold(strings.TrimSpace(preflight.Mode), "repo_only") && strings.TrimSpace(preflight.Reason) != "" {
				fmt.Fprintf(os.Stdout, "[ui-scout] Preflight fallback: %s\n", strings.TrimSpace(preflight.Reason))
			}
		}
		rawOutput, err = runScoutRole(runtime, repoSlug, options.Focus, options.CodexArgs, role, policy, preflight)
		if persistErr := persistScoutExecutionArtifacts(runtime, repoPath, artifactDir); persistErr != nil {
			return persistErr
		}
		if err != nil {
			return err
		}
	}
	report, err := parseScoutReport(rawOutput, role)
	if err != nil {
		rawPath, writeErr := writeScoutRawOutput(repoPath, rawOutput, role)
		if writeErr == nil {
			return fmt.Errorf("%w\nRaw %s output saved to %s", err, role, rawPath)
		}
		return err
	}
	report.Proposals = normalizeScoutProposals(report.Proposals, policy, role)
	if report.Repo == "" {
		report.Repo = repoSlug
		if report.Repo == "" {
			report.Repo = filepath.Base(repoPath)
		}
	}
	if report.GeneratedAt == "" {
		report.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	}

	artifactDir, err = writeLocalScoutArtifacts(artifactDir, report, policy, rawOutput, role, preflight)
	if err != nil {
		return err
	}
	prefix := scoutOutputPrefix(role)
	fmt.Fprintf(os.Stdout, "[%s] Saved %s locally: %s\n", prefix, scoutProposalNoun(role), artifactDir)
	if len(report.Proposals) == 0 {
		fmt.Fprintf(os.Stdout, "[%s] No grounded %s found.\n", prefix, scoutProposalNoun(role))
		return nil
	}
	if policy.IssueDestination == improvementDestinationLocal {
		fmt.Fprintf(os.Stdout, "[%s] Keeping %d %s local by policy.\n", prefix, len(report.Proposals), scoutItemsCountNoun(role))
		return nil
	}
	if !githubTarget {
		fmt.Fprintf(os.Stdout, "[%s] Local repo detected; keeping %s local.\n", prefix, scoutProposalNoun(role))
		return nil
	}
	results, err := publishScoutIssues(repoSlug, report.Proposals, policy, options.DryRun, role)
	if err != nil {
		return err
	}
	for _, result := range results {
		if result.DryRun {
			fmt.Fprintf(os.Stdout, "[%s] Would create issue: %s\n", prefix, result.Title)
		} else {
			fmt.Fprintf(os.Stdout, "[%s] Created issue: %s\n", prefix, result.URL)
		}
	}
	return nil
}

func runScoutStart(cwd string, options ImproveOptions) error {
	repoPath, err := resolveScoutStartRepoPath(cwd, options)
	if err != nil {
		return err
	}
	repoSlug, _ := normalizeImproveGithubRepo(options.Target)
	roles, err := supportedScoutRolesWithReadLock(repoPath, repoAccessLockOwner{
		Backend: "scout-start",
		RunID:   sanitizePathToken(defaultString(strings.TrimSpace(repoSlug), filepath.Base(repoPath))),
		Purpose: "supported-roles",
		Label:   "scout-start-supported-roles",
	})
	if err != nil {
		return err
	}
	if len(roles) == 0 {
		fmt.Fprintf(os.Stdout, "[start] No supported scout policies found in %s; nothing to run.\n", repoPath)
		return nil
	}
	_, githubTarget := normalizeImproveGithubRepo(options.Target)
	policies, err := readScoutPoliciesForRolesWithReadLock(repoPath, roles, repoAccessLockOwner{
		Backend: "scout-start",
		RunID:   sanitizePathToken(defaultString(strings.TrimSpace(repoSlug), filepath.Base(repoPath))),
		Purpose: "policy-read",
		Label:   "scout-start-policy-read",
	})
	if err != nil {
		return err
	}
	autoLocal := !githubTarget && scoutStartAutoMode(policies, roles)
	if autoLocal {
		if err := withSourceWriteLock(repoPath, repoAccessLockOwner{
			Backend: "scout-start",
			RunID:   sanitizePathToken(defaultString(strings.TrimSpace(repoSlug), filepath.Base(repoPath))),
			Purpose: "auto-default-branch",
			Label:   "scout-start-default-branch",
		}, func() error {
			return ensureScoutDefaultBranch(repoPath)
		}); err != nil {
			return err
		}
	}
	if autoLocal && strings.TrimSpace(options.FromFile) == "" {
		picked, err := startRunLocalScoutPickup(repoPath, options.CodexArgs)
		if err != nil {
			return err
		}
		if picked {
			return nil
		}
	}
	dueRoles := make([]string, 0, len(roles))
	for _, role := range roles {
		policy := policies[role]
		decision, err := scoutScheduleDecisionForRole(repoPath, repoSlug, role, policy, time.Now().UTC())
		if err != nil {
			return err
		}
		if !decision.Due {
			fmt.Fprintf(os.Stdout, "[start] %s supported; skipped by schedule: %s\n", role, decision.Reason)
			continue
		}
		dueRoles = append(dueRoles, role)
	}
	if len(dueRoles) == 0 {
		fmt.Fprintf(os.Stdout, "[start] No scout roles due in %s.\n", repoPath)
		return nil
	}
	for _, role := range dueRoles {
		fmt.Fprintf(os.Stdout, "[start] %s supported; running.\n", role)
		if err := runScout(cwd, options, role); err != nil {
			return err
		}
	}
	if autoLocal {
		committed := false
		err := withSourceWriteLock(repoPath, repoAccessLockOwner{
			Backend: "scout-start",
			RunID:   sanitizePathToken(defaultString(strings.TrimSpace(repoSlug), filepath.Base(repoPath))),
			Purpose: "commit-artifacts",
			Label:   "scout-start-commit-artifacts",
		}, func() error {
			var lockErr error
			committed, lockErr = commitScoutArtifactsToDefault(repoPath)
			return lockErr
		})
		if err != nil {
			return err
		}
		if committed {
			fmt.Fprintln(os.Stdout, "[start] Committed scout artifacts to default branch.")
		} else {
			fmt.Fprintln(os.Stdout, "[start] No scout artifact changes to commit on default branch.")
		}
	}
	if autoLocal && strings.TrimSpace(options.FromFile) == "" {
		if _, err := startRunLocalScoutPickup(repoPath, options.CodexArgs); err != nil {
			return err
		}
	}
	return nil
}

func resolveScoutStartRepoPath(cwd string, options ImproveOptions) (string, error) {
	repoSlug, githubTarget := normalizeImproveGithubRepo(options.Target)
	if githubTarget {
		return ensureImproveGithubCheckout(repoSlug)
	}
	repoPath := strings.TrimSpace(options.RepoPath)
	if repoPath == "" {
		if strings.TrimSpace(options.Target) != "" {
			repoPath = options.Target
		} else {
			repoPath = cwd
		}
	}
	absolute, err := filepath.Abs(repoPath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("start repo path must be a directory: %s", absolute)
	}
	return absolute, nil
}

func supportedScoutRoles(repoPath string) []string {
	roles := []string{}
	for _, role := range supportedScoutRoleOrder {
		if scoutPolicyExists(repoPath, role) {
			roles = append(roles, role)
		}
	}
	return roles
}

func supportedScoutRolesWithReadLock(repoPath string, owner repoAccessLockOwner) ([]string, error) {
	roles := []string{}
	err := withSourceReadLock(repoPath, owner, func() error {
		roles = supportedScoutRoles(repoPath)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return roles, nil
}

func scoutPolicyExists(repoPath string, role string) bool {
	for _, path := range repoScoutReadPaths(repoPath, role) {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func scoutStartAutoMode(policies map[string]scoutPolicy, roles []string) bool {
	if len(roles) == 0 {
		return false
	}
	for _, role := range roles {
		policy := policies[role]
		if strings.ToLower(strings.TrimSpace(policy.Mode)) != "auto" {
			return false
		}
	}
	return true
}

func readScoutPolicyWithReadLock(repoPath string, role string, owner repoAccessLockOwner) (scoutPolicy, error) {
	policy := scoutPolicy{}
	err := withSourceReadLock(repoPath, owner, func() error {
		policy = readScoutPolicy(repoPath, role)
		return nil
	})
	if err != nil {
		return scoutPolicy{}, err
	}
	return policy, nil
}

func readScoutPoliciesForRolesWithReadLock(repoPath string, roles []string, owner repoAccessLockOwner) (map[string]scoutPolicy, error) {
	policies := map[string]scoutPolicy{}
	err := withSourceReadLock(repoPath, owner, func() error {
		for _, role := range roles {
			policies[role] = readScoutPolicy(repoPath, role)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return policies, nil
}

func ensureScoutDefaultBranch(repoPath string) error {
	if _, err := githubGitOutput(repoPath, "rev-parse", "--is-inside-work-tree"); err != nil {
		return fmt.Errorf("scout auto mode requires a local git repo: %w", err)
	}
	gitignoreChanged, err := ensureScoutRuntimeGitignore(repoPath)
	if err != nil {
		return err
	}
	status, err := githubGitOutput(repoPath, "status", "--porcelain")
	if err != nil {
		return err
	}
	dirty := scoutRelevantDirtyStatusLines(status)
	if gitignoreChanged {
		dirty = withoutScoutGitignoreStatusLines(dirty)
	}
	if len(dirty) > 0 {
		return fmt.Errorf("scout auto mode requires a clean worktree before switching to default branch")
	}
	defaultBranch, err := resolveScoutDefaultBranch(repoPath)
	if err != nil {
		return err
	}
	current, err := githubGitOutput(repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(current) == defaultBranch {
		return nil
	}
	return githubRunGit(repoPath, "checkout", defaultBranch)
}

func ensureScoutRuntimeGitignore(repoPath string) (bool, error) {
	path := filepath.Join(repoPath, ".gitignore")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	content := string(existing)
	lines := strings.Split(content, "\n")
	missing := []string{}
	for _, entry := range []string{".codex", ".codex/"} {
		if !gitignoreHasEntry(lines, entry) {
			missing = append(missing, entry)
		}
	}
	if len(missing) == 0 {
		return false, nil
	}
	var builder strings.Builder
	builder.WriteString(content)
	if content != "" && !strings.HasSuffix(content, "\n") {
		builder.WriteString("\n")
	}
	if content != "" {
		builder.WriteString("\n")
	}
	builder.WriteString("# Codex/NANA runtime state\n")
	for _, entry := range missing {
		builder.WriteString(entry)
		builder.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func gitignoreHasEntry(lines []string, entry string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) == entry {
			return true
		}
	}
	return false
}

func scoutRelevantDirtyStatusLines(status string) []string {
	dirty := []string{}
	for _, line := range strings.Split(status, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		path := strings.TrimSpace(line)
		if len(line) > 3 {
			path = strings.TrimSpace(line[3:])
		}
		path = strings.Trim(path, `"`)
		if path == ".codex" || strings.HasPrefix(path, ".codex/") {
			continue
		}
		dirty = append(dirty, line)
	}
	return dirty
}

func withoutScoutGitignoreStatusLines(lines []string) []string {
	filtered := []string{}
	for _, line := range lines {
		path := strings.TrimSpace(line)
		if len(line) > 3 {
			path = strings.TrimSpace(line[3:])
		}
		path = strings.Trim(path, `"`)
		if path == ".gitignore" {
			continue
		}
		filtered = append(filtered, line)
	}
	return filtered
}

func resolveScoutDefaultBranch(repoPath string) (string, error) {
	if output, err := githubGitOutput(repoPath, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		branch := strings.TrimSpace(output)
		if strings.Contains(branch, "/") {
			branch = strings.TrimPrefix(branch, strings.SplitN(branch, "/", 2)[0]+"/")
		}
		if branch != "" {
			if err := githubRunGit(repoPath, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
				return branch, nil
			}
			return "", fmt.Errorf("scout auto mode resolved default branch %q from origin/HEAD, but no matching local branch exists", branch)
		}
	}
	for _, branch := range []string{"main", "master", "trunk", "default"} {
		if err := githubRunGit(repoPath, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
			return branch, nil
		}
	}
	return "", fmt.Errorf("scout auto mode requires a resolvable local default branch")
}

func commitScoutArtifactsToDefault(repoPath string) (bool, error) {
	paths := existingScoutArtifactRoots(repoPath)
	if len(paths) == 0 {
		return false, nil
	}
	addArgs := append([]string{"add", "-f", "--"}, paths...)
	if err := githubRunGit(repoPath, addArgs...); err != nil {
		return false, err
	}
	diffArgs := append([]string{"diff", "--cached", "--quiet", "--"}, paths...)
	if scoutGitQuiet(repoPath, diffArgs...) {
		return false, nil
	}
	message, err := buildScoutArtifactCommitMessage(repoPath)
	if err != nil {
		return false, err
	}
	messageFile, err := os.CreateTemp("", "nana-scout-commit-*.txt")
	if err != nil {
		return false, err
	}
	messagePath := messageFile.Name()
	if _, err := messageFile.WriteString(message); err != nil {
		_ = messageFile.Close()
		_ = os.Remove(messagePath)
		return false, err
	}
	if err := messageFile.Close(); err != nil {
		_ = os.Remove(messagePath)
		return false, err
	}
	defer os.Remove(messagePath)
	if err := githubRunGit(repoPath, "commit", "-F", messagePath); err != nil {
		return false, err
	}
	return true, nil
}

type scoutCommitItem struct {
	Role     string
	Title    string
	Artifact string
}

func buildScoutArtifactCommitMessage(repoPath string) (string, error) {
	items, err := stagedScoutCommitItems(repoPath)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "Record scout startup artifacts\n", nil
	}
	subject := fmt.Sprintf("Record scout item: %s", items[0].Title)
	if len(items) > 1 {
		subject = fmt.Sprintf("Record %d scout items: %s", len(items), items[0].Title)
	}
	lines := []string{truncateCommitSubject(subject), "", "Scout items:"}
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("- %s: %s", scoutIssueHeading(item.Role), item.Title))
		lines = append(lines, fmt.Sprintf("  Artifact: %s", item.Artifact))
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func stagedScoutCommitItems(repoPath string) ([]scoutCommitItem, error) {
	diffTargets := []string{}
	for _, role := range supportedScoutRoleOrder {
		diffTargets = append(diffTargets, filepath.ToSlash(filepath.Join(".nana", scoutArtifactRoot(role))))
	}
	args := append([]string{"diff", "--cached", "--name-only", "--"}, diffTargets...)
	output, err := githubGitOutput(repoPath, args...)
	if err != nil {
		return nil, err
	}
	staged := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		path := strings.TrimSpace(filepath.ToSlash(line))
		if path != "" {
			staged[path] = true
		}
	}
	items := []scoutCommitItem{}
	for _, role := range supportedScoutRoleOrder {
		matches, err := filepath.Glob(filepath.Join(repoPath, ".nana", scoutArtifactRoot(role), "*", "proposals.json"))
		if err != nil {
			return nil, err
		}
		sort.Strings(matches)
		for _, path := range matches {
			rel, err := filepath.Rel(repoPath, path)
			if err != nil {
				return nil, err
			}
			rel = filepath.ToSlash(rel)
			if !staged[rel] {
				continue
			}
			var report improvementReport
			if err := readGithubJSON(path, &report); err != nil {
				continue
			}
			artifact := filepath.ToSlash(filepath.Dir(rel))
			for _, proposal := range report.Proposals {
				title := strings.TrimSpace(proposal.Title)
				if title == "" {
					continue
				}
				items = append(items, scoutCommitItem{Role: role, Title: title, Artifact: artifact})
			}
		}
	}
	return items, nil
}

func truncateCommitSubject(subject string) string {
	const limit = 72
	subject = strings.TrimSpace(strings.ReplaceAll(subject, "\n", " "))
	if len(subject) <= limit {
		return subject
	}
	if limit <= 3 {
		return subject[:limit]
	}
	return strings.TrimSpace(subject[:limit-3]) + "..."
}

func existingScoutArtifactRoots(repoPath string) []string {
	paths := []string{}
	if info, err := os.Stat(filepath.Join(repoPath, ".gitignore")); err == nil && !info.IsDir() {
		paths = append(paths, ".gitignore")
	}
	for _, role := range supportedScoutRoleOrder {
		rel := filepath.ToSlash(filepath.Join(".nana", scoutArtifactRoot(role)))
		if info, err := os.Stat(filepath.Join(repoPath, rel)); err == nil && info.IsDir() {
			paths = append(paths, rel)
		}
	}
	return uniqueStrings(paths)
}

func scoutGitQuiet(repoPath string, args ...string) bool {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	cmd.Env = githubGitEnv()
	return cmd.Run() == nil
}

func normalizeImproveGithubRepo(target string) (string, bool) {
	raw := strings.TrimSpace(target)
	if raw == "" {
		return "", false
	}
	if validRepoSlug(raw) {
		return raw, true
	}
	prefix := "https://github.com/"
	if !strings.HasPrefix(raw, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(raw, prefix)
	rest = strings.TrimSuffix(rest, ".git")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0] + "/" + strings.TrimSuffix(parts[1], ".git"), true
}

func ensureImproveGithubCheckout(repoSlug string) (string, error) {
	apiBaseURL := strings.TrimSpace(os.Getenv("GITHUB_API_URL"))
	if apiBaseURL == "" {
		apiBaseURL = "https://api.github.com"
	}
	token, err := resolveGithubToken()
	if err != nil {
		return "", err
	}
	var repository githubRepositoryPayload
	if err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s", repoSlug), &repository); err != nil {
		return "", err
	}
	paths := githubManagedPaths(repoSlug)
	meta, err := ensureGithubManagedRepoMetadata(paths, githubTargetContext{Repository: repository}, time.Now().UTC())
	if err != nil {
		return "", err
	}
	if err := withManagedSourceWriteLock(repoSlug, repoAccessLockOwner{
		Backend: "github-improve",
		RunID:   fmt.Sprintf("improve-%d", time.Now().UTC().UnixNano()),
		Purpose: "source-setup",
		Label:   "github-improve-source",
	}, func() error {
		return ensureGithubSourceClone(paths, meta)
	}); err != nil {
		return "", err
	}
	return paths.SourcePath, nil
}

func defaultScoutPolicy() scoutPolicy {
	return scoutPolicy{
		Version:          1,
		Schedule:         scoutScheduleWhenResolved,
		IssueDestination: improvementDestinationLocal,
		Labels:           []string{},
	}
}

func defaultImprovementPolicy() improvementPolicy {
	return defaultScoutPolicy()
}

func readImprovementPolicy(repoPath string) scoutPolicy {
	return readScoutPolicy(repoPath, improvementScoutRole)
}

func readScoutPolicy(repoPath string, role string) scoutPolicy {
	policy := defaultScoutPolicy()
	for _, path := range repoScoutReadPaths(repoPath, role) {
		var candidate scoutPolicy
		if err := readGithubJSON(path, &candidate); err != nil {
			continue
		}
		mergeScoutPolicy(&policy, candidate)
	}
	policy.IssueDestination = normalizeScoutDestination(policy.IssueDestination)
	policy.Schedule = effectiveScoutSchedule(policy)
	policy.Labels = normalizeScoutLabels(policy.Labels, role)
	policy.SessionLimit = effectiveScoutSessionLimit(policy, role)
	return policy
}

func mergeScoutPolicy(target *scoutPolicy, source scoutPolicy) {
	if source.Version != 0 {
		target.Version = source.Version
	}
	if strings.TrimSpace(source.Mode) != "" {
		target.Mode = source.Mode
	}
	if strings.TrimSpace(source.Schedule) != "" {
		target.Schedule = source.Schedule
	}
	if strings.TrimSpace(source.IssueDestination) != "" {
		target.IssueDestination = source.IssueDestination
	}
	if strings.TrimSpace(source.ForkRepo) != "" {
		target.ForkRepo = source.ForkRepo
	}
	if source.Labels != nil {
		target.Labels = source.Labels
	}
	if source.SessionLimit > 0 {
		target.SessionLimit = source.SessionLimit
	}
}

func mergeImprovementPolicy(target *improvementPolicy, source improvementPolicy) {
	mergeScoutPolicy(target, source)
}

func normalizeScoutDestination(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case improvementDestinationTarget, "repo":
		return improvementDestinationTarget
	case improvementDestinationFork:
		return improvementDestinationFork
	default:
		return improvementDestinationLocal
	}
}

func normalizeImprovementDestination(value string) string {
	return normalizeScoutDestination(value)
}

func normalizeImprovementLabels(labels []string) []string {
	return normalizeScoutLabels(labels, improvementScoutRole)
}

func normalizeScoutLabels(labels []string, role string) []string {
	spec := scoutRoleSpecFor(role)
	base := spec.BaseLabel
	forbidden := "enhancement"
	if base != "improvement" {
		forbidden = ""
	}
	out := []string{base, role}
	for _, label := range labels {
		normalized := strings.ToLower(strings.TrimSpace(label))
		if normalized == "" || normalized == forbidden {
			continue
		}
		out = append(out, normalized)
	}
	return uniqueStrings(out)
}

func effectiveScoutSessionLimit(policy scoutPolicy, role string) int {
	if !scoutRoleSupportsSessionLimit(role) {
		return 0
	}
	if policy.SessionLimit <= 0 {
		return defaultScoutSessionLimit
	}
	if policy.SessionLimit > maxScoutSessionLimit {
		return maxScoutSessionLimit
	}
	return policy.SessionLimit
}

func runScoutRole(runtime scoutExecutionRuntime, repoSlug string, focus []string, codexArgs []string, role string, policy scoutPolicy, preflight *uiScoutPreflight) ([]byte, error) {
	promptSurface, err := readScoutPrompt(role)
	if err != nil {
		return nil, err
	}
	repoLabel := repoSlug
	if repoLabel == "" {
		repoLabel = filepath.Base(runtime.RepoPath)
	}
	taskLines := []string{
		strings.TrimSpace(promptSurface),
		"",
		"Task:",
		fmt.Sprintf("- Inspect repo: %s", repoLabel),
		fmt.Sprintf("- Focus: %s", strings.Join(focus, ", ")),
		fmt.Sprintf("- Artifact directory: %s", runtime.ArtifactDir),
		"- Return only the JSON output contract.",
		fmt.Sprintf("- Treat results as %s.", scoutProposalNoun(role)),
	}
	if scoutRoleUsesPreflight(role) {
		taskLines = append(taskLines,
			fmt.Sprintf("- Split page audits across parallel subagents with a hard cap of %d concurrent sessions.", effectiveScoutSessionLimit(policy, role)),
			"- Prefer real UI pages first; when real pages are blocked, use mocked/demo/storybook pages if present and identify them as mocked.",
			"- Save screenshots and per-page evidence files inside the artifact directory and reference them in the JSON output.",
		)
		if preflight != nil {
			taskLines = append(taskLines,
				fmt.Sprintf("- Preflight mode: %s", defaultString(preflight.Mode, "unknown")),
				fmt.Sprintf("- Preflight surface kind: %s", defaultString(preflight.SurfaceKind, "unknown")),
				fmt.Sprintf("- Preflight surface target: %s", defaultString(preflight.SurfaceTarget, "(none)")),
				fmt.Sprintf("- Browser ready: %t", preflight.BrowserReady),
			)
			if strings.TrimSpace(preflight.Reason) != "" {
				taskLines = append(taskLines, fmt.Sprintf("- Preflight reason: %s", preflight.Reason))
			}
		}
	}
	return runScoutPrompt(runtime, strings.Join(taskLines, "\n"), codexArgs, role)
}

func runUIScoutPreflight(runtime scoutExecutionRuntime, repoSlug string, focus []string, codexArgs []string) (*uiScoutPreflight, error) {
	repoLabel := repoSlug
	if repoLabel == "" {
		repoLabel = filepath.Base(runtime.RepoPath)
	}
	task := strings.Join([]string{
		uiScoutPreflightPrompt,
		"",
		"Task:",
		fmt.Sprintf("- Inspect repo: %s", repoLabel),
		fmt.Sprintf("- Focus: %s", strings.Join(focus, ", ")),
		"- Return only the JSON output contract.",
	}, "\n")
	stdout, err := runScoutPrompt(runtime, task, codexArgs, "ui-scout-preflight")
	if err != nil {
		return nil, fmt.Errorf("ui-scout preflight failed: %w", err)
	}
	preflight, err := parseUIScoutPreflight(stdout)
	if err != nil {
		return nil, err
	}
	if preflight.GeneratedAt == "" {
		preflight.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return preflight, nil
}

func parseUIScoutPreflight(content []byte) (*uiScoutPreflight, error) {
	trimmed := bytes.TrimSpace(extractImprovementJSONObject(content))
	if len(trimmed) == 0 {
		trimmed = bytes.TrimSpace(content)
	}
	var preflight uiScoutPreflight
	if err := json.Unmarshal(trimmed, &preflight); err != nil {
		return nil, fmt.Errorf("ui-scout preflight output did not match the expected JSON schema")
	}
	preflight.Version = 1
	preflight.Mode = normalizeUIScoutPreflightMode(preflight.Mode)
	preflight.SurfaceKind = normalizeUIScoutSurfaceKind(preflight.SurfaceKind)
	preflight.SurfaceTarget = strings.TrimSpace(preflight.SurfaceTarget)
	preflight.Reason = strings.TrimSpace(preflight.Reason)
	if preflight.Mode == "" {
		preflight.Mode = "repo_only"
	}
	if preflight.SurfaceKind == "" {
		preflight.SurfaceKind = "unknown"
	}
	return &preflight, nil
}

func runScoutPrompt(runtime scoutExecutionRuntime, task string, codexArgs []string, alias string) ([]byte, error) {
	repoLock, err := acquireSourceReadLock(runtime.RepoPath, repoAccessLockOwner{
		Backend: "scout",
		RunID:   sanitizePathToken(filepath.Base(runtime.ArtifactDir)),
		Purpose: "prompt-" + sanitizePathToken(alias),
		Label:   "scout-" + sanitizePathToken(alias),
	})
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = repoLock.Release()
	}()
	normalizedCodexArgs, fastMode := NormalizeCodexLaunchArgsWithFast(codexArgs)
	task = prefixCodexFastPrompt(task, fastMode)
	result, err := runManagedCodexPrompt(codexManagedPromptOptions{
		CommandDir:       runtime.RepoPath,
		InstructionsRoot: runtime.RepoPath,
		CodexHome:        runtime.CodexHome,
		FreshArgsPrefix:  []string{"exec", "-C", runtime.RepoPath},
		CommonArgs:       normalizedCodexArgs,
		Prompt:           task,
		PromptTransport:  codexPromptTransportArgWithDash,
		CheckpointPath:   filepath.Join(runtime.StateDir, sanitizePathToken(alias)+"-checkpoint.json"),
		StepKey:          alias,
		ResumeStrategy:   codexResumeSamePrompt,
		Env:              append(buildCodexEnv(NotifyTempContract{}, runtime.CodexHome), "NANA_PROJECT_AGENTS_ROOT="+runtime.RepoPath),
		RateLimitPolicy:  codexRateLimitPolicyDefault(runtime.RateLimitPolicy),
		OnPause:          runtime.OnPause,
		OnResume:         runtime.OnResume,
	})
	if err != nil {
		if strings.TrimSpace(result.Stderr) != "" {
			return nil, fmt.Errorf("%w\n%s", err, result.Stderr)
		}
		return nil, err
	}
	return []byte(result.Stdout), nil
}

func normalizeUIScoutPreflightMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "live", "repo_only", "blocked":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeUIScoutSurfaceKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "app", "storybook", "demo", "mock", "unknown":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func readImprovementScoutPrompt() (string, error) {
	return readScoutPrompt(improvementScoutRole)
}

func readScoutPrompt(role string) (string, error) {
	prompts, err := gocliassets.Prompts()
	if err == nil {
		if content := strings.TrimSpace(prompts[role+".md"]); content != "" {
			return content, nil
		}
	}
	content, readErr := os.ReadFile(filepath.Join(resolvePackageRoot(), "prompts", role+".md"))
	if readErr != nil {
		if err != nil {
			return "", err
		}
		return "", readErr
	}
	return string(content), nil
}

func parseImprovementReport(content []byte) (scoutReport, error) {
	return parseScoutReport(content, improvementScoutRole)
}

func parseScoutReport(content []byte, role string) (scoutReport, error) {
	trimmed := bytes.TrimSpace(extractImprovementJSONObject(content))
	if len(trimmed) == 0 {
		trimmed = bytes.TrimSpace(content)
	}
	var report scoutReport
	if err := json.Unmarshal(trimmed, &report); err == nil && report.Proposals != nil {
		return report, nil
	}
	var proposals []scoutFinding
	if err := json.Unmarshal(trimmed, &proposals); err == nil {
		return scoutReport{Version: 1, Proposals: proposals}, nil
	}
	return scoutReport{}, fmt.Errorf("%s output did not match the proposal JSON schema", role)
}

func extractImprovementJSONObject(content []byte) []byte {
	text := string(content)
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		return []byte(text[start : end+1])
	}
	return nil
}

func normalizeImprovementProposals(proposals []scoutFinding, policy scoutPolicy) []scoutFinding {
	return normalizeScoutProposals(proposals, policy, improvementScoutRole)
}

func normalizeScoutProposals(proposals []scoutFinding, policy scoutPolicy, role string) []scoutFinding {
	out := make([]scoutFinding, 0, len(proposals))
	for _, proposal := range proposals {
		proposal.Title = strings.TrimSpace(proposal.Title)
		proposal.Summary = strings.TrimSpace(proposal.Summary)
		if proposal.Title == "" || proposal.Summary == "" {
			continue
		}
		area := strings.ToLower(strings.TrimSpace(proposal.Area))
		switch area {
		case "ux":
			proposal.Area = "UX"
		case "perf", "performance":
			proposal.Area = "Perf"
		case "ui":
			proposal.Area = "UI"
		case "a11y", "accessibility":
			proposal.Area = "Accessibility"
		default:
			if strings.TrimSpace(proposal.Area) == "" {
				proposal.Area = scoutDefaultArea(role)
			}
		}
		proposal.Page = strings.TrimSpace(proposal.Page)
		proposal.Route = strings.TrimSpace(proposal.Route)
		proposal.WorkType = inferScoutWorkType(role, proposal).WorkType
		proposal.Severity = normalizeUIScoutSeverity(proposal.Severity)
		proposal.TargetKind = normalizeUIScoutTargetKind(proposal.TargetKind)
		proposal.Labels = normalizeScoutLabels(append(append([]string{}, policy.Labels...), proposal.Labels...), role)
		out = append(out, proposal)
	}
	return out
}

func normalizeUIScoutSeverity(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "critical", "major", "minor", "cosmetic":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeUIScoutTargetKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "real", "mock":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func writeImprovementRawOutput(repoPath string, rawOutput []byte) (string, error) {
	return writeScoutRawOutput(repoPath, rawOutput, improvementScoutRole)
}

func writeScoutRawOutput(repoPath string, rawOutput []byte, role string) (string, error) {
	dir := filepath.Join(repoPath, ".nana", scoutArtifactRoot(role), "raw")
	path := filepath.Join(dir, fmt.Sprintf("raw-%d.txt", time.Now().UTC().UnixNano()))
	if err := withScoutRepoWriteLock(repoPath, role, "raw-output", func() error {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, rawOutput, 0o644)
	}); err != nil {
		return "", err
	}
	return path, nil
}

func writeLocalImprovementArtifacts(repoPath string, report scoutReport, policy scoutPolicy, rawOutput []byte) (string, error) {
	artifactDir, err := prepareLocalScoutArtifactDir(repoPath, improvementScoutRole)
	if err != nil {
		return "", err
	}
	return writeLocalScoutArtifacts(artifactDir, report, policy, rawOutput, improvementScoutRole, nil)
}

func prepareLocalScoutArtifactDir(repoPath string, role string) (string, error) {
	dir := filepath.Join(repoPath, ".nana", scoutArtifactRoot(role), scoutRunID(role))
	if err := withScoutRepoWriteLock(repoPath, role, "prepare-artifact-dir", func() error {
		return os.MkdirAll(dir, 0o755)
	}); err != nil {
		return "", err
	}
	return dir, nil
}

func scoutRunID(role string) string {
	prefix := "improve"
	switch role {
	case enhancementScoutRole:
		prefix = "enhance"
	case uiScoutRole:
		prefix = "ui-scout"
	}
	return fmt.Sprintf("%s-%d", prefix, time.Now().UTC().UnixNano())
}

func scoutRunManifestPath(dir string) string {
	return filepath.Join(dir, "manifest.json")
}

func writeScoutRunManifest(dir string, manifest scoutRunManifest) error {
	repoPath, err := scoutRepoRootForArtifactPath(dir)
	if err != nil {
		return err
	}
	return withScoutRepoWriteLock(repoPath, manifest.Role, "write-run-manifest", func() error {
		return writeGithubJSON(scoutRunManifestPath(dir), manifest)
	})
}

func readScoutRunManifest(path string) (scoutRunManifest, error) {
	var manifest scoutRunManifest
	if err := readGithubJSON(path, &manifest); err != nil {
		return scoutRunManifest{}, err
	}
	return manifest, nil
}

func resolveScoutRunDir(repoPath string, role string, options ImproveOptions) (string, error) {
	root := filepath.Join(repoPath, ".nana", scoutArtifactRoot(role))
	if strings.TrimSpace(options.ResumeRunID) != "" {
		runDir := filepath.Join(root, strings.TrimSpace(options.ResumeRunID))
		if _, err := os.Stat(scoutRunManifestPath(runDir)); err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("scout run %s not found for %s at %s", options.ResumeRunID, role, runDir)
			}
			return "", err
		}
		return runDir, nil
	}
	if !options.ResumeLast {
		return "", fmt.Errorf("scout resume requires --resume <run-id> or --last")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no prior %s runs found in %s", role, root)
		}
		return "", err
	}
	type candidate struct {
		path    string
		modTime time.Time
	}
	candidates := []candidate{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runDir := filepath.Join(root, entry.Name())
		info, err := os.Stat(scoutRunManifestPath(runDir))
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{path: runDir, modTime: info.ModTime()})
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no prior %s runs found in %s", role, root)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].modTime.After(candidates[j].modTime) })
	return candidates[0].path, nil
}

func writeLocalScoutArtifacts(dir string, report scoutReport, policy scoutPolicy, rawOutput []byte, role string, preflight *uiScoutPreflight) (string, error) {
	repoPath, err := scoutRepoRootForArtifactPath(dir)
	if err != nil {
		return "", err
	}
	if err := withScoutRepoWriteLock(repoPath, role, "write-artifacts", func() error {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := writeGithubJSON(filepath.Join(dir, "proposals.json"), report); err != nil {
			return err
		}
		if err := writeGithubJSON(filepath.Join(dir, "policy.json"), policy); err != nil {
			return err
		}
		if preflight != nil {
			if err := writeGithubJSON(filepath.Join(dir, "preflight.json"), preflight); err != nil {
				return err
			}
		}
		if len(bytes.TrimSpace(rawOutput)) > 0 {
			if err := os.WriteFile(filepath.Join(dir, "raw-output.txt"), rawOutput, 0o644); err != nil {
				return err
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "issue-drafts.md"), []byte(renderScoutIssueDrafts(report, role, preflight)), 0o644); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return "", err
	}
	return dir, nil
}

func scoutRepoRootForArtifactPath(path string) (string, error) {
	current := filepath.Clean(strings.TrimSpace(path))
	if current == "" {
		return "", fmt.Errorf("scout artifact path is required")
	}
	for {
		if filepath.Base(current) == ".nana" {
			return filepath.Dir(current), nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return "", fmt.Errorf("could not determine scout repo root from %s", path)
}

func withScoutRepoWriteLock(repoPath string, role string, purpose string, fn func() error) error {
	return withSourceWriteLock(repoPath, repoAccessLockOwner{
		Backend: "scout",
		RunID:   sanitizePathToken(defaultString(strings.TrimSpace(role), filepath.Base(repoPath))),
		Purpose: purpose,
		Label:   "scout-" + sanitizePathToken(defaultString(strings.TrimSpace(role), "artifact")),
	}, fn)
}

func renderImprovementIssueDrafts(report scoutReport) string {
	return renderScoutIssueDrafts(report, improvementScoutRole, nil)
}

func renderScoutIssueDrafts(report scoutReport, role string, preflight *uiScoutPreflight) string {
	lines := []string{
		"# " + scoutIssueHeading(role) + " Drafts",
		"",
		fmt.Sprintf("Repo: %s", defaultString(report.Repo, "(local)")),
		fmt.Sprintf("Generated: %s", defaultString(report.GeneratedAt, "(unknown)")),
		"",
		scoutDraftWording(role),
		"",
	}
	if role == uiScoutRole && preflight != nil {
		lines = append(lines,
			fmt.Sprintf("Audit mode: %s", defaultString(preflight.Mode, "unknown")),
			fmt.Sprintf("Surface kind: %s", defaultString(preflight.SurfaceKind, "unknown")),
			fmt.Sprintf("Surface target: %s", defaultString(preflight.SurfaceTarget, "(none)")),
			fmt.Sprintf("Browser ready: %t", preflight.BrowserReady),
		)
		if strings.TrimSpace(preflight.Reason) != "" {
			lines = append(lines, "Reason: "+preflight.Reason)
		}
		lines = append(lines, "")
	}
	for index, proposal := range report.Proposals {
		lines = append(lines,
			fmt.Sprintf("## %d. %s", index+1, proposal.Title),
			"",
			fmt.Sprintf("- Area: %s", defaultString(proposal.Area, scoutDefaultArea(role))),
			fmt.Sprintf("- Labels: %s", strings.Join(normalizeScoutLabels(proposal.Labels, role), ", ")),
			fmt.Sprintf("- Confidence: %s", defaultString(proposal.Confidence, "unknown")),
			"",
			proposal.Summary,
			"",
		)
		if strings.TrimSpace(proposal.Page) != "" {
			lines = append(lines, "Page: "+proposal.Page, "")
		}
		if strings.TrimSpace(proposal.Route) != "" {
			lines = append(lines, "Route: "+proposal.Route, "")
		}
		if strings.TrimSpace(proposal.Severity) != "" {
			lines = append(lines, "Severity: "+proposal.Severity, "")
		}
		if strings.TrimSpace(proposal.TargetKind) != "" {
			lines = append(lines, "Target kind: "+proposal.TargetKind, "")
		}
		if strings.TrimSpace(proposal.Rationale) != "" {
			lines = append(lines, "Rationale: "+proposal.Rationale, "")
		}
		if strings.TrimSpace(proposal.Evidence) != "" {
			lines = append(lines, "Evidence: "+proposal.Evidence, "")
		}
		if strings.TrimSpace(proposal.Impact) != "" {
			lines = append(lines, "Impact: "+proposal.Impact, "")
		}
		if len(proposal.Files) > 0 {
			lines = append(lines, "Files: "+strings.Join(proposal.Files, ", "), "")
		}
		if len(proposal.Screenshots) > 0 {
			lines = append(lines, "Screenshots: "+strings.Join(proposal.Screenshots, ", "), "")
		}
		if strings.TrimSpace(proposal.SuggestedNextStep) != "" {
			lines = append(lines, "Suggested next step: "+proposal.SuggestedNextStep, "")
		}
	}
	return strings.Join(lines, "\n")
}

func publishImprovementIssues(repoSlug string, proposals []scoutFinding, policy scoutPolicy, dryRun bool) ([]scoutIssueResult, error) {
	return publishScoutIssues(repoSlug, proposals, policy, dryRun, improvementScoutRole)
}

func publishScoutIssues(repoSlug string, proposals []scoutFinding, policy scoutPolicy, dryRun bool, role string) ([]scoutIssueResult, error) {
	destination := normalizeScoutDestination(policy.IssueDestination)
	targetRepo := repoSlug
	apiBaseURL := strings.TrimSpace(os.Getenv("GITHUB_API_URL"))
	if apiBaseURL == "" {
		apiBaseURL = "https://api.github.com"
	}
	token, err := resolveGithubToken()
	if err != nil {
		return nil, err
	}
	if destination == improvementDestinationFork || destination == improvementDestinationTarget {
		targetRepo, err = resolveScoutIssueTargetRepo(repoSlug, policy, role)
		if err != nil {
			return nil, err
		}
	}
	if !validRepoSlug(targetRepo) {
		return nil, fmt.Errorf("invalid %s issue target repo: %s", role, targetRepo)
	}
	results := make([]scoutIssueResult, 0, len(proposals))
	for _, proposal := range proposals {
		proposal.Labels = normalizeScoutLabels(append(append([]string{}, policy.Labels...), proposal.Labels...), role)
		title := formatScoutIssueTitle(proposal, role)
		if dryRun {
			results = append(results, scoutIssueResult{Title: title, DryRun: true})
			continue
		}
		payload := map[string]any{
			"title":  title,
			"body":   renderScoutIssueBody(proposal, role),
			"labels": normalizeScoutLabels(proposal.Labels, role),
		}
		var created struct {
			HTMLURL string `json:"html_url"`
		}
		if err := githubAPIRequestJSON(http.MethodPost, apiBaseURL, token, fmt.Sprintf("/repos/%s/issues", targetRepo), payload, &created); err != nil {
			return nil, err
		}
		results = append(results, scoutIssueResult{Title: title, URL: created.HTMLURL})
	}
	return results, nil
}

func countOpenScoutIssues(apiBaseURL string, token string, repoSlug string, role string) (int, error) {
	var issues []struct {
		Number      int `json:"number"`
		PullRequest *struct {
			URL string `json:"url"`
		} `json:"pull_request,omitempty"`
	}
	path := fmt.Sprintf("/repos/%s/issues?state=open&labels=%s&per_page=100", repoSlug, url.QueryEscape(role))
	if err := githubAPIGetJSON(apiBaseURL, token, path, &issues); err != nil {
		return 0, err
	}
	count := 0
	for _, issue := range issues {
		if issue.PullRequest == nil {
			count++
		}
	}
	return count, nil
}

func formatScoutIssueTitle(proposal scoutFinding, role string) string {
	area := strings.TrimSpace(proposal.Area)
	if area == "" || area == scoutIssueHeading(role) || area == scoutDefaultArea(role) {
		return proposal.Title
	}
	if strings.HasPrefix(strings.ToLower(proposal.Title), strings.ToLower(area)+":") {
		return proposal.Title
	}
	return fmt.Sprintf("%s: %s", area, proposal.Title)
}

func renderImprovementIssueBody(proposal scoutFinding) string {
	return renderScoutIssueBody(proposal, improvementScoutRole)
}

func renderScoutIssueBody(proposal scoutFinding, role string) string {
	lines := []string{
		"## " + scoutIssueHeading(role),
		"",
		proposal.Summary,
		"",
		scoutIssueWording(role),
		"",
	}
	if strings.TrimSpace(proposal.Rationale) != "" {
		lines = append(lines, "## Rationale", "", proposal.Rationale, "")
	}
	if strings.TrimSpace(proposal.Page) != "" || strings.TrimSpace(proposal.Route) != "" || strings.TrimSpace(proposal.Severity) != "" || strings.TrimSpace(proposal.TargetKind) != "" {
		lines = append(lines, "## Audit Context", "")
		if strings.TrimSpace(proposal.Page) != "" {
			lines = append(lines, fmt.Sprintf("- Page: %s", proposal.Page))
		}
		if strings.TrimSpace(proposal.Route) != "" {
			lines = append(lines, fmt.Sprintf("- Route: %s", proposal.Route))
		}
		if strings.TrimSpace(proposal.Severity) != "" {
			lines = append(lines, fmt.Sprintf("- Severity: %s", proposal.Severity))
		}
		if strings.TrimSpace(proposal.TargetKind) != "" {
			lines = append(lines, fmt.Sprintf("- Target kind: %s", proposal.TargetKind))
		}
		lines = append(lines, "")
	}
	if strings.TrimSpace(proposal.Evidence) != "" {
		lines = append(lines, "## Evidence", "", proposal.Evidence, "")
	}
	if strings.TrimSpace(proposal.Impact) != "" {
		lines = append(lines, "## Impact", "", proposal.Impact, "")
	}
	if strings.TrimSpace(proposal.SuggestedNextStep) != "" {
		lines = append(lines, "## Suggested Next Step", "", proposal.SuggestedNextStep, "")
	}
	if len(proposal.Files) > 0 {
		lines = append(lines, "## Files", "")
		for _, file := range proposal.Files {
			lines = append(lines, "- `"+file+"`")
		}
		lines = append(lines, "")
	}
	if len(proposal.Screenshots) > 0 {
		lines = append(lines, "## Screenshots", "")
		for _, screenshot := range proposal.Screenshots {
			lines = append(lines, "- `"+screenshot+"`")
		}
		lines = append(lines, "")
	}
	if strings.TrimSpace(proposal.Confidence) != "" {
		lines = append(lines, "Confidence: "+proposal.Confidence)
	}
	return strings.Join(lines, "\n")
}

func scoutArtifactRoot(role string) string {
	return scoutRoleSpecFor(role).ArtifactRoot
}

func scoutOutputPrefix(role string) string {
	return scoutRoleSpecFor(role).OutputPrefix
}

func scoutProposalNoun(role string) string {
	return scoutRoleSpecFor(role).ResultPlural
}

func scoutIssueHeading(role string) string {
	return scoutRoleSpecFor(role).IssueHeading
}

func scoutDefaultArea(role string) string {
	return scoutRoleSpecFor(role).DefaultArea
}

func scoutDraftWording(role string) string {
	return scoutRoleSpecFor(role).DraftWording
}

func scoutIssueWording(role string) string {
	return scoutRoleSpecFor(role).IssueWording
}

func scoutItemsCountNoun(role string) string {
	return scoutRoleSpecFor(role).ItemCountNoun
}
