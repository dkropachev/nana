package gocli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func runGithubWorkCompletionLoop(manifestPath string, runDir string, manifest *githubWorkManifest, codexArgs []string) error {
	if manifest == nil {
		return fmt.Errorf("missing GitHub work manifest")
	}
	if err := ensureGithubWorkBaselineSHA(manifestPath, manifest); err != nil {
		return err
	}
	localManifest := githubWorkLocalManifest(*manifest, manifestPath)
	hasDiff, err := localWorkSandboxHasDiff(localManifest)
	if err != nil {
		return err
	}
	if !hasDiff {
		manifest.CurrentPhase = "completed"
		manifest.CurrentRound = 0
		manifest.FinalGateStatus = "no-op"
		manifest.CandidateAuditStatus = "no-op"
		manifest.CandidateBlockedPaths = nil
		manifest.LastError = ""
		manifest.UpdatedAt = ISOTimeNow()
		return persistGithubWorkManifest(manifestPath, *manifest)
	}

	completionDir := filepath.Join(runDir, "completion")
	if err := os.MkdirAll(completionDir, 0o755); err != nil {
		return err
	}

	verification, findings, summary, err := runGithubCompletionReviewCycle(manifestPath, completionDir, manifest, codexArgs, 0)
	recordGithubCompletionRound(manifest, summary)
	if err := persistGithubWorkManifest(manifestPath, *manifest); err != nil {
		return err
	}
	if err != nil {
		return err
	}
	if verification.Passed && len(findings) == 0 {
		return nil
	}

	for round := 1; round <= localWorkMaxReviewRounds && (!verification.Passed || len(findings) > 0); round++ {
		summary, verification, findings, err = runGithubHardeningRound(manifestPath, completionDir, manifest, codexArgs, round, verification, findings)
		if summary.Round > 0 || summary.Status != "" {
			recordGithubCompletionRound(manifest, summary)
			if err := persistGithubWorkManifest(manifestPath, *manifest); err != nil {
				return err
			}
		}
		if err != nil {
			return err
		}
	}

	if verification.Passed && len(findings) == 0 {
		return nil
	}
	manifest.ExecutionStatus = "failed"
	manifest.CurrentPhase = "max-rounds"
	manifest.CurrentRound = localWorkMaxReviewRounds
	manifest.LastError = buildGithubCompletionExhaustedMessage(verification, findings)
	manifest.UpdatedAt = ISOTimeNow()
	if err := persistGithubWorkManifest(manifestPath, *manifest); err != nil {
		return err
	}
	return errors.New(manifest.LastError)
}

func captureGithubWorkBaselineIfMissing(manifestPath string, manifest *githubWorkManifest) (bool, error) {
	if manifest == nil {
		return false, fmt.Errorf("missing GitHub work manifest")
	}
	if strings.TrimSpace(manifest.BaselineSHA) != "" {
		return false, nil
	}
	baselineSHA, err := githubGitOutput(manifest.SandboxRepoPath, "rev-parse", "HEAD")
	if err != nil {
		return false, err
	}
	manifest.BaselineSHA = strings.TrimSpace(baselineSHA)
	manifest.UpdatedAt = ISOTimeNow()
	if err := persistGithubWorkManifest(manifestPath, *manifest); err != nil {
		return false, err
	}
	return true, nil
}

func requireGithubWorkBaselineForCompletionResume(manifest *githubWorkManifest) error {
	if manifest == nil {
		return fmt.Errorf("missing GitHub work manifest")
	}
	if strings.TrimSpace(manifest.BaselineSHA) != "" {
		return nil
	}
	return fmt.Errorf("GitHub work run %s is missing baseline_sha and cannot resume directly into completion; rerun leader execution first", manifest.RunID)
}

