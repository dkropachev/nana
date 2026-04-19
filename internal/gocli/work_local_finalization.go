package gocli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

type localWorkFinalApplyTarget struct {
	RemoteName string
	Branch     string
}

type localWorkFinalApplyLockState struct {
	Version      int    `json:"version"`
	RunID        string `json:"run_id"`
	RepoRoot     string `json:"repo_root"`
	SourceBranch string `json:"source_branch,omitempty"`
	Phase        string `json:"phase,omitempty"`
	CreatedAt    string `json:"created_at"`
}

func runLocalWorkFinalReviewGate(manifest localWorkManifest, codexArgs []string, iterationDir string, phase string) ([]githubPullReviewFinding, []string, []localWorkFinalGateRoleResult, int, error) {
	context, err := buildReviewPromptContext(manifest.SandboxRepoPath, []string{manifest.BaselineSHA}, reviewPromptContextOptions{
		ChangedFilesLimit: reviewPromptChangedFilesLimit,
		MaxHunksPerFile:   reviewPromptMaxHunksPerFile,
		MaxLinesPerFile:   reviewPromptMaxLinesPerFile,
		MaxCharsPerFile:   reviewPromptMaxCharsPerFile,
	})
	if err != nil {
		return nil, nil, nil, 0, err
	}
	selectedRoles := selectLocalWorkFinalGateRoles(context.ChangedFiles, manifest.VerificationPlan)
	allFindings := []githubPullReviewFinding{}
	rolesWithFindings := []string{}
	roleResults := []localWorkFinalGateRoleResult{}
	totalFindings := 0
	for _, role := range selectedRoles {
		prompt, err := buildLocalWorkSpecializedReviewPrompt(manifest, role, context)
		if err != nil {
			return nil, nil, nil, 0, err
		}
		prefix := fmt.Sprintf("final-gate-%s-%s", sanitizePathToken(phase), sanitizePathToken(role))
		promptPath := filepath.Join(iterationDir, prefix+"-prompt.md")
		stdoutPath := filepath.Join(iterationDir, prefix+"-stdout.log")
		stderrPath := filepath.Join(iterationDir, prefix+"-stderr.log")
		findingsPath := filepath.Join(iterationDir, prefix+"-findings.json")
		checkpointPath := filepath.Join(iterationDir, prefix+"-checkpoint.json")
		if checkpoint, err := readCodexStepCheckpoint(checkpointPath); err == nil && checkpoint.Status == "completed" {
			var findings []githubPullReviewFinding
			if err := readGithubJSON(findingsPath, &findings); err == nil {
				roleResults = append(roleResults, localWorkFinalGateRoleResult{
					Role:     role,
					Findings: len(findings),
				})
				if len(findings) > 0 {
					rolesWithFindings = append(rolesWithFindings, role)
					totalFindings += len(findings)
					allFindings = append(allFindings, findings...)
				}
				continue
			}
		}
		if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
			return nil, nil, nil, 0, err
		}
		result, findings, err := runLocalWorkReviewWithAlias(manifest, codexArgs, prompt, role, checkpointPath)
		if writeErr := os.WriteFile(stdoutPath, []byte(result.Stdout), 0o644); writeErr != nil {
			return nil, nil, nil, 0, writeErr
		}
		if writeErr := os.WriteFile(stderrPath, []byte(result.Stderr), 0o644); writeErr != nil {
			return nil, nil, nil, 0, writeErr
		}
		if writeErr := os.WriteFile(findingsPath, mustMarshalJSON(findings), 0o644); writeErr != nil {
			return nil, nil, nil, 0, writeErr
		}
		if err != nil {
			return nil, nil, nil, 0, err
		}
		roleResults = append(roleResults, localWorkFinalGateRoleResult{
			Role:     role,
			Findings: len(findings),
		})
		if len(findings) > 0 {
			rolesWithFindings = append(rolesWithFindings, role)
			totalFindings += len(findings)
			allFindings = append(allFindings, findings...)
		}
	}
	return allFindings, uniqueStrings(rolesWithFindings), roleResults, totalFindings, nil
}

