package gocli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

type githubWorkOnActionPolicy struct {
	Commit        bool `json:"commit"`
	Push          bool `json:"push"`
	OpenDraftPR   bool `json:"open_draft_pr"`
	RequestReview bool `json:"request_review"`
	Merge         bool `json:"merge"`
}

type githubWorkOnPolicy struct {
	Version              int                       `json:"version"`
	Experimental         *bool                     `json:"experimental,omitempty"`
	AllowedActions       *githubWorkOnActionPolicy `json:"allowed_actions,omitempty"`
	FeedbackSource       string                    `json:"feedback_source,omitempty"`
	RepoNativeStrictness string                    `json:"repo_native_strictness,omitempty"`
	HumanGate            string                    `json:"human_gate,omitempty"`
	MergeMethod          string                    `json:"merge_method,omitempty"`
	UpdatedAt            string                    `json:"updated_at,omitempty"`
}

type githubResolvedWorkOnPolicy struct {
	Version              int                      `json:"version"`
	Experimental         bool                     `json:"experimental"`
	AllowedActions       githubWorkOnActionPolicy `json:"allowed_actions"`
	FeedbackSource       string                   `json:"feedback_source"`
	RepoNativeStrictness string                   `json:"repo_native_strictness"`
	HumanGate            string                   `json:"human_gate"`
	MergeMethod          string                   `json:"merge_method"`
	SourceMap            map[string]string        `json:"source_map,omitempty"`
}

type githubCommitStyleProfile struct {
	Kind       string   `json:"kind"`
	Confidence float64  `json:"confidence"`
	Evidence   []string `json:"evidence,omitempty"`
}