func runGithubHardeningRound(manifestPath string, completionDir string, manifest *githubWorkManifest, codexArgs []string, round int, verification localWorkVerificationReport, findings []githubPullReviewFinding) (githubWorkCompletionRoundSummary, localWorkVerificationReport, []githubPullReviewFinding, error) {
	localManifest := githubWorkLocalManifest(*manifest, manifestPath)
	prefix := githubCompletionRoundPrefix(round)
	checkpointPath := filepath.Join(completionDir, prefix+"-hardening-checkpoint.json")
	promptPath := filepath.Join(completionDir, prefix+"-hardening-prompt.md")
	stdoutPath := filepath.Join(completionDir, prefix+"-hardening-stdout.log")
	stderrPath := filepath.Join(completionDir, prefix+"-hardening-stderr.log")
	if !completedCodexCheckpoint(checkpointPath) {
		preHardeningUntracked, err := localWorkUntrackedFiles(manifest.SandboxRepoPath)
		if err != nil {
			return githubWorkCompletionRoundSummary{}, localWorkVerificationReport{}, nil, err
		}
		if err := setGithubWorkPhase(manifestPath, manifest, "completion-harden", round); err != nil {
			return githubWorkCompletionRoundSummary{}, localWorkVerificationReport{}, nil, err
		}
		fmt.Fprintf(os.Stdout, "[github] Completion round %d: hardening %d finding(s).\n", round, len(findings))
		prompt, err := buildLocalWorkHardeningPrompt(localManifest, verification, findings)
		if err != nil {
			return githubWorkCompletionRoundSummary{}, localWorkVerificationReport{}, nil, err
		}
		if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
			return githubWorkCompletionRoundSummary{}, localWorkVerificationReport{}, nil, err
		}
		result, err := runLocalWorkCodexPrompt(localManifest, codexArgs, prompt, fmt.Sprintf("github-hardener-round-%d", round), checkpointPath)
		if err := os.WriteFile(stdoutPath, []byte(result.Stdout), 0o644); err != nil {
			return githubWorkCompletionRoundSummary{}, localWorkVerificationReport{}, nil, err
		}
		if err := os.WriteFile(stderrPath, []byte(result.Stderr), 0o644); err != nil {
			return githubWorkCompletionRoundSummary{}, localWorkVerificationReport{}, nil, err
		}
		if err != nil {
			return githubWorkCompletionRoundSummary{}, localWorkVerificationReport{}, nil, err
		}
		if err := refreshLocalWorkSandboxIntentToAddIgnoring(manifest.SandboxRepoPath, preHardeningUntracked); err != nil {
			return githubWorkCompletionRoundSummary{}, localWorkVerificationReport{}, nil, err
		}
	}
	verification, findings, summary, err := runGithubCompletionReviewCycle(manifestPath, completionDir, manifest, codexArgs, round)
	return summary, verification, findings, err
}

