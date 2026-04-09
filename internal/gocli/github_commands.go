package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type githubCommandResult struct {
	LegacyArgs []string
	Handled    bool
}

type githubWorkonManifest struct {
	RunID             string `json:"run_id"`
	RepoSlug          string `json:"repo_slug"`
	TargetURL         string `json:"target_url"`
	UpdatedAt         string `json:"updated_at"`
	PublishedPRNumber int    `json:"published_pr_number"`
	SandboxID         string `json:"sandbox_id"`
}

type githubPullReviewFinding struct {
	Fingerprint     string `json:"fingerprint"`
	Title           string `json:"title"`
	Path            string `json:"path"`
	Line            int    `json:"line,omitempty"`
	Detail          string `json:"detail"`
	UserExplanation string `json:"user_explanation,omitempty"`
	ChangedLineInPR bool   `json:"changed_line_in_pr,omitempty"`
	PRPermalink     string `json:"pr_permalink,omitempty"`
	MainPermalink   string `json:"main_permalink,omitempty"`
}

type githubPullStatePayload struct {
	Number int    `json:"number"`
	State  string `json:"state"`
}

func GithubIssue(cwd string, args []string) (githubCommandResult, error) {
	command := ""
	rest := []string{}
	if len(args) == 0 {
		fmt.Fprint(os.Stdout, IssueHelp)
		return githubCommandResult{Handled: true}, nil
	}
	if args[0] == "issue" {
		if len(args) == 1 || isHelpToken(args[1]) {
			fmt.Fprint(os.Stdout, IssueHelp)
			return githubCommandResult{Handled: true}, nil
		}
		command = args[1]
		rest = append([]string{}, args[2:]...)
	} else {
		command = args[0]
		rest = append([]string{}, args[1:]...)
	}
	if len(rest) > 0 && isHelpToken(rest[0]) {
		fmt.Fprint(os.Stdout, IssueHelp)
		return githubCommandResult{Handled: true}, nil
	}

	switch command {
	case "implement":
		if len(rest) == 0 {
			return githubCommandResult{}, fmt.Errorf("Usage: nana issue implement <github-issue-url> [work-on start flags...]")
		}
		return githubCommandResult{LegacyArgs: append([]string{"work-on", "start"}, rest...)}, nil
	case "investigate":
		if len(rest) == 0 {
			return githubCommandResult{}, fmt.Errorf("Usage: nana issue investigate <github-issue-url> [work-on start flags...]")
		}
		return githubCommandResult{LegacyArgs: append([]string{"issue", "investigate"}, rest...)}, nil
	case "sync":
		legacyArgs, err := normalizeGithubIssueSyncArgs(rest)
		if err != nil {
			return githubCommandResult{}, err
		}
		return githubCommandResult{LegacyArgs: legacyArgs}, nil
	default:
		if args[0] == "issue" {
			return githubCommandResult{}, fmt.Errorf("Unknown issue subcommand: %s", command)
		}
		return githubCommandResult{}, fmt.Errorf("nana: unknown command: %s", command)
	}
}

func normalizeGithubIssueSyncArgs(args []string) ([]string, error) {
	if len(args) > 0 && strings.HasPrefix(strings.TrimSpace(args[0]), "https://github.com/") {
		runID, err := ResolveGithubRunIDForTargetURL(args[0])
		if err != nil {
			return nil, err
		}
		if runID == "" {
			return nil, fmt.Errorf("No managed NANA run found for %s", args[0])
		}
		return append([]string{"work-on", "sync", "--run-id", runID, args[0]}, args[1:]...), nil
	}
	return append([]string{"work-on", "sync"}, args...), nil
}

func GithubReview(cwd string, args []string) (githubCommandResult, error) {
	if len(args) == 0 || isHelpToken(args[0]) {
		fmt.Fprint(os.Stdout, GithubReviewHelp)
		return githubCommandResult{Handled: true}, nil
	}
	if args[0] != "followup" {
		return githubCommandResult{LegacyArgs: append([]string{}, append([]string{"review"}, args...)...)}, nil
	}
	target, allowOpen, err := parseGithubReviewFollowupArgs(args[1:])
	if err != nil {
		return githubCommandResult{}, err
	}
	if err := githubReviewFollowup(target, allowOpen); err != nil {
		return githubCommandResult{}, err
	}
	return githubCommandResult{Handled: true}, nil
}