func buildLocalWorkSpecializedReviewPrompt(manifest localWorkManifest, role string, context reviewPromptContext) (string, error) {
	lines := []string{
		"Review this local implementation and return JSON only.",
		"This is a mandatory final completion gate for local work. Report only actionable issues that should block declaring the run complete.",
		"Use existing verification artifacts first. You may run targeted tests, diagnostics, or runtime commands when needed to ground a finding or approval. Do not rerun broad/full suites unless the diff or missing evidence specifically requires it. Do not edit files.",
		fmt.Sprintf("Review role: %s", role),
		`Schema: {"findings":[{"title":"...","severity":"low|medium|high|critical","path":"...","line":123,"summary":"...","detail":"...","fix":"...","rationale":"..."}]}`,
		"If there are no actionable issues, return {\"findings\":[]}.",
		fmt.Sprintf("Repo root: %s", manifest.RepoRoot),
		fmt.Sprintf("Baseline SHA: %s", manifest.BaselineSHA),
		fmt.Sprintf("Changed files: %s", context.ChangedFilesText),
	}
	if role == "qa-tester" {
		lines = append(lines,
			"QA focus:",
			"- Focus on user-facing runtime behavior and CLI/app smoke checks for changed executable or interactive surfaces.",
			"- Run targeted CLI/app checks when behavior cannot be validated from existing verification artifacts and diff review alone.",
			"- If the diff has no meaningful runtime or user-facing behavior, return {\"findings\":[]}.",
		)
	}
	if promptSurface, err := readGithubEmbeddedPromptSurface(role); err == nil && strings.TrimSpace(promptSurface) != "" {
		lines = append(lines, "", strings.TrimSpace(promptSurface))
	}
	if context.Shortstat != "" {
		lines = append(lines, "Shortstat:", context.Shortstat)
	}
	lines = append(lines, "Diff summary:", context.DiffSummary)
	return capPromptChars(strings.Join(lines, "\n\n"), reviewPromptLocalCharLimit), nil
}

func selectLocalWorkFinalGateRoles(changedFiles []string, plan *githubVerificationPlan) []string {
	roles := []string{"quality-reviewer"}
	hasBenchmarkPlan := plan != nil && len(plan.Benchmarks) > 0
	for _, path := range changedFiles {
		if pathTriggersLocalSecurityReview(path) && !slices.Contains(roles, "security-reviewer") {
			roles = append(roles, "security-reviewer")
		}
		if (hasBenchmarkPlan || pathTriggersLocalPerformanceReview(path)) && !slices.Contains(roles, "performance-reviewer") {
			roles = append(roles, "performance-reviewer")
		}
		if pathTriggersLocalQAReview(path) && !slices.Contains(roles, "qa-tester") {
			roles = append(roles, "qa-tester")
		}
	}
	return roles
}

func selectLocalWorkFinalGateRolesForManifest(manifest localWorkManifest) ([]string, error) {
	changedFilesOutput, err := githubGitOutput(manifest.SandboxRepoPath, "diff", "--name-only", manifest.BaselineSHA)
	if err != nil {
		return nil, err
	}
	return selectLocalWorkFinalGateRoles(collectTrimmedLines(changedFilesOutput), manifest.VerificationPlan), nil
}