func runGithubCompletionReviewCycle(manifestPath string, completionDir string, manifest *githubWorkManifest, codexArgs []string, round int) (localWorkVerificationReport, []githubPullReviewFinding, githubWorkCompletionRoundSummary, error) {
	if err := refreshGithubVerificationArtifactsInPlace(manifestPath, manifest); err != nil {
		return localWorkVerificationReport{}, nil, githubWorkCompletionRoundSummary{}, err
	}
	localManifest := githubWorkLocalManifest(*manifest, manifestPath)
	prefix := githubCompletionRoundPrefix(round)

	verificationPath := filepath.Join(completionDir, prefix+"-verification.json")
	verification, err := loadOrRunGithubVerificationPhase(manifestPath, manifest, verificationPath)
	if err != nil {
		return localWorkVerificationReport{}, nil, githubWorkCompletionRoundSummary{}, err
	}

	reviewFindings, err := loadOrRunGithubReviewPhase(manifestPath, manifest, codexArgs, completionDir, prefix)
	if err != nil {
		return localWorkVerificationReport{}, nil, githubWorkCompletionRoundSummary{}, err
	}

	allFindings := append([]githubPullReviewFinding{}, reviewFindings...)
	finalGateStatus := ""
	finalGateRoles := []string{}
	finalGateRoleResults := []localWorkFinalGateRoleResult{}
	finalGateFindings := 0
	candidateAuditStatus := ""
	candidateBlockedPaths := []string{}

	if verification.Passed && len(reviewFindings) == 0 {
		hasDiff, err := localWorkSandboxHasDiff(localManifest)
		if err != nil {
			return localWorkVerificationReport{}, nil, githubWorkCompletionRoundSummary{}, err
		}
		if !hasDiff {
			finalGateStatus = "no-op"
			candidateAuditStatus = "no-op"
		} else {
			audit, err := auditLocalWorkCandidateFiles(localManifest)
			if err != nil {
				return localWorkVerificationReport{}, nil, githubWorkCompletionRoundSummary{}, err
			}
			candidateAuditStatus = audit.Status
			candidateBlockedPaths = append([]string{}, audit.BlockedPaths...)
			if audit.Status == "blocked-candidate-files" {
				finalGateStatus = "blocked"
			} else {
				if err := setGithubWorkPhase(manifestPath, manifest, "completion-final-review", round); err != nil {
					return localWorkVerificationReport{}, nil, githubWorkCompletionRoundSummary{}, err
				}
				fmt.Fprintf(os.Stdout, "[github] Completion %s: running final review gate.\n", prefix)
				gateFindings, gateRoles, gateRoleResults, gateCount, err := runLocalWorkFinalReviewGate(localManifest, codexArgs, completionDir, prefix)
				if err != nil {
					return localWorkVerificationReport{}, nil, githubWorkCompletionRoundSummary{}, err
				}
				allFindings = append(allFindings, gateFindings...)
				finalGateFindings = gateCount
				finalGateRoles = append(finalGateRoles, gateRoles...)
				finalGateRoleResults = mergeFinalGateRoleResults(finalGateRoleResults, gateRoleResults)
				if gateCount > 0 {
					finalGateStatus = "findings"
				} else {
					finalGateStatus = "passed"
				}
			}
		}
	}

	summary := githubWorkCompletionRoundSummary{
		Round:                 round,
		Status:                "retrying",
		VerificationPassed:    verification.Passed,
		VerificationSummary:   summarizeLocalVerification(verification),
		ReviewFindings:        len(reviewFindings),
		FinalGateStatus:       finalGateStatus,
		FinalGateFindings:     finalGateFindings,
		FinalGateRoles:        uniqueStrings(finalGateRoles),
		CandidateAuditStatus:  candidateAuditStatus,
		CandidateBlockedPaths: append([]string{}, candidateBlockedPaths...),
	}

	manifest.FinalGateStatus = finalGateStatus
	manifest.FinalGateRoleResults = finalGateRoleResults
	manifest.CandidateAuditStatus = candidateAuditStatus
	manifest.CandidateBlockedPaths = append([]string{}, candidateBlockedPaths...)

	if candidateAuditStatus == "blocked-candidate-files" {
		summary.Status = "blocked"
		manifest.ExecutionStatus = "blocked"
		manifest.CurrentPhase = "candidate-blocked"
		manifest.CurrentRound = round
		manifest.LastError = localWorkCandidateBlockedMessage(candidateBlockedPaths)
		manifest.UpdatedAt = ISOTimeNow()
		return verification, nil, summary, errors.New(manifest.LastError)
	}

	filtered := filterKnownFindings(allFindings, manifest.RejectedFindingFingerprints, manifest.PreexistingFindingFingerprints)
	validated, rejected, err := runGithubValidationPhase(manifestPath, manifest, codexArgs, completionDir, round, filtered.Findings)
	if err != nil {
		return localWorkVerificationReport{}, nil, githubWorkCompletionRoundSummary{}, err
	}
	preexistingFindings := rememberedFindingsFromValidated(validated, localWorkFindingPreexisting)
	manifest.RejectedFindingFingerprints = uniqueStrings(append(manifest.RejectedFindingFingerprints, rejected...))
	manifest.PreexistingFindingFingerprints = uniqueStrings(append(manifest.PreexistingFindingFingerprints, rememberedFindingFingerprints(preexistingFindings)...))
	manifest.PreexistingFindings = mergeRememberedFindings(manifest.PreexistingFindings, preexistingFindings)

	summary.ValidatedFindings = len(validated)
	summary.ConfirmedFindings = countValidatedFindingsByStatus(validated, localWorkFindingConfirmed)
	summary.RejectedFindings = len(rejected)
	summary.PreexistingFindings = len(preexistingFindings)
	summary.ModifiedFindings = countValidatedFindingsByStatus(validated, localWorkFindingModified)

	finalFindings := findingsFromValidated(validated)
	if verification.Passed && len(finalFindings) == 0 {
		summary.Status = "completed"
	}
	manifest.LastError = ""
	manifest.UpdatedAt = ISOTimeNow()
	return verification, finalFindings, summary, nil
}

