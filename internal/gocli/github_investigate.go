package gocli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

type githubManagedRepoPaths struct {
	RepoRoot                 string
	SourcePath               string
	RepoMetaPath             string
	RepoSettingsPath         string
	RepoVerificationPlanPath string
}

type githubManagedRepoMetadata struct {
	Version       int    `json:"version"`
	RepoName      string `json:"repo_name"`
	RepoSlug      string `json:"repo_slug"`
	RepoOwner     string `json:"repo_owner"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
	RepoRoot      string `json:"repo_root"`
	SourcePath    string `json:"source_path"`
	UpdatedAt     string `json:"updated_at"`
}

type githubRepositoryPayload struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
}

type githubIssuePayload struct {
	Title       string `json:"title"`
	State       string `json:"state"`
	PullRequest *struct {
		URL string `json:"url"`
	} `json:"pull_request,omitempty"`
}

type githubTargetContext struct {
	Repository githubRepositoryPayload
	Issue      githubIssuePayload
}

type githubVerificationPlan struct {
	Version         int                            `json:"version,omitempty"`
	Source          string                         `json:"source"`
	Lint            []string                       `json:"lint"`
	Compile         []string                       `json:"compile"`
	Unit            []string                       `json:"unit"`
	Integration     []string                       `json:"integration"`
	PlanFingerprint string                         `json:"plan_fingerprint,omitempty"`
	SourceFiles     []githubVerificationSourceFile `json:"source_files,omitempty"`
}

type githubVerificationSourceFile struct {
	Path     string `json:"path"`
	Checksum string `json:"checksum"`
	Kind     string `json:"kind"`
}

type githubConsiderationInference struct {
	Considerations []string
}

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
	if err := ensureGithubSourceClone(paths, repoMeta); err != nil {
		return err
	}

	verificationPlan := detectGithubVerificationPlan(paths.SourcePath)
	if err := writeGithubJSON(paths.RepoVerificationPlanPath, verificationPlan); err != nil {
		return err
	}

	settings, _ := readGithubRepoSettings(paths.RepoSettingsPath)
	if settings == nil {
		inferred := inferGithubInitialRepoConsiderations(paths.SourcePath, repoMeta.RepoSlug, verificationPlan)
		settings = &githubRepoSettings{
			Version:               4,
			DefaultConsiderations: inferred.Considerations,
			DefaultRoleLayout:     "split",
			UpdatedAt:             now.Format(time.RFC3339),
		}
	}
	settings.HotPathAPIProfile = inferGithubHotPathProfile(paths.SourcePath, now)
	settings.UpdatedAt = now.Format(time.RFC3339)
	if settings.Version == 0 {
		settings.Version = 4
	}
	if settings.DefaultRoleLayout == "" {
		settings.DefaultRoleLayout = "split"
	}
	if err := writeGithubJSON(paths.RepoSettingsPath, settings); err != nil {
		return err
	}

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

	fmt.Fprintf(os.Stdout, "[github] Investigated %s %s #%d\n", repoMeta.RepoSlug, target.kind, target.number)
	fmt.Fprintf(os.Stdout, "[github] Title: %s\n", context.Issue.Title)
	fmt.Fprintf(os.Stdout, "[github] Managed repo root: %s\n", paths.RepoRoot)
	fmt.Fprintf(os.Stdout, "[github] Source path: %s\n", paths.SourcePath)
	fmt.Fprintf(os.Stdout, "[github] Default branch: %s\n", repoMeta.DefaultBranch)
	fmt.Fprintf(os.Stdout, "[github] Suggested considerations: %s\n", joinOrNone(considerations))
	fmt.Fprintf(os.Stdout, "[github] Suggested role layout: %s\n", defaultString(roleLayout, "split"))
	fmt.Fprintf(os.Stdout, "[github] Review-rules mode: %s\n", reviewRulesMode)
	fmt.Fprintf(
		os.Stdout,
		"[github] Verification plan: lint=%d compile=%d unit=%d integration=%d\n",
		len(verificationPlan.Lint),
		len(verificationPlan.Compile),
		len(verificationPlan.Unit),
		len(verificationPlan.Integration),
	)
	if settings.HotPathAPIProfile != nil {
		fmt.Fprintf(os.Stdout, "[github] Hot-path API files: %s\n", joinOrNone(settings.HotPathAPIProfile.HotPathAPIFiles))
		fmt.Fprintf(os.Stdout, "[github] Hot-path API tokens: %s\n", joinOrNone(settings.HotPathAPIProfile.APIIdentifierTokens))
	} else {
		fmt.Fprintf(os.Stdout, "[github] Hot-path API files: (none detected)\n")
		fmt.Fprintf(os.Stdout, "[github] Hot-path API tokens: (none detected)\n")
	}
	fmt.Fprintln(os.Stdout, "[github] Suggested pipeline:")
	for _, line := range buildGithubConsiderationInstructionLines(considerations, roleLayout) {
		fmt.Fprintln(os.Stdout, line)
	}
	fmt.Fprintf(os.Stdout, "[github] Next: nana implement %s\n", githubCanonicalTargetURL(target))
	return nil
}

func githubManagedPaths(repoSlug string) githubManagedRepoPaths {
	repoRoot := filepath.Join(githubNanaHome(), "repos", filepath.FromSlash(repoSlug))
	return githubManagedRepoPaths{
		RepoRoot:                 repoRoot,
		SourcePath:               filepath.Join(repoRoot, "source"),
		RepoMetaPath:             filepath.Join(repoRoot, "repo.json"),
		RepoSettingsPath:         filepath.Join(repoRoot, "settings.json"),
		RepoVerificationPlanPath: filepath.Join(repoRoot, "verification-plan.json"),
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
		Version:       1,
		RepoName:      target.Repository.Name,
		RepoSlug:      target.Repository.FullName,
		RepoOwner:     repoOwner,
		CloneURL:      target.Repository.CloneURL,
		DefaultBranch: target.Repository.DefaultBranch,
		HTMLURL:       target.Repository.HTMLURL,
		RepoRoot:      paths.RepoRoot,
		SourcePath:    paths.SourcePath,
		UpdatedAt:     now.Format(time.RFC3339),
	}
	if err := writeGithubJSON(paths.RepoMetaPath, metadata); err != nil {
		return nil, err
	}
	return metadata, nil
}

func ensureGithubSourceClone(paths githubManagedRepoPaths, repoMeta *githubManagedRepoMetadata) error {
	if _, err := os.Stat(paths.SourcePath); os.IsNotExist(err) {
		if err := githubRunGit("", "clone", repoMeta.CloneURL, paths.SourcePath); err != nil {
			return err
		}
	} else {
		currentOrigin, err := githubGitOutput(paths.SourcePath, "remote", "get-url", "origin")
		if err != nil || strings.TrimSpace(currentOrigin) != strings.TrimSpace(repoMeta.CloneURL) {
			if err := githubRunGit(paths.SourcePath, "remote", "set-url", "origin", repoMeta.CloneURL); err != nil {
				return err
			}
		}
	}
	return githubRunGit(paths.SourcePath, "fetch", "--prune", "origin")
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
	files := trackedRepoFiles(repoCheckoutPath)
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
	files := trackedRepoFiles(repoCheckoutPath)
	apiSurfaceFiles := []string{}
	hotPathFiles := []string{}
	tokens := []string{}
	for _, path := range files {
		lower := strings.ToLower(path)
		if strings.Contains(lower, "openapi") || strings.Contains(lower, "swagger") || strings.Contains(lower, "graphql") || strings.Contains(lower, "proto") || strings.Contains(lower, "/api/") {
			apiSurfaceFiles = append(apiSurfaceFiles, path)
			base := filepath.Base(path)
			base = strings.TrimSuffix(base, filepath.Ext(base))
			for _, token := range regexp.MustCompile(`[^A-Za-z0-9]+`).Split(base, -1) {
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

func sandboxVerificationDir(sandboxPath string) string {
	return filepath.Join(sandboxPath, ".nana", "work-on", "verify")
}

func refreshGithubVerificationArtifacts(runID string, useLast bool) error {
	manifestPath, repoRoot, err := resolveGithubRunManifestPath(runID, useLast)
	if err != nil {
		return err
	}
	manifest, err := readGithubWorkonManifest(manifestPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(manifest.SandboxPath) == "" || strings.TrimSpace(manifest.SandboxRepoPath) == "" {
		return fmt.Errorf("run %s is missing sandbox paths required for verify-refresh", manifest.RunID)
	}
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
	status := "already current"
	if beforePlan != plan.PlanFingerprint || beforePlan == "" {
		status = "refreshed"
	}
	fmt.Fprintf(os.Stdout, "[github] Verification artifacts for run %s %s.\n", manifest.RunID, status)
	if len(plan.SourceFiles) > 0 {
		parts := make([]string, 0, len(plan.SourceFiles))
		for _, sourceFile := range plan.SourceFiles {
			parts = append(parts, fmt.Sprintf("%s:%s", sourceFile.Path, sourceFile.Checksum))
		}
		fmt.Fprintf(os.Stdout, "[github] Verification source files: %s\n", strings.Join(parts, ", "))
	}
	if scriptsDir != "" {
		fmt.Fprintf(os.Stdout, "[github] Verification scripts directory: %s\n", scriptsDir)
	}
	return nil
}

func writeGithubVerificationScripts(sandboxPath string, repoCheckoutPath string, plan githubVerificationPlan, runID string) (string, error) {
	dir := sandboxVerificationDir(sandboxPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	writeScript := func(name string, body []string) error {
		path := filepath.Join(dir, name)
		return os.WriteFile(path, []byte(strings.Join(body, "\n")+"\n"), 0o755)
	}
	buildCommandScript := func(commands []string, emptyMessage string) []string {
		lines := []string{
			"#!/usr/bin/env bash",
			"set -euo pipefail",
			fmt.Sprintf("cd %q", repoCheckoutPath),
		}
		if len(commands) == 0 {
			lines = append(lines, fmt.Sprintf("echo %q", emptyMessage))
		} else {
			lines = append(lines, commands...)
		}
		return lines
	}
	if err := writeScript("lint.sh", buildCommandScript(plan.Lint, "No lint command detected for this repo.")); err != nil {
		return "", err
	}
	if err := writeScript("compile.sh", buildCommandScript(plan.Compile, "No compile command detected for this repo.")); err != nil {
		return "", err
	}
	if err := writeScript("unit-tests.sh", buildCommandScript(plan.Unit, "No unit-test command detected for this repo.")); err != nil {
		return "", err
	}
	if err := writeScript("integration-tests.sh", buildCommandScript(plan.Integration, "No integration-test command detected for this repo.")); err != nil {
		return "", err
	}
	if err := writeScript("refresh.sh", []string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		fmt.Sprintf("nana work-on verify-refresh --run-id %q", runID),
	}); err != nil {
		return "", err
	}
	if err := writeScript("worker-done.sh", []string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		fmt.Sprintf("DIR=%q", dir),
		"\"$DIR/refresh.sh\"",
		"\"$DIR/lint.sh\"",
		"\"$DIR/compile.sh\"",
		"\"$DIR/unit-tests.sh\"",
	}); err != nil {
		return "", err
	}
	if err := writeScript("all.sh", []string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		fmt.Sprintf("DIR=%q", dir),
		"\"$DIR/refresh.sh\"",
		"\"$DIR/lint.sh\"",
		"\"$DIR/compile.sh\"",
		"\"$DIR/unit-tests.sh\"",
		"\"$DIR/integration-tests.sh\"",
	}); err != nil {
		return "", err
	}
	return dir, nil
}

func detectGithubVerificationPlan(repoCheckoutPath string) githubVerificationPlan {
	var source string
	var lint, compile, unit, integration []string
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
		if targets["test"] {
			unit = append(unit, "make test")
		}
		if targets["test-unit"] {
			unit = append(unit, "make test-unit")
		}
		if targets["test-integration"] {
			integration = append(integration, "make test-integration")
		}
		if len(lint)+len(compile)+len(unit)+len(integration) > 0 {
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
			lint = []string{"gofmt -w .", "go vet ./..."}
			compile = []string{"go test ./..."}
			unit = []string{"go test ./..."}
			addSourceFile(path, "heuristic")
		}
	}
	if source == "" {
		source = "heuristic"
	}
	fingerprintInput := strings.Join(append(append(append(append([]string{source}, lint...), compile...), unit...), integration...), "\n")
	for _, item := range sourceFiles {
		fingerprintInput += "\n" + item.Path + "\n" + item.Checksum + "\n" + item.Kind
	}
	sum := sha256.Sum256([]byte(fingerprintInput))
	return githubVerificationPlan{
		Source:          source,
		Lint:            lint,
		Compile:         compile,
		Unit:            unit,
		Integration:     integration,
		PlanFingerprint: hex.EncodeToString(sum[:]),
		SourceFiles:     sourceFiles,
	}
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
