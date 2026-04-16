package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func runLocalWorkFinalReviewGate(manifest localWorkManifest, codexArgs []string, iterationDir string, phase string) ([]githubPullReviewFinding, []string, []localWorkFinalGateRoleResult, int, error) {
	allFindings := []githubPullReviewFinding{}
	rolesWithFindings := []string{}
	roleResults := []localWorkFinalGateRoleResult{}
	totalFindings := 0
	for _, role := range localWorkFinalReviewGateRoles {
		prompt, err := buildLocalWorkSpecializedReviewPrompt(manifest, role)
		if err != nil {
			return nil, nil, nil, 0, err
		}
		prefix := fmt.Sprintf("final-gate-%s-%s", sanitizePathToken(phase), sanitizePathToken(role))
		promptPath := filepath.Join(iterationDir, prefix+"-prompt.md")
		stdoutPath := filepath.Join(iterationDir, prefix+"-stdout.log")
		stderrPath := filepath.Join(iterationDir, prefix+"-stderr.log")
		findingsPath := filepath.Join(iterationDir, prefix+"-findings.json")
		if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
			return nil, nil, nil, 0, err
		}
		result, findings, err := runLocalWorkReviewWithAlias(manifest, codexArgs, prompt, role)
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

func buildLocalWorkSpecializedReviewPrompt(manifest localWorkManifest, role string) (string, error) {
	changedFilesOutput, err := githubGitOutput(manifest.SandboxRepoPath, "diff", "--name-only", manifest.BaselineSHA)
	if err != nil {
		return "", err
	}
	diffOutput, err := githubGitOutput(manifest.SandboxRepoPath, "diff", manifest.BaselineSHA)
	if err != nil {
		return "", err
	}
	changedFiles := []string{}
	for _, line := range strings.Split(changedFilesOutput, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			changedFiles = append(changedFiles, line)
		}
	}
	lines := []string{
		"Review this local implementation and return JSON only.",
		"This is a mandatory final completion gate for local work. Report only actionable issues that should block declaring the run complete.",
		"Use existing verification artifacts first. You may run targeted tests, diagnostics, or runtime commands when needed to ground a finding or approval. Do not rerun broad/full suites unless the diff or missing evidence specifically requires it. Do not edit files.",
		fmt.Sprintf("Review role: %s", role),
		`Schema: {"findings":[{"title":"...","severity":"low|medium|high|critical","path":"...","line":123,"summary":"...","detail":"...","fix":"...","rationale":"..."}]}`,
		"If there are no actionable issues, return {\"findings\":[]}.",
		fmt.Sprintf("Repo root: %s", manifest.RepoRoot),
		fmt.Sprintf("Baseline SHA: %s", manifest.BaselineSHA),
		fmt.Sprintf("Changed files: %s", strings.Join(changedFiles, ", ")),
		"Diff:",
		diffOutput,
	}
	if role == "qa-tester" {
		lines = append(lines,
			"QA focus:",
			"- Focus on user-facing runtime behavior and CLI/app smoke checks for changed executable or interactive surfaces.",
			"- Run targeted CLI/app checks when behavior cannot be validated from existing verification artifacts and diff review alone.",
			"- If the diff has no meaningful runtime or user-facing behavior, return {\"findings\":[]}.",
		)
	}
	if promptSurface, err := readGithubPromptSurface(role); err == nil && strings.TrimSpace(promptSurface) != "" {
		lines = append(lines, "", strings.TrimSpace(promptSurface))
	}
	return strings.Join(lines, "\n\n"), nil
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
	sourceHead, err := githubGitOutput(manifest.RepoRoot, "rev-parse", "HEAD")
	if err != nil {
		return localWorkFinalApplyResult{Status: "blocked-before-apply", Error: err.Error()}
	}
	if strings.TrimSpace(sourceHead) != strings.TrimSpace(manifest.BaselineSHA) {
		return localWorkFinalApplyResult{
			Status: "blocked-before-apply",
			Error:  "source checkout HEAD changed since work started; restore " + strings.TrimSpace(manifest.BaselineSHA) + " or manually apply the sandbox diff from " + manifest.SandboxRepoPath,
		}
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
	if err := githubRunGit(manifest.RepoRoot, "apply", "--check", "--index", patchPath); err != nil {
		return localWorkFinalApplyResult{Status: "blocked-before-apply", Error: err.Error()}
	}
	if err := githubRunGit(manifest.RepoRoot, "apply", "--index", patchPath); err != nil {
		return localWorkFinalApplyResult{Status: "blocked-before-apply", Error: err.Error()}
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
	return localWorkFinalApplyResult{Status: "committed", CommitSHA: strings.TrimSpace(commitSHA)}
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
	if manifest.Status != "blocked" {
		return ""
	}
	switch {
	case manifest.CandidateAuditStatus == "blocked-candidate-files":
		return "remove generated/runtime files from the sandbox diff, then rerun or manually recover from " + manifest.SandboxRepoPath
	case manifest.FinalApplyStatus == "blocked-before-apply":
		return "clean or restore the source checkout, then run nana work resume --run-id " + manifest.RunID
	case manifest.FinalApplyStatus == "blocked-after-apply":
		return "inspect staged source checkout changes and either commit or reset them manually; resume will not retry this state"
	default:
		return "inspect the run retrospective and resolve the blocker before continuing"
	}
}
