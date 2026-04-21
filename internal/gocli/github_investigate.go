package gocli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

var githubHotPathTokenSplitPattern = regexp.MustCompile(`[^A-Za-z0-9]+`)

type githubManagedRepoPaths struct {
	RepoRoot                 string
	SourcePath               string
	RepoMetaPath             string
	RepoSettingsPath         string
	RepoVerificationPlanPath string
	RepoProfilePath          string
	StartStatePath           string
	ReviewRulesPath          string
	PlannedRunsDir           string
	ReviewsRoot              string
}

type githubManagedRepoMetadata struct {
	Version            int    `json:"version"`
	RepoName           string `json:"repo_name"`
	RepoSlug           string `json:"repo_slug"`
	RepoOwner          string `json:"repo_owner"`
	CloneURL           string `json:"clone_url"`
	CanonicalOriginURL string `json:"canonical_origin_url,omitempty"`
	DefaultBranch      string `json:"default_branch"`
	HTMLURL            string `json:"html_url"`
	RepoRoot           string `json:"repo_root"`
	SourcePath         string `json:"source_path"`
	UpdatedAt          string `json:"updated_at"`
}

type githubRepositoryPayload struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
}

type githubIssuePayload struct {
	Title       string        `json:"title"`
	Body        string        `json:"body,omitempty"`
	State       string        `json:"state"`
	Labels      []githubLabel `json:"labels,omitempty"`
	User        githubActor   `json:"user"`
	PullRequest *struct {
		URL string `json:"url"`
	} `json:"pull_request,omitempty"`
}

type githubTargetContext struct {
	Repository githubRepositoryPayload
	Issue      githubIssuePayload
}

type githubConsiderationInference struct {
	Considerations []string
}

var githubManagedOriginPreflight = preflightGithubManagedOriginAccess