func parseGithubReviewFollowupArgs(args []string) (parsedGithubTarget, bool, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return parsedGithubTarget{}, false, fmt.Errorf("Usage: nana review followup <github-pr-url> [--allow-open]\n\n%s", GithubReviewHelp)
	}
	target, err := parseGithubTargetURL(args[0])
	if err != nil {
		return parsedGithubTarget{}, false, err
	}
	if target.kind != "pr" {
		return parsedGithubTarget{}, false, fmt.Errorf("nana review followup expects a pull request URL.\n%s", GithubReviewHelp)
	}
	allowOpen := false
	for _, token := range args[1:] {
		if token == "--allow-open" {
			allowOpen = true
			continue
		}
		return parsedGithubTarget{}, false, fmt.Errorf("Unknown review followup option: %s\n%s", token, GithubReviewHelp)
	}
	return target, allowOpen, nil
}

func githubReviewFollowup(target parsedGithubTarget, allowOpen bool) error {
	apiBaseURL := strings.TrimSpace(os.Getenv("GITHUB_API_URL"))
	if apiBaseURL == "" {
		apiBaseURL = "https://api.github.com"
	}
	token, err := resolveGithubToken()
	if err != nil {
		return err
	}
	var pull githubPullStatePayload
	if err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls/%d", target.repoSlug, target.number), &pull); err != nil {
		return err
	}
	if !allowOpen && !strings.EqualFold(strings.TrimSpace(pull.State), "closed") {
		return fmt.Errorf("PR #%d is still open. Re-run with --allow-open to inspect pre-existing findings before closure.", target.number)
	}
	findings, err := loadPersistedPullReviewPreexistingFindings(target.repoSlug, target.number)
	if err != nil {
		return err
	}
	targetURL := githubCanonicalTargetURL(target)
	if len(findings) == 0 {
		fmt.Fprintf(os.Stdout, "[review] No persisted pre-existing findings for %s.\n", targetURL)
		return nil
	}
	fmt.Fprintf(os.Stdout, "[review] Pre-existing findings for %s:\n", targetURL)
	for _, finding := range findings {
		fmt.Fprintf(os.Stdout, "- %s (%s)\n", finding.Title, renderGithubFindingReference(finding))
		fmt.Fprintf(os.Stdout, "  %s\n", defaultString(strings.TrimSpace(finding.UserExplanation), strings.TrimSpace(finding.Detail)))
		if link := renderGithubFindingLink(finding); link != "" {
			fmt.Fprintf(os.Stdout, "  %s\n", link)
		}
	}
	return nil
}

func loadPersistedPullReviewPreexistingFindings(repoSlug string, prNumber int) ([]githubPullReviewFinding, error) {
	runsDir := filepath.Join(githubRepoRoot(repoSlug), "reviews", fmt.Sprintf("pr-%d", prNumber), "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	runNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			runNames = append(runNames, entry.Name())
		}
	}
	slices.Sort(runNames)
	findings := []githubPullReviewFinding{}
	seen := map[string]bool{}
	for _, runName := range runNames {
		path := filepath.Join(runsDir, runName, "dropped-preexisting.json")
		var batch []githubPullReviewFinding
		if err := readGithubJSON(path, &batch); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, finding := range batch {
			key := strings.TrimSpace(finding.Fingerprint)
			if key == "" {
				key = fmt.Sprintf("%s|%s|%d", strings.TrimSpace(finding.Title), strings.TrimSpace(finding.Path), finding.Line)
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			findings = append(findings, finding)
		}
	}
	return findings, nil
}

func renderGithubFindingReference(finding githubPullReviewFinding) string {
	if finding.Line > 0 {
		return fmt.Sprintf("%s:%d", finding.Path, finding.Line)
	}
	return finding.Path
}