func loadOrRunGithubVerificationPhase(manifestPath string, manifest *githubWorkManifest, artifactPath string) (localWorkVerificationReport, error) {
	if fileExists(artifactPath) {
		var report localWorkVerificationReport
		if err := readGithubJSON(artifactPath, &report); err == nil {
			return report, nil
		}
	}
	if err := setGithubWorkPhase(manifestPath, manifest, "completion-verify", githubCompletionRoundFromPath(artifactPath)); err != nil {
		return localWorkVerificationReport{}, err
	}
	fmt.Fprintf(os.Stdout, "[github] %s: running verification.\n", filepath.Base(strings.TrimSuffix(artifactPath, ".json")))
	report, err := runLocalVerification(manifest.SandboxRepoPath, *manifest.VerificationPlan, true)
	if err != nil {
		return localWorkVerificationReport{}, err
	}
	if err := os.WriteFile(artifactPath, mustMarshalJSON(report), 0o644); err != nil {
		return localWorkVerificationReport{}, err
	}
	return report, nil
}

func loadOrRunGithubReviewPhase(manifestPath string, manifest *githubWorkManifest, codexArgs []string, completionDir string, prefix string) ([]githubPullReviewFinding, error) {
	findingsPath := filepath.Join(completionDir, prefix+"-review-findings.json")
	checkpointPath := filepath.Join(completionDir, prefix+"-review-checkpoint.json")
	if fileExists(findingsPath) && completedCodexCheckpoint(checkpointPath) {
		var findings []githubPullReviewFinding
		if err := readGithubJSON(findingsPath, &findings); err == nil {
			return findings, nil
		}
	}
	localManifest := githubWorkLocalManifest(*manifest, manifestPath)
	if err := setGithubWorkPhase(manifestPath, manifest, "completion-review", githubCompletionRoundFromPrefix(prefix)); err != nil {
		return nil, err
	}
	prompt, err := buildLocalWorkReviewPrompt(localManifest)
	if err != nil {
		return nil, err
	}
	promptPath := filepath.Join(completionDir, prefix+"-review-prompt.md")
	stdoutPath := filepath.Join(completionDir, prefix+"-review-stdout.log")
	stderrPath := filepath.Join(completionDir, prefix+"-review-stderr.log")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		return nil, err
	}
	result, findings, err := runLocalWorkReviewWithAlias(localManifest, codexArgs, prompt, "github-reviewer", checkpointPath)
	if err := os.WriteFile(stdoutPath, []byte(result.Stdout), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(stderrPath, []byte(result.Stderr), 0o644); err != nil {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(findingsPath, mustMarshalJSON(findings), 0o644); err != nil {
		return nil, err
	}
	return findings, nil
}

func runGithubValidationPhase(manifestPath string, manifest *githubWorkManifest, codexArgs []string, completionDir string, round int, findings []githubPullReviewFinding) ([]localWorkValidatedFinding, []string, error) {
	if len(findings) == 0 {
		validatedPath := filepath.Join(completionDir, githubCompletionRoundPrefix(round)+"-validated-findings.json")
		rejectedPath := filepath.Join(completionDir, githubCompletionRoundPrefix(round)+"-rejected-fingerprints.json")
		if !fileExists(validatedPath) {
			if err := writeJSONArtifact(validatedPath, []localWorkValidatedFinding{}); err != nil {
				return nil, nil, err
			}
		}
		if !fileExists(rejectedPath) {
			if err := writeJSONArtifact(rejectedPath, []string{}); err != nil {
				return nil, nil, err
			}
		}
		return nil, nil, nil
	}
	validatedPath := filepath.Join(completionDir, githubCompletionRoundPrefix(round)+"-validated-findings.json")
	rejectedPath := filepath.Join(completionDir, githubCompletionRoundPrefix(round)+"-rejected-fingerprints.json")
	if fileExists(validatedPath) && fileExists(rejectedPath) {
		var validated []localWorkValidatedFinding
		var rejected []string
		if err := readGithubJSON(validatedPath, &validated); err == nil {
			if err := readGithubJSON(rejectedPath, &rejected); err == nil {
				return validated, rejected, nil
			}
		}
	}
	if err := setGithubWorkPhase(manifestPath, manifest, "completion-validate", round); err != nil {
		return nil, nil, err
	}
	groups := groupFindingsAsSingletons(findings)
	grouping := localWorkGroupingResult{
		RequestedPolicy: localWorkSingletonPolicy,
		EffectivePolicy: localWorkSingletonPolicy,
		Attempts:        1,
		Groups:          groupingGroupsFromFindingGroups(groups),
	}
	if err := writeJSONArtifact(filepath.Join(completionDir, githubCompletionRoundPrefix(round)+"-grouping-result.json"), grouping); err != nil {
		return nil, nil, err
	}
	context := &localWorkValidationContextState{
		Name:             githubCompletionRoundPrefix(round),
		Round:            round,
		RequestedPolicy:  localWorkSingletonPolicy,
		EffectivePolicy:  localWorkSingletonPolicy,
		GroupingComplete: true,
	}
	localManifest := githubWorkLocalManifest(*manifest, manifestPath)
	validated, rejected, _, err := validateFindingGroups(localManifest, codexArgs, completionDir, round, groups, context, nil)
	if err != nil {
		return nil, nil, err
	}
	if err := writeJSONArtifact(validatedPath, validated); err != nil {
		return nil, nil, err
	}
	if err := writeJSONArtifact(rejectedPath, rejected); err != nil {
		return nil, nil, err
	}
	return validated, rejected, nil
}

func refreshGithubVerificationArtifactsInPlace(manifestPath string, manifest *githubWorkManifest) error {
	if manifest == nil {
		return fmt.Errorf("missing GitHub work manifest")
	}
	plan := detectGithubVerificationPlan(manifest.SandboxRepoPath)
	scriptsDir, err := writeGithubVerificationScripts(manifest.SandboxPath, manifest.SandboxRepoPath, plan, manifest.RunID)
	if err != nil {
		return err
	}
	manifest.VerificationPlan = &plan
	manifest.VerificationScriptsDir = scriptsDir
	manifest.UpdatedAt = ISOTimeNow()
	return persistGithubWorkManifest(manifestPath, *manifest)
}

func ensureGithubWorkBaselineSHA(manifestPath string, manifest *githubWorkManifest) error {
	if manifest == nil {
		return fmt.Errorf("missing GitHub work manifest")
	}
	_, err := captureGithubWorkBaselineIfMissing(manifestPath, manifest)
	return err
}

func githubWorkLocalManifest(manifest githubWorkManifest, manifestPath string) localWorkManifest {
	repoRoot := strings.TrimSpace(manifest.SourcePath)
	if repoRoot == "" {
		repoRoot = strings.TrimSpace(manifest.ManagedRepoRoot)
	}
	if repoRoot == "" {
		repoRoot = strings.TrimSpace(manifest.SandboxPath)
	}
	return localWorkManifest{
		RunID:                 manifest.RunID,
		RepoRoot:              repoRoot,
		RepoName:              manifest.RepoName,
		SandboxPath:           manifest.SandboxPath,
		SandboxRepoPath:       manifest.SandboxRepoPath,
		BaselineSHA:           manifest.BaselineSHA,
		VerificationPlan:      manifest.VerificationPlan,
		WorkType:              manifest.WorkType,
		IntegrationPolicy:     "always",
		GroupingPolicy:        localWorkSingletonPolicy,
		ValidationParallelism: 1,
		CurrentIteration:      1,
		RateLimitPolicy:       string(codexRateLimitPolicyWaitInProcess),
		APIBaseURL:            manifest.APIBaseURL,
		PauseManifestPath:     manifestPath,
	}
}

func setGithubWorkPhase(manifestPath string, manifest *githubWorkManifest, phase string, round int) error {
	if manifest == nil {
		return fmt.Errorf("missing GitHub work manifest")
	}
	manifest.ExecutionStatus = "running"
	manifest.CurrentPhase = phase
	manifest.CurrentRound = round
	manifest.UpdatedAt = ISOTimeNow()
	return persistGithubWorkManifest(manifestPath, *manifest)
}

func persistGithubWorkManifest(manifestPath string, manifest githubWorkManifest) error {
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		return err
	}
	return indexGithubWorkRunManifest(manifestPath, manifest)
}

