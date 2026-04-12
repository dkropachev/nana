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
	"strings"
	"time"

	"github.com/Yeachan-Heo/nana/internal/gocliassets"
)

const ImproveHelp = `nana improve - Discover UX and performance improvements

Usage:
  nana improve [owner/repo|github-url] [--repo <path>] [--focus <ux,perf>] [--from-file <proposals.json>] [--dry-run] [--local-only] [-- codex-args...]
  nana improve help

Behavior:
  - runs the improvement-scout role against the selected repo
  - local repos always keep proposals under .nana/improvements/
  - GitHub repos read .nana/improvement-policy.json or .github/nana-improvement-policy.json from the repo checkout
  - GitHub policy issue_destination controls publication: local, repo/target, or fork
  - emits 5 proposals per run by default; policy max_issues can raise the cap up to 50
  - created issues are labeled with improvement-scout, never enhancement, and count toward that role's open-issue cap

Policy example:
  {"version":1,"issue_destination":"repo","labels":["improvement","ux","perf"]}
  {"version":1,"issue_destination":"fork","fork_repo":"my-user/widget","labels":["improvement"]}
`

const EnhanceHelp = `nana enhance - Discover repo enhancements that help a project move forward

Usage:
  nana enhance [owner/repo|github-url] [--repo <path>] [--focus <ux,perf>] [--from-file <proposals.json>] [--dry-run] [--local-only] [-- codex-args...]
  nana enhance help

Behavior:
  - runs the enhancement-scout role against the selected repo
  - local repos always keep proposals under .nana/enhancements/
  - GitHub repos read .nana/enhancement-policy.json or .github/nana-enhancement-policy.json from the repo checkout
  - GitHub policy issue_destination controls publication: local, repo/target, or fork
  - emits 5 proposals per run by default; policy max_issues can raise the cap up to 50
  - created issues are labeled with enhancement-scout and count toward that role's open-issue cap

Policy example:
  {"version":1,"issue_destination":"repo","labels":["enhancement"]}
  {"version":1,"issue_destination":"fork","fork_repo":"my-user/widget","labels":["enhancement"]}
`

const ScoutStartHelp = `nana start - Run supported repo startup automation

Usage:
  nana start [owner/repo|github-url] [--repo <path>] [--focus <ux,perf>] [--from-file <proposals.json>] [--dry-run] [--local-only] [-- codex-args...]
  nana start help

Behavior:
  - detects scout support from repo policy files
  - runs improvement-scout when .nana/improvement-policy.json or .github/nana-improvement-policy.json exists
  - runs enhancement-scout when .nana/enhancement-policy.json or .github/nana-enhancement-policy.json exists
  - local repos keep proposals under .nana/improvements/ or .nana/enhancements/
  - GitHub targets follow their scout policy issue_destination
  - local repos with mode "auto" in every supported scout policy commit generated artifacts to the repo's default branch
  - auto mode requires a clean worktree and a resolvable local default branch
  - exits cleanly when the repo does not declare supported scout policies
`

const (
	improvementDestinationLocal  = "local"
	improvementDestinationTarget = "target"
	improvementDestinationFork   = "fork"

	improvementScoutRole = "improvement-scout"
	enhancementScoutRole = "enhancement-scout"
	defaultScoutIssueCap = 5
	maxScoutIssueCap     = 50
)

type ImproveOptions struct {
	Target    string
	RepoPath  string
	Focus     []string
	FromFile  string
	DryRun    bool
	LocalOnly bool
	CodexArgs []string
}

type improvementPolicy struct {
	Version          int      `json:"version"`
	Mode             string   `json:"mode,omitempty"`
	IssueDestination string   `json:"issue_destination,omitempty"`
	ForkRepo         string   `json:"fork_repo,omitempty"`
	Labels           []string `json:"labels,omitempty"`
	MaxIssues        int      `json:"max_issues,omitempty"`
}