func githubInvestigateTarget(targetURL string) error {
	target, err := parseGithubTargetURL(targetURL)
	if err != nil {
		return err
	}
	if target.kind != "issue" {
		return fmt.Errorf("nana issue investigate expects a GitHub issue URL.\n%s", IssueHelp)
	}

	apiBaseURL := strings.TrimSpace(os.Getenv("GITHUB_API_URL"))
	if apiBaseURL == "" {
		apiBaseURL = "https://api.github.com"
	}
	token, err := resolveGithubToken()
	if err != nil {
		return err
	}

	context, err := githubFetchTargetContext(target, apiBaseURL, token)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	paths := githubManagedPaths(target.repoSlug)
	repoMeta, err := ensureGithubManagedRepoMetadata(paths, context, now)
	if err != nil {
		return err
	}
	sourceLockOwner := repoAccessLockOwner{
		Backend: "github-investigate",
		RunID:   fmt.Sprintf("investigate-%d", now.UnixNano()),
		Purpose: "source-inspect",
		Label:   "github-investigate-source",
	}
	prepared, err := prepareGithubInvestigateSource(paths, repoMeta, sourceLockOwner, now, nil)
	if err != nil {
		return err
	}
	verificationPlan := prepared.RepoVerificationPlan
	settings := prepared.Settings
	profile := prepared.Profile
	profilePath := prepared.ProfilePath
	policy := prepared.Policy

	considerations := settings.DefaultConsiderations
	roleLayout := settings.DefaultRoleLayout
	reviewRulesMode := settings.ReviewRulesMode
	if reviewRulesMode == "" {
		globalConfig, _ := readGithubReviewRulesGlobalConfig()
		if globalConfig != nil && globalConfig.DefaultMode != "" {
			reviewRulesMode = globalConfig.DefaultMode
		} else {
			reviewRulesMode = "manual"
		}
	}

	fmt.Fprintf(currentGithubStdout(), "[github] Investigated %s %s #%d\n", repoMeta.RepoSlug, target.kind, target.number)
	fmt.Fprintf(currentGithubStdout(), "[github] Title: %s\n", context.Issue.Title)
	fmt.Fprintf(currentGithubStdout(), "[github] Managed repo root: %s\n", paths.RepoRoot)
	fmt.Fprintf(currentGithubStdout(), "[github] Source path: %s\n", paths.SourcePath)
	fmt.Fprintf(currentGithubStdout(), "[github] Default branch: %s\n", repoMeta.DefaultBranch)
	fmt.Fprintf(currentGithubStdout(), "[github] Suggested considerations: %s\n", joinOrNone(considerations))
	fmt.Fprintf(currentGithubStdout(), "[github] Suggested role layout: %s\n", defaultString(roleLayout, "split"))
	fmt.Fprintf(currentGithubStdout(), "[github] Review-rules mode: %s\n", reviewRulesMode)
	fmt.Fprintf(currentGithubStdout(), "[github] Work-on policy: experimental=%t feedback_source=%s repo_native=%s human_gate=%s\n", policy.Experimental, policy.FeedbackSource, policy.RepoNativeStrictness, policy.HumanGate)
	if profile != nil {
		fmt.Fprintf(currentGithubStdout(), "[github] Repo profile fingerprint: %s\n", profile.Fingerprint)
		if profilePath != "" {
			fmt.Fprintf(currentGithubStdout(), "[github] Repo profile path: %s\n", profilePath)
		}
		if profile.CommitStyle != nil {
			fmt.Fprintf(currentGithubStdout(), "[github] Repo commit style: %s (confidence %.2f)\n", profile.CommitStyle.Kind, profile.CommitStyle.Confidence)
		}
		if profile.PullRequestTemplate != nil {
			fmt.Fprintf(currentGithubStdout(), "[github] Repo PR template: %s\n", profile.PullRequestTemplate.Path)
		}
	}
	fmt.Fprintf(
		currentGithubStdout(),
		"[github] Verification plan: lint=%d compile=%d unit=%d integration=%d benchmark=%d\n",
		len(verificationPlan.Lint),
		len(verificationPlan.Compile),
		len(verificationPlan.Unit),
		len(verificationPlan.Integration),
		len(verificationPlan.Benchmarks),
	)
	for _, warning := range verificationPlan.Warnings {
		fmt.Fprintf(currentGithubStdout(), "[github] Verification warning: %s\n", warning)
	}
	if settings.HotPathAPIProfile != nil {
		fmt.Fprintf(currentGithubStdout(), "[github] Hot-path API files: %s\n", joinOrNone(settings.HotPathAPIProfile.HotPathAPIFiles))
		fmt.Fprintf(currentGithubStdout(), "[github] Hot-path API tokens: %s\n", joinOrNone(settings.HotPathAPIProfile.APIIdentifierTokens))
	} else {
		fmt.Fprintf(currentGithubStdout(), "[github] Hot-path API files: (none detected)\n")
		fmt.Fprintf(currentGithubStdout(), "[github] Hot-path API tokens: (none detected)\n")
	}
	fmt.Fprintln(currentGithubStdout(), "[github] Suggested pipeline:")
	for _, line := range buildGithubConsiderationInstructionLines(considerations, roleLayout) {
		fmt.Fprintln(currentGithubStdout(), line)
	}
	fmt.Fprintf(currentGithubStdout(), "[github] Next: nana implement %s\n", githubCanonicalTargetURL(target))
	return nil
}

func prepareGithubInvestigateSource(paths githubManagedRepoPaths, repoMeta *githubManagedRepoMetadata, owner repoAccessLockOwner, now time.Time, observeReadPhase func(sourcePath string) error) (githubPreparedManagedSource, error) {
	return inspectGithubManagedSource(paths, repoMeta, owner, now, observeReadPhase, nil)
}

func githubManagedPaths(repoSlug string) githubManagedRepoPaths {
	repoRoot := githubWorkRepoRoot(repoSlug)
	sourcePath := filepath.Join(repoRoot, "source")
	return githubManagedRepoPaths{
		RepoRoot:                 repoRoot,
		SourcePath:               sourcePath,
		RepoMetaPath:             filepath.Join(repoRoot, "repo.json"),
		RepoSettingsPath:         filepath.Join(repoRoot, "settings.json"),
		RepoVerificationPlanPath: filepath.Join(repoRoot, "verification-plan.json"),
		RepoProfilePath:          filepath.Join(repoRoot, "repo-profile.json"),
		StartStatePath:           filepath.Join(repoRoot, "start-state.json"),
		ReviewRulesPath:          filepath.Join(sourcePath, ".nana", "repo-review-rules.json"),
		PlannedRunsDir:           filepath.Join(repoRoot, "planned-runs"),
		ReviewsRoot:              filepath.Join(repoRoot, "reviews"),
	}
}

