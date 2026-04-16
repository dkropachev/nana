package gocli

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var (
	githubAPIHTTPClient                 = &http.Client{Timeout: 10 * time.Second}
	githubReviewRuleFencedCodePattern   = regexp.MustCompile("```[\\s\\S]*?```")
	githubReviewRuleInlineCodePattern   = regexp.MustCompile("`[^`]*`")
	githubReviewRuleMarkdownLinkPattern = regexp.MustCompile(`\[[^\]]+\]\([^)]+\)`)
)

type githubReviewerPolicy struct {
	TrustedReviewers   []string `json:"trusted_reviewers,omitempty"`
	BlockedReviewers   []string `json:"blocked_reviewers,omitempty"`
	MinDistinctReviews int      `json:"min_distinct_reviewers,omitempty"`
}

type githubGlobalReviewRulesConfig struct {
	Version        int                   `json:"version"`
	DefaultMode    string                `json:"default_mode"`
	ReviewerPolicy *githubReviewerPolicy `json:"reviewer_policy,omitempty"`
	UpdatedAt      string                `json:"updated_at"`
}

type githubRepoSettings struct {
	Version                   int                   `json:"version"`
	DefaultConsiderations     []string              `json:"default_considerations,omitempty"`
	DefaultRoleLayout         string                `json:"default_role_layout,omitempty"`
	ReviewRulesMode           string                `json:"review_rules_mode,omitempty"`
	ReviewRulesReviewerPolicy *githubReviewerPolicy `json:"review_rules_reviewer_policy,omitempty"`
	RepoMode                  string                `json:"repo_mode,omitempty"`
	IssuePickMode             string                `json:"issue_pick_mode,omitempty"`
	PRForwardMode             string                `json:"pr_forward_mode,omitempty"`
	ForkIssuesMode            string                `json:"fork_issues_mode,omitempty"`
	ImplementMode             string                `json:"implement_mode,omitempty"`
	PublishTarget             string                `json:"publish_target,omitempty"`
	HotPathAPIProfile         *githubHotPathProfile `json:"hot_path_api_profile,omitempty"`
	UpdatedAt                 string                `json:"updated_at"`
}

type githubHotPathProfile struct {
	Version             int      `json:"version"`
	AnalyzedAt          string   `json:"analyzed_at"`
	APISurfaceFiles     []string `json:"api_surface_files,omitempty"`
	HotPathAPIFiles     []string `json:"hot_path_api_files,omitempty"`
	APIIdentifierTokens []string `json:"api_identifier_tokens,omitempty"`
	Evidence            []string `json:"evidence,omitempty"`
}

type githubIssueTokenStats struct {
	Version     int                               `json:"version"`
	RepoSlug    string                            `json:"repo_slug"`
	IssueNumber int                               `json:"issue_number"`
	UpdatedAt   string                            `json:"updated_at"`
	Totals      githubIssueTokenTotals            `json:"totals"`
	Sandboxes   map[string]githubIssueTokenRollup `json:"sandboxes"`
}