type githubPullRequestTemplateProfile struct {
	Path     string   `json:"path"`
	Sections []string `json:"sections,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
}

type githubCodeownersProfile struct {
	Path     string   `json:"path"`
	Evidence []string `json:"evidence,omitempty"`
}

type githubReviewRulesSummary struct {
	Path          string   `json:"path"`
	ApprovedCount int      `json:"approved_count"`
	PendingCount  int      `json:"pending_count"`
	DisabledCount int      `json:"disabled_count"`
	ArchivedCount int      `json:"archived_count"`
	Evidence      []string `json:"evidence,omitempty"`
}

type githubRepoProfile struct {
	Version                 int                               `json:"version"`
	GeneratedAt             string                            `json:"generated_at"`
	RepoSlug                string                            `json:"repo_slug,omitempty"`
	RepoPath                string                            `json:"repo_path"`
	Fingerprint             string                            `json:"fingerprint"`
	VerificationPlan        *githubVerificationPlan           `json:"verification_plan,omitempty"`
	SuggestedConsiderations []string                          `json:"suggested_considerations,omitempty"`
	CommitStyle             *githubCommitStyleProfile         `json:"commit_style,omitempty"`
	PullRequestTemplate     *githubPullRequestTemplateProfile `json:"pull_request_template,omitempty"`
	Codeowners              *githubCodeownersProfile          `json:"codeowners,omitempty"`
	WorkflowFiles           []string                          `json:"workflow_files,omitempty"`
	StyleSignals            []string                          `json:"style_signals,omitempty"`
	ReviewRules             *githubReviewRulesSummary         `json:"review_rules,omitempty"`
	Evidence                []string                          `json:"evidence,omitempty"`
	Warnings                []string                          `json:"warnings,omitempty"`
}

type githubExplainPayload struct {
	RunID                 string                      `json:"run_id"`
	RepoSlug              string                      `json:"repo_slug"`
	TargetURL             string                      `json:"target_url"`
	Policy                *githubResolvedWorkOnPolicy `json:"policy,omitempty"`
	RepoProfilePath       string                      `json:"repo_profile_path,omitempty"`
	RepoProfile           *githubRepoProfile          `json:"repo_profile,omitempty"`
	ControlPlaneReviewers []string                    `json:"control_plane_reviewers,omitempty"`
	IgnoredFeedbackActors map[string]int              `json:"ignored_feedback_actors,omitempty"`
	RequestedReviewers    []string                    `json:"requested_reviewers,omitempty"`
	ReviewRequestState    string                      `json:"review_request_state,omitempty"`
	ReviewRequestError    string                      `json:"review_request_error,omitempty"`
	MergeState            string                      `json:"merge_state,omitempty"`
	MergeError            string                      `json:"merge_error,omitempty"`
	MergeMethod           string                      `json:"merge_method,omitempty"`
	MergedPRNumber        int                         `json:"merged_pr_number,omitempty"`
	MergedSHA             string                      `json:"merged_sha,omitempty"`
	NeedsHuman            bool                        `json:"needs_human,omitempty"`
	NeedsHumanReason      string                      `json:"needs_human_reason,omitempty"`
	NextAction            string                      `json:"next_action,omitempty"`
}

const (
	githubFeedbackSourceAssignedTrusted = "assigned_trusted"
	githubFeedbackSourceAnyHuman        = "any_human"
	githubFeedbackSourceManual          = "manual"

	githubRepoNativeStrictnessAdvisory = "advisory"
	githubRepoNativeStrictnessEnforced = "enforced"

	githubHumanGateNone        = "none"
	githubHumanGateSpecial     = "special_modes"
	githubHumanGatePublishTime = "publish_time"
	githubHumanGateAlways      = "always"
)

func defaultGithubResolvedWorkOnPolicy() githubResolvedWorkOnPolicy {
	return githubResolvedWorkOnPolicy{
		Version:      1,
		Experimental: false,
		AllowedActions: githubWorkOnActionPolicy{
			Commit:        true,
			Push:          true,
			OpenDraftPR:   true,
			RequestReview: false,
			Merge:         false,
		},
		FeedbackSource:       githubFeedbackSourceAssignedTrusted,
		RepoNativeStrictness: githubRepoNativeStrictnessAdvisory,
		HumanGate:            githubHumanGateSpecial,
		MergeMethod:          "squash",
		SourceMap: map[string]string{
			"experimental":                   "builtin",
			"allowed_actions.commit":         "builtin",
			"allowed_actions.push":           "builtin",
			"allowed_actions.open_draft_pr":  "builtin",
			"allowed_actions.request_review": "builtin",
			"allowed_actions.merge":          "builtin",
			"feedback_source":                "builtin",
			"repo_native_strictness":         "builtin",
			"human_gate":                     "builtin",
			"merge_method":                   "builtin",
		},
	}
}

func githubGlobalWorkOnPolicyPath() string {
	return filepath.Join(githubNanaHome(), "github-workon", "policy.json")
}

func githubRepoProfilePath(repoSlug string) string {
	return filepath.Join(githubNanaHome(), "repos", filepath.FromSlash(repoSlug), "repo-profile.json")
}

func readGithubWorkOnPolicy(path string) (*githubWorkOnPolicy, error) {
	var policy githubWorkOnPolicy
	if err := readGithubJSON(path, &policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

func readGithubRepoProfile(path string) (*githubRepoProfile, error) {
	var profile githubRepoProfile
	if err := readGithubJSON(path, &profile); err != nil {
		return nil, err
	}
	return &profile, nil
}

func resolveGithubWorkOnPolicy(repoPath string) (githubResolvedWorkOnPolicy, error) {
	resolved := defaultGithubResolvedWorkOnPolicy()
	apply := func(path string, source string) error {
		policy, err := readGithubWorkOnPolicy(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		mergeGithubWorkOnPolicy(&resolved, *policy, source)
		return nil
	}
	if err := apply(githubGlobalWorkOnPolicyPath(), "global"); err != nil {
		return githubResolvedWorkOnPolicy{}, err
	}
	if strings.TrimSpace(repoPath) != "" {
		if err := apply(filepath.Join(repoPath, ".github", "nana-work-on-policy.json"), ".github/nana-work-on-policy.json"); err != nil {
			return githubResolvedWorkOnPolicy{}, err
		}
		if err := apply(filepath.Join(repoPath, ".nana", "work-on-policy.json"), ".nana/work-on-policy.json"); err != nil {
			return githubResolvedWorkOnPolicy{}, err
		}
	}
	return resolved, nil
}

func resolveGithubEffectiveReviewerPolicy(repoSlug string) *githubReviewerPolicy {
	settings, _ := readGithubRepoSettings(githubRepoSettingsPath(repoSlug))
	globalConfig, _ := readGithubReviewRulesGlobalConfig()
	if settings != nil {
		if policy := normalizeGithubReviewerPolicy(settings.ReviewRulesReviewerPolicy); policy != nil {
			return policy
		}
	}
	if globalConfig != nil {
		return normalizeGithubReviewerPolicy(globalConfig.ReviewerPolicy)
	}
	return nil
}

func mergeGithubWorkOnPolicy(target *githubResolvedWorkOnPolicy, policy githubWorkOnPolicy, source string) {
	target.Version = max(target.Version, policy.Version)
	if policy.Experimental != nil {
		target.Experimental = *policy.Experimental
		target.SourceMap["experimental"] = source
	}
	if policy.AllowedActions != nil {
		target.AllowedActions = *policy.AllowedActions
		target.SourceMap["allowed_actions.commit"] = source
		target.SourceMap["allowed_actions.push"] = source
		target.SourceMap["allowed_actions.open_draft_pr"] = source
		target.SourceMap["allowed_actions.request_review"] = source
		target.SourceMap["allowed_actions.merge"] = source
	}
	if normalized := normalizeGithubFeedbackSource(policy.FeedbackSource); normalized != "" {
		target.FeedbackSource = normalized
		target.SourceMap["feedback_source"] = source
	}
	if normalized := normalizeGithubRepoNativeStrictness(policy.RepoNativeStrictness); normalized != "" {
		target.RepoNativeStrictness = normalized
		target.SourceMap["repo_native_strictness"] = source
	}
	if normalized := normalizeGithubHumanGate(policy.HumanGate); normalized != "" {
		target.HumanGate = normalized
		target.SourceMap["human_gate"] = source
	}
	if normalized := normalizeGithubMergeMethod(policy.MergeMethod); normalized != "" {
		target.MergeMethod = normalized
		target.SourceMap["merge_method"] = source
	}
}

func normalizeGithubFeedbackSource(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case githubFeedbackSourceAssignedTrusted, githubFeedbackSourceAnyHuman, githubFeedbackSourceManual:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeGithubRepoNativeStrictness(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case githubRepoNativeStrictnessAdvisory, githubRepoNativeStrictnessEnforced:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeGithubHumanGate(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case githubHumanGateNone, githubHumanGateSpecial, githubHumanGatePublishTime, githubHumanGateAlways:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeGithubMergeMethod(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "merge", "squash", "rebase":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func githubRepoNativeEnabled(policy *githubResolvedWorkOnPolicy) bool {
	return policy != nil && policy.Experimental
}

func inferGithubRepoSlugFromRepo(repoPath string) string {
	output, err := githubGitOutput(repoPath, "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	remote := strings.TrimSpace(output)
	remote = strings.TrimSuffix(remote, ".git")
	if strings.HasPrefix(remote, "https://github.com/") {
		parts := strings.Split(strings.TrimPrefix(remote, "https://github.com/"), "/")
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1]
		}
	}
	if strings.HasPrefix(remote, "git@github.com:") {
		parts := strings.Split(strings.TrimPrefix(remote, "git@github.com:"), "/")
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1]
		}
	}
	return ""
}

func refreshGithubRepoProfile(repoSlug string, repoPath string, plan githubVerificationPlan, considerations []string, now time.Time) (*githubRepoProfile, string, error) {
	profile, err := generateGithubRepoProfile(repoSlug, repoPath, plan, considerations, now)
	if err != nil {
		return nil, "", err
	}
	if !validRepoSlug(repoSlug) {
		return &profile, "", nil
	}
	path := githubRepoProfilePath(repoSlug)
	if err := writeGithubJSON(path, profile); err != nil {
		return nil, "", err
	}
	return &profile, path, nil
}

func generateGithubRepoProfile(repoSlug string, repoPath string, plan githubVerificationPlan, considerations []string, now time.Time) (githubRepoProfile, error) {
	profile := githubRepoProfile{
		Version:                 1,
		GeneratedAt:             now.Format(time.RFC3339),
		RepoSlug:                repoSlug,
		RepoPath:                repoPath,
		VerificationPlan:        &plan,
		SuggestedConsiderations: append([]string{}, considerations...),
	}
	if commitStyle := detectGithubCommitStyle(repoPath); commitStyle != nil {
		profile.CommitStyle = commitStyle
		profile.Evidence = append(profile.Evidence, commitStyle.Evidence...)
		if commitStyle.Kind == "generic" && commitStyle.Confidence > 0 && commitStyle.Confidence < 0.6 {
			profile.Warnings = append(profile.Warnings, "mixed commit history detected; repo-native commit shaping disabled")
		}
	}
	if template := detectGithubPullRequestTemplate(repoPath); template != nil {
		profile.PullRequestTemplate = template
		profile.Evidence = append(profile.Evidence, template.Evidence...)
		if warning := githubPullRequestTemplateWarning(template); warning != "" {
			profile.Warnings = append(profile.Warnings, warning)
		}
	} else {
		profile.Warnings = append(profile.Warnings, "no pull request template detected")
	}
	if codeowners := detectGithubCodeowners(repoPath); codeowners != nil {
		profile.Codeowners = codeowners
		profile.Evidence = append(profile.Evidence, codeowners.Evidence...)
	}
	profile.WorkflowFiles = detectGithubWorkflowFiles(repoPath)
	if len(profile.WorkflowFiles) > 0 {
		profile.Evidence = append(profile.Evidence, fmt.Sprintf("workflow files detected: %s", strings.Join(profile.WorkflowFiles, ", ")))
	} else {
		profile.Warnings = append(profile.Warnings, "no GitHub workflow files detected")
	}
	profile.StyleSignals = detectGithubStyleSignals(repoPath)
	if len(profile.StyleSignals) > 0 {
		profile.Evidence = append(profile.Evidence, fmt.Sprintf("style signals detected: %s", strings.Join(profile.StyleSignals, ", ")))
	}
	if rules := detectGithubReviewRulesSummary(repoSlug); rules != nil {
		profile.ReviewRules = rules
		profile.Evidence = append(profile.Evidence, rules.Evidence...)
	} else if validRepoSlug(repoSlug) {
		profile.Warnings = append(profile.Warnings, "no repo review-rules document detected")
	}
	fingerprintInput := []string{profile.RepoSlug, profile.RepoPath, plan.PlanFingerprint, strings.Join(profile.SuggestedConsiderations, ","), strings.Join(profile.WorkflowFiles, ","), strings.Join(profile.StyleSignals, ",")}
	if profile.CommitStyle != nil {
		fingerprintInput = append(fingerprintInput, profile.CommitStyle.Kind, fmt.Sprintf("%.2f", profile.CommitStyle.Confidence))
	}
	if profile.PullRequestTemplate != nil {
		fingerprintInput = append(fingerprintInput, profile.PullRequestTemplate.Path, strings.Join(profile.PullRequestTemplate.Sections, ","))
	}
	if profile.Codeowners != nil {
		fingerprintInput = append(fingerprintInput, profile.Codeowners.Path)
	}
	if profile.ReviewRules != nil {
		fingerprintInput = append(fingerprintInput, fmt.Sprintf("%d/%d/%d/%d", profile.ReviewRules.ApprovedCount, profile.ReviewRules.PendingCount, profile.ReviewRules.DisabledCount, profile.ReviewRules.ArchivedCount))
	}
	sum := sha256.Sum256([]byte(strings.Join(fingerprintInput, "\n")))
	profile.Fingerprint = hex.EncodeToString(sum[:])
	return profile, nil
}

func detectGithubCommitStyle(repoPath string) *githubCommitStyleProfile {
	output, err := githubGitOutput(repoPath, "log", "--format=%s", "-n", "20")
	if err != nil {
		return &githubCommitStyleProfile{
			Kind:       "generic",
			Confidence: 0,
			Evidence:   []string{"commit style unavailable: no git history"},
		}
	}
	lines := []string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return &githubCommitStyleProfile{
			Kind:       "generic",
			Confidence: 0,
			Evidence:   []string{"commit style unavailable: empty history"},
		}
	}
	conventionalRe := regexp.MustCompile(`^[a-z]+(\([^)]+\))?!?: `)
	conventional := 0
	for _, line := range lines {
		if conventionalRe.MatchString(line) {
			conventional++
		}
	}
	confidence := float64(conventional) / float64(len(lines))
	kind := "generic"
	if confidence >= 0.6 {
		kind = "conventional"
	}
	return &githubCommitStyleProfile{
		Kind:       kind,
		Confidence: confidence,
		Evidence: []string{
			fmt.Sprintf("commit style scan: %d/%d recent subjects matched conventional prefixes", conventional, len(lines)),
		},
	}
}

func detectGithubPullRequestTemplate(repoPath string) *githubPullRequestTemplateProfile {
	candidates := []string{
		filepath.Join(repoPath, ".github", "PULL_REQUEST_TEMPLATE.md"),
		filepath.Join(repoPath, ".github", "pull_request_template.md"),
		filepath.Join(repoPath, "PULL_REQUEST_TEMPLATE.md"),
		filepath.Join(repoPath, "docs", "PULL_REQUEST_TEMPLATE.md"),
	}
	for _, path := range candidates {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		sections := []string{}
		for _, line := range strings.Split(string(content), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "## ") {
				sections = append(sections, strings.TrimSpace(strings.TrimPrefix(line, "## ")))
			}
		}
		rel, _ := filepath.Rel(repoPath, path)
		return &githubPullRequestTemplateProfile{
			Path:     filepath.ToSlash(rel),
			Sections: sections,
			Evidence: []string{fmt.Sprintf("pull request template detected: %s", filepath.ToSlash(rel))},
		}
	}
	return nil
}

func githubPullRequestTemplateWarning(template *githubPullRequestTemplateProfile) string {
	if template == nil {
		return ""
	}
	if len(template.Sections) == 0 {
		return "pull request template has no recognized markdown sections"
	}
	recognized := 0
	for _, section := range template.Sections {
		switch strings.ToLower(strings.TrimSpace(section)) {
		case "summary", "changes", "validation", "checklist", "related":
			recognized++
		}
	}
	if recognized == 0 {
		return "pull request template sections are unsupported; repo-native PR shaping disabled"
	}
	if recognized < len(template.Sections) {
		return "pull request template has unsupported sections; unsupported sections will be omitted"
	}
	return ""
}

func githubPullRequestTemplateSupported(template *githubPullRequestTemplateProfile) bool {
	return githubPullRequestTemplateWarning(template) == "" || strings.Contains(githubPullRequestTemplateWarning(template), "unsupported sections will be omitted")
}

func detectGithubCodeowners(repoPath string) *githubCodeownersProfile {
	candidates := []string{
		filepath.Join(repoPath, ".github", "CODEOWNERS"),
		filepath.Join(repoPath, "CODEOWNERS"),
		filepath.Join(repoPath, "docs", "CODEOWNERS"),
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			rel, _ := filepath.Rel(repoPath, path)
			return &githubCodeownersProfile{
				Path:     filepath.ToSlash(rel),
				Evidence: []string{fmt.Sprintf("codeowners detected: %s", filepath.ToSlash(rel))},
			}
		}
	}
	return nil
}

func detectGithubWorkflowFiles(repoPath string) []string {
	files := []string{}
	workflowRoot := filepath.Join(repoPath, ".github", "workflows")
	entries, err := os.ReadDir(workflowRoot)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		files = append(files, filepath.ToSlash(filepath.Join(".github", "workflows", entry.Name())))
	}
	slices.Sort(files)
	return files
}

func detectGithubStyleSignals(repoPath string) []string {
	signals := []string{}
	candidates := []string{
		"gofmt",
		"biome.json",
		".eslintrc",
		".editorconfig",
		"ruff.toml",
		".golangci.yml",
		".golangci.yaml",
	}
	for _, candidate := range candidates {
		if candidate == "gofmt" {
			if _, err := os.Stat(filepath.Join(repoPath, "go.mod")); err == nil {
				signals = append(signals, "go.mod")
			}
			continue
		}
		if _, err := os.Stat(filepath.Join(repoPath, candidate)); err == nil {
			signals = append(signals, candidate)
		}
	}
	return uniqueStrings(signals)
}

func detectGithubReviewRulesSummary(repoSlug string) *githubReviewRulesSummary {
	if !validRepoSlug(repoSlug) {
		return nil
	}
	path := filepath.Join(githubNanaHome(), "repos", filepath.FromSlash(repoSlug), "source", ".nana", "repo-review-rules.json")
	var document githubReviewRuleDocument
	if err := readGithubJSON(path, &document); err != nil {
		return nil
	}
	return &githubReviewRulesSummary{
		Path:          path,
		ApprovedCount: len(document.ApprovedRules),
		PendingCount:  len(document.PendingCandidates),
		DisabledCount: len(document.DisabledRules),
		ArchivedCount: len(document.ArchivedRules),
		Evidence: []string{
			fmt.Sprintf("review rules detected: approved=%d pending=%d disabled=%d archived=%d", len(document.ApprovedRules), len(document.PendingCandidates), len(document.DisabledRules), len(document.ArchivedRules)),
		},
	}
}

func buildGithubControlPlaneReviewers(manifest githubWorkonManifest, reviewerOverride string, apiBaseURL string, token string) ([]string, error) {
	override := strings.TrimSpace(strings.TrimPrefix(reviewerOverride, "@"))
	if override != "" {
		return []string{strings.ToLower(override)}, nil
	}
	if manifest.Policy != nil && manifest.Policy.FeedbackSource == githubFeedbackSourceAnyHuman {
		return []string{"*"}, nil
	}
	if manifest.Policy != nil && manifest.Policy.FeedbackSource == githubFeedbackSourceManual {
		if reviewer := strings.TrimSpace(strings.TrimPrefix(manifest.ReviewReviewer, "@")); reviewer != "" {
			return []string{strings.ToLower(reviewer)}, nil
		}
	}
	if len(manifest.ControlPlaneReviewers) > 0 {
		return uniqueStrings(cleanLogins(manifest.ControlPlaneReviewers)), nil
	}
	actors := []string{}
	reviewerPolicy := normalizeGithubReviewerPolicy(manifest.EffectiveReviewerPolicy)
	if manifest.Policy == nil || manifest.Policy.FeedbackSource != githubFeedbackSourceAnyHuman {
		actors = append(actors, reviewerPolicy.GetTrusted()...)
		assigned, err := fetchGithubAssignedControlPlaneReviewers(manifest, apiBaseURL, token)
		if err != nil {
			return nil, err
		}
		actors = append(actors, assigned...)
	}
	actors = uniqueStrings(cleanLogins(actors))
	blocked := map[string]bool{}
	for _, login := range reviewerPolicy.GetBlocked() {
		blocked[strings.ToLower(login)] = true
	}
	filtered := []string{}
	for _, actor := range actors {
		if !blocked[strings.ToLower(actor)] {
			filtered = append(filtered, actor)
		}
	}
	return filtered, nil
}

func fetchGithubAssignedControlPlaneReviewers(manifest githubWorkonManifest, apiBaseURL string, token string) ([]string, error) {
	actors := []string{}
	issueNumber := manifest.TargetNumber
	if issueNumber > 0 {
		var issue struct {
			Assignees []githubActor `json:"assignees"`
		}
		if err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s/issues/%d", manifest.RepoSlug, issueNumber), &issue); err == nil {
			for _, assignee := range issue.Assignees {
				actors = append(actors, assignee.Login)
			}
		}
	}
	prNumber := 0
	if manifest.TargetKind == "pr" {
		prNumber = manifest.TargetNumber
	} else if manifest.PublishedPRNumber > 0 {
		prNumber = manifest.PublishedPRNumber
	}
	if prNumber > 0 {
		var requested struct {
			Users []githubActor `json:"users"`
		}
		if err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s/pulls/%d/requested_reviewers", manifest.RepoSlug, prNumber), &requested); err == nil {
			for _, user := range requested.Users {
				actors = append(actors, user.Login)
			}
		}
	}
	return uniqueStrings(cleanLogins(actors)), nil
}

func determineGithubHumanGateState(policy *githubResolvedWorkOnPolicy, createPR bool) (bool, string, string) {
	if policy == nil {
		return false, "", "continue"
	}
	switch policy.HumanGate {
	case githubHumanGateAlways:
		return true, "policy requires GitHub human feedback before completion", "wait_for_github_feedback"
	case githubHumanGatePublishTime:
		if createPR {
			return true, "policy requires GitHub human feedback after publication", "wait_for_github_feedback"
		}
	}
	return false, "", "continue"
}

func buildGithubRuntimeContextLines(manifest githubWorkonManifest) []string {
	lines := []string{}
	if manifest.Policy != nil {
		lines = append(lines,
			fmt.Sprintf("Policy: experimental=%t feedback_source=%s repo_native=%s human_gate=%s.", manifest.Policy.Experimental, manifest.Policy.FeedbackSource, manifest.Policy.RepoNativeStrictness, manifest.Policy.HumanGate),
			fmt.Sprintf("Allowed actions: commit=%t push=%t open_draft_pr=%t request_review=%t merge=%t.", manifest.Policy.AllowedActions.Commit, manifest.Policy.AllowedActions.Push, manifest.Policy.AllowedActions.OpenDraftPR, manifest.Policy.AllowedActions.RequestReview, manifest.Policy.AllowedActions.Merge),
		)
	}
	if manifest.RepoProfile != nil {
		if manifest.RepoProfile.CommitStyle != nil {
			lines = append(lines, fmt.Sprintf("Repo commit style: %s (confidence %.2f).", manifest.RepoProfile.CommitStyle.Kind, manifest.RepoProfile.CommitStyle.Confidence))
		}
		if manifest.RepoProfile.PullRequestTemplate != nil {
			lines = append(lines, fmt.Sprintf("Repo PR template: %s.", manifest.RepoProfile.PullRequestTemplate.Path))
		}
		if manifest.RepoProfile.ReviewRules != nil {
			lines = append(lines, fmt.Sprintf("Repo review rules: approved=%d pending=%d.", manifest.RepoProfile.ReviewRules.ApprovedCount, manifest.RepoProfile.ReviewRules.PendingCount))
		}
	}
	if len(manifest.ControlPlaneReviewers) > 0 {
		lines = append(lines, fmt.Sprintf("GitHub human control plane: %s.", formatGithubActorSet(manifest.ControlPlaneReviewers)))
	}
	if len(manifest.IgnoredFeedbackActors) > 0 {
		lines = append(lines, fmt.Sprintf("Ignored feedback actors: %s.", formatGithubIgnoredActorReasons(manifest.IgnoredFeedbackActors)))
	}
	if strings.TrimSpace(manifest.ReviewRequestState) != "" && manifest.ReviewRequestState != "not_requested" {
		lines = append(lines, fmt.Sprintf("Review request state: %s.", manifest.ReviewRequestState))
		if len(manifest.RequestedReviewers) > 0 {
			lines = append(lines, fmt.Sprintf("Requested reviewers: %s.", formatGithubActorSet(manifest.RequestedReviewers)))
		}
		if strings.TrimSpace(manifest.ReviewRequestError) != "" {
			lines = append(lines, fmt.Sprintf("Review request error: %s.", manifest.ReviewRequestError))
		}
	}
	if strings.TrimSpace(manifest.MergeState) != "" && manifest.MergeState != "not_attempted" {
		lines = append(lines, fmt.Sprintf("Merge state: %s.", manifest.MergeState))
		if strings.TrimSpace(manifest.MergeError) != "" {
			lines = append(lines, fmt.Sprintf("Merge error: %s.", manifest.MergeError))
		}
		if strings.TrimSpace(manifest.MergedSHA) != "" {
			lines = append(lines, fmt.Sprintf("Merged SHA: %s.", manifest.MergedSHA))
		}
	}
	if manifest.NeedsHuman {
		lines = append(lines, fmt.Sprintf("Current human gate: %s.", defaultString(manifest.NeedsHumanReason, "waiting for GitHub feedback")))
	}
	return lines
}

func buildGithubExplainPayload(manifest githubWorkonManifest) githubExplainPayload {
	return githubExplainPayload{
		RunID:                 manifest.RunID,
		RepoSlug:              manifest.RepoSlug,
		TargetURL:             manifest.TargetURL,
		Policy:                manifest.Policy,
		RepoProfilePath:       manifest.RepoProfilePath,
		RepoProfile:           manifest.RepoProfile,
		ControlPlaneReviewers: append([]string{}, manifest.ControlPlaneReviewers...),
		IgnoredFeedbackActors: cloneGithubIgnoredActorMap(manifest.IgnoredFeedbackActors),
		RequestedReviewers:    append([]string{}, manifest.RequestedReviewers...),
		ReviewRequestState:    manifest.ReviewRequestState,
		ReviewRequestError:    manifest.ReviewRequestError,
		MergeState:            manifest.MergeState,
		MergeError:            manifest.MergeError,
		MergeMethod:           manifest.MergeMethod,
		MergedPRNumber:        manifest.MergedPRNumber,
		MergedSHA:             manifest.MergedSHA,
		NeedsHuman:            manifest.NeedsHuman,
		NeedsHumanReason:      manifest.NeedsHumanReason,
		NextAction:            manifest.NextAction,
	}
}

func profileFingerprint(profile *githubRepoProfile) string {
	if profile == nil {
		return ""
	}
	return profile.Fingerprint
}

func hydrateGithubWorkonManifestDefaults(manifest *githubWorkonManifest) {
	if manifest == nil {
		return
	}
	if manifest.Policy == nil {
		policy := defaultGithubResolvedWorkOnPolicy()
		manifest.Policy = &policy
	}
	if len(manifest.ControlPlaneReviewers) == 0 && strings.TrimSpace(manifest.ReviewReviewer) != "" {
		reviewer := strings.TrimSpace(strings.TrimPrefix(manifest.ReviewReviewer, "@"))
		if reviewer != "" && reviewer != "me" {
			manifest.ControlPlaneReviewers = []string{strings.ToLower(reviewer)}
		}
	}
	if manifest.RepoProfile != nil && strings.TrimSpace(manifest.RepoProfileFingerprint) == "" {
		manifest.RepoProfileFingerprint = manifest.RepoProfile.Fingerprint
	}
	if strings.TrimSpace(manifest.NextAction) == "" {
		manifest.NextAction = "continue"
		if manifest.NeedsHuman {
			manifest.NextAction = "wait_for_github_feedback"
		}
	}
	if strings.TrimSpace(manifest.ReviewRequestState) == "" {
		manifest.ReviewRequestState = "not_requested"
	}
	if strings.TrimSpace(manifest.MergeState) == "" {
		manifest.MergeState = "not_attempted"
	}
	if strings.TrimSpace(manifest.MergeMethod) == "" {
		manifest.MergeMethod = githubEffectiveMergeMethod(manifest.Policy)
	}
}

func githubEffectiveMergeMethod(policy *githubResolvedWorkOnPolicy) string {
	if policy == nil {
		return "squash"
	}
	if method := normalizeGithubMergeMethod(policy.MergeMethod); method != "" {
		return method
	}
	return "squash"
}

func formatGithubActorSet(reviewers []string) string {
	if len(reviewers) == 0 {
		return "(none)"
	}
	formatted := make([]string, 0, len(reviewers))
	for _, reviewer := range reviewers {
		reviewer = strings.TrimSpace(strings.TrimPrefix(reviewer, "@"))
		if reviewer == "*" {
			return "any non-author human"
		}
		if reviewer == "" {
			continue
		}
		formatted = append(formatted, "@"+reviewer)
	}
	if len(formatted) == 0 {
		return "(none)"
	}
	return strings.Join(formatted, ", ")
}

func cloneGithubIgnoredActorMap(values map[string]int) map[string]int {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]int, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func formatGithubIgnoredActorReasons(values map[string]int) string {
	if len(values) == 0 {
		return "(none)"
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, values[key]))
	}
	return strings.Join(parts, ", ")
}