func githubFetchTargetContext(target parsedGithubTarget, apiBaseURL string, token string) (githubTargetContext, error) {
	var repository githubRepositoryPayload
	if err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s", target.repoSlug), &repository); err != nil {
		return githubTargetContext{}, err
	}
	var issue githubIssuePayload
	if err := githubAPIGetJSON(apiBaseURL, token, fmt.Sprintf("/repos/%s/issues/%d", target.repoSlug, target.number), &issue); err != nil {
		return githubTargetContext{}, err
	}
	if target.kind == "issue" && issue.PullRequest != nil {
		return githubTargetContext{}, fmt.Errorf("Target %s#%d is a pull request. Use its pull request URL instead.", target.repoSlug, target.number)
	}
	return githubTargetContext{Repository: repository, Issue: issue}, nil
}

func ensureGithubManagedRepoMetadata(paths githubManagedRepoPaths, target githubTargetContext, now time.Time) (*githubManagedRepoMetadata, error) {
	var existing githubManagedRepoMetadata
	if err := readGithubJSON(paths.RepoMetaPath, &existing); err == nil {
		normalizeGithubManagedRepoMetadata(&existing)
		if !strings.EqualFold(strings.TrimSpace(existing.RepoSlug), strings.TrimSpace(target.Repository.FullName)) {
			return nil, fmt.Errorf("Managed repo path collision at %s: expected %s, found %s.", paths.RepoRoot, target.Repository.FullName, existing.RepoSlug)
		}
	}
	parts := strings.SplitN(target.Repository.FullName, "/", 2)
	repoOwner := ""
	if len(parts) > 0 {
		repoOwner = parts[0]
	}
	metadata := &githubManagedRepoMetadata{
		Version:            2,
		RepoName:           target.Repository.Name,
		RepoSlug:           target.Repository.FullName,
		RepoOwner:          repoOwner,
		CloneURL:           target.Repository.CloneURL,
		CanonicalOriginURL: canonicalGithubSSHRemote(target.Repository.FullName, target.Repository.HTMLURL),
		DefaultBranch:      target.Repository.DefaultBranch,
		HTMLURL:            target.Repository.HTMLURL,
		RepoRoot:           paths.RepoRoot,
		SourcePath:         paths.SourcePath,
		UpdatedAt:          now.Format(time.RFC3339),
	}
	normalizeGithubManagedRepoMetadata(metadata)
	if err := writeGithubJSON(paths.RepoMetaPath, metadata); err != nil {
		return nil, err
	}
	return metadata, nil
}

func ensureGithubSourceClone(paths githubManagedRepoPaths, repoMeta *githubManagedRepoMetadata) error {
	normalizeGithubManagedRepoMetadata(repoMeta)
	cloneURL := strings.TrimSpace(repoMeta.CloneURL)
	if cloneURL == "" {
		cloneURL = githubManagedCanonicalOriginURL(repoMeta)
	}
	if _, err := os.Stat(paths.SourcePath); os.IsNotExist(err) {
		if err := githubRunGit("", "clone", cloneURL, paths.SourcePath); err != nil {
			return err
		}
	}
	if err := ensureGithubManagedOrigin(paths.SourcePath, repoMeta); err != nil {
		return err
	}
	if err := githubManagedOriginPreflight(paths.SourcePath, repoMeta); err != nil {
		return err
	}
	return githubRunGit(paths.SourcePath, "fetch", "--prune", "origin")
}

func normalizeGithubManagedRepoMetadata(metadata *githubManagedRepoMetadata) {
	if metadata == nil {
		return
	}
	if metadata.Version == 0 {
		metadata.Version = 2
	}
	if strings.TrimSpace(metadata.CanonicalOriginURL) == "" && strings.TrimSpace(metadata.RepoSlug) != "" {
		metadata.CanonicalOriginURL = canonicalGithubSSHRemote(metadata.RepoSlug, metadata.HTMLURL)
	}
}

func githubManagedCanonicalOriginURL(metadata *githubManagedRepoMetadata) string {
	if metadata == nil {
		return ""
	}
	normalizeGithubManagedRepoMetadata(metadata)
	return strings.TrimSpace(metadata.CanonicalOriginURL)
}