func pathTriggersLocalSecurityReview(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(strings.TrimSpace(path)))
	if lower == "" {
		return false
	}
	if strings.HasPrefix(lower, ".github/workflows/") {
		return true
	}
	for _, token := range []string{"auth", "oauth", "token", "secret", "crypto", "jwt", "session", "permission", "policy", "rbac", "acl"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func pathTriggersLocalPerformanceReview(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(strings.TrimSpace(path)))
	if lower == "" {
		return false
	}
	for _, token := range []string{"perf", "benchmark", "bench", "cache", "index", "search", "query", "hotpath"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func pathTriggersLocalQAReview(path string) bool {
	normalized := filepath.ToSlash(strings.TrimSpace(path))
	lower := strings.ToLower(normalized)
	if lower == "" {
		return false
	}
	for _, prefix := range []string{"cmd/", "hooks/", "runtime/", "hud/", "help/", "start_ui_assets/"} {
		if strings.HasPrefix(lower, prefix) || strings.Contains(lower, "/"+prefix) {
			return true
		}
	}
	if filepath.Base(lower) == "main.go" {
		return true
	}
	switch filepath.Ext(lower) {
	case ".html", ".css", ".js", ".jsx", ".ts", ".tsx":
		return true
	default:
		return false
	}
}

func mergeFinalGateRoleResults(existing []localWorkFinalGateRoleResult, incoming []localWorkFinalGateRoleResult) []localWorkFinalGateRoleResult {
	counts := map[string]int{}
	order := []string{}
	for _, result := range append(append([]localWorkFinalGateRoleResult{}, existing...), incoming...) {
		role := strings.TrimSpace(result.Role)
		if role == "" {
			continue
		}
		if _, ok := counts[role]; !ok {
			order = append(order, role)
		}
		counts[role] += result.Findings
	}
	out := make([]localWorkFinalGateRoleResult, 0, len(order))
	for _, role := range order {
		out = append(out, localWorkFinalGateRoleResult{
			Role:     role,
			Findings: counts[role],
		})
	}
	return out
}

func applyLocalWorkFinalDiff(manifest localWorkManifest) localWorkFinalApplyResult {
	runDir := localWorkRunDirByID(manifest.RepoID, manifest.RunID)
	releaseFinalApplyLock, err := acquireLocalWorkFinalApplyLock(manifest, "final-apply")
	if err != nil {
		return localWorkFinalApplyResult{Status: "blocked-before-apply", Error: err.Error()}
	}
	defer releaseFinalApplyLock()
	if err := cleanupDirtyManagedLocalWorkRepo(manifest.RepoRoot, "before final apply"); err != nil {
		return localWorkFinalApplyResult{Status: "blocked-before-apply", Error: err.Error()}
	}
	sourceStatus, err := githubGitOutput(manifest.RepoRoot, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return localWorkFinalApplyResult{Status: "blocked-before-apply", Error: err.Error()}
	}
	if strings.TrimSpace(sourceStatus) != "" {
		return localWorkFinalApplyResult{
			Status: "blocked-before-apply",
			Error:  "source checkout has local changes; commit, stash, or remove them and run nana work resume --run-id " + manifest.RunID,
		}
	}
	target, err := syncLocalWorkFinalApplyTarget(manifest)
	if err != nil {
		return localWorkFinalApplyResult{Status: "blocked-before-apply", Error: err.Error()}
	}
	diffOutput, err := githubGitOutput(manifest.SandboxRepoPath, "diff", "--binary", manifest.BaselineSHA)
	if err != nil {
		return localWorkFinalApplyResult{Status: "blocked-before-apply", Error: err.Error()}
	}
	patchPath := filepath.Join(runDir, "final-source-apply.patch")
	if err := os.WriteFile(patchPath, []byte(diffOutput), 0o644); err != nil {
		return localWorkFinalApplyResult{Status: "blocked-before-apply", Error: err.Error()}
	}
	if strings.TrimSpace(diffOutput) == "" {
		return localWorkFinalApplyResult{Status: "no-op"}
	}
	if err := githubRunGit(manifest.RepoRoot, "apply", "--3way", "--index", patchPath); err != nil {
		resolvedPaths, resolveErr := applyLocalWorkManagedSandboxFallback(manifest)
		if resolveErr != nil {
			return localWorkFinalApplyResult{Status: "blocked-before-apply", Error: fmt.Sprintf("final apply onto %s failed after syncing target branch: %v", localWorkFinalApplyTargetLabel(target, manifest.SourceBranch), err)}
		}
		fmt.Fprintf(os.Stdout, "[local] Auto-resolved final apply conflicts by preferring sandbox changes for: %s\n", strings.Join(resolvedPaths, ", "))
	}
	if err := githubRunGit(manifest.RepoRoot, "commit", "-m", fmt.Sprintf("nana work: apply %s", manifest.RunID)); err != nil {
		return localWorkFinalApplyResult{
			Status: "blocked-after-apply",
			Error:  "source checkout contains staged final-apply changes, but commit failed; inspect and commit or reset them manually: " + err.Error(),
		}
	}
	commitSHA, err := githubGitOutput(manifest.RepoRoot, "rev-parse", "HEAD")
	if err != nil {
		return localWorkFinalApplyResult{Status: "blocked-after-apply", Error: err.Error()}
	}
	if target.RemoteName != "" {
		if err := pushLocalWorkFinalApplyTarget(manifest.RepoRoot, target); err != nil {
			return localWorkFinalApplyResult{
				Status:    "blocked-after-apply",
				CommitSHA: strings.TrimSpace(commitSHA),
				Error:     fmt.Sprintf("source checkout contains committed final-apply changes, but push to %s failed; inspect and push or reconcile manually: %v", localWorkFinalApplyTargetLabel(target, manifest.SourceBranch), err),
			}
		}
		return localWorkFinalApplyResult{Status: "pushed", CommitSHA: strings.TrimSpace(commitSHA)}
	}
	return localWorkFinalApplyResult{Status: "committed", CommitSHA: strings.TrimSpace(commitSHA)}
}

func syncLocalWorkFinalApplyTarget(manifest localWorkManifest) (localWorkFinalApplyTarget, error) {
	return syncLocalWorkTrackedBranch(manifest.RepoRoot, manifest.SourceBranch, "before final apply")
}

func checkoutLocalWorkSourceBranch(repoPath string, sourceBranch string) error {
	branch := strings.TrimSpace(sourceBranch)
	if branch == "" || branch == "HEAD" {
		return nil
	}
	currentBranch, err := githubGitOutput(repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(currentBranch) == branch {
		return nil
	}
	return githubRunGit(repoPath, "checkout", branch)
}

func syncLocalWorkTrackedBranch(repoPath string, sourceBranch string, phase string) (localWorkFinalApplyTarget, error) {
	if err := checkoutLocalWorkSourceBranch(repoPath, sourceBranch); err != nil {
		return localWorkFinalApplyTarget{}, err
	}
	if err := ensureManagedSourceOriginImmutable(repoPath); err != nil {
		return localWorkFinalApplyTarget{}, err
	}
	if err := preflightManagedSourceOriginAccess(repoPath); err != nil {
		return localWorkFinalApplyTarget{}, err
	}
	target, err := resolveLocalWorkFinalApplyTarget(repoPath, sourceBranch)
	if err != nil {
		return localWorkFinalApplyTarget{}, err
	}
	if target.RemoteName == "" || target.Branch == "" {
		return target, nil
	}
	if err := githubRunGit(repoPath, "fetch", "--prune", target.RemoteName); err != nil {
		return target, fmt.Errorf("failed to fetch target branch %s %s: %w", localWorkFinalApplyTargetLabel(target, sourceBranch), phase, err)
	}
	if err := refreshLocalWorkTrackedBranch(repoPath, sourceBranch, target, phase); err != nil {
		return target, err
	}
	return target, nil
}

func refreshLocalWorkTrackedBranch(repoPath string, sourceBranch string, target localWorkFinalApplyTarget, phase string) error {
	remoteRef := fmt.Sprintf("%s/%s", target.RemoteName, target.Branch)
	currentSHA, err := githubGitOutput(repoPath, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	remoteSHA, err := githubGitOutput(repoPath, "rev-parse", remoteRef)
	if err != nil {
		return fmt.Errorf("failed to resolve target branch %s %s: %w", localWorkFinalApplyTargetLabel(target, sourceBranch), phase, err)
	}
	currentSHA = strings.TrimSpace(currentSHA)
	remoteSHA = strings.TrimSpace(remoteSHA)
	if currentSHA == "" || remoteSHA == "" || currentSHA == remoteSHA {
		return nil
	}

	remoteContainedLocally, err := localWorkGitRefIsAncestor(repoPath, remoteRef, "HEAD")
	if err != nil {
		return err
	}
	if remoteContainedLocally {
		return nil
	}

	localBehindRemote, err := localWorkGitRefIsAncestor(repoPath, "HEAD", remoteRef)
	if err != nil {
		return err
	}
	if localBehindRemote {
		if err := githubRunGit(repoPath, "merge", "--ff-only", remoteRef); err != nil {
			return fmt.Errorf("failed to fast-forward target branch %s %s: %w", localWorkFinalApplyTargetLabel(target, sourceBranch), phase, err)
		}
		return nil
	}

	managedRepoSlug := findManagedRepoSlugForSourcePath(repoPath)
	if strings.TrimSpace(managedRepoSlug) == "" {
		return fmt.Errorf("source branch %s diverged from %s; update it before work can continue", defaultString(strings.TrimSpace(sourceBranch), "HEAD"), localWorkFinalApplyTargetLabel(target, sourceBranch))
	}
	backupRef, err := backupLocalWorkManagedBranch(repoPath, sourceBranch, currentSHA)
	if err != nil {
		return err
	}
	if err := githubRunGit(repoPath, "reset", "--hard", remoteRef); err != nil {
		return fmt.Errorf("failed to refresh managed source checkout for %s to %s %s: %w", managedRepoSlug, localWorkFinalApplyTargetLabel(target, sourceBranch), phase, err)
	}
	fmt.Fprintf(os.Stdout, "[local] Refreshed managed source checkout %s to %s %s; preserved previous HEAD at %s.\n",
		managedRepoSlug,
		localWorkFinalApplyTargetLabel(target, sourceBranch),
		phase,
		backupRef,
	)
	return nil
}

func backupLocalWorkManagedBranch(repoPath string, sourceBranch string, currentSHA string) (string, error) {
	branchToken := sanitizePathToken(defaultString(strings.TrimSpace(sourceBranch), "head"))
	backupRef := fmt.Sprintf("nana/autosave/%s-%d", branchToken, time.Now().UnixNano())
	if err := githubRunGit(repoPath, "branch", backupRef, strings.TrimSpace(currentSHA)); err != nil {
		return "", fmt.Errorf("failed to preserve managed source branch state at %s before refresh: %w", strings.TrimSpace(currentSHA), err)
	}
	return backupRef, nil
}

func localWorkGitRefIsAncestor(repoPath string, ancestor string, descendant string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", strings.TrimSpace(ancestor), strings.TrimSpace(descendant))
	cmd.Dir = repoPath
	cmd.Env = githubGitEnv()
	output, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	exitErr := &exec.ExitError{}
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor %s %s failed: %v\n%s", strings.TrimSpace(ancestor), strings.TrimSpace(descendant), err, output)
}

func cleanupDirtyManagedLocalWorkRepo(repoPath string, phase string) error {
	managedRepoSlug := findManagedRepoSlugForSourcePath(repoPath)
	if strings.TrimSpace(managedRepoSlug) == "" {
		return nil
	}
	statusOutput, err := githubGitOutput(repoPath, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	if strings.TrimSpace(statusOutput) == "" {
		return nil
	}
	_ = githubRunGit(repoPath, "rebase", "--abort")
	_ = githubRunGit(repoPath, "merge", "--abort")
	if err := githubRunGit(repoPath, "reset", "--hard"); err != nil {
		return fmt.Errorf("failed to reset dirty managed source checkout %s %s: %w", managedRepoSlug, phase, err)
	}
	if err := githubRunGit(repoPath, "clean", "-fd"); err != nil {
		return fmt.Errorf("failed to clean dirty managed source checkout %s %s: %w", managedRepoSlug, phase, err)
	}
	fmt.Fprintf(os.Stdout, "[local] Reset dirty managed source checkout %s %s.\n", managedRepoSlug, phase)
	return nil
}

func preflightManagedSourceOriginAccess(repoPath string) error {
	repoSlug := strings.TrimSpace(findManagedRepoSlugForSourcePath(repoPath))
	if repoSlug == "" {
		return nil
	}
	metadata := &githubManagedRepoMetadata{RepoSlug: repoSlug}
	if err := readGithubJSON(githubManagedPaths(repoSlug).RepoMetaPath, metadata); err == nil {
		normalizeGithubManagedRepoMetadata(metadata)
	}
	if strings.TrimSpace(metadata.RepoSlug) == "" {
		metadata.RepoSlug = repoSlug
	}
	if strings.TrimSpace(metadata.CanonicalOriginURL) == "" {
		metadata.CanonicalOriginURL = canonicalGithubSSHRemote(repoSlug, metadata.HTMLURL)
	}
	return githubManagedOriginPreflight(repoPath, metadata)
}

func localWorkFinalApplyLockPath(repoRoot string) string {
	return filepath.Join(localWorkRepoDir(repoRoot), "final-apply.lock.json")
}

func acquireLocalWorkFinalApplyLock(manifest localWorkManifest, phase string) (func(), error) {
	lockPath := localWorkFinalApplyLockPath(manifest.RepoRoot)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	desired := localWorkFinalApplyLockState{
		Version:      1,
		RunID:        manifest.RunID,
		RepoRoot:     manifest.RepoRoot,
		SourceBranch: manifest.SourceBranch,
		Phase:        phase,
		CreatedAt:    now.Format(time.RFC3339),
	}
	for attempt := 0; attempt < 2; attempt++ {
		file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			content := mustMarshalJSON(desired)
			if _, writeErr := file.Write(content); writeErr != nil {
				_ = file.Close()
				_ = os.Remove(lockPath)
				return nil, writeErr
			}
			if closeErr := file.Close(); closeErr != nil {
				_ = os.Remove(lockPath)
				return nil, closeErr
			}
			return func() {
				_ = os.Remove(lockPath)
			}, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		existing, stale, readErr := readLocalWorkFinalApplyLock(lockPath, manifest)
		if readErr != nil {
			return nil, readErr
		}
		if stale || strings.TrimSpace(existing.RunID) == strings.TrimSpace(manifest.RunID) {
			_ = os.Remove(lockPath)
			continue
		}
		return nil, fmt.Errorf("final apply already in progress for %s by run %s", manifest.RepoRoot, existing.RunID)
	}
	return nil, fmt.Errorf("could not acquire final apply lock for %s", manifest.RepoRoot)
}

func readLocalWorkFinalApplyLock(lockPath string, manifest localWorkManifest) (localWorkFinalApplyLockState, bool, error) {
	var existing localWorkFinalApplyLockState
	if err := readGithubJSON(lockPath, &existing); err != nil {
		return localWorkFinalApplyLockState{}, false, err
	}
	createdAt, err := time.Parse(time.RFC3339, strings.TrimSpace(existing.CreatedAt))
	if err != nil {
		return existing, true, nil
	}
	if time.Since(createdAt) > localWorkStaleRunThreshold {
		return existing, true, nil
	}
	if strings.TrimSpace(existing.RunID) == "" {
		return existing, true, nil
	}
	ownerManifest, err := readLocalWorkManifestByRunID(existing.RunID)
	if err != nil {
		return existing, true, nil
	}
	if strings.TrimSpace(ownerManifest.Status) != "running" && !localWorkResolveAllowed(ownerManifest) {
		return existing, true, nil
	}
	return existing, false, nil
}

func autoResolveLocalWorkApplyConflicts(repoPath string) ([]string, error) {
	managedRepoSlug := findManagedRepoSlugForSourcePath(repoPath)
	if strings.TrimSpace(managedRepoSlug) == "" {
		return nil, fmt.Errorf("automatic final-apply conflict resolution is only enabled for managed source checkouts")
	}
	conflictedOutput, err := githubGitOutput(repoPath, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	conflictedPaths := collectTrimmedLines(conflictedOutput)
	if len(conflictedPaths) == 0 {
		return nil, fmt.Errorf("no conflicted paths were available for automatic resolution")
	}
	for _, path := range conflictedPaths {
		if err := githubRunGit(repoPath, "checkout", "--theirs", "--", path); err != nil {
			return nil, fmt.Errorf("failed to prefer sandbox changes for %s: %w", path, err)
		}
		if err := githubRunGit(repoPath, "add", "--", path); err != nil {
			return nil, fmt.Errorf("failed to stage automatically resolved path %s: %w", path, err)
		}
	}
	remainingOutput, err := githubGitOutput(repoPath, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	if remaining := collectTrimmedLines(remainingOutput); len(remaining) > 0 {
		return nil, fmt.Errorf("automatic resolution left unresolved conflicts in: %s", strings.Join(remaining, ", "))
	}
	return conflictedPaths, nil
}

func applyLocalWorkManagedSandboxFallback(manifest localWorkManifest) ([]string, error) {
	managedRepoSlug := findManagedRepoSlugForSourcePath(manifest.RepoRoot)
	if strings.TrimSpace(managedRepoSlug) == "" {
		return nil, fmt.Errorf("automatic final-apply fallback is only enabled for managed source checkouts")
	}
	if err := githubRunGit(manifest.RepoRoot, "reset", "--hard"); err != nil {
		return nil, err
	}
	if err := githubRunGit(manifest.RepoRoot, "clean", "-fd"); err != nil {
		return nil, err
	}
	changedPathsOutput, err := githubGitOutput(manifest.SandboxRepoPath, "diff", "--name-only", manifest.BaselineSHA)
	if err != nil {
		return nil, err
	}
	changedPaths := collectTrimmedLines(changedPathsOutput)
	if len(changedPaths) == 0 {
		return nil, fmt.Errorf("no sandbox changes were available for managed fallback apply")
	}
	for _, path := range changedPaths {
		sourcePath := filepath.Join(manifest.RepoRoot, filepath.FromSlash(path))
		sandboxPath := filepath.Join(manifest.SandboxRepoPath, filepath.FromSlash(path))
		info, statErr := os.Lstat(sandboxPath)
		switch {
		case statErr == nil && info.IsDir():
			continue
		case statErr == nil:
			if err := copyFile(sandboxPath, sourcePath); err != nil {
				return nil, fmt.Errorf("failed to copy sandbox path %s into managed source checkout: %w", path, err)
			}
			if err := os.Chmod(sourcePath, info.Mode().Perm()); err != nil {
				return nil, fmt.Errorf("failed to restore mode for %s: %w", path, err)
			}
		case os.IsNotExist(statErr):
			if _, err := removePathIfExists(sourcePath); err != nil {
				return nil, fmt.Errorf("failed to remove source path %s during managed fallback apply: %w", path, err)
			}
		default:
			return nil, statErr
		}
		if err := githubRunGit(manifest.RepoRoot, "add", "--all", "--", path); err != nil {
			return nil, fmt.Errorf("failed to stage %s during managed fallback apply: %w", path, err)
		}
	}
	return changedPaths, nil
}

func resolveLocalWorkFinalApplyTarget(repoPath string, sourceBranch string) (localWorkFinalApplyTarget, error) {
	branch := strings.TrimSpace(sourceBranch)
	if branch == "" || branch == "HEAD" {
		return localWorkFinalApplyTarget{}, nil
	}
	if strings.TrimSpace(findManagedRepoSlugForSourcePath(repoPath)) != "" {
		return localWorkFinalApplyTarget{RemoteName: "origin", Branch: branch}, nil
	}
	if _, err := githubGitOutput(repoPath, "remote", "get-url", "origin"); err != nil {
		return localWorkFinalApplyTarget{}, nil
	}
	upstream, err := githubGitOutput(repoPath, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err == nil {
		parts := strings.SplitN(strings.TrimSpace(upstream), "/", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
			return localWorkFinalApplyTarget{RemoteName: strings.TrimSpace(parts[0]), Branch: strings.TrimSpace(parts[1])}, nil
		}
	}
	return localWorkFinalApplyTarget{RemoteName: "origin", Branch: branch}, nil
}

func ensureManagedSourceOriginImmutable(repoPath string) error {
	repoSlug := strings.TrimSpace(findManagedRepoSlugForSourcePath(repoPath))
	if repoSlug == "" {
		return nil
	}
	metadata := &githubManagedRepoMetadata{RepoSlug: repoSlug}
	if err := readGithubJSON(githubManagedPaths(repoSlug).RepoMetaPath, metadata); err == nil {
		normalizeGithubManagedRepoMetadata(metadata)
	}
	if strings.TrimSpace(metadata.RepoSlug) == "" {
		metadata.RepoSlug = repoSlug
	}
	if strings.TrimSpace(metadata.CanonicalOriginURL) == "" {
		metadata.CanonicalOriginURL = canonicalGithubSSHRemote(repoSlug, metadata.HTMLURL)
	}
	return ensureGithubManagedOrigin(repoPath, metadata)
}

func pushLocalWorkFinalApplyTarget(repoPath string, target localWorkFinalApplyTarget) error {
	if target.RemoteName == "" || target.Branch == "" {
		return nil
	}
	pushRef := fmt.Sprintf("HEAD:%s", target.Branch)
	firstErr := githubRunGit(repoPath, "push", target.RemoteName, pushRef)
	if firstErr == nil {
		return nil
	}
	if err := githubRunGit(repoPath, "fetch", "--prune", target.RemoteName); err != nil {
		return fmt.Errorf("initial push failed (%v) and fetch retry did not complete: %w", firstErr, err)
	}
	if err := githubRunGit(repoPath, "pull", "--rebase", target.RemoteName, target.Branch); err != nil {
		_ = githubRunGit(repoPath, "rebase", "--abort")
		return fmt.Errorf("initial push failed (%v) and automatic rebase retry on %s did not complete: %w", firstErr, localWorkFinalApplyTargetLabel(target, ""), err)
	}
	if err := githubRunGit(repoPath, "push", target.RemoteName, pushRef); err != nil {
		return fmt.Errorf("initial push failed (%v) and retry push to %s also failed: %w", firstErr, localWorkFinalApplyTargetLabel(target, ""), err)
	}
	return nil
}

func localWorkFinalApplyTargetLabel(target localWorkFinalApplyTarget, fallbackBranch string) string {
	branch := strings.TrimSpace(target.Branch)
	if branch == "" {
		branch = strings.TrimSpace(fallbackBranch)
	}
	remote := strings.TrimSpace(target.RemoteName)
	switch {
	case remote != "" && branch != "":
		return remote + "/" + branch
	case branch != "":
		return branch
	default:
		return "the source branch"
	}
}

func localWorkSandboxHasDiff(manifest localWorkManifest) (bool, error) {
	diffOutput, err := githubGitOutput(manifest.SandboxRepoPath, "diff", "--name-only", manifest.BaselineSHA)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(diffOutput) != "", nil
}

func refreshLocalWorkSandboxIntentToAdd(repoPath string) error {
	return refreshLocalWorkSandboxIntentToAddIgnoring(repoPath, nil)
}

func refreshLocalWorkSandboxIntentToAddIgnoring(repoPath string, ignored []string) error {
	untracked, err := localWorkUntrackedFiles(repoPath)
	if err != nil {
		return err
	}
	ignoredSet := map[string]bool{}
	for _, path := range ignored {
		ignoredSet[filepath.ToSlash(strings.TrimSpace(path))] = true
	}
	files := []string{}
	for _, path := range untracked {
		normalized := filepath.ToSlash(strings.TrimSpace(path))
		if normalized == "" || ignoredSet[normalized] {
			continue
		}
		files = append(files, path)
	}
	if len(files) == 0 {
		return nil
	}
	args := append([]string{"add", "-N", "--"}, files...)
	return githubRunGit(repoPath, args...)
}

func localWorkUntrackedFiles(repoPath string) ([]string, error) {
	output, err := githubGitOutput(repoPath, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	files := []string{}
	for _, part := range strings.Split(output, "\x00") {
		path := strings.TrimSpace(part)
		if path != "" {
			files = append(files, filepath.ToSlash(path))
		}
	}
	sort.Strings(files)
	return files, nil
}

func auditLocalWorkCandidateFiles(manifest localWorkManifest) (localWorkCandidateAuditResult, error) {
	output, err := githubGitOutput(manifest.SandboxRepoPath, "diff", "--name-only", manifest.BaselineSHA)
	if err != nil {
		return localWorkCandidateAuditResult{}, err
	}
	blocked := []string{}
	for _, line := range strings.Split(output, "\n") {
		path := strings.TrimSpace(line)
		if path == "" {
			continue
		}
		if localWorkCandidatePathBlocked(path) {
			blocked = append(blocked, path)
		}
	}
	if len(blocked) > 0 {
		sort.Strings(blocked)
		return localWorkCandidateAuditResult{
			Status:       "blocked-candidate-files",
			BlockedPaths: blocked,
			Error:        localWorkCandidateBlockedMessage(blocked),
		}, nil
	}
	return localWorkCandidateAuditResult{Status: "passed"}, nil
}

func localWorkCandidatePathBlocked(path string) bool {
	normalized := filepath.ToSlash(strings.TrimSpace(path))
	lower := strings.ToLower(normalized)
	if lower == "" {
		return false
	}
	if strings.HasSuffix(lower, ".log") {
		return true
	}
	parts := strings.Split(lower, "/")
	for _, part := range parts {
		switch part {
		case ".nana", ".codex", "target", "node_modules", "coverage", ".cache":
			return true
		}
	}
	return false
}

func localWorkCandidateBlockedMessage(paths []string) string {
	if len(paths) == 0 {
		return "candidate diff contains generated or runtime files"
	}
	return "candidate diff contains generated or runtime files: " + strings.Join(paths, ", ")
}

func localWorkBlockedNextAction(manifest localWorkManifest) string {
	if manifest.Status == "paused" {
		if strings.TrimSpace(manifest.PauseUntil) != "" {
			return "waiting for managed account capacity until " + strings.TrimSpace(manifest.PauseUntil)
		}
		return "waiting for managed account capacity"
	}
	if manifest.Status != "blocked" {
		return ""
	}
	if supersededBy, supersededReason, err := localWorkEffectiveSupersededInfo(manifest); err == nil && supersededBy != "" {
		manifest.SupersededByRunID = supersededBy
		manifest.SupersededReason = supersededReason
	}
	switch {
	case localWorkIsSuperseded(manifest):
		return localWorkSupersededReason(manifest)
	case manifest.CandidateAuditStatus == "blocked-candidate-files":
		return "remove generated/runtime files from the sandbox diff, then rerun or manually recover from " + manifest.SandboxRepoPath
	case manifest.FinalApplyStatus == "blocked-before-apply":
		return "run nana work resolve --run-id " + manifest.RunID + " to refresh the source checkout and finish final apply"
	case manifest.FinalApplyStatus == "blocked-after-apply":
		return "run nana work resolve --run-id " + manifest.RunID + " to retry commit/push of the final-applied source changes"
	default:
		return "inspect the run retrospective and resolve the blocker before continuing"
	}
}

func localWorkResolveAllowed(manifest localWorkManifest) bool {
	if localWorkIsSuperseded(manifest) {
		return false
	}
	return localWorkHasResolvableBlockedApply(manifest)
}