func renderGithubFindingLink(finding githubPullReviewFinding) string {
	if finding.ChangedLineInPR && strings.TrimSpace(finding.PRPermalink) != "" {
		return finding.PRPermalink
	}
	return strings.TrimSpace(finding.MainPermalink)
}

func ResolveGithubRunIDForTargetURL(targetURL string) (string, error) {
	target, err := parseGithubTargetURL(targetURL)
	if err != nil {
		return "", err
	}
	manifest, err := findLatestRunManifestForTargetURL(target)
	if err != nil {
		return "", err
	}
	if manifest == nil && target.kind == "pr" {
		manifest, err = findLatestRunManifestForPRSandboxLink(target)
		if err != nil {
			return "", err
		}
	}
	if manifest == nil {
		return "", nil
	}
	return strings.TrimSpace(manifest.RunID), nil
}

func findLatestRunManifestForTargetURL(target parsedGithubTarget) (*githubWorkonManifest, error) {
	reposRoot := filepath.Join(githubNanaHome(), "repos")
	entries, err := os.ReadDir(reposRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	normalizedTargetURL := githubCanonicalTargetURL(target)
	var latest *githubWorkonManifest
	for _, ownerEntry := range entries {
		if !ownerEntry.IsDir() {
			continue
		}
		repoEntries, err := os.ReadDir(filepath.Join(reposRoot, ownerEntry.Name()))
		if err != nil {
			return nil, err
		}
		for _, repoEntry := range repoEntries {
			if !repoEntry.IsDir() {
				continue
			}
			runsDir := filepath.Join(reposRoot, ownerEntry.Name(), repoEntry.Name(), "runs")
			runEntries, err := os.ReadDir(runsDir)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
			for _, runEntry := range runEntries {
				if !runEntry.IsDir() {
					continue
				}
				manifest, err := readGithubWorkonManifest(filepath.Join(runsDir, runEntry.Name(), "manifest.json"))
				if err != nil {
					continue
				}
				exactTargetMatch := strings.TrimSpace(manifest.TargetURL) == normalizedTargetURL
				linkedPRMatch := target.kind == "pr" &&
					strings.EqualFold(strings.TrimSpace(manifest.RepoSlug), strings.TrimSpace(target.repoSlug)) &&
					manifest.PublishedPRNumber == target.number
				if !exactTargetMatch && !linkedPRMatch {
					continue
				}
				if latest == nil || strings.TrimSpace(manifest.UpdatedAt) > strings.TrimSpace(latest.UpdatedAt) {
					copied := manifest
					latest = &copied
				}
			}
		}
	}
	return latest, nil
}

func findLatestRunManifestForPRSandboxLink(target parsedGithubTarget) (*githubWorkonManifest, error) {
	prSandboxPath := githubSandboxPath(target.repoSlug, buildGithubTargetSandboxID("pr", target.number))
	info, err := os.Lstat(prSandboxPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil, nil
	}
	resolvedSandboxPath, err := filepath.EvalSymlinks(prSandboxPath)
	if err != nil {
		return nil, err
	}
	metadata, err := readGithubSandboxMetadata(resolvedSandboxPath)
	if err != nil || strings.TrimSpace(metadata.SandboxID) == "" {
		return nil, err
	}
	return findLatestRunManifestForSandbox(target.repoSlug, metadata.SandboxID)
}

func findLatestRunManifestForSandbox(repoSlug string, sandboxID string) (*githubWorkonManifest, error) {
	runsDir := filepath.Join(githubRepoRoot(repoSlug), "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var latest *githubWorkonManifest
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, err := readGithubWorkonManifest(filepath.Join(runsDir, entry.Name(), "manifest.json"))
		if err != nil || strings.TrimSpace(manifest.SandboxID) != sandboxID {
			continue
		}
		if latest == nil || strings.TrimSpace(manifest.UpdatedAt) > strings.TrimSpace(latest.UpdatedAt) {
			copied := manifest
			latest = &copied
		}
	}
	return latest, nil
}

func readGithubWorkonManifest(path string) (githubWorkonManifest, error) {
	var manifest githubWorkonManifest
	if err := readGithubJSON(path, &manifest); err != nil {
		return githubWorkonManifest{}, err
	}
	return manifest, nil
}