func completedCodexCheckpoint(path string) bool {
	checkpoint, err := readCodexStepCheckpoint(path)
	if err != nil {
		return false
	}
	return checkpoint.Status == "completed"
}

func githubCompletionRoundPrefix(round int) string {
	if round <= 0 {
		return "bootstrap"
	}
	return fmt.Sprintf("round-%d", round)
}

func githubCompletionRoundFromPrefix(prefix string) int {
	if prefix == "bootstrap" {
		return 0
	}
	if strings.HasPrefix(prefix, "round-") {
		if value, err := strconv.Atoi(strings.TrimPrefix(prefix, "round-")); err == nil {
			return value
		}
	}
	return 0
}

func githubCompletionRoundFromPath(path string) int {
	base := filepath.Base(path)
	switch {
	case strings.HasPrefix(base, "bootstrap-"):
		return 0
	case strings.HasPrefix(base, "round-"):
		parts := strings.SplitN(base, "-", 3)
		if len(parts) >= 2 {
			if value, err := strconv.Atoi(parts[1]); err == nil {
				return value
			}
		}
	}
	return 0
}

func recordGithubCompletionRound(manifest *githubWorkManifest, summary githubWorkCompletionRoundSummary) {
	if manifest == nil {
		return
	}
	replaced := false
	for i := range manifest.CompletionRounds {
		if manifest.CompletionRounds[i].Round == summary.Round {
			manifest.CompletionRounds[i] = summary
			replaced = true
			break
		}
	}
	if !replaced {
		manifest.CompletionRounds = append(manifest.CompletionRounds, summary)
	}
	sort.Slice(manifest.CompletionRounds, func(i, j int) bool {
		return manifest.CompletionRounds[i].Round < manifest.CompletionRounds[j].Round
	})
}

func buildGithubCompletionExhaustedMessage(verification localWorkVerificationReport, findings []githubPullReviewFinding) string {
	if !verification.Passed && len(findings) > 0 {
		return fmt.Sprintf("GitHub work completion loop exhausted hardening rounds with verification failures (%s) and %d actionable finding(s)", summarizeLocalVerification(verification), len(findings))
	}
	if !verification.Passed {
		return fmt.Sprintf("GitHub work completion loop exhausted hardening rounds with verification failures (%s)", summarizeLocalVerification(verification))
	}
	return fmt.Sprintf("GitHub work completion loop exhausted hardening rounds with %d actionable finding(s)", len(findings))
}