func canonicalGithubSSHRemote(repoSlug string, htmlURL string) string {
	repoSlug = strings.TrimSpace(repoSlug)
	if repoSlug == "" {
		return ""
	}
	host := "github.com"
	if parsed, err := url.Parse(strings.TrimSpace(htmlURL)); err == nil && strings.TrimSpace(parsed.Host) != "" {
		host = strings.TrimSpace(parsed.Host)
	}
	if strings.Contains(host, ":") {
		return fmt.Sprintf("ssh://git@%s/%s.git", host, repoSlug)
	}
	return fmt.Sprintf("git@%s:%s.git", host, repoSlug)
}

func ensureGithubManagedOrigin(repoPath string, repoMeta *githubManagedRepoMetadata) error {
	canonical := githubManagedCanonicalOriginURL(repoMeta)
	if canonical == "" {
		return fmt.Errorf("managed source checkout %s is missing a canonical origin", repoPath)
	}
	currentOrigin, err := githubGitOutput(repoPath, "remote", "get-url", "origin")
	if err != nil {
		if err := githubRunGit(repoPath, "remote", "add", "origin", canonical); err != nil {
			return err
		}
		fmt.Fprintf(currentGithubStdout(), "[github] Repaired managed source origin for %s to %s.\n", repoPath, canonical)
		return nil
	}
	current := strings.TrimSpace(currentOrigin)
	if current != canonical {
		if err := githubRunGit(repoPath, "remote", "set-url", "origin", canonical); err != nil {
			return err
		}
		fmt.Fprintf(currentGithubStdout(), "[github] Repaired managed source origin for %s to %s.\n", repoPath, canonical)
	}
	pushOrigin, pushErr := githubGitOutput(repoPath, "remote", "get-url", "--push", "origin")
	if pushErr == nil && strings.TrimSpace(pushOrigin) == canonical {
		return nil
	}
	return githubRunGit(repoPath, "remote", "set-url", "--push", "origin", canonical)
}

func preflightGithubManagedOriginAccess(repoPath string, repoMeta *githubManagedRepoMetadata) error {
	canonical := githubManagedCanonicalOriginURL(repoMeta)
	if canonical == "" {
		return fmt.Errorf("managed source checkout %s is missing a canonical origin", repoPath)
	}
	if githubManagedCloneURLIsLocal(repoMeta) {
		return nil
	}
	if _, err := githubGitOutput(repoPath, "ls-remote", "--exit-code", "origin", "HEAD"); err != nil {
		return fmt.Errorf("managed source checkout %s requires working SSH access to %s; verify GitHub SSH keys/agent on this machine: %w", repoPath, canonical, err)
	}
	return nil
}

func githubManagedCloneURLIsLocal(repoMeta *githubManagedRepoMetadata) bool {
	if repoMeta == nil {
		return false
	}
	raw := strings.TrimSpace(repoMeta.CloneURL)
	if raw == "" {
		return false
	}
	if parsed, err := url.Parse(raw); err == nil {
		switch parsed.Scheme {
		case "file":
			return true
		case "http", "https", "ssh", "git":
			return false
		}
	}
	if filepath.IsAbs(raw) {
		return true
	}
	if info, err := os.Stat(raw); err == nil && (info.IsDir() || info.Mode().IsRegular()) {
		return true
	}
	return false
}

func githubRunGit(cwd string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = githubGitEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return nil
}

func githubGitOutput(cwd string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	cmd.Env = githubGitEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output), nil
}

func githubGitEnv() []string {
	env := append([]string{}, os.Environ()...)
	ensure := func(key string, value string) {
		for _, existing := range env {
			if strings.HasPrefix(existing, key+"=") {
				return
			}
		}
		env = append(env, key+"="+value)
	}
	ensure("GIT_AUTHOR_NAME", "Nana")
	ensure("GIT_AUTHOR_EMAIL", "nana@example.invalid")
	ensure("GIT_COMMITTER_NAME", "Nana")
	ensure("GIT_COMMITTER_EMAIL", "nana@example.invalid")
	return env
}

func readMakefileTargets(path string) (map[string]bool, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	targets := map[string]bool{}
	re := regexp.MustCompile(`(?m)^([A-Za-z0-9_.-]+):`)
	for _, match := range re.FindAllStringSubmatch(string(content), -1) {
		target := strings.TrimSpace(match[1])
		if target != "" && !strings.HasPrefix(target, ".") {
			targets[target] = true
		}
	}
	return targets, nil
}

func inferGithubInitialRepoConsiderations(repoCheckoutPath string, repoSlug string, verificationPlan githubVerificationPlan) githubConsiderationInference {
	return inferGithubInitialRepoConsiderationsFromFiles(trackedRepoFiles(repoCheckoutPath), repoSlug, verificationPlan)
}