type improvementReport struct {
	Version     int                   `json:"version"`
	Repo        string                `json:"repo,omitempty"`
	GeneratedAt string                `json:"generated_at,omitempty"`
	Proposals   []improvementProposal `json:"proposals"`
}

type improvementProposal struct {
	Title             string   `json:"title"`
	Area              string   `json:"area,omitempty"`
	Summary           string   `json:"summary"`
	Rationale         string   `json:"rationale,omitempty"`
	Evidence          string   `json:"evidence,omitempty"`
	Impact            string   `json:"impact,omitempty"`
	SuggestedNextStep string   `json:"suggested_next_step,omitempty"`
	Confidence        string   `json:"confidence,omitempty"`
	Files             []string `json:"files,omitempty"`
	Labels            []string `json:"labels,omitempty"`
}

type improvementIssueResult struct {
	Title  string
	URL    string
	DryRun bool
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

func StartScouts(cwd string, args []string) error {
	if len(args) > 0 && isHelpToken(args[0]) || len(args) > 0 && args[0] == "help" {
		fmt.Fprint(os.Stdout, ScoutStartHelp)
		return nil
	}
	options, err := parseScoutArgs(args, ScoutStartHelp, "start")
	if err != nil {
		return err
	}
	return startRunScoutStart(cwd, options)
}

func parseImproveArgs(args []string) (ImproveOptions, error) {
	return parseScoutArgs(args, ImproveHelp, "improve")
}

func parseScoutArgs(args []string, help string, command string) (ImproveOptions, error) {
	options := ImproveOptions{Focus: []string{"ux", "perf"}}
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
				options.Focus, err = parseScoutFocus(value, help, command)
				if err != nil {
					return ImproveOptions{}, err
				}
				index++
			case strings.HasPrefix(token, "--focus="):
				parsed, err := parseScoutFocus(strings.TrimPrefix(token, "--focus="), help, command)
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
			case token == "--dry-run":
				options.DryRun = true
			case token == "--local-only":
				options.LocalOnly = true
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

func runScout(cwd string, options ImproveOptions, role string) error {
	repoSlug, githubTarget := normalizeImproveGithubRepo(options.Target)
	repoPath := strings.TrimSpace(options.RepoPath)
	if repoPath == "" {
		if strings.TrimSpace(options.Target) != "" && !githubTarget {
			repoPath = options.Target
		} else {
			repoPath = cwd
		}
	}

	var err error
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

	policy := readScoutPolicy(repoPath, role)
	if !githubTarget || options.LocalOnly {
		policy.IssueDestination = improvementDestinationLocal
	}
	policy.Labels = normalizeScoutLabels(policy.Labels, role)

	rawOutput := []byte{}
	if strings.TrimSpace(options.FromFile) != "" {
		rawOutput, err = os.ReadFile(options.FromFile)
		if err != nil {
			return err
		}
	} else {
		rawOutput, err = runScoutRole(repoPath, repoSlug, options.Focus, options.CodexArgs, role)
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

	artifactDir, err := writeLocalScoutArtifacts(repoPath, report, policy, rawOutput, role)
	if err != nil {
		return err
	}
	prefix := scoutOutputPrefix(role)
	fmt.Fprintf(os.Stdout, "[%s] Saved proposals locally: %s\n", prefix, artifactDir)
	if len(report.Proposals) == 0 {
		fmt.Fprintf(os.Stdout, "[%s] No grounded %s proposals found.\n", prefix, scoutProposalNoun(role))
		return nil
	}
	if policy.IssueDestination == improvementDestinationLocal {
		fmt.Fprintf(os.Stdout, "[%s] Keeping %d proposal(s) local by policy.\n", prefix, len(report.Proposals))
		return nil
	}
	if !githubTarget {
		fmt.Fprintf(os.Stdout, "[%s] Local repo detected; keeping proposals local.\n", prefix)
		return nil
	}
	results, err := publishScoutIssues(repoSlug, report.Proposals, policy, options.DryRun, role)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Fprintf(os.Stdout, "[%s] Open issue cap reached for %s; no issues created.\n", prefix, role)
		return nil
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
	roles := supportedScoutRoles(repoPath)
	if len(roles) == 0 {
		fmt.Fprintf(os.Stdout, "[start] No supported scout policies found in %s; nothing to run.\n", repoPath)
		return nil
	}
	_, githubTarget := normalizeImproveGithubRepo(options.Target)
	autoLocal := !githubTarget && scoutStartAutoMode(repoPath, roles)
	if autoLocal {
		if err := ensureScoutDefaultBranch(repoPath); err != nil {
			return err
		}
	}
	for _, role := range roles {
		fmt.Fprintf(os.Stdout, "[start] %s supported; running.\n", role)
		if err := runScout(cwd, options, role); err != nil {
			return err
		}
	}
	if autoLocal {
		committed, err := commitScoutArtifactsToDefault(repoPath)
		if err != nil {
			return err
		}
		if committed {
			fmt.Fprintln(os.Stdout, "[start] Committed scout artifacts to default branch.")
		} else {
			fmt.Fprintln(os.Stdout, "[start] No scout artifact changes to commit on default branch.")
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
	for _, role := range []string{improvementScoutRole, enhancementScoutRole} {
		if scoutPolicyExists(repoPath, role) {
			roles = append(roles, role)
		}
	}
	return roles
}

func scoutPolicyExists(repoPath string, role string) bool {
	for _, rel := range scoutPolicyPaths(role) {
		if info, err := os.Stat(filepath.Join(repoPath, rel)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func scoutStartAutoMode(repoPath string, roles []string) bool {
	if len(roles) == 0 {
		return false
	}
	for _, role := range roles {
		policy := readScoutPolicy(repoPath, role)
		if strings.ToLower(strings.TrimSpace(policy.Mode)) != "auto" {
			return false
		}
	}
	return true
}

func ensureScoutDefaultBranch(repoPath string) error {
	if _, err := githubGitOutput(repoPath, "rev-parse", "--is-inside-work-tree"); err != nil {
		return fmt.Errorf("scout auto mode requires a local git repo: %w", err)
	}
	status, err := githubGitOutput(repoPath, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
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
	if err := githubRunGit(repoPath, "commit", "-m", "Record scout startup artifacts"); err != nil {
		return false, err
	}
	return true, nil
}

func existingScoutArtifactRoots(repoPath string) []string {
	paths := []string{}
	for _, rel := range []string{".nana/improvements", ".nana/enhancements"} {
		if info, err := os.Stat(filepath.Join(repoPath, rel)); err == nil && info.IsDir() {
			paths = append(paths, rel)
		}
	}
	return paths
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
	if err := ensureGithubSourceClone(paths, meta); err != nil {
		return "", err
	}
	return paths.SourcePath, nil
}

func defaultImprovementPolicy() improvementPolicy {
	return improvementPolicy{
		Version:          1,
		IssueDestination: improvementDestinationLocal,
		Labels:           []string{},
		MaxIssues:        defaultScoutIssueCap,
	}
}

func readImprovementPolicy(repoPath string) improvementPolicy {
	return readScoutPolicy(repoPath, improvementScoutRole)
}

func readScoutPolicy(repoPath string, role string) improvementPolicy {
	policy := defaultImprovementPolicy()
	for _, rel := range scoutPolicyPaths(role) {
		var candidate improvementPolicy
		if err := readGithubJSON(filepath.Join(repoPath, rel), &candidate); err != nil {
			continue
		}
		mergeImprovementPolicy(&policy, candidate)
	}
	policy.IssueDestination = normalizeImprovementDestination(policy.IssueDestination)
	policy.Labels = normalizeScoutLabels(policy.Labels, role)
	if policy.MaxIssues <= 0 {
		policy.MaxIssues = defaultScoutIssueCap
	} else if policy.MaxIssues > maxScoutIssueCap {
		policy.MaxIssues = maxScoutIssueCap
	}
	return policy
}

func scoutPolicyPaths(role string) []string {
	if role == enhancementScoutRole {
		return []string{
			filepath.Join(".github", "nana-enhancement-policy.json"),
			filepath.Join(".nana", "enhancement-policy.json"),
		}
	}
	return []string{
		filepath.Join(".github", "nana-improvement-policy.json"),
		filepath.Join(".nana", "improvement-policy.json"),
	}
}

func mergeImprovementPolicy(target *improvementPolicy, source improvementPolicy) {
	if source.Version != 0 {
		target.Version = source.Version
	}
	if strings.TrimSpace(source.Mode) != "" {
		target.Mode = source.Mode
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
	if source.MaxIssues > 0 {
		target.MaxIssues = source.MaxIssues
	}
}

func normalizeImprovementDestination(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case improvementDestinationTarget, "repo":
		return improvementDestinationTarget
	case improvementDestinationFork:
		return improvementDestinationFork
	default:
		return improvementDestinationLocal
	}
}

func normalizeImprovementLabels(labels []string) []string {
	return normalizeScoutLabels(labels, improvementScoutRole)
}

func normalizeScoutLabels(labels []string, role string) []string {
	base := "improvement"
	forbidden := "enhancement"
	if role == enhancementScoutRole {
		base = "enhancement"
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

func runScoutRole(repoPath string, repoSlug string, focus []string, codexArgs []string, role string) ([]byte, error) {
	promptSurface, err := readScoutPrompt(role)
	if err != nil {
		return nil, err
	}
	repoLabel := repoSlug
	if repoLabel == "" {
		repoLabel = filepath.Base(repoPath)
	}
	task := strings.Join([]string{
		strings.TrimSpace(promptSurface),
		"",
		"Task:",
		fmt.Sprintf("- Inspect repo: %s", repoLabel),
		fmt.Sprintf("- Focus: %s", strings.Join(focus, ", ")),
		"- Return only the JSON output contract.",
		fmt.Sprintf("- Treat proposals as %s.", scoutProposalNoun(role)),
	}, "\n")
	args := append([]string{"exec", "-C", repoPath}, codexArgs...)
	args = append(args, task)
	cmd := exec.Command("codex", args...)
	cmd.Dir = repoPath
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("%s failed: %w\n%s", role, err, stderr.String())
		}
		return nil, err
	}
	if stderr.Len() > 0 {
		fmt.Fprint(os.Stdout, stderr.String())
	}
	return stdout.Bytes(), nil
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

func parseImprovementReport(content []byte) (improvementReport, error) {
	return parseScoutReport(content, improvementScoutRole)
}

func parseScoutReport(content []byte, role string) (improvementReport, error) {
	trimmed := bytes.TrimSpace(extractImprovementJSONObject(content))
	if len(trimmed) == 0 {
		trimmed = bytes.TrimSpace(content)
	}
	var report improvementReport
	if err := json.Unmarshal(trimmed, &report); err == nil && report.Proposals != nil {
		return report, nil
	}
	var proposals []improvementProposal
	if err := json.Unmarshal(trimmed, &proposals); err == nil {
		return improvementReport{Version: 1, Proposals: proposals}, nil
	}
	return improvementReport{}, fmt.Errorf("%s output did not match the proposal JSON schema", role)
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

func normalizeImprovementProposals(proposals []improvementProposal, policy improvementPolicy) []improvementProposal {
	return normalizeScoutProposals(proposals, policy, improvementScoutRole)
}

func normalizeScoutProposals(proposals []improvementProposal, policy improvementPolicy, role string) []improvementProposal {
	limit := len(proposals)
	maxIssues := effectiveScoutMaxIssues(policy)
	if maxIssues < limit {
		limit = maxIssues
	}
	out := make([]improvementProposal, 0, limit)
	for _, proposal := range proposals {
		if len(out) >= limit {
			break
		}
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
		default:
			proposal.Area = scoutIssueHeading(role)
		}
		proposal.Labels = normalizeScoutLabels(append(append([]string{}, policy.Labels...), proposal.Labels...), role)
		out = append(out, proposal)
	}
	return out
}

func writeImprovementRawOutput(repoPath string, rawOutput []byte) (string, error) {
	return writeScoutRawOutput(repoPath, rawOutput, improvementScoutRole)
}

func writeScoutRawOutput(repoPath string, rawOutput []byte, role string) (string, error) {
	dir := filepath.Join(repoPath, ".nana", scoutArtifactRoot(role), "raw")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("raw-%d.txt", time.Now().UTC().UnixNano()))
	return path, os.WriteFile(path, rawOutput, 0o644)
}

func writeLocalImprovementArtifacts(repoPath string, report improvementReport, policy improvementPolicy, rawOutput []byte) (string, error) {
	return writeLocalScoutArtifacts(repoPath, report, policy, rawOutput, improvementScoutRole)
}

func writeLocalScoutArtifacts(repoPath string, report improvementReport, policy improvementPolicy, rawOutput []byte, role string) (string, error) {
	runID := fmt.Sprintf("improve-%d", time.Now().UTC().UnixNano())
	if role == enhancementScoutRole {
		runID = fmt.Sprintf("enhance-%d", time.Now().UTC().UnixNano())
	}
	dir := filepath.Join(repoPath, ".nana", scoutArtifactRoot(role), runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := writeGithubJSON(filepath.Join(dir, "proposals.json"), report); err != nil {
		return "", err
	}
	if err := writeGithubJSON(filepath.Join(dir, "policy.json"), policy); err != nil {
		return "", err
	}
	if len(bytes.TrimSpace(rawOutput)) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "raw-output.txt"), rawOutput, 0o644); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "issue-drafts.md"), []byte(renderScoutIssueDrafts(report, role)), 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

func renderImprovementIssueDrafts(report improvementReport) string {
	return renderScoutIssueDrafts(report, improvementScoutRole)
}

func renderScoutIssueDrafts(report improvementReport, role string) string {
	lines := []string{
		"# " + scoutIssueHeading(role) + " Proposals",
		"",
		fmt.Sprintf("Repo: %s", defaultString(report.Repo, "(local)")),
		fmt.Sprintf("Generated: %s", defaultString(report.GeneratedAt, "(unknown)")),
		"",
		scoutDraftWording(role),
		"",
	}
	for index, proposal := range report.Proposals {
		lines = append(lines,
			fmt.Sprintf("## %d. %s", index+1, proposal.Title),
			"",
			fmt.Sprintf("- Area: %s", defaultString(proposal.Area, scoutIssueHeading(role))),
			fmt.Sprintf("- Labels: %s", strings.Join(normalizeScoutLabels(proposal.Labels, role), ", ")),
			fmt.Sprintf("- Confidence: %s", defaultString(proposal.Confidence, "unknown")),
			"",
			proposal.Summary,
			"",
		)
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
		if strings.TrimSpace(proposal.SuggestedNextStep) != "" {
			lines = append(lines, "Suggested next step: "+proposal.SuggestedNextStep, "")
		}
	}
	return strings.Join(lines, "\n")
}

func publishImprovementIssues(repoSlug string, proposals []improvementProposal, policy improvementPolicy, dryRun bool) ([]improvementIssueResult, error) {
	return publishScoutIssues(repoSlug, proposals, policy, dryRun, improvementScoutRole)
}

func publishScoutIssues(repoSlug string, proposals []improvementProposal, policy improvementPolicy, dryRun bool, role string) ([]improvementIssueResult, error) {
	destination := normalizeImprovementDestination(policy.IssueDestination)
	targetRepo := repoSlug
	apiBaseURL := strings.TrimSpace(os.Getenv("GITHUB_API_URL"))
	if apiBaseURL == "" {
		apiBaseURL = "https://api.github.com"
	}
	token, err := resolveGithubToken()
	if err != nil {
		return nil, err
	}
	if destination == improvementDestinationFork {
		targetRepo = strings.TrimSpace(policy.ForkRepo)
		if targetRepo == "" {
			var viewer struct {
				Login string `json:"login"`
			}
			if err := githubAPIGetJSON(apiBaseURL, token, "/user", &viewer); err != nil {
				return nil, err
			}
			parts := strings.Split(repoSlug, "/")
			if len(parts) != 2 || viewer.Login == "" {
				return nil, fmt.Errorf("cannot infer fork repo for %s; set fork_repo in %s policy", repoSlug, role)
			}
			targetRepo = viewer.Login + "/" + parts[1]
		}
	}
	if !validRepoSlug(targetRepo) {
		return nil, fmt.Errorf("invalid %s issue target repo: %s", role, targetRepo)
	}
	maxIssues := effectiveScoutMaxIssues(policy)
	openCount, err := countOpenScoutIssues(apiBaseURL, token, targetRepo, role)
	if err != nil {
		return nil, err
	}
	remaining := maxIssues - openCount
	if remaining <= 0 {
		return []improvementIssueResult{}, nil
	}
	if len(proposals) > remaining {
		proposals = proposals[:remaining]
	}
	results := make([]improvementIssueResult, 0, len(proposals))
	for _, proposal := range proposals {
		proposal.Labels = normalizeScoutLabels(append(append([]string{}, policy.Labels...), proposal.Labels...), role)
		title := formatImprovementIssueTitle(proposal)
		if dryRun {
			results = append(results, improvementIssueResult{Title: title, DryRun: true})
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
		results = append(results, improvementIssueResult{Title: title, URL: created.HTMLURL})
	}
	return results, nil
}

func effectiveScoutMaxIssues(policy improvementPolicy) int {
	if policy.MaxIssues <= 0 {
		return defaultScoutIssueCap
	}
	if policy.MaxIssues > maxScoutIssueCap {
		return maxScoutIssueCap
	}
	return policy.MaxIssues
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

func formatImprovementIssueTitle(proposal improvementProposal) string {
	area := strings.TrimSpace(proposal.Area)
	if area == "" || area == "Improvement" {
		return proposal.Title
	}
	if strings.HasPrefix(strings.ToLower(proposal.Title), strings.ToLower(area)+":") {
		return proposal.Title
	}
	return fmt.Sprintf("%s: %s", area, proposal.Title)
}

func renderImprovementIssueBody(proposal improvementProposal) string {
	return renderScoutIssueBody(proposal, improvementScoutRole)
}

func renderScoutIssueBody(proposal improvementProposal, role string) string {
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
	if strings.TrimSpace(proposal.Confidence) != "" {
		lines = append(lines, "Confidence: "+proposal.Confidence)
	}
	return strings.Join(lines, "\n")
}

func scoutArtifactRoot(role string) string {
	if role == enhancementScoutRole {
		return "enhancements"
	}
	return "improvements"
}

func scoutOutputPrefix(role string) string {
	if role == enhancementScoutRole {
		return "enhance"
	}
	return "improve"
}

func scoutProposalNoun(role string) string {
	if role == enhancementScoutRole {
		return "enhancements"
	}
	return "improvements"
}

func scoutIssueHeading(role string) string {
	if role == enhancementScoutRole {
		return "Enhancement"
	}
	return "Improvement"
}

func scoutDraftWording(role string) string {
	if role == enhancementScoutRole {
		return "These are enhancement proposals intended to help the repo move forward."
	}
	return "These are improvement proposals, not enhancement requests."
}

func scoutIssueWording(role string) string {
	if role == enhancementScoutRole {
		return "This is an enhancement proposal intended to help the repo move forward."
	}
	return "This is an improvement proposal, not an enhancement request."
}