type githubIssueTokenTotals struct {
	InputTokens       int `json:"input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	TotalTokens       int `json:"total_tokens"`
	SessionsAccounted int `json:"sessions_accounted"`
}

type githubIssueTokenRollup struct {
	InputTokens       int    `json:"input_tokens"`
	OutputTokens      int    `json:"output_tokens"`
	TotalTokens       int    `json:"total_tokens"`
	SessionsAccounted int    `json:"sessions_accounted"`
	LastFingerprint   string `json:"last_accounted_fingerprint,omitempty"`
	LastAccountedAt   string `json:"last_accounted_at,omitempty"`
}

type githubRetrospectiveManifest struct {
	RunID           string   `json:"run_id"`
	RepoSlug        string   `json:"repo_slug"`
	RepoOwner       string   `json:"repo_owner"`
	RepoName        string   `json:"repo_name"`
	SandboxPath     string   `json:"sandbox_path"`
	SandboxRepoPath string   `json:"sandbox_repo_path"`
	RoleLayout      string   `json:"role_layout"`
	Considerations  []string `json:"considerations_active"`
}

type githubLatestRunPointer struct {
	RepoRoot string `json:"repo_root"`
	RunID    string `json:"run_id"`
}

type githubThreadUsageArtifact struct {
	Version     int                    `json:"version"`
	GeneratedAt string                 `json:"generated_at"`
	SandboxPath string                 `json:"sandbox_path"`
	Rows        []githubThreadUsageRow `json:"rows"`
	TotalTokens int                    `json:"total_tokens"`
}

type githubThreadUsageRow struct {
	Nickname   string `json:"nickname"`
	Role       string `json:"role"`
	TokensUsed int    `json:"tokens_used"`
	StartedAt  int64  `json:"started_at"`
	UpdatedAt  int64  `json:"updated_at"`
}

type githubSandboxMetadata struct {
	SandboxID    string `json:"sandbox_id"`
	TargetKind   string `json:"target_kind"`
	TargetNumber int    `json:"target_number"`
}

type githubRunManifestIndex struct {
	RepoSlug          string `json:"repo_slug"`
	TargetKind        string `json:"target_kind"`
	TargetNumber      int    `json:"target_number"`
	PublishedPRNumber int    `json:"published_pr_number"`
}

type githubReviewRuleScanSource struct {
	RepoSlug     string
	SourceTarget string
	PRNumbers    []int
	ScanAllPRs   bool
}

type githubReviewRuleDocument struct {
	ApprovedRules     []githubReviewRule `json:"approved_rules"`
	PendingCandidates []githubReviewRule `json:"pending_candidates"`
	DisabledRules     []githubReviewRule `json:"disabled_rules,omitempty"`
	ArchivedRules     []githubReviewRule `json:"archived_rules,omitempty"`
	UpdatedAt         string             `json:"updated_at,omitempty"`
}

type githubReviewRule struct {
	ID               string                     `json:"id"`
	Title            string                     `json:"title"`
	Category         string                     `json:"category"`
	Confidence       float64                    `json:"confidence"`
	ReviewerCount    int                        `json:"reviewer_count"`
	ExtractionOrigin string                     `json:"extraction_origin,omitempty"`
	ExtractionReason string                     `json:"extraction_reason,omitempty"`
	PathScopes       []string                   `json:"path_scopes,omitempty"`
	Rule             string                     `json:"rule,omitempty"`
	Evidence         []githubReviewRuleEvidence `json:"evidence,omitempty"`
	UpdatedAt        string                     `json:"updated_at,omitempty"`
}

type githubReviewRuleEvidence struct {
	Kind                  string `json:"kind,omitempty"`
	PRNumber              int    `json:"pr_number,omitempty"`
	Reviewer              string `json:"reviewer,omitempty"`
	Path                  string `json:"path,omitempty"`
	Line                  int    `json:"line,omitempty"`
	Excerpt               string `json:"excerpt,omitempty"`
	CodeContextExcerpt    string `json:"code_context_excerpt,omitempty"`
	CodeContextProvenance string `json:"code_context_provenance,omitempty"`
	CodeContextRef        string `json:"code_context_ref,omitempty"`
}

type githubReviewRulesLastScan struct {
	At                    string `json:"at,omitempty"`
	Source                string `json:"source,omitempty"`
	SourceTarget          string `json:"source_target,omitempty"`
	ScannedPRs            int    `json:"scanned_prs,omitempty"`
	ScannedReviews        int    `json:"scanned_reviews,omitempty"`
	ScannedReviewComments int    `json:"scanned_review_comments,omitempty"`
}

type githubPullRequestPayload struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Head    struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}

type githubPullReviewPayload struct {
	ID      int    `json:"id"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
	State   string `json:"state"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
}

type githubPullReviewCommentPayload struct {
	ID                  int    `json:"id"`
	HTMLURL             string `json:"html_url"`
	Body                string `json:"body"`
	Path                string `json:"path"`
	Line                int    `json:"line"`
	OriginalLine        int    `json:"original_line"`
	DiffHunk            string `json:"diff_hunk"`
	PullRequestReviewID int    `json:"pull_request_review_id"`
	User                struct {
		Login string `json:"login"`
	} `json:"user"`
}

type githubContentPayload struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

type githubReviewRuleTemplate struct {
	Category string
	Title    string
	Rule     string
	Patterns []*regexp.Regexp
}

type githubReviewSignal struct {
	template githubReviewRuleTemplate
	evidence githubReviewRuleEvidence
}

var supportedGithubConsiderations = []string{"arch", "perf", "api", "security", "dependency", "style", "qa"}
var supportedGithubRoleLayouts = []string{"split", "reviewer+executor"}
var supportedReviewRulesModes = []string{"manual", "automatic"}
var supportedGithubAutomationModes = []string{"manual", "auto", "labeled"}
var supportedGithubPublishTargets = []string{"local-branch", "fork", "repo"}
var supportedGithubRepoModes = []string{"disabled", "local", "fork", "repo"}
var supportedGithubIssuePickModes = []string{"manual", "label", "auto"}
var supportedGithubPRForwardModes = []string{"approve", "auto"}

type githubLane struct {
	alias    string
	role     string
	mode     string
	owner    string
	blocking bool
	purpose  string
}

func GithubWork(cwd string, args []string) error {
	if len(args) == 0 || isHelpToken(args[0]) {
		fmt.Fprint(os.Stdout, GithubWorkHelp)
		return nil
	}
	if args[0] != "defaults" || len(args) < 2 {
		if args[0] == "stats" {
			return githubWorkStats(args[1:])
		}
		if args[0] == "retrospective" {
			return githubWorkRetrospective(args[1:])
		}
		if args[0] == "explain" {
			return githubWorkExplain(args[1:])
		}
		return fmt.Errorf("Unknown work subcommand: %s\n\n%s", args[0], GithubWorkHelp)
	}

	switch args[1] {
	case "set":
		return githubDefaultsSet(args[2:])
	case "show":
		return githubDefaultsShow(args[2:])
	default:
		return fmt.Errorf("Unknown work defaults subcommand: %s\n\n%s", args[1], GithubWorkHelp)
	}
}

func GithubReviewRules(cwd string, args []string) error {
	if len(args) == 0 || isHelpToken(args[0]) {
		fmt.Fprint(os.Stdout, ReviewRulesHelp)
		return nil
	}
	if args[0] != "config" || len(args) < 2 {
		return githubReviewRulesLifecycle(args)
	}

	switch args[1] {
	case "set":
		return githubReviewRulesConfigSet(args[2:])
	case "show":
		return githubReviewRulesConfigShow(args[2:])
	default:
		return fmt.Errorf("Unknown review-rules config subcommand: %s\n\n%s", args[1], ReviewRulesHelp)
	}
}

func githubDefaultsSet(args []string) error {
	if len(args) == 0 || !validRepoSlug(args[0]) {
		return fmt.Errorf("Usage: nana work defaults set <owner/repo> [--considerations <list>] [--role-layout <split|reviewer+executor>] [--review-rules-mode <manual|automatic>] [--repo-mode <disabled|local|fork|repo>] [--issue-pick <manual|label|auto>] [--pr-forward <approve|auto>] [--fork-issues <manual|auto|label>] [--implement <manual|auto|label>] [--publish <local|fork|repo>] [--review-rules-trusted-reviewers <a,b>] [--review-rules-blocked-reviewers <a,b>] [--review-rules-min-distinct-reviewers <n>]\n\n%s", GithubWorkHelp)
	}

	repoSlug := args[0]
	settingsPath := githubRepoSettingsPath(repoSlug)
	existing, _ := readGithubRepoSettings(settingsPath)
	if existing == nil {
		existing = &githubRepoSettings{}
	}

	considerations := append([]string{}, existing.DefaultConsiderations...)
	roleLayout := existing.DefaultRoleLayout
	reviewRulesMode := existing.ReviewRulesMode
	forkIssuesMode := existing.ForkIssuesMode
	implementMode := existing.ImplementMode
	publishTarget := existing.PublishTarget
	repoWorkMode := defaultString(normalizeGithubRepoMode(existing.RepoMode), publishTargetToRepoMode(existing.PublishTarget))
	issuePickMode := defaultString(normalizeGithubIssuePickMode(existing.IssuePickMode), automationModeToIssuePickMode(existing.ImplementMode))
	if issuePickMode == "" {
		issuePickMode = automationModeToIssuePickMode(existing.ForkIssuesMode)
	}
	prForwardMode := existing.PRForwardMode
	policy := &githubReviewerPolicy{}
	if existing.ReviewRulesReviewerPolicy != nil {
		policy = normalizeGithubReviewerPolicy(existing.ReviewRulesReviewerPolicy)
		if policy == nil {
			policy = &githubReviewerPolicy{}
		}
	}

	for index := 1; index < len(args); index++ {
		token := args[index]
		switch {
		case token == "--considerations":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return err
			}
			parsed, err := parseGithubConsiderations(value, token)
			if err != nil {
				return err
			}
			considerations = parsed
			index++
		case strings.HasPrefix(token, "--considerations="):
			parsed, err := parseGithubConsiderations(strings.TrimPrefix(token, "--considerations="), "--considerations")
			if err != nil {
				return err
			}
			considerations = parsed
		case token == "--role-layout":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return err
			}
			parsed, err := parseGithubRoleLayout(value, token)
			if err != nil {
				return err
			}
			roleLayout = parsed
			index++
		case strings.HasPrefix(token, "--role-layout="):
			parsed, err := parseGithubRoleLayout(strings.TrimPrefix(token, "--role-layout="), "--role-layout")
			if err != nil {
				return err
			}
			roleLayout = parsed
		case token == "--review-rules-mode":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return err
			}
			parsed, err := parseGithubReviewRulesMode(value, token)
			if err != nil {
				return err
			}
			reviewRulesMode = parsed
			index++
		case strings.HasPrefix(token, "--review-rules-mode="):
			parsed, err := parseGithubReviewRulesMode(strings.TrimPrefix(token, "--review-rules-mode="), "--review-rules-mode")
			if err != nil {
				return err
			}
			reviewRulesMode = parsed
		case token == "--repo-mode":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return err
			}
			parsed, err := parseGithubRepoMode(value, token)
			if err != nil {
				return err
			}
			repoWorkMode = parsed
			publishTarget = repoModeToPublishTarget(parsed)
			index++
		case strings.HasPrefix(token, "--repo-mode="):
			parsed, err := parseGithubRepoMode(strings.TrimPrefix(token, "--repo-mode="), "--repo-mode")
			if err != nil {
				return err
			}
			repoWorkMode = parsed
			publishTarget = repoModeToPublishTarget(parsed)
		case token == "--issue-pick":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return err
			}
			parsed, err := parseGithubIssuePickMode(value, token)
			if err != nil {
				return err
			}
			issuePickMode = parsed
			forkIssuesMode = issuePickModeToAutomationMode(parsed)
			implementMode = issuePickModeToAutomationMode(parsed)
			index++
		case strings.HasPrefix(token, "--issue-pick="):
			parsed, err := parseGithubIssuePickMode(strings.TrimPrefix(token, "--issue-pick="), "--issue-pick")
			if err != nil {
				return err
			}
			issuePickMode = parsed
			forkIssuesMode = issuePickModeToAutomationMode(parsed)
			implementMode = issuePickModeToAutomationMode(parsed)
		case token == "--pr-forward":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return err
			}
			parsed, err := parseGithubPRForwardMode(value, token)
			if err != nil {
				return err
			}
			prForwardMode = parsed
			index++
		case strings.HasPrefix(token, "--pr-forward="):
			parsed, err := parseGithubPRForwardMode(strings.TrimPrefix(token, "--pr-forward="), "--pr-forward")
			if err != nil {
				return err
			}
			prForwardMode = parsed
		case token == "--fork-issues":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return err
			}
			parsed, err := parseGithubAutomationMode(value, token)
			if err != nil {
				return err
			}
			forkIssuesMode = parsed
			issuePickMode = automationModeToIssuePickMode(parsed)
			index++
		case strings.HasPrefix(token, "--fork-issues="):
			parsed, err := parseGithubAutomationMode(strings.TrimPrefix(token, "--fork-issues="), "--fork-issues")
			if err != nil {
				return err
			}
			forkIssuesMode = parsed
			issuePickMode = automationModeToIssuePickMode(parsed)
		case token == "--implement":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return err
			}
			parsed, err := parseGithubAutomationMode(value, token)
			if err != nil {
				return err
			}
			implementMode = parsed
			issuePickMode = automationModeToIssuePickMode(parsed)
			index++
		case strings.HasPrefix(token, "--implement="):
			parsed, err := parseGithubAutomationMode(strings.TrimPrefix(token, "--implement="), "--implement")
			if err != nil {
				return err
			}
			implementMode = parsed
			issuePickMode = automationModeToIssuePickMode(parsed)
		case token == "--publish":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return err
			}
			parsed, err := parseGithubPublishTarget(value, token)
			if err != nil {
				return err
			}
			publishTarget = parsed
			repoWorkMode = publishTargetToRepoMode(parsed)
			index++
		case strings.HasPrefix(token, "--publish="):
			parsed, err := parseGithubPublishTarget(strings.TrimPrefix(token, "--publish="), "--publish")
			if err != nil {
				return err
			}
			publishTarget = parsed
			repoWorkMode = publishTargetToRepoMode(parsed)
		case token == "--review-rules-trusted-reviewers":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return err
			}
			policy.TrustedReviewers, err = parseGithubLoginList(value, token)
			if err != nil {
				return err
			}
			index++
		case strings.HasPrefix(token, "--review-rules-trusted-reviewers="):
			parsed, err := parseGithubLoginList(strings.TrimPrefix(token, "--review-rules-trusted-reviewers="), "--review-rules-trusted-reviewers")
			if err != nil {
				return err
			}
			policy.TrustedReviewers = parsed
		case token == "--review-rules-blocked-reviewers":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return err
			}
			policy.BlockedReviewers, err = parseGithubLoginList(value, token)
			if err != nil {
				return err
			}
			index++
		case strings.HasPrefix(token, "--review-rules-blocked-reviewers="):
			parsed, err := parseGithubLoginList(strings.TrimPrefix(token, "--review-rules-blocked-reviewers="), "--review-rules-blocked-reviewers")
			if err != nil {
				return err
			}
			policy.BlockedReviewers = parsed
		case token == "--review-rules-min-distinct-reviewers":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return err
			}
			parsed, err := parseGithubPositiveInt(value, token)
			if err != nil {
				return err
			}
			policy.MinDistinctReviews = parsed
			index++
		case strings.HasPrefix(token, "--review-rules-min-distinct-reviewers="):
			parsed, err := parseGithubPositiveInt(strings.TrimPrefix(token, "--review-rules-min-distinct-reviewers="), "--review-rules-min-distinct-reviewers")
			if err != nil {
				return err
			}
			policy.MinDistinctReviews = parsed
		}
	}

	if roleLayout == "" {
		roleLayout = "split"
	}
	repoWorkMode = defaultString(normalizeGithubRepoMode(repoWorkMode), "local")
	if repoWorkMode == "disabled" {
		publishTarget = ""
	} else {
		publishTarget = defaultString(repoModeToPublishTarget(repoWorkMode), normalizeGithubPublishTarget(publishTarget))
	}
	if publishTarget == "" && repoWorkMode != "disabled" {
		publishTarget = "local-branch"
	}
	issuePickMode = defaultString(normalizeGithubIssuePickMode(issuePickMode), "manual")
	prForwardMode = defaultString(normalizeGithubPRForwardMode(prForwardMode), "approve")
	settings := githubRepoSettings{
		Version:                   6,
		DefaultConsiderations:     considerations,
		DefaultRoleLayout:         roleLayout,
		ReviewRulesMode:           reviewRulesMode,
		ReviewRulesReviewerPolicy: normalizeGithubReviewerPolicy(policy),
		RepoMode:                  repoWorkMode,
		IssuePickMode:             issuePickMode,
		PRForwardMode:             prForwardMode,
		ForkIssuesMode:            normalizeGithubAutomationMode(forkIssuesMode),
		ImplementMode:             normalizeGithubAutomationMode(implementMode),
		PublishTarget:             normalizeGithubPublishTarget(publishTarget),
		UpdatedAt:                 time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeGithubJSON(settingsPath, settings); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "[github] Saved default considerations for %s: %s\n", repoSlug, joinOrNone(settings.DefaultConsiderations))
	fmt.Fprintf(os.Stdout, "[github] Saved role layout for %s: %s\n", repoSlug, defaultString(settings.DefaultRoleLayout, "split"))
	fmt.Fprintf(os.Stdout, "[github] Saved review-rules mode for %s: %s\n", repoSlug, defaultString(settings.ReviewRulesMode, "manual"))
	fmt.Fprintf(os.Stdout, "[github] Saved review-rules trusted reviewers for %s: %s\n", repoSlug, joinOrNone(settings.ReviewRulesReviewerPolicy.GetTrusted()))
	fmt.Fprintf(os.Stdout, "[github] Saved review-rules blocked reviewers for %s: %s\n", repoSlug, joinOrNone(settings.ReviewRulesReviewerPolicy.GetBlocked()))
	fmt.Fprintf(os.Stdout, "[github] Saved review-rules min distinct reviewers for %s: %s\n", repoSlug, intOrNone(settings.ReviewRulesReviewerPolicy.GetMinDistinct()))
	fmt.Fprintf(os.Stdout, "[github] Saved repo mode for %s: %s\n", repoSlug, defaultString(settings.RepoMode, "local"))
	fmt.Fprintf(os.Stdout, "[github] Saved issue-pick mode for %s: %s\n", repoSlug, defaultString(settings.IssuePickMode, "manual"))
	fmt.Fprintf(os.Stdout, "[github] Saved PR forward mode for %s: %s\n", repoSlug, defaultString(settings.PRForwardMode, "approve"))
	fmt.Fprintf(os.Stdout, "[github] Saved fork-issues mode for %s: %s\n", repoSlug, defaultString(settings.ForkIssuesMode, "manual"))
	fmt.Fprintf(os.Stdout, "[github] Saved implement mode for %s: %s\n", repoSlug, defaultString(settings.ImplementMode, "manual"))
	fmt.Fprintf(os.Stdout, "[github] Saved publish target for %s: %s\n", repoSlug, defaultString(settings.PublishTarget, "(none)"))
	fmt.Fprintf(os.Stdout, "[github] Settings path: %s\n", settingsPath)
	return nil
}

func githubDefaultsShow(args []string) error {
	if len(args) == 0 || !validRepoSlug(args[0]) {
		return fmt.Errorf("Usage: nana work defaults show <owner/repo>\n\n%s", GithubWorkHelp)
	}
	repoSlug := args[0]
	settingsPath := githubRepoSettingsPath(repoSlug)
	settings, _ := readGithubRepoSettings(settingsPath)
	globalConfig, _ := readGithubReviewRulesGlobalConfig()

	defaults := []string{}
	roleLayout := "split"
	reviewRulesRepoMode := ""
	repoPolicy := (*githubReviewerPolicy)(nil)
	repoWorkMode := "local"
	issuePickMode := "manual"
	prForwardMode := "approve"
	forkIssuesMode := "manual"
	implementMode := "manual"
	publishTarget := "local-branch"
	if settings != nil {
		defaults = settings.DefaultConsiderations
		if settings.DefaultRoleLayout != "" {
			roleLayout = settings.DefaultRoleLayout
		}
		reviewRulesRepoMode = settings.ReviewRulesMode
		repoPolicy = settings.ReviewRulesReviewerPolicy
		repoWorkMode = resolvedGithubRepoMode(settings)
		issuePickMode = resolvedGithubIssuePickMode(settings)
		prForwardMode = resolvedGithubPRForwardMode(settings)
		forkIssuesMode = defaultString(normalizeGithubAutomationMode(settings.ForkIssuesMode), "manual")
		implementMode = defaultString(normalizeGithubAutomationMode(settings.ImplementMode), "manual")
		if repoWorkMode == "disabled" {
			publishTarget = ""
		} else {
			publishTarget = defaultString(normalizeGithubPublishTarget(settings.PublishTarget), repoModeToPublishTarget(repoWorkMode))
		}
	}
	effectiveMode := reviewRulesRepoMode
	if effectiveMode == "" {
		if globalConfig != nil && globalConfig.DefaultMode != "" {
			effectiveMode = globalConfig.DefaultMode
		} else {
			effectiveMode = "manual"
		}
	}
	effectivePolicy := normalizeGithubReviewerPolicy(repoPolicy)
	if effectivePolicy == nil && globalConfig != nil {
		effectivePolicy = normalizeGithubReviewerPolicy(globalConfig.ReviewerPolicy)
	}

	fmt.Fprintf(os.Stdout, "[github] Default considerations for %s: %s\n", repoSlug, joinOrNone(defaults))
	fmt.Fprintf(os.Stdout, "[github] Default role layout for %s: %s\n", repoSlug, roleLayout)
	fmt.Fprintf(os.Stdout, "[github] Repo review-rules mode for %s: %s\n", repoSlug, defaultString(reviewRulesRepoMode, "(none)"))
	fmt.Fprintf(os.Stdout, "[github] Effective review-rules mode for %s: %s\n", repoSlug, effectiveMode)
	fmt.Fprintf(os.Stdout, "[github] Repo reviewer policy for %s: %s\n", repoSlug, formatGithubReviewerPolicy(repoPolicy))
	fmt.Fprintf(os.Stdout, "[github] Effective reviewer policy for %s: %s\n", repoSlug, formatGithubReviewerPolicy(effectivePolicy))
	fmt.Fprintf(os.Stdout, "[github] Repo mode for %s: %s\n", repoSlug, repoWorkMode)
	fmt.Fprintf(os.Stdout, "[github] Issue-pick mode for %s: %s\n", repoSlug, issuePickMode)
	fmt.Fprintf(os.Stdout, "[github] PR forward mode for %s: %s\n", repoSlug, prForwardMode)
	fmt.Fprintf(os.Stdout, "[github] Fork issues mode for %s: %s\n", repoSlug, forkIssuesMode)
	fmt.Fprintf(os.Stdout, "[github] Implement mode for %s: %s\n", repoSlug, implementMode)
	fmt.Fprintf(os.Stdout, "[github] Publish target for %s: %s\n", repoSlug, defaultString(publishTarget, "(none)"))
	fmt.Fprintln(os.Stdout, "[github] Resolved default pipeline:")
	for _, line := range buildGithubConsiderationInstructionLines(defaults, roleLayout) {
		fmt.Fprintf(os.Stdout, "%s\n", line)
	}
	fmt.Fprintf(os.Stdout, "[github] Settings path: %s\n", settingsPath)
	return nil
}

func githubWorkStats(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("Usage: nana work stats <github-issue-or-pr-url>\n\n%s", GithubWorkHelp)
	}
	target, err := parseGithubTargetURL(args[0])
	if err != nil {
		return err
	}
	issueNumber := target.number
	if target.kind == "pr" {
		associatedIssueNumber, err := resolveGithubIssueAssociationNumber(
			githubSandboxPath(target.repoSlug, buildGithubTargetSandboxID(target.kind, target.number)),
			target.kind,
			target.number,
		)
		if err != nil {
			return err
		}
		if associatedIssueNumber == 0 {
			return fmt.Errorf("PR #%d is not currently linked to an NANA-managed issue sandbox, so no issue token stats are available.", target.number)
		}
		issueNumber = associatedIssueNumber
	}
	statsPath := githubIssueStatsPath(target.repoSlug, issueNumber)
	var stats githubIssueTokenStats
	if err := readGithubJSON(statsPath, &stats); err != nil {
		return err
	}
	for _, line := range buildIssueStatsLines(stats) {
		fmt.Fprintln(os.Stdout, line)
	}
	return nil
}

func githubWorkRetrospective(args []string) error {
	runID := ""
	useLast := true
	for index := 0; index < len(args); index++ {
		token := args[index]
		switch {
		case token == "--run-id":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return err
			}
			runID = strings.TrimSpace(value)
			useLast = false
			index++
		case strings.HasPrefix(token, "--run-id="):
			runID = strings.TrimSpace(strings.TrimPrefix(token, "--run-id="))
			useLast = false
		case token == "--last":
			useLast = true
		}
	}

	manifestPath, repoRoot, err := resolveGithubRunManifestPath(runID, useLast)
	if err != nil {
		return err
	}
	var manifest githubRetrospectiveManifest
	if err := readGithubJSON(manifestPath, &manifest); err != nil {
		return err
	}
	runDir := filepath.Dir(manifestPath)
	artifact, err := writeThreadUsageArtifact(runDir, manifest.SandboxPath)
	if err != nil {
		return err
	}
	markdown := buildGithubRetrospectiveMarkdown(manifest, artifact)
	if err := os.WriteFile(filepath.Join(runDir, "retrospective.md"), []byte(markdown), 0o644); err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(markdown), "\n") {
		fmt.Fprintln(os.Stdout, line)
	}
	_ = repoRoot
	return nil
}

func githubWorkExplain(args []string) error {
	runID := ""
	useLast := true
	jsonOutput := false
	for index := 0; index < len(args); index++ {
		token := args[index]
		switch {
		case token == "--run-id":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return err
			}
			runID = strings.TrimSpace(value)
			useLast = false
			index++
		case strings.HasPrefix(token, "--run-id="):
			runID = strings.TrimSpace(strings.TrimPrefix(token, "--run-id="))
			useLast = false
		case token == "--last":
			useLast = true
		case token == "--json":
			jsonOutput = true
		default:
			return fmt.Errorf("Unknown work explain option: %s\n\n%s", token, GithubWorkHelp)
		}
	}
	manifestPath, _, err := resolveGithubRunManifestPath(runID, useLast)
	if err != nil {
		return err
	}
	manifest, err := readGithubWorkManifest(manifestPath)
	if err != nil {
		return err
	}
	if manifest.RepoProfile == nil && strings.TrimSpace(manifest.RepoProfilePath) != "" {
		profile, err := readGithubRepoProfile(manifest.RepoProfilePath)
		if err == nil {
			manifest.RepoProfile = profile
		}
	}
	payload := buildGithubExplainPayload(manifest)
	if jsonOutput {
		content, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "%s\n", string(content))
		return nil
	}
	lines := []string{
		"# NANA Work Explain",
		"",
		fmt.Sprintf("Run id: %s", manifest.RunID),
		fmt.Sprintf("Repo: %s", manifest.RepoSlug),
		fmt.Sprintf("Target: %s", manifest.TargetURL),
		"",
	}
	if manifest.Policy != nil {
		lines = append(lines,
			fmt.Sprintf("Policy: experimental=%t feedback_source=%s repo_native=%s human_gate=%s", manifest.Policy.Experimental, manifest.Policy.FeedbackSource, manifest.Policy.RepoNativeStrictness, manifest.Policy.HumanGate),
			fmt.Sprintf("Policy sources: experimental=%s feedback_source=%s repo_native=%s human_gate=%s", manifest.Policy.SourceMap["experimental"], manifest.Policy.SourceMap["feedback_source"], manifest.Policy.SourceMap["repo_native_strictness"], manifest.Policy.SourceMap["human_gate"]),
			"",
		)
	}
	if manifest.RepoProfile != nil {
		lines = append(lines,
			fmt.Sprintf("Repo profile fingerprint: %s", manifest.RepoProfile.Fingerprint),
			fmt.Sprintf("Repo profile path: %s", defaultString(manifest.RepoProfilePath, "(none)")),
		)
		if manifest.RepoProfile.CommitStyle != nil {
			lines = append(lines, fmt.Sprintf("Repo commit style: %s (confidence %.2f)", manifest.RepoProfile.CommitStyle.Kind, manifest.RepoProfile.CommitStyle.Confidence))
		}
		if manifest.RepoProfile.PullRequestTemplate != nil {
			lines = append(lines, fmt.Sprintf("Repo PR template: %s", manifest.RepoProfile.PullRequestTemplate.Path))
		}
		if manifest.RepoProfile.ReviewRules != nil {
			lines = append(lines, fmt.Sprintf("Repo review rules: approved=%d pending=%d disabled=%d archived=%d", manifest.RepoProfile.ReviewRules.ApprovedCount, manifest.RepoProfile.ReviewRules.PendingCount, manifest.RepoProfile.ReviewRules.DisabledCount, manifest.RepoProfile.ReviewRules.ArchivedCount))
		}
		for _, warning := range manifest.RepoProfile.Warnings {
			lines = append(lines, fmt.Sprintf("Repo profile warning: %s", warning))
		}
		lines = append(lines, "")
	}
	lines = append(lines,
		fmt.Sprintf("GitHub control plane: %s", formatGithubActorSet(manifest.ControlPlaneReviewers)),
		fmt.Sprintf("Ignored feedback actors: %s", formatGithubIgnoredActorReasons(manifest.IgnoredFeedbackActors)),
		fmt.Sprintf("Review request state: %s", defaultString(manifest.ReviewRequestState, "not_requested")),
		fmt.Sprintf("Requested reviewers: %s", formatGithubActorSet(manifest.RequestedReviewers)),
		fmt.Sprintf("Review request error: %s", defaultString(manifest.ReviewRequestError, "(none)")),
		fmt.Sprintf("Merge state: %s", defaultString(manifest.MergeState, "not_attempted")),
		fmt.Sprintf("Merge method: %s", defaultString(manifest.MergeMethod, "squash")),
		fmt.Sprintf("Merge error: %s", defaultString(manifest.MergeError, "(none)")),
		fmt.Sprintf("Needs human: %t", manifest.NeedsHuman),
		fmt.Sprintf("Needs human reason: %s", defaultString(manifest.NeedsHumanReason, "(none)")),
		fmt.Sprintf("Next action: %s", defaultString(manifest.NextAction, "continue")),
	)
	for _, line := range lines {
		fmt.Fprintln(os.Stdout, line)
	}
	return nil
}

func githubReviewRulesConfigSet(args []string) error {
	mode := "manual"
	existing, _ := readGithubReviewRulesGlobalConfig()
	policy := &githubReviewerPolicy{}
	if existing != nil {
		if existing.DefaultMode != "" {
			mode = existing.DefaultMode
		}
		if existing.ReviewerPolicy != nil {
			policy = normalizeGithubReviewerPolicy(existing.ReviewerPolicy)
			if policy == nil {
				policy = &githubReviewerPolicy{}
			}
		}
	}

	for index := 0; index < len(args); index++ {
		token := args[index]
		switch {
		case token == "--mode":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return err
			}
			parsed, err := parseGithubReviewRulesMode(value, token)
			if err != nil {
				return err
			}
			mode = parsed
			index++
		case strings.HasPrefix(token, "--mode="):
			parsed, err := parseGithubReviewRulesMode(strings.TrimPrefix(token, "--mode="), "--mode")
			if err != nil {
				return err
			}
			mode = parsed
		case token == "--trusted-reviewers":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return err
			}
			parsed, err := parseGithubLoginList(value, token)
			if err != nil {
				return err
			}
			policy.TrustedReviewers = parsed
			index++
		case strings.HasPrefix(token, "--trusted-reviewers="):
			parsed, err := parseGithubLoginList(strings.TrimPrefix(token, "--trusted-reviewers="), "--trusted-reviewers")
			if err != nil {
				return err
			}
			policy.TrustedReviewers = parsed
		case token == "--blocked-reviewers":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return err
			}
			parsed, err := parseGithubLoginList(value, token)
			if err != nil {
				return err
			}
			policy.BlockedReviewers = parsed
			index++
		case strings.HasPrefix(token, "--blocked-reviewers="):
			parsed, err := parseGithubLoginList(strings.TrimPrefix(token, "--blocked-reviewers="), "--blocked-reviewers")
			if err != nil {
				return err
			}
			policy.BlockedReviewers = parsed
		case token == "--min-distinct-reviewers":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return err
			}
			parsed, err := parseGithubPositiveInt(value, token)
			if err != nil {
				return err
			}
			policy.MinDistinctReviews = parsed
			index++
		case strings.HasPrefix(token, "--min-distinct-reviewers="):
			parsed, err := parseGithubPositiveInt(strings.TrimPrefix(token, "--min-distinct-reviewers="), "--min-distinct-reviewers")
			if err != nil {
				return err
			}
			policy.MinDistinctReviews = parsed
		}
	}

	config := githubGlobalReviewRulesConfig{
		Version:        1,
		DefaultMode:    mode,
		ReviewerPolicy: normalizeGithubReviewerPolicy(policy),
		UpdatedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	configPath := githubReviewRulesGlobalConfigPath()
	if err := writeGithubJSON(configPath, config); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "[github] Saved global review-rules mode: %s\n", config.DefaultMode)
	fmt.Fprintf(os.Stdout, "[github] Saved global reviewer policy: %s\n", formatGithubReviewerPolicy(config.ReviewerPolicy))
	fmt.Fprintf(os.Stdout, "[github] Config path: %s\n", configPath)
	return nil
}

func githubReviewRulesConfigShow(args []string) error {
	globalConfig, _ := readGithubReviewRulesGlobalConfig()
	mode := "manual"
	policy := (*githubReviewerPolicy)(nil)
	if globalConfig != nil {
		if globalConfig.DefaultMode != "" {
			mode = globalConfig.DefaultMode
		}
		policy = globalConfig.ReviewerPolicy
	}
	configPath := githubReviewRulesGlobalConfigPath()
	fmt.Fprintf(os.Stdout, "[github] Global review-rules mode: %s\n", mode)
	fmt.Fprintf(os.Stdout, "[github] Global reviewer policy: %s\n", formatGithubReviewerPolicy(policy))
	fmt.Fprintf(os.Stdout, "[github] Config path: %s\n", configPath)

	if len(args) == 0 {
		return nil
	}
	repoSlug, err := resolveGithubRepoSlugLocator(args[0])
	if err != nil {
		return fmt.Errorf("GitHub review-rules config show currently supports <owner/repo>, issue URLs, or PR URLs in the Go CLI")
	}
	settings, _ := readGithubRepoSettings(githubRepoSettingsPath(repoSlug))
	reviewRulesRepoMode := ""
	repoPolicy := (*githubReviewerPolicy)(nil)
	repoWorkMode := "local"
	issuePickMode := "manual"
	prForwardMode := "approve"
	forkIssuesMode := "manual"
	implementMode := "manual"
	publishTarget := "local-branch"
	if settings != nil {
		reviewRulesRepoMode = settings.ReviewRulesMode
		repoPolicy = settings.ReviewRulesReviewerPolicy
		repoWorkMode = resolvedGithubRepoMode(settings)
		issuePickMode = resolvedGithubIssuePickMode(settings)
		prForwardMode = resolvedGithubPRForwardMode(settings)
		forkIssuesMode = defaultString(normalizeGithubAutomationMode(settings.ForkIssuesMode), "manual")
		implementMode = defaultString(normalizeGithubAutomationMode(settings.ImplementMode), "manual")
		if repoWorkMode == "disabled" {
			publishTarget = ""
		} else {
			publishTarget = defaultString(normalizeGithubPublishTarget(settings.PublishTarget), repoModeToPublishTarget(repoWorkMode))
		}
	}
	effectiveMode := reviewRulesRepoMode
	if effectiveMode == "" {
		effectiveMode = mode
	}
	effectivePolicy := normalizeGithubReviewerPolicy(repoPolicy)
	if effectivePolicy == nil {
		effectivePolicy = normalizeGithubReviewerPolicy(policy)
	}
	fmt.Fprintf(os.Stdout, "[github] Repo review-rules mode for %s: %s\n", repoSlug, defaultString(reviewRulesRepoMode, "(none)"))
	fmt.Fprintf(os.Stdout, "[github] Effective review-rules mode for %s: %s\n", repoSlug, effectiveMode)
	fmt.Fprintf(os.Stdout, "[github] Repo reviewer policy for %s: %s\n", repoSlug, formatGithubReviewerPolicy(repoPolicy))
	fmt.Fprintf(os.Stdout, "[github] Effective reviewer policy for %s: %s\n", repoSlug, formatGithubReviewerPolicy(effectivePolicy))
	fmt.Fprintf(os.Stdout, "[github] Repo mode for %s: %s\n", repoSlug, repoWorkMode)
	fmt.Fprintf(os.Stdout, "[github] Issue-pick mode for %s: %s\n", repoSlug, issuePickMode)
	fmt.Fprintf(os.Stdout, "[github] PR forward mode for %s: %s\n", repoSlug, prForwardMode)
	fmt.Fprintf(os.Stdout, "[github] Fork issues mode for %s: %s\n", repoSlug, forkIssuesMode)
	fmt.Fprintf(os.Stdout, "[github] Implement mode for %s: %s\n", repoSlug, implementMode)
	fmt.Fprintf(os.Stdout, "[github] Publish target for %s: %s\n", repoSlug, defaultString(publishTarget, "(none)"))
	return nil
}

func githubReviewRulesLifecycle(args []string) error {
	if len(args) == 0 || isHelpToken(args[0]) {
		fmt.Fprint(os.Stdout, ReviewRulesHelp)
		return nil
	}
	subcommand := args[0]
	if subcommand == "scan" {
		if len(args) < 2 {
			return fmt.Errorf("GitHub review-rules scan currently supports <owner/repo>, issue URLs, or PR URLs in the Go CLI")
		}
		return githubReviewRulesScan(args[1])
	}
	if len(args) < 2 {
		return fmt.Errorf("GitHub review-rules local lifecycle commands currently support <owner/repo>, issue URLs, or PR URLs in the Go CLI")
	}
	repoSlug, err := resolveGithubRepoSlugLocator(args[1])
	if err != nil {
		return fmt.Errorf("GitHub review-rules local lifecycle commands currently support <owner/repo>, issue URLs, or PR URLs in the Go CLI")
	}
	rulesPath := githubManagedPaths(repoSlug).ReviewRulesPath
	var document githubReviewRuleDocument
	if err := readGithubJSON(rulesPath, &document); err != nil {
		return fmt.Errorf("[github] No repo review rules file present for %s.\n[github] Run: nana review-rules scan %s", repoSlug, repoSlug)
	}

	switch subcommand {
	case "list":
		fmt.Fprintf(os.Stdout, "[github] Repo review rules for %s\n", repoSlug)
		fmt.Fprintf(os.Stdout, "[github] Rules file: %s\n", rulesPath)
		fmt.Fprintf(os.Stdout, "[github] Approved rules=%d pending candidates=%d disabled=%d archived=%d.\n", len(document.ApprovedRules), len(document.PendingCandidates), len(document.DisabledRules), len(document.ArchivedRules))
		for _, rule := range document.ApprovedRules {
			fmt.Fprintf(os.Stdout, "[github] approved %s\n", formatGithubReviewRuleSummary(rule))
		}
		for _, rule := range document.PendingCandidates {
			fmt.Fprintf(os.Stdout, "[github] pending %s\n", formatGithubReviewRuleSummary(rule))
		}
		for _, rule := range document.DisabledRules {
			fmt.Fprintf(os.Stdout, "[github] disabled %s\n", formatGithubReviewRuleSummary(rule))
		}
		for _, rule := range document.ArchivedRules {
			fmt.Fprintf(os.Stdout, "[github] archived %s\n", formatGithubReviewRuleSummary(rule))
		}
		return nil
	case "approve":
		selectors := cleanSelectors(args[2:])
		if len(selectors) == 0 {
			return fmt.Errorf("Missing rule id(s) to approve")
		}
		approveAll := slices.Contains(selectors, "all")
		selected := map[string]bool{}
		for _, selector := range selectors {
			if selector != "all" {
				selected[selector] = true
			}
		}
		approved := append([]githubReviewRule{}, document.ApprovedRules...)
		remaining := []githubReviewRule{}
		moved := 0
		for _, rule := range document.PendingCandidates {
			if approveAll || selected[rule.ID] {
				rule.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
				approved = append(approved, rule)
				moved++
			} else {
				remaining = append(remaining, rule)
			}
		}
		if moved == 0 {
			return fmt.Errorf("No pending review rules matched %s", strings.Join(selectors, ", "))
		}
		document.ApprovedRules = approved
		document.PendingCandidates = remaining
		document.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := writeGithubJSON(rulesPath, document); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "[github] Approved %d repo review rule(s) for %s.\n", moved, repoSlug)
		fmt.Fprintf(os.Stdout, "[github] Rules file: %s\n", rulesPath)
		return nil
	case "disable", "enable", "archive":
		selectors := cleanSelectors(args[2:])
		if len(selectors) == 0 {
			return fmt.Errorf("Missing rule id(s) for %s", subcommand)
		}
		moved, updated := rewriteGithubRuleLifecycle(document, subcommand, selectors)
		if moved == 0 {
			return fmt.Errorf("No review rules matched %s", strings.Join(selectors, ", "))
		}
		if err := writeGithubJSON(rulesPath, updated); err != nil {
			return err
		}
		action := map[string]string{"disable": "Disabled", "enable": "Enabled", "archive": "Archived"}[subcommand]
		fmt.Fprintf(os.Stdout, "[github] %s %d review rule(s) for %s.\n", action, moved, repoSlug)
		fmt.Fprintf(os.Stdout, "[github] Rules file: %s\n", rulesPath)
		return nil
	case "explain":
		if len(args) < 3 || strings.TrimSpace(args[2]) == "" {
			return fmt.Errorf("Missing rule id for explain")
		}
		state, rule := findGithubReviewRule(document, strings.TrimSpace(args[2]))
		if rule == nil {
			return fmt.Errorf("No review rule found for %s", args[2])
		}
		fmt.Fprintf(os.Stdout, "[github] Rule %s (%s)\n", rule.ID, state)
		fmt.Fprintf(os.Stdout, "[github] Title: %s\n", rule.Title)
		fmt.Fprintf(os.Stdout, "[github] Category: %s\n", rule.Category)
		fmt.Fprintf(os.Stdout, "[github] Confidence: %.2f\n", rule.Confidence)
		fmt.Fprintf(os.Stdout, "[github] Reviewer count: %d\n", rule.ReviewerCount)
		fmt.Fprintf(os.Stdout, "[github] Extraction origin: %s\n", defaultString(rule.ExtractionOrigin, ""))
		fmt.Fprintf(os.Stdout, "[github] Extraction reason: %s\n", defaultString(rule.ExtractionReason, ""))
		fmt.Fprintf(os.Stdout, "[github] Path scopes: %s\n", joinOrNone(rule.PathScopes))
		for _, evidence := range rule.Evidence {
			fmt.Fprintf(os.Stdout, "[github] Evidence: kind=%s pr=#%d reviewer=@%s path=%s line=%s provenance=%s ref=%s\n",
				defaultString(evidence.Kind, ""),
				evidence.PRNumber,
				defaultString(evidence.Reviewer, "unknown"),
				defaultString(evidence.Path, "(none)"),
				intOrNone(evidence.Line),
				defaultString(evidence.CodeContextProvenance, "unknown"),
				defaultString(evidence.CodeContextRef, "(none)"),
			)
			fmt.Fprintf(os.Stdout, "[github]   excerpt: %s\n", evidence.Excerpt)
			if strings.TrimSpace(evidence.CodeContextExcerpt) != "" {
				for _, line := range strings.Split(evidence.CodeContextExcerpt, "\n") {
					fmt.Fprintf(os.Stdout, "[github]   code: %s\n", line)
				}
			}
		}
		return nil
	default:
		return fmt.Errorf("Unknown review-rules subcommand: %s\n\n%s", subcommand, ReviewRulesHelp)
	}
}

func githubNanaHome() string {
	baseHome := strings.TrimSpace(os.Getenv("HOME"))
	if baseHome == "" {
		baseHome, _ = os.UserHomeDir()
	}
	return filepath.Join(baseHome, ".nana")
}

func githubRepoSettingsPath(repoSlug string) string {
	return filepath.Join(githubWorkRepoRoot(repoSlug), "settings.json")
}

func githubReviewRulesGlobalConfigPath() string {
	return githubWorkReviewRulesGlobalConfigPath()
}

func readGithubRepoSettings(path string) (*githubRepoSettings, error) {
	var settings githubRepoSettings
	if err := readGithubJSON(path, &settings); err != nil {
		return nil, err
	}
	return &settings, nil
}

func readGithubReviewRulesGlobalConfig() (*githubGlobalReviewRulesConfig, error) {
	var config githubGlobalReviewRulesConfig
	if err := readGithubJSON(githubReviewRulesGlobalConfigPath(), &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func readGithubJSON(path string, target interface{}) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(content, target)
}

func writeGithubJSON(path string, value interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(content, '\n'), 0o644)
}

func validRepoSlug(value string) bool {
	parts := strings.Split(strings.TrimSpace(value), "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
}

func requireFlagValue(args []string, index int, flag string) (string, error) {
	if index+1 >= len(args) {
		return "", fmt.Errorf("Missing value after %s.\n%s", flag, GithubWorkHelp)
	}
	return args[index+1], nil
}

func parseGithubConsiderations(value string, flag string) ([]string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return nil, fmt.Errorf("Missing value after %s.\n%s", flag, GithubWorkHelp)
	}
	parts := []string{}
	for _, part := range strings.Split(raw, ",") {
		normalized := strings.ToLower(strings.TrimSpace(part))
		if normalized == "" {
			continue
		}
		if !slices.Contains(supportedGithubConsiderations, normalized) {
			return nil, fmt.Errorf("Invalid considerations: %s. Expected one or more of %s.", normalized, strings.Join(supportedGithubConsiderations, ", "))
		}
		parts = append(parts, normalized)
	}
	return uniqueStrings(parts), nil
}

func parseGithubRoleLayout(value string, flag string) (string, error) {
	raw := strings.ToLower(strings.TrimSpace(value))
	if raw == "" {
		return "", fmt.Errorf("Missing value after %s.\n%s", flag, GithubWorkHelp)
	}
	switch raw {
	case "merged", "reviewer-executor", "reviewer_executor":
		return "reviewer+executor", nil
	case "split", "reviewer+executor":
		return raw, nil
	default:
		return "", fmt.Errorf("Invalid %s value: %s. Expected one of %s.\n%s", flag, value, strings.Join(supportedGithubRoleLayouts, ", "), GithubWorkHelp)
	}
}

func parseGithubReviewRulesMode(value string, flag string) (string, error) {
	raw := strings.ToLower(strings.TrimSpace(value))
	if !slices.Contains(supportedReviewRulesModes, raw) {
		return "", fmt.Errorf("Invalid %s value: %s. Expected one of manual, automatic.\n%s", flag, value, GithubWorkHelp)
	}
	return raw, nil
}

func parseGithubAutomationMode(value string, flag string) (string, error) {
	raw := strings.ToLower(strings.TrimSpace(value))
	if raw == "label" {
		raw = "labeled"
	}
	if !slices.Contains(supportedGithubAutomationModes, raw) {
		return "", fmt.Errorf("Invalid %s value: %s. Expected one of %s.", flag, value, strings.Join(supportedGithubAutomationModes, ", "))
	}
	return raw, nil
}

func normalizeGithubAutomationMode(value string) string {
	raw := strings.ToLower(strings.TrimSpace(value))
	if raw == "label" {
		raw = "labeled"
	}
	if slices.Contains(supportedGithubAutomationModes, raw) {
		return raw
	}
	return ""
}

func parseGithubPublishTarget(value string, flag string) (string, error) {
	raw := strings.ToLower(strings.TrimSpace(value))
	if raw == "local" {
		raw = "local-branch"
	}
	if !slices.Contains(supportedGithubPublishTargets, raw) {
		return "", fmt.Errorf("Invalid %s value: %s. Expected one of local, fork, repo.", flag, value)
	}
	return raw, nil
}

func normalizeGithubPublishTarget(value string) string {
	raw := strings.ToLower(strings.TrimSpace(value))
	if raw == "local" {
		raw = "local-branch"
	}
	if slices.Contains(supportedGithubPublishTargets, raw) {
		return raw
	}
	return ""
}

func parseGithubRepoMode(value string, flag string) (string, error) {
	raw := normalizeGithubRepoMode(value)
	if raw == "" {
		return "", fmt.Errorf("Invalid %s value: %s. Expected one of %s.", flag, value, strings.Join(supportedGithubRepoModes, ", "))
	}
	return raw, nil
}

func normalizeGithubRepoMode(value string) string {
	raw := strings.ToLower(strings.TrimSpace(value))
	switch raw {
	case "local-branch":
		return "local"
	case "disabled", "local", "fork", "repo":
		return raw
	default:
		return ""
	}
}

func repoModeToPublishTarget(value string) string {
	switch normalizeGithubRepoMode(value) {
	case "disabled":
		return ""
	case "local":
		return "local-branch"
	case "fork":
		return "fork"
	case "repo":
		return "repo"
	default:
		return ""
	}
}

func publishTargetToRepoMode(value string) string {
	switch normalizeGithubPublishTarget(value) {
	case "local-branch":
		return "local"
	case "fork":
		return "fork"
	case "repo":
		return "repo"
	default:
		return ""
	}
}

func parseGithubIssuePickMode(value string, flag string) (string, error) {
	raw := normalizeGithubIssuePickMode(value)
	if raw == "" {
		return "", fmt.Errorf("Invalid %s value: %s. Expected one of %s.", flag, value, strings.Join(supportedGithubIssuePickModes, ", "))
	}
	return raw, nil
}

func normalizeGithubIssuePickMode(value string) string {
	raw := strings.ToLower(strings.TrimSpace(value))
	switch raw {
	case "labeled":
		return "label"
	case "manual", "label", "auto":
		return raw
	default:
		return ""
	}
}

func issuePickModeToAutomationMode(value string) string {
	switch normalizeGithubIssuePickMode(value) {
	case "label":
		return "labeled"
	case "manual":
		return "manual"
	case "auto":
		return "auto"
	default:
		return ""
	}
}

func automationModeToIssuePickMode(value string) string {
	return normalizeGithubIssuePickMode(normalizeGithubAutomationMode(value))
}

func parseGithubPRForwardMode(value string, flag string) (string, error) {
	raw := normalizeGithubPRForwardMode(value)
	if raw == "" {
		return "", fmt.Errorf("Invalid %s value: %s. Expected one of %s.", flag, value, strings.Join(supportedGithubPRForwardModes, ", "))
	}
	return raw, nil
}

func normalizeGithubPRForwardMode(value string) string {
	raw := strings.ToLower(strings.TrimSpace(value))
	if slices.Contains(supportedGithubPRForwardModes, raw) {
		return raw
	}
	return ""
}

func resolvedGithubRepoMode(settings *githubRepoSettings) string {
	if settings == nil {
		return "local"
	}
	if mode := normalizeGithubRepoMode(settings.RepoMode); mode != "" {
		return mode
	}
	if mode := publishTargetToRepoMode(settings.PublishTarget); mode != "" {
		return mode
	}
	return "local"
}

func resolvedGithubIssuePickMode(settings *githubRepoSettings) string {
	if settings == nil {
		return "manual"
	}
	if mode := normalizeGithubIssuePickMode(settings.IssuePickMode); mode != "" {
		return mode
	}
	if mode := automationModeToIssuePickMode(settings.ImplementMode); mode != "" {
		return mode
	}
	if mode := automationModeToIssuePickMode(settings.ForkIssuesMode); mode != "" {
		return mode
	}
	return "manual"
}

func resolvedGithubPRForwardMode(settings *githubRepoSettings) string {
	if settings == nil {
		return "approve"
	}
	return defaultString(normalizeGithubPRForwardMode(settings.PRForwardMode), "approve")
}

func parseGithubLoginList(value string, flag string) ([]string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return nil, fmt.Errorf("Missing value after %s.\n%s", flag, GithubWorkHelp)
	}
	if strings.EqualFold(raw, "none") || strings.EqualFold(raw, "(none)") {
		return []string{}, nil
	}
	parts := []string{}
	for _, part := range strings.Split(raw, ",") {
		normalized := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(part, "@")))
		if normalized != "" {
			parts = append(parts, normalized)
		}
	}
	return uniqueStrings(parts), nil
}

func parseGithubPositiveInt(value string, flag string) (int, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return 0, fmt.Errorf("Missing value after %s.\n%s", flag, GithubWorkHelp)
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("Invalid %s value: %s. Expected a non-negative integer.\n%s", flag, value, GithubWorkHelp)
	}
	return parsed, nil
}

func normalizeGithubReviewerPolicy(policy *githubReviewerPolicy) *githubReviewerPolicy {
	if policy == nil {
		return nil
	}
	trusted := uniqueStrings(cleanLogins(policy.TrustedReviewers))
	blocked := uniqueStrings(cleanLogins(policy.BlockedReviewers))
	minDistinct := policy.MinDistinctReviews
	if minDistinct < 0 {
		minDistinct = 0
	}
	if len(trusted) == 0 && len(blocked) == 0 && minDistinct == 0 {
		return nil
	}
	return &githubReviewerPolicy{
		TrustedReviewers:   trusted,
		BlockedReviewers:   blocked,
		MinDistinctReviews: minDistinct,
	}
}

func cleanLogins(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(value, "@")))
		if normalized != "" {
			cleaned = append(cleaned, normalized)
		}
	}
	return cleaned
}

func formatGithubReviewerPolicy(policy *githubReviewerPolicy) string {
	policy = normalizeGithubReviewerPolicy(policy)
	if policy == nil {
		return "(none)"
	}
	parts := []string{}
	if len(policy.TrustedReviewers) > 0 {
		parts = append(parts, "trusted reviewers="+strings.Join(policy.TrustedReviewers, ","))
	}
	if len(policy.BlockedReviewers) > 0 {
		parts = append(parts, "blocked reviewers="+strings.Join(policy.BlockedReviewers, ","))
	}
	if policy.MinDistinctReviews > 0 {
		parts = append(parts, fmt.Sprintf("min distinct reviewers=%d", policy.MinDistinctReviews))
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, "; ")
}

func buildGithubConsiderationInstructionLines(activeConsiderations []string, roleLayout string) []string {
	lines := []string{
		defaultsHeader(activeConsiderations),
		fmt.Sprintf("Role layout: %s.", roleLayout),
		"Pipeline is composed from the base coder lane plus the active consideration packs.",
		"Execution is staged:",
		"- Bootstrap loop: use only the coder lane plus the architect overview lane to land the basic feature and get minimal verification working.",
		"- Hardening loop: only after the basic feature exists and basic verification passes, activate the remaining consideration lanes for API, perf, QA, security, dependency, and style follow-up.",
	}
	if len(activeConsiderations) == 0 {
		lines = append(lines, "Stay single-owner for this run. Do not spawn native subagents or tmux team workers unless considerations are added later.")
	} else {
		lines = append(lines, "Run this as a coordinated multi-lane session using isolated Codex lane processes, not native subagents.")
		lines = append(lines, "Launch lane processes with `nana work lane-exec --run-id <run-id> --lane <alias>` so each lane gets its own CODEX_HOME and MCP profile.")
		if roleLayout == "reviewer+executor" {
			lines = append(lines, "For merged lanes, run one isolated lane process per lane alias so that the same agent owns both review and implementation for that lane.")
		} else {
			lines = append(lines, "Do not run every lane immediately. Start with the architect overview plus coder loop, and defer the extra reviewer/executor lanes until the basic feature is implemented.")
		}
	}
	lines = append(lines, "Pipeline:")
	for _, lane := range buildGithubPipeline(activeConsiderations, roleLayout) {
		blockingLabel := "advisory"
		if lane.blocking {
			blockingLabel = "blocking"
		}
		lines = append(lines, fmt.Sprintf("    - %s -> %s [%s, owner=%s, %s] %s", lane.alias, lane.role, lane.mode, lane.owner, blockingLabel, lane.purpose))
	}
	return lines
}

func buildIssueStatsLines(stats githubIssueTokenStats) []string {
	lines := []string{
		fmt.Sprintf("[github] Token stats for %s issue #%d", stats.RepoSlug, stats.IssueNumber),
		fmt.Sprintf("[github] Total input tokens: %d (%s)", stats.Totals.InputTokens, formatGithubTokenCount(stats.Totals.InputTokens)),
		fmt.Sprintf("[github] Total output tokens: %d (%s)", stats.Totals.OutputTokens, formatGithubTokenCount(stats.Totals.OutputTokens)),
		fmt.Sprintf("[github] Total tokens: %d (%s)", stats.Totals.TotalTokens, formatGithubTokenCount(stats.Totals.TotalTokens)),
		fmt.Sprintf("[github] Sessions accounted: %d", stats.Totals.SessionsAccounted),
	}
	if len(stats.Sandboxes) > 0 {
		lines = append(lines, "[github] Sandbox breakdown:")
		keys := make([]string, 0, len(stats.Sandboxes))
		for key := range stats.Sandboxes {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			sandbox := stats.Sandboxes[key]
			lines = append(lines, fmt.Sprintf("[github]   - %s: total=%d input=%d output=%d sessions=%d", key, sandbox.TotalTokens, sandbox.InputTokens, sandbox.OutputTokens, sandbox.SessionsAccounted))
		}
	}
	return lines
}

func resolveGithubRunManifestPath(runID string, useLast bool) (string, string, error) {
	if runID != "" {
		if manifestPath, repoRoot, ok := resolveGithubRunManifestPathFromIndex(runID); ok {
			return manifestPath, repoRoot, nil
		}
		reposRoot := githubWorkReposRoot()
		foundManifest := ""
		foundRepoRoot := ""
		_ = filepath.Walk(reposRoot, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info == nil || info.IsDir() || info.Name() != "manifest.json" {
				return nil
			}
			if filepath.Base(filepath.Dir(path)) == runID {
				foundManifest = path
				foundRepoRoot = filepath.Dir(filepath.Dir(filepath.Dir(path)))
				return filepath.SkipAll
			}
			return nil
		})
		if foundManifest != "" {
			if manifest, err := readGithubWorkManifest(foundManifest); err == nil {
				_ = indexGithubWorkRunManifest(foundManifest, manifest)
			}
			return foundManifest, foundRepoRoot, nil
		}
	}
	if useLast || runID == "" {
		if entry, err := latestWorkRunIndex("github"); err == nil {
			if manifestPath, repoRoot, ok := githubRunManifestPathFromIndexEntry(entry); ok {
				return manifestPath, repoRoot, nil
			}
		}
		var latest githubLatestRunPointer
		path := githubWorkLatestRunPath()
		if err := readGithubJSON(path, &latest); err != nil {
			return "", "", fmt.Errorf("No GitHub work run found in %s. Start one first with `nana work start <url>`.", githubWorkReposRoot())
		}
		manifestPath := filepath.Join(latest.RepoRoot, "runs", latest.RunID, "manifest.json")
		if manifest, err := readGithubWorkManifest(manifestPath); err == nil {
			_ = indexGithubWorkRunManifest(manifestPath, manifest)
		}
		return manifestPath, latest.RepoRoot, nil
	}
	return "", "", fmt.Errorf("Run %s was not found under %s.", runID, githubWorkReposRoot())
}

func resolveGithubRunManifestPathFromIndex(runID string) (string, string, bool) {
	entry, err := readWorkRunIndex(runID)
	if err != nil || entry.Backend != "github" {
		return "", "", false
	}
	return githubRunManifestPathFromIndexEntry(entry)
}

func githubRunManifestPathFromIndexEntry(entry workRunIndexEntry) (string, string, bool) {
	manifestPath := strings.TrimSpace(entry.ManifestPath)
	if manifestPath == "" {
		return "", "", false
	}
	if _, err := os.Stat(manifestPath); err != nil {
		return "", "", false
	}
	repoRoot := strings.TrimSpace(entry.RepoRoot)
	if repoRoot == "" {
		repoRoot = filepath.Dir(filepath.Dir(filepath.Dir(manifestPath)))
	}
	return manifestPath, repoRoot, true
}

func indexGithubWorkRunManifest(manifestPath string, manifest githubWorkManifest) error {
	repoRoot := strings.TrimSpace(manifest.ManagedRepoRoot)
	if repoRoot == "" && strings.TrimSpace(manifestPath) != "" {
		repoRoot = filepath.Dir(filepath.Dir(filepath.Dir(manifestPath)))
	}
	updatedAt := strings.TrimSpace(manifest.UpdatedAt)
	if updatedAt == "" {
		updatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return writeWorkRunIndex(workRunIndexEntry{
		RunID:        manifest.RunID,
		Backend:      "github",
		RepoKey:      manifest.RepoSlug,
		RepoRoot:     repoRoot,
		RepoName:     manifest.RepoName,
		RepoSlug:     manifest.RepoSlug,
		ManifestPath: manifestPath,
		UpdatedAt:    updatedAt,
		TargetKind:   manifest.TargetKind,
	})
}

func writeThreadUsageArtifact(runDir string, sandboxPath string) (*githubThreadUsageArtifact, error) {
	rows, err := readThreadRowsFromRollouts(filepath.Join(sandboxPath, ".codex", "sessions"))
	if err != nil {
		return nil, err
	}
	total := 0
	for _, row := range rows {
		total += row.TokensUsed
	}
	artifact := &githubThreadUsageArtifact{
		Version:     1,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		SandboxPath: sandboxPath,
		Rows:        rows,
		TotalTokens: total,
	}
	if err := writeGithubJSON(filepath.Join(runDir, "thread-usage.json"), artifact); err != nil {
		return nil, err
	}
	return artifact, nil
}

func readThreadRowsFromRollouts(sessionsRoot string) ([]githubThreadUsageRow, error) {
	files := []string{}
	err := filepath.Walk(sessionsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info != nil && !info.IsDir() && strings.HasSuffix(path, ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	slices.Sort(files)

	rows := make([]githubThreadUsageRow, 0, len(files))
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		row := githubThreadUsageRow{}
		for _, line := range strings.Split(string(content), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var parsed map[string]any
			if err := json.Unmarshal([]byte(line), &parsed); err != nil {
				continue
			}
			if timestamp, ok := parsed["timestamp"].(string); ok {
				if parsedTime, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
					if row.StartedAt == 0 || parsedTime.Unix() < row.StartedAt {
						row.StartedAt = parsedTime.Unix()
					}
					if parsedTime.Unix() > row.UpdatedAt {
						row.UpdatedAt = parsedTime.Unix()
					}
				}
			}
			if parsed["type"] == "session_meta" {
				if payload, ok := parsed["payload"].(map[string]any); ok {
					if nickname, ok := payload["agent_nickname"].(string); ok {
						row.Nickname = nickname
					}
					if role, ok := payload["agent_role"].(string); ok {
						row.Role = role
					}
				}
			}
			if parsed["type"] == "event_msg" {
				if payload, ok := parsed["payload"].(map[string]any); ok && payload["type"] == "token_count" {
					if info, ok := payload["info"].(map[string]any); ok {
						if usage, ok := info["total_token_usage"].(map[string]any); ok {
							if total, ok := usage["total_tokens"].(float64); ok && int(total) > row.TokensUsed {
								row.TokensUsed = int(total)
							}
						}
					}
				}
			}
		}
		if row.StartedAt > 0 || row.TokensUsed > 0 {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func buildGithubRetrospectiveMarkdown(manifest githubRetrospectiveManifest, artifact *githubThreadUsageArtifact) string {
	lines := []string{
		"# NANA Work Retrospective",
		"",
		fmt.Sprintf("- Repo: %s", manifest.RepoSlug),
		fmt.Sprintf("- Run id: %s", manifest.RunID),
		fmt.Sprintf("- Role layout: %s", defaultString(manifest.RoleLayout, "split")),
		fmt.Sprintf("- Active considerations: %s", joinOrNone(manifest.Considerations)),
		fmt.Sprintf("- Total thread tokens: %d", artifact.TotalTokens),
		"",
		"## Thread Usage",
	}
	for _, row := range artifact.Rows {
		lines = append(lines, fmt.Sprintf("- %s: role=%s class=%s tokens=%d", defaultString(row.Nickname, "(leader)"), defaultString(row.Role, "(none)"), classifyThreadRole(row.Role), row.TokensUsed))
	}
	lines = append(lines,
		"",
		"## Efficiency Findings",
		"- Token-heavy reviewer lanes may indicate over-reading or late-stage coordination churn.",
		"",
		"## Missing Features / Angles To Investigate",
		"- Compare bootstrap token spend against the hardening lanes and confirm the feature was stabilized before expanding review scope.",
		"",
		"## Different Angles",
		"- Planning angle: did the bootstrap loop lock the minimal feature first?",
		"- Role angle: did reviewer/executor split increase handoff overhead?",
		"- Delivery angle: did publication and verification require manual salvage?",
		"",
	)
	return strings.Join(lines, "\n")
}

var githubReviewRuleLibrary = []githubReviewRuleTemplate{
	{
		Category: "qa",
		Title:    "Require regression coverage for behavior changes",
		Rule:     "Add or update targeted regression coverage for behavior changes and bug fixes before considering the work complete.",
		Patterns: []*regexp.Regexp{regexp.MustCompile(`\bregression\b`), regexp.MustCompile(`\btest(s|ing)?\b`), regexp.MustCompile(`\bcoverage\b`), regexp.MustCompile(`\bassert(ion|s)?\b`), regexp.MustCompile(`\bunit test\b`)},
	},
	{
		Category: "api",
		Title:    "Protect public API compatibility",
		Rule:     "Treat public APIs, schemas, and documented contracts as compatibility surfaces; avoid silent breakage and call out migrations explicitly.",
		Patterns: []*regexp.Regexp{regexp.MustCompile(`\bpublic api\b`), regexp.MustCompile(`\bbackward`), regexp.MustCompile(`\bcompatib`), regexp.MustCompile(`\bcontract\b`), regexp.MustCompile(`\bschema\b`), regexp.MustCompile(`\bsignature\b`), regexp.MustCompile(`\bbreaking\b`)},
	},
	{
		Category: "style",
		Title:    "Keep naming and style consistent",
		Rule:     "Keep naming, structure, and formatting aligned with repository conventions; prefer clarity and consistency over cleverness.",
		Patterns: []*regexp.Regexp{regexp.MustCompile(`\bnaming?\b`), regexp.MustCompile(`\breadab`), regexp.MustCompile(`\bstyle\b`), regexp.MustCompile(`\bconsistent\b`), regexp.MustCompile(`\bformat\b`), regexp.MustCompile(`\brename\b`), regexp.MustCompile(`\bclarity\b`)},
	},
	{
		Category: "dependency",
		Title:    "Avoid unnecessary dependency expansion",
		Rule:     "Prefer existing repository utilities and avoid new dependencies or version churn unless the tradeoff is explicit and justified.",
		Patterns: []*regexp.Regexp{regexp.MustCompile(`\bdependenc`), regexp.MustCompile(`\bthird[- ]party\b`), regexp.MustCompile(`\blibrary\b`), regexp.MustCompile(`\bpackage\b`), regexp.MustCompile(`\bversion\b`), regexp.MustCompile(`\bvendor\b`)},
	},
	{
		Category: "security",
		Title:    "Validate security-sensitive changes explicitly",
		Rule:     "Validate authentication, authorization, input handling, and secret exposure paths explicitly when code touches security-sensitive surfaces.",
		Patterns: []*regexp.Regexp{regexp.MustCompile(`\bsecurity\b`), regexp.MustCompile(`\bauth(entication|orization)?\b`), regexp.MustCompile(`\bsecret\b`), regexp.MustCompile(`\bsanitiz`), regexp.MustCompile(`\bvalidate\b`), regexp.MustCompile(`\binjection\b`), regexp.MustCompile(`\bpermission\b`)},
	},
	{
		Category: "arch",
		Title:    "Preserve architectural boundaries",
		Rule:     "Keep module boundaries, ownership, and architectural responsibilities explicit instead of leaking concerns across layers.",
		Patterns: []*regexp.Regexp{regexp.MustCompile(`\barchitect`), regexp.MustCompile(`\blayer\b`), regexp.MustCompile(`\bmodule\b`), regexp.MustCompile(`\babstraction\b`), regexp.MustCompile(`\bboundar`), regexp.MustCompile(`\bseparation\b`), regexp.MustCompile(`\bownership\b`)},
	},
	{
		Category: "process",
		Title:    "Respond to review with explicit resolution notes",
		Rule:     "When review feedback changes behavior or design, make the resolution explicit in code, tests, or PR discussion instead of leaving the rationale implicit.",
		Patterns: []*regexp.Regexp{regexp.MustCompile(`\brationale\b`), regexp.MustCompile(`\bexplain\b`), regexp.MustCompile(`\bclarify\b`), regexp.MustCompile(`\bdocument\b`), regexp.MustCompile(`\bwhy\b`), regexp.MustCompile(`\bnote\b`)},
	},
}

func githubReviewRulesScan(locator string) error {
	source, err := resolveGithubReviewRuleScanSource(locator)
	if err != nil {
		return err
	}
	apiBaseURL := strings.TrimSpace(os.Getenv("GITHUB_API_URL"))
	if apiBaseURL == "" {
		apiBaseURL = "https://api.github.com"
	}
	token, err := resolveGithubToken()
	if err != nil {
		return err
	}

	pulls := uniqueInts(source.PRNumbers)
	if source.ScanAllPRs {
		pulls, err = githubAPIListPulls(source.RepoSlug, apiBaseURL, token)
		if err != nil {
			return err
		}
	}
	reviews, comments, headShas, err := githubAPICollectReviewHistory(source.RepoSlug, pulls, apiBaseURL, token)
	if err != nil {
		return err
	}
	candidates := buildGithubReviewRuleCandidates(source.RepoSlug, reviews, comments, headShas, apiBaseURL, token)
	now := time.Now().UTC()
	rulesPath := githubManagedPaths(source.RepoSlug).ReviewRulesPath
	existing := githubReviewRuleDocument{}
	_ = readGithubJSON(rulesPath, &existing)
	document := mergeGithubReviewRuleScanResults(existing, source.RepoSlug, candidates, githubReviewRulesLastScan{
		At:                    now.Format(time.RFC3339),
		Source:                githubReviewRuleScanKind(source),
		SourceTarget:          source.SourceTarget,
		ScannedPRs:            len(uniqueInts(pulls)),
		ScannedReviews:        len(reviews),
		ScannedReviewComments: len(comments),
	}, now)
	if err := writeGithubJSON(rulesPath, document); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "[github] Scanned PR review history for %s from %s.\n", source.RepoSlug, source.SourceTarget)
	fmt.Fprintf(os.Stdout, "[github] Rules file: %s\n", rulesPath)
	fmt.Fprintf(os.Stdout, "[github] Approved rules=%d pending candidates=%d.\n", len(document.ApprovedRules), len(document.PendingCandidates))
	for _, rule := range document.PendingCandidates {
		fmt.Fprintf(os.Stdout, "[github] pending %s\n", formatGithubReviewRuleSummary(rule))
	}
	return nil
}

func resolveGithubToken() (string, error) {
	return resolveGithubTokenForAPIBase(strings.TrimSpace(os.Getenv("GITHUB_API_URL")))
}

func resolveGithubTokenForAPIBase(apiBaseURL string) (string, error) {
	if token := strings.TrimSpace(os.Getenv("GH_TOKEN")); token != "" {
		return token, nil
	}
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		return token, nil
	}
	host := githubCLIHostForAPIBase(apiBaseURL)
	if path, err := exec.LookPath("gh"); err == nil {
		argSets := [][]string{{"auth", "token"}}
		if host != "" {
			argSets = append([][]string{{"auth", "token", "--hostname", host}}, argSets...)
		}
		seen := map[string]struct{}{}
		for _, args := range argSets {
			key := strings.Join(args, "\x00")
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			cmd := exec.Command(path, args...)
			output, err := cmd.Output()
			if err == nil {
				if token := strings.TrimSpace(string(output)); token != "" {
					return token, nil
				}
			}
		}
	}
	if host != "" {
		return "", fmt.Errorf("GitHub token not found. Set GH_TOKEN or GITHUB_TOKEN, or configure gh auth for %s.", host)
	}
	return "", fmt.Errorf("GitHub token not found. Set GH_TOKEN or GITHUB_TOKEN, or configure gh auth.")
}

func githubCLIHostForAPIBase(apiBaseURL string) string {
	if host := strings.TrimSpace(os.Getenv("GH_HOST")); host != "" {
		return host
	}
	if strings.TrimSpace(apiBaseURL) == "" {
		return ""
	}
	parsed, err := url.Parse(apiBaseURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	switch host {
	case "", "github.com", "api.github.com", "uploads.github.com":
		return "github.com"
	default:
		return host
	}
}

func githubAPIListPulls(repoSlug string, apiBaseURL string, token string) ([]int, error) {
	pulls := []int{}
	for page := 1; page <= 20; page++ {
		path := fmt.Sprintf("/repos/%s/pulls?state=all&per_page=100&page=%d", repoSlug, page)
		var batch []githubPullRequestPayload
		if err := githubAPIGetJSON(apiBaseURL, token, path, &batch); err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		for _, pr := range batch {
			pulls = append(pulls, pr.Number)
		}
		if len(batch) < 100 {
			break
		}
	}
	return pulls, nil
}

func githubAPICollectReviewHistory(repoSlug string, prNumbers []int, apiBaseURL string, token string) ([]githubPullReviewPayload, []githubPullReviewCommentPayload, map[int]string, error) {
	reviews := []githubPullReviewPayload{}
	comments := []githubPullReviewCommentPayload{}
	headShas := map[int]string{}
	for _, prNumber := range uniqueInts(prNumbers) {
		var pull githubPullRequestPayload
		if err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls/%d", repoSlug, prNumber), &pull); err != nil {
			return nil, nil, nil, err
		}
		headShas[prNumber] = pull.Head.SHA

		var prReviews []githubPullReviewPayload
		if err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", repoSlug, prNumber), &prReviews); err != nil {
			return nil, nil, nil, err
		}
		reviews = append(reviews, prReviews...)

		var prComments []githubPullReviewCommentPayload
		if err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls/%d/comments?per_page=100", repoSlug, prNumber), &prComments); err != nil {
			return nil, nil, nil, err
		}
		comments = append(comments, prComments...)
	}
	return reviews, comments, headShas, nil
}

func githubAPIGetJSON(apiBaseURL string, token string, path string, target interface{}) error {
	request, err := http.NewRequest(http.MethodGet, strings.TrimRight(apiBaseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := githubAPIHTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		return fmt.Errorf("GitHub API request failed (%d %s)%s", response.StatusCode, response.Status, renderGithubDetail(body))
	}
	return json.NewDecoder(response.Body).Decode(target)
}

func renderGithubDetail(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}
	return ": " + trimmed
}

func buildGithubReviewRuleCandidates(repoSlug string, reviews []githubPullReviewPayload, comments []githubPullReviewCommentPayload, headShas map[int]string, apiBaseURL string, token string) []githubReviewRule {
	signals := []githubReviewSignal{}
	for _, review := range reviews {
		if template, ok := classifyGithubReviewRuleTemplate(review.Body); ok {
			signals = append(signals, githubReviewSignal{
				template: template,
				evidence: githubReviewRuleEvidence{
					Kind:     "review",
					PRNumber: review.ID / 1000000,
					Reviewer: review.User.Login,
					Excerpt:  buildGithubReviewRuleExcerpt(review.Body, 180),
				},
			})
		}
	}
	for _, comment := range comments {
		if template, ok := classifyGithubReviewRuleTemplate(comment.Body); ok {
			evidence := githubReviewRuleEvidence{
				Kind:     "review_comment",
				Reviewer: comment.User.Login,
				Path:     comment.Path,
				Line:     choosePositive(comment.Line, comment.OriginalLine),
				Excerpt:  buildGithubReviewRuleExcerpt(comment.Body, 180),
			}
			if evidence.Path != "" && evidence.Line > 0 {
				if codeContext, provenance, ref := fetchGithubCodeContext(repoSlug, evidence.Path, evidence.Line, headShas, apiBaseURL, token); codeContext != "" {
					evidence.CodeContextExcerpt = codeContext
					evidence.CodeContextProvenance = provenance
					evidence.CodeContextRef = ref
				}
			}
			evidence.PRNumber = comment.ID / 1000000
			signals = append(signals, githubReviewSignal{template: template, evidence: evidence})
		}
	}

	buckets := map[string][]githubReviewSignal{}
	for _, item := range signals {
		key := item.template.Category + "\n" + item.template.Rule
		buckets[key] = append(buckets[key], item)
	}
	candidates := []githubReviewRule{}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, bucket := range buckets {
		template := bucket[0].template
		if len(bucket) < 2 {
			continue
		}
		reviewerCount := countDistinctReviewers(bucket)
		if reviewerCount < 1 {
			continue
		}
		evidence := make([]githubReviewRuleEvidence, 0, len(bucket))
		for _, item := range bucket {
			evidence = append(evidence, item.evidence)
		}
		candidates = append(candidates, githubReviewRule{
			ID:               buildGithubReviewRuleID(template.Category, template.Rule),
			Title:            template.Title,
			Rule:             template.Rule,
			Category:         template.Category,
			Confidence:       clampConfidence(0.55 + float64(len(evidence))*0.12),
			ReviewerCount:    reviewerCount,
			PathScopes:       deriveGithubReviewRulePathScopes(evidence),
			ExtractionOrigin: deriveGithubReviewRuleExtractionOrigin(evidence),
			ExtractionReason: buildGithubReviewRuleExtractionReason(evidence),
			Evidence:         evidence,
			UpdatedAt:        now,
		})
	}
	slices.SortFunc(candidates, func(left, right githubReviewRule) int {
		if left.Confidence > right.Confidence {
			return -1
		}
		if left.Confidence < right.Confidence {
			return 1
		}
		return strings.Compare(left.Title, right.Title)
	})
	return candidates
}

func classifyGithubReviewRuleTemplate(text string) (githubReviewRuleTemplate, bool) {
	normalized := normalizeGithubReviewRuleEvidenceText(text)
	if normalized == "" {
		return githubReviewRuleTemplate{}, false
	}
	best := githubReviewRuleTemplate{}
	bestScore := 0
	for _, template := range githubReviewRuleLibrary {
		score := 0
		for _, pattern := range template.Patterns {
			if pattern.MatchString(normalized) {
				score++
			}
		}
		if score > bestScore {
			bestScore = score
			best = template
		}
	}
	return best, bestScore > 0
}

func normalizeGithubReviewRuleEvidenceText(text string) string {
	text = githubReviewRuleFencedCodePattern.ReplaceAllString(text, " ")
	text = githubReviewRuleInlineCodePattern.ReplaceAllString(text, " ")
	text = githubReviewRuleMarkdownLinkPattern.ReplaceAllString(text, " ")
	text = strings.Join(strings.Fields(text), " ")
	return strings.TrimSpace(text)
}

func buildGithubReviewRuleExcerpt(text string, maxLength int) string {
	normalized := normalizeGithubReviewRuleEvidenceText(text)
	if len(normalized) <= maxLength {
		return normalized
	}
	return strings.TrimSpace(normalized[:maxLength-1]) + "…"
}

func buildGithubReviewRuleID(category string, rule string) string {
	return fmt.Sprintf("%s-%s", category, shortHash(category+"\n"+rule))
}

func shortHash(value string) string {
	const alphabet = "0123456789abcdef"
	hash := 0
	for _, ch := range value {
		hash = (hash*31 + int(ch)) & 0x7fffffff
	}
	out := make([]byte, 10)
	for index := range out {
		out[index] = alphabet[(hash>>(index%8*4))&0xf]
	}
	return string(out)
}

func clampConfidence(value float64) float64 {
	if value > 0.95 {
		value = 0.95
	}
	return float64(int(value*100+0.5)) / 100.0
}

func countDistinctReviewers(items []githubReviewSignal) int {
	seen := map[string]bool{}
	for _, item := range items {
		reviewer := strings.ToLower(strings.TrimSpace(item.evidence.Reviewer))
		if reviewer != "" {
			seen[reviewer] = true
		}
	}
	return len(seen)
}

func deriveGithubReviewRulePathScopes(evidence []githubReviewRuleEvidence) []string {
	scopes := map[string]bool{}
	for _, item := range evidence {
		if item.Path == "" {
			continue
		}
		normalized := strings.ReplaceAll(item.Path, "\\", "/")
		if index := strings.LastIndex(normalized, "/"); index > 0 {
			scopes[normalized[:index]] = true
		} else {
			scopes[normalized] = true
		}
	}
	values := []string{}
	for value := range scopes {
		values = append(values, value)
	}
	slices.Sort(values)
	return values
}

func deriveGithubReviewRuleExtractionOrigin(evidence []githubReviewRuleEvidence) string {
	kinds := map[string]bool{}
	for _, item := range evidence {
		kinds[item.Kind] = true
	}
	if len(kinds) == 1 {
		if kinds["review"] {
			return "reviews"
		}
		return "review_comments"
	}
	return "mixed"
}

func buildGithubReviewRuleExtractionReason(evidence []githubReviewRuleEvidence) string {
	prs := map[int]bool{}
	paths := map[string]bool{}
	reviews := 0
	comments := 0
	for _, item := range evidence {
		if item.Kind == "review" {
			reviews++
		}
		if item.Kind == "review_comment" {
			comments++
		}
		if item.PRNumber > 0 {
			prs[item.PRNumber] = true
		}
		if item.Path != "" {
			paths[item.Path] = true
		}
	}
	origin := deriveGithubReviewRuleExtractionOrigin(evidence)
	originLabel := origin
	if origin == "mixed" {
		originLabel = fmt.Sprintf("mixed reviews/comments (%d reviews, %d comments)", reviews, comments)
	} else {
		originLabel = strings.ReplaceAll(origin, "_", " ")
	}
	reason := fmt.Sprintf("Repeated %s across %d PR%s", originLabel, len(prs), plural(len(prs)))
	if len(paths) > 0 {
		reason += fmt.Sprintf(" and %d path scope%s", len(paths), plural(len(paths)))
	}
	return reason + "."
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func choosePositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func fetchGithubCodeContext(repoSlug string, path string, line int, headShas map[int]string, apiBaseURL string, token string) (string, string, string) {
	refs := make([]string, 0, len(headShas))
	for _, ref := range headShas {
		if strings.TrimSpace(ref) != "" {
			refs = append(refs, ref)
		}
	}
	slices.Sort(refs)
	for _, ref := range refs {
		var payload githubContentPayload
		err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s/contents/%s?ref=%s", repoSlug, escapeGitHubPath(path), url.QueryEscape(ref)), &payload)
		if err != nil || payload.Encoding != "base64" || payload.Content == "" {
			continue
		}
		decoded, decodeErr := decodeBase64String(payload.Content)
		if decodeErr != nil {
			continue
		}
		lines := strings.Split(decoded, "\n")
		start := githubMaxInt(line-3, 1)
		end := githubMinInt(line+2, len(lines))
		snippet := []string{}
		for idx := start; idx <= end; idx++ {
			snippet = append(snippet, fmt.Sprintf("%d: %s", idx, lines[idx-1]))
		}
		return strings.Join(snippet, "\n"), "pr_head_sha", ref
	}
	return "", "", ""
}

func escapeGitHubPath(path string) string {
	parts := strings.Split(path, "/")
	for index, part := range parts {
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func decodeBase64String(value string) (string, error) {
	decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, strings.NewReader(strings.ReplaceAll(value, "\n", ""))))
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

func mergeGithubReviewRuleScanResults(existing githubReviewRuleDocument, repoSlug string, candidates []githubReviewRule, lastScan githubReviewRulesLastScan, now time.Time) githubReviewRuleDocument {
	approvedByID := map[string]githubReviewRule{}
	for _, rule := range existing.ApprovedRules {
		approvedByID[rule.ID] = rule
	}
	pendingByID := map[string]githubReviewRule{}
	for _, rule := range existing.PendingCandidates {
		pendingByID[rule.ID] = rule
	}
	disabledByID := map[string]githubReviewRule{}
	for _, rule := range existing.DisabledRules {
		disabledByID[rule.ID] = rule
	}
	archivedByID := map[string]githubReviewRule{}
	for _, rule := range existing.ArchivedRules {
		archivedByID[rule.ID] = rule
	}
	normalized := make([]githubReviewRule, 0, len(candidates))
	for _, rule := range candidates {
		if previous, ok := pendingByID[rule.ID]; ok {
			rule.UpdatedAt = previous.UpdatedAt
		}
		normalized = append(normalized, rule)
	}
	pending := []githubReviewRule{}
	for _, rule := range normalized {
		if approvedByID[rule.ID].ID != "" || disabledByID[rule.ID].ID != "" || archivedByID[rule.ID].ID != "" {
			continue
		}
		pending = append(pending, rule)
	}
	document := githubReviewRuleDocument{
		ApprovedRules:     existing.ApprovedRules,
		PendingCandidates: pending,
		DisabledRules:     existing.DisabledRules,
		ArchivedRules:     existing.ArchivedRules,
		UpdatedAt:         now.Format(time.RFC3339),
	}
	_ = repoSlug
	_ = lastScan
	return document
}

func uniqueInts(values []int) []int {
	seen := map[int]bool{}
	out := []int{}
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	slices.Sort(out)
	return out
}

func githubMaxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func githubMinInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func formatGithubReviewRuleSummary(rule githubReviewRule) string {
	return fmt.Sprintf("%s [%s] confidence=%.2f reviewers=%d %s", rule.ID, rule.Category, rule.Confidence, rule.ReviewerCount, defaultString(rule.Title, rule.Rule))
}

func cleanSelectors(values []string) []string {
	out := []string{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func rewriteGithubRuleLifecycle(document githubReviewRuleDocument, action string, selectors []string) (int, githubReviewRuleDocument) {
	targetState := map[string]string{"disable": "disabled", "enable": "approved", "archive": "archived"}[action]
	approveAll := slices.Contains(selectors, "all")
	selected := map[string]bool{}
	for _, selector := range selectors {
		if selector != "all" {
			selected[selector] = true
		}
	}
	buckets := map[string][]githubReviewRule{
		"approved": append([]githubReviewRule{}, document.ApprovedRules...),
		"pending":  append([]githubReviewRule{}, document.PendingCandidates...),
		"disabled": append([]githubReviewRule{}, document.DisabledRules...),
		"archived": append([]githubReviewRule{}, document.ArchivedRules...),
	}
	take := func(state string) []githubReviewRule {
		moved := []githubReviewRule{}
		kept := []githubReviewRule{}
		for _, rule := range buckets[state] {
			if approveAll || selected[rule.ID] {
				rule.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
				moved = append(moved, rule)
			} else {
				kept = append(kept, rule)
			}
		}
		buckets[state] = kept
		return moved
	}
	moved := append(append(append(take("approved"), take("pending")...), take("disabled")...), take("archived")...)
	if len(moved) == 0 {
		return 0, document
	}
	buckets[targetState] = append(buckets[targetState], moved...)
	document.ApprovedRules = buckets["approved"]
	document.PendingCandidates = buckets["pending"]
	document.DisabledRules = buckets["disabled"]
	document.ArchivedRules = buckets["archived"]
	document.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return len(moved), document
}

func findGithubReviewRule(document githubReviewRuleDocument, id string) (string, *githubReviewRule) {
	for _, candidate := range document.ApprovedRules {
		if candidate.ID == id {
			rule := candidate
			return "approved", &rule
		}
	}
	for _, candidate := range document.PendingCandidates {
		if candidate.ID == id {
			rule := candidate
			return "pending", &rule
		}
	}
	for _, candidate := range document.DisabledRules {
		if candidate.ID == id {
			rule := candidate
			return "disabled", &rule
		}
	}
	for _, candidate := range document.ArchivedRules {
		if candidate.ID == id {
			rule := candidate
			return "archived", &rule
		}
	}
	return "", nil
}

func classifyThreadRole(role string) string {
	normalized := strings.TrimSpace(role)
	if normalized == "" {
		return "leader"
	}
	if strings.Contains(normalized, "executor") || normalized == "coder" || normalized == "test-engineer" {
		return "executor"
	}
	return "reviewer"
}

func formatGithubTokenCount(value int) string {
	switch {
	case value >= 1000000:
		return fmt.Sprintf("%.1fM", float64(value)/1000000.0)
	case value >= 1000:
		return fmt.Sprintf("%.1fk", float64(value)/1000.0)
	default:
		return strconv.Itoa(value)
	}
}

type parsedGithubTarget struct {
	repoSlug string
	kind     string
	number   int
}

func parseGithubTargetURL(raw string) (parsedGithubTarget, error) {
	const prefix = "https://github.com/"
	if !strings.HasPrefix(raw, prefix) {
		return parsedGithubTarget{}, fmt.Errorf("Invalid GitHub URL: %s.\n%s", raw, GithubWorkHelp)
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(raw, prefix), "/"), "/")
	if len(parts) < 4 {
		return parsedGithubTarget{}, fmt.Errorf("Unsupported GitHub URL shape: %s. Expected an issue or pull request URL.\n%s", raw, GithubWorkHelp)
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return parsedGithubTarget{}, fmt.Errorf("Invalid GitHub target number in URL: %s.\n%s", raw, GithubWorkHelp)
	}
	kind := ""
	switch strings.ToLower(parts[2]) {
	case "issues":
		kind = "issue"
	case "pull", "pulls":
		kind = "pr"
	default:
		return parsedGithubTarget{}, fmt.Errorf("Unsupported GitHub URL shape: %s. Expected an issue or pull request URL.\n%s", raw, GithubWorkHelp)
	}
	return parsedGithubTarget{
		repoSlug: parts[0] + "/" + parts[1],
		kind:     kind,
		number:   number,
	}, nil
}

func resolveGithubRepoSlugLocator(locator string) (string, error) {
	trimmed := strings.TrimSpace(locator)
	if validRepoSlug(trimmed) {
		return trimmed, nil
	}
	target, err := parseGithubTargetURL(trimmed)
	if err != nil {
		return "", err
	}
	return target.repoSlug, nil
}

func resolveGithubReviewRuleScanSource(locator string) (githubReviewRuleScanSource, error) {
	trimmed := strings.TrimSpace(locator)
	if validRepoSlug(trimmed) {
		return githubReviewRuleScanSource{
			RepoSlug:     trimmed,
			SourceTarget: trimmed,
			ScanAllPRs:   true,
		}, nil
	}
	target, err := parseGithubTargetURL(trimmed)
	if err != nil {
		return githubReviewRuleScanSource{}, err
	}
	source := githubReviewRuleScanSource{
		RepoSlug:     target.repoSlug,
		SourceTarget: githubCanonicalTargetURL(target),
	}
	if target.kind == "pr" {
		source.PRNumbers = []int{target.number}
		return source, nil
	}
	source.PRNumbers = collectGithubIssueLinkedPullNumbers(target.repoSlug, target.number)
	return source, nil
}

func githubCanonicalTargetURL(target parsedGithubTarget) string {
	resource := "issues"
	if target.kind == "pr" {
		resource = "pull"
	}
	return fmt.Sprintf("https://github.com/%s/%s/%d", target.repoSlug, resource, target.number)
}

func githubReviewRuleScanKind(source githubReviewRuleScanSource) string {
	if source.ScanAllPRs {
		return "repo"
	}
	if strings.Contains(source.SourceTarget, "/pull/") {
		return "pr"
	}
	return "issue"
}

func githubRepoRoot(repoSlug string) string {
	return githubWorkRepoRoot(repoSlug)
}

func githubIssueStatsPath(repoSlug string, issueNumber int) string {
	return githubWorkIssueStatsPath(repoSlug, issueNumber)
}

func githubSandboxPath(repoSlug string, sandboxID string) string {
	return githubWorkSandboxPath(repoSlug, sandboxID)
}

func buildGithubTargetSandboxID(targetKind string, targetNumber int) string {
	return fmt.Sprintf("%s-%d", targetKind, targetNumber)
}

func collectGithubIssueLinkedPullNumbers(repoSlug string, issueNumber int) []int {
	runsDir := filepath.Join(githubRepoRoot(repoSlug), "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return nil
	}
	prNumbers := map[int]bool{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		var manifest githubRunManifestIndex
		if err := readGithubJSON(filepath.Join(runsDir, entry.Name(), "manifest.json"), &manifest); err != nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(manifest.RepoSlug), strings.TrimSpace(repoSlug)) {
			continue
		}
		if manifest.TargetKind != "issue" || manifest.TargetNumber != issueNumber || manifest.PublishedPRNumber <= 0 {
			continue
		}
		prNumbers[manifest.PublishedPRNumber] = true
	}
	pulls := make([]int, 0, len(prNumbers))
	for prNumber := range prNumbers {
		pulls = append(pulls, prNumber)
	}
	slices.Sort(pulls)
	return pulls
}

func readGithubSandboxMetadata(sandboxPath string) (*githubSandboxMetadata, error) {
	var metadata githubSandboxMetadata
	if err := readGithubJSON(filepath.Join(sandboxPath, ".nana", "sandbox.json"), &metadata); err != nil {
		return nil, err
	}
	return &metadata, nil
}

func resolveGithubIssueAssociationNumber(sandboxPath string, fallbackTargetKind string, fallbackTargetNumber int) (int, error) {
	if fallbackTargetKind == "issue" && fallbackTargetNumber > 0 {
		return fallbackTargetNumber, nil
	}
	if info, err := os.Lstat(sandboxPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		resolvedSandboxPath, err := filepath.EvalSymlinks(sandboxPath)
		if err == nil {
			if metadata, err := readGithubSandboxMetadata(resolvedSandboxPath); err == nil && metadata.TargetKind == "issue" {
				return metadata.TargetNumber, nil
			}
		}
	}
	metadata, err := readGithubSandboxMetadata(sandboxPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if metadata.TargetKind == "issue" {
		return metadata.TargetNumber, nil
	}
	return 0, nil
}

func defaultsHeader(activeConsiderations []string) string {
	if len(activeConsiderations) == 0 {
		return "Active considerations: none."
	}
	return fmt.Sprintf("Active considerations: %s.", strings.Join(activeConsiderations, ", "))
}

func buildGithubPipeline(activeConsiderations []string, roleLayout string) []githubLane {
	lanes := []githubLane{
		{alias: "coder", role: "executor", mode: "execute", owner: "self", blocking: true, purpose: "Implement the requested changes and own the main delivery loop."},
	}
	has := func(name string) bool { return slices.Contains(activeConsiderations, name) }
	if roleLayout == "reviewer+executor" {
		if has("security") {
			lanes = append(lanes, githubLane{alias: "security-reviewer", role: "security-reviewer+executor", mode: "review+execute", owner: "self", blocking: true, purpose: "Review security risk and implement the required remediations in the same lane."})
		}
		if has("style") {
			lanes = append(lanes, githubLane{alias: "style-reviewer", role: "style-reviewer+executor", mode: "review+execute", owner: "self", blocking: false, purpose: "Review and apply style/lint consistency fixes inside a single merged lane."})
		}
	} else {
		if has("qa") {
			lanes = append(lanes, githubLane{alias: "test-engineer", role: "test-engineer", mode: "execute", owner: "self", blocking: true, purpose: "Design/write tests and strengthen regression coverage."})
		}
		if has("style") {
			lanes = append(lanes, githubLane{alias: "style-reviewer", role: "style-reviewer", mode: "review", owner: "coder", blocking: false, purpose: "Review formatting, naming, and lint/style consistency before closeout."})
		}
		if has("security") {
			lanes = append(lanes, githubLane{alias: "security-reviewer", role: "security-reviewer", mode: "review", owner: "coder", blocking: true, purpose: "Review trust boundaries, authn/authz, and vulnerability exposure."})
		}
	}
	return lanes
}

func joinOrNone(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	return strings.Join(values, ", ")
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func intOrNone(value int) string {
	if value <= 0 {
		return "(none)"
	}
	return strconv.Itoa(value)
}

func (policy *githubReviewerPolicy) GetTrusted() []string {
	if policy == nil {
		return nil
	}
	return policy.TrustedReviewers
}

func (policy *githubReviewerPolicy) GetBlocked() []string {
	if policy == nil {
		return nil
	}
	return policy.BlockedReviewers
}

func (policy *githubReviewerPolicy) GetMinDistinct() int {
	if policy == nil {
		return 0
	}
	return policy.MinDistinctReviews
}