func inferGithubInitialRepoConsiderationsFromFiles(files []string, repoSlug string, verificationPlan githubVerificationPlan) githubConsiderationInference {
	lowerPaths := make([]string, 0, len(files))
	for _, path := range files {
		lowerPaths = append(lowerPaths, strings.ToLower(path))
	}
	considerations := map[string]bool{}
	if len(verificationPlan.Unit) > 0 || len(verificationPlan.Integration) > 0 || hasPathMatch(lowerPaths, "test", "__tests__", "spec", "integration") {
		considerations["qa"] = true
	}
	if len(verificationPlan.Lint) > 0 || hasSuffixMatch(lowerPaths, "biome.json", ".eslintrc", ".editorconfig", "ruff.toml") {
		considerations["style"] = true
	}
	if hasSuffixMatch(lowerPaths, "package.json", "pom.xml", "cargo.toml", "go.mod", "pyproject.toml", "requirements.txt") {
		considerations["dependency"] = true
	}
	if hasPathMatch(lowerPaths, "openapi", "swagger", "graphql", "proto", "/api/", "sdk", "client") || strings.Contains(strings.ToLower(repoSlug), "api") {
		considerations["api"] = true
	}
	if hasPathMatch(lowerPaths, "benchmark", "perf", "performance", "latency") {
		considerations["perf"] = true
	}
	if hasPathMatch(lowerPaths, "security", "auth", "policy", "secret", "permission") {
		considerations["security"] = true
	}
	if hasPathMatch(lowerPaths, "architecture", "adr", "module", "schema") {
		considerations["arch"] = true
	}
	if len(considerations) == 0 {
		considerations["qa"] = true
	}
	out := make([]string, 0, len(considerations))
	for item := range considerations {
		out = append(out, item)
	}
	slices.Sort(out)
	return githubConsiderationInference{Considerations: out}
}

func trackedRepoFiles(repoCheckoutPath string) []string {
	output, err := githubGitOutput(repoCheckoutPath, "ls-files")
	if err == nil {
		parts := []string{}
		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				parts = append(parts, line)
			}
		}
		return parts
	}
	files := []string{}
	_ = filepath.Walk(repoCheckoutPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(repoCheckoutPath, path)
		if relErr == nil {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	return files
}

func hasPathMatch(paths []string, needles ...string) bool {
	for _, path := range paths {
		for _, needle := range needles {
			if strings.Contains(path, strings.ToLower(needle)) {
				return true
			}
		}
	}
	return false
}

func hasSuffixMatch(paths []string, suffixes ...string) bool {
	for _, path := range paths {
		for _, suffix := range suffixes {
			if strings.HasSuffix(path, strings.ToLower(suffix)) {
				return true
			}
		}
	}
	return false
}

func inferGithubHotPathProfile(repoCheckoutPath string, now time.Time) *githubHotPathProfile {
	return inferGithubHotPathProfileFromFiles(trackedRepoFiles(repoCheckoutPath), now)
}

func inferGithubHotPathProfileFromFiles(files []string, now time.Time) *githubHotPathProfile {
	apiSurfaceFiles := []string{}
	hotPathFiles := []string{}
	tokens := []string{}
	for _, path := range files {
		lower := strings.ToLower(path)
		if strings.Contains(lower, "openapi") || strings.Contains(lower, "swagger") || strings.Contains(lower, "graphql") || strings.Contains(lower, "proto") || strings.Contains(lower, "/api/") {
			apiSurfaceFiles = append(apiSurfaceFiles, path)
			base := filepath.Base(path)
			base = strings.TrimSuffix(base, filepath.Ext(base))
			for _, token := range githubHotPathTokenSplitPattern.Split(base, -1) {
				token = strings.TrimSpace(token)
				if len(token) >= 3 {
					tokens = append(tokens, token)
				}
			}
			if strings.Contains(lower, "perf") || strings.Contains(lower, "benchmark") {
				hotPathFiles = append(hotPathFiles, path)
			}
		}
	}
	apiSurfaceFiles = uniqueStrings(apiSurfaceFiles)
	hotPathFiles = uniqueStrings(hotPathFiles)
	tokens = uniqueStrings(tokens)
	slices.Sort(apiSurfaceFiles)
	slices.Sort(hotPathFiles)
	slices.Sort(tokens)
	evidence := []string{}
	if len(apiSurfaceFiles) > 0 {
		evidence = append(evidence, fmt.Sprintf("api surface files detected: %s", strings.Join(apiSurfaceFiles, ", ")))
	}
	if len(hotPathFiles) > 0 {
		evidence = append(evidence, fmt.Sprintf("hot-path api files detected: %s", strings.Join(hotPathFiles, ", ")))
	}
	if len(tokens) > 0 {
		evidence = append(evidence, fmt.Sprintf("api identifier tokens extracted: %s", strings.Join(tokens, ", ")))
	}
	return &githubHotPathProfile{
		Version:             1,
		AnalyzedAt:          now.Format(time.RFC3339),
		APISurfaceFiles:     apiSurfaceFiles,
		HotPathAPIFiles:     hotPathFiles,
		APIIdentifierTokens: tokens,
		Evidence:            evidence,
	}
}

func refreshGithubVerificationArtifacts(runID string, useLast bool) error {
	return refreshGithubVerificationArtifactsWithIO(runID, useLast, currentGithubStdout())
}

func refreshGithubVerificationArtifactsWithIO(runID string, useLast bool, stdout io.Writer) error {
	manifestPath, repoRoot, err := resolveGithubRunManifestPath(runID, useLast)
	if err != nil {
		return err
	}
	manifest, err := readGithubWorkManifest(manifestPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(manifest.SandboxPath) == "" || strings.TrimSpace(manifest.SandboxRepoPath) == "" {
		return fmt.Errorf("run %s is missing sandbox paths required for verify-refresh", manifest.RunID)
	}
	sandboxLock, err := acquireSandboxWriteLock(manifest.SandboxRepoPath, repoAccessLockOwner{
		Backend: "github-work",
		RunID:   manifest.RunID,
		Purpose: "verification-refresh",
		Label:   "github-work-verify-refresh",
	})
	if err != nil {
		return err
	}
	defer func() {
		_ = sandboxLock.Release()
	}()
	plan := detectGithubVerificationPlan(manifest.SandboxRepoPath)
	if err := writeGithubJSON(filepath.Join(repoRoot, "verification-plan.json"), plan); err != nil {
		return err
	}
	scriptsDir, err := writeGithubVerificationScripts(manifest.SandboxPath, manifest.SandboxRepoPath, plan, manifest.RunID)
	if err != nil {
		return err
	}
	beforePlan := ""
	if manifest.VerificationPlan != nil {
		beforePlan = manifest.VerificationPlan.PlanFingerprint
	}
	manifest.VerificationPlan = &plan
	manifest.VerificationScriptsDir = scriptsDir
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		return err
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		return err
	}
	status := "already current"
	if beforePlan != plan.PlanFingerprint || beforePlan == "" {
		status = "refreshed"
	}
	fmt.Fprintf(stdout, "[github] Verification artifacts for run %s %s.\n", manifest.RunID, status)
	if len(plan.SourceFiles) > 0 {
		parts := make([]string, 0, len(plan.SourceFiles))
		for _, sourceFile := range plan.SourceFiles {
			parts = append(parts, fmt.Sprintf("%s:%s", sourceFile.Path, sourceFile.Checksum))
		}
		fmt.Fprintf(stdout, "[github] Verification source files: %s\n", strings.Join(parts, ", "))
	}
	if scriptsDir != "" {
		fmt.Fprintf(stdout, "[github] Verification scripts directory: %s\n", scriptsDir)
	}
	return nil
}

func writeGithubVerificationScripts(sandboxPath string, repoCheckoutPath string, plan githubVerificationPlan, runID string) (string, error) {
	return writeVerificationScripts("work", sandboxPath, repoCheckoutPath, plan, []string{"nana", "work", "verify-refresh", "--run-id", runID})
}

func detectGithubVerificationPlan(repoCheckoutPath string) githubVerificationPlan {
	var source string
	var lint, compile, unit, integration, benchmarks, warnings []string
	var sourceFiles []githubVerificationSourceFile
	addSourceFile := func(path string, kind string) {
		checksum, err := checksumFile(path)
		if err != nil {
			return
		}
		rel, err := filepath.Rel(repoCheckoutPath, path)
		if err != nil {
			rel = path
		}
		sourceFiles = append(sourceFiles, githubVerificationSourceFile{
			Path:     filepath.ToSlash(rel),
			Checksum: checksum,
			Kind:     kind,
		})
	}
	makefilePath := filepath.Join(repoCheckoutPath, "Makefile")
	if targets, err := readMakefileTargets(makefilePath); err == nil {
		source = "makefile"
		if targets["lint"] {
			lint = append(lint, "make lint")
		}
		if targets["build"] {
			compile = append(compile, "make build")
		}
		if targets["compile"] {
			compile = append(compile, "make compile")
		}
		if targets["test-unit"] {
			unit = append(unit, "make test-unit")
		} else if targets["test"] {
			unit = append(unit, "make test")
			if targets["test-integration"] {
				warnings = append(warnings, "Repo exposes both `test` and `test-integration`, but no dedicated `test-unit`. Split unit and integration so Nana can run them separately.")
			}
		}
		if targets["test-integration"] {
			integration = append(integration, "make test-integration")
		}
		explicitBenchmarkEntrypoint := false
		switch {
		case targets["test-benchmark"]:
			benchmarks = append(benchmarks, "make test-benchmark")
			explicitBenchmarkEntrypoint = true
		case targets["benchmark"]:
			benchmarks = append(benchmarks, "make benchmark")
			explicitBenchmarkEntrypoint = true
		case targets["test-benchmark-jmh"]:
			benchmarks = append(benchmarks, "make test-benchmark-jmh")
		}
		if !explicitBenchmarkEntrypoint && (targets["test-benchmark-e2e"] || targets["test-benchmark-jmh"] || targets["test-benchmark-jmh-quick"]) {
			warnings = append(warnings, "Repo exposes benchmark targets but no single benchmark entrypoint. Add a dedicated benchmark target so Nana can keep benchmarks separate from unit and integration tests.")
		}
		if len(lint)+len(compile)+len(unit)+len(integration)+len(benchmarks) > 0 {
			addSourceFile(makefilePath, "makefile")
		}
	}
	if source == "" {
		if path := filepath.Join(repoCheckoutPath, "pom.xml"); githubFileExists(path) {
			source = "heuristic"
			compile = []string{"mvn -q -DskipTests compile", "mvn -q test-compile"}
			unit = []string{"mvn -q test"}
			addSourceFile(path, "heuristic")
		} else if path := filepath.Join(repoCheckoutPath, "package.json"); githubFileExists(path) {
			source = "heuristic"
			lint = []string{"npm run lint --if-present"}
			compile = []string{"npm run build --if-present"}
			unit = []string{"npm test -- --runInBand"}
			integration = []string{"npm run test:integration --if-present"}
			addSourceFile(path, "heuristic")
		} else if path := filepath.Join(repoCheckoutPath, "Cargo.toml"); githubFileExists(path) {
			source = "heuristic"
			lint = []string{"cargo fmt --check", "cargo clippy --all-targets -- -D warnings"}
			compile = []string{"cargo check"}
			unit = []string{"cargo test --lib"}
			addSourceFile(path, "heuristic")
		} else if path := filepath.Join(repoCheckoutPath, "go.mod"); githubFileExists(path) {
			source = "heuristic"
			lint = []string{`test -z "$(gofmt -l .)"`, "go vet ./..."}
			compile = []string{"go test -run '^$' ./..."}
			unit = []string{"go test ./..."}
			addSourceFile(path, "heuristic")
		}
	}
	if source == "" {
		source = "heuristic"
	}
	fingerprintInput := strings.Join(append(append(append(append(append(append([]string{source}, lint...), compile...), unit...), integration...), benchmarks...), warnings...), "\n")
	for _, item := range sourceFiles {
		fingerprintInput += "\n" + item.Path + "\n" + item.Checksum + "\n" + item.Kind
	}
	sum := sha256.Sum256([]byte(fingerprintInput))
	plan := githubVerificationPlan{
		Version:         1,
		Source:          source,
		Lint:            lint,
		Compile:         compile,
		Unit:            unit,
		Integration:     integration,
		Benchmarks:      benchmarks,
		Warnings:        warnings,
		PlanFingerprint: hex.EncodeToString(sum[:]),
		SourceFiles:     sourceFiles,
	}
	deriveManagedVerificationDefaults(&plan, repoCheckoutPath, inferGithubRepoSlugFromRepo(repoCheckoutPath))
	return plan
}

func checksumFile(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}

func githubFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
