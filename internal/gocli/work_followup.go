package gocli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	workFollowupMaxRounds                = 3
	workFollowupDecisionNoFollowups      = "no_followups"
	workFollowupDecisionFollowups        = "followups"
	workFollowupDecisionApprovedFollowup = "approved_followups"
	workFollowupKindFunctionalChange     = "functional_change"
	workFollowupKindTestCoverage         = "test_coverage"
)

type workFollowupItem struct {
	Title         string `json:"title"`
	Kind          string `json:"kind"`
	Summary       string `json:"summary,omitempty"`
	Rationale     string `json:"rationale,omitempty"`
	GoalAlignment string `json:"goal_alignment,omitempty"`
}

type workFollowupRejectedItem struct {
	Title  string `json:"title"`
	Reason string `json:"reason,omitempty"`
}

type workFollowupPlannerResult struct {
	Decision string             `json:"decision"`
	Items    []workFollowupItem `json:"items,omitempty"`
}

type workFollowupReviewResult struct {
	Decision      string                     `json:"decision"`
	ApprovedItems []workFollowupItem         `json:"approved_items,omitempty"`
	RejectedItems []workFollowupRejectedItem `json:"rejected_items,omitempty"`
	Summary       string                     `json:"summary,omitempty"`
}

type workFollowupRoundSummary struct {
	Round           int      `json:"round"`
	PlannerDecision string   `json:"planner_decision,omitempty"`
	ReviewDecision  string   `json:"review_decision,omitempty"`
	ProposedItems   int      `json:"proposed_items,omitempty"`
	ApprovedItems   int      `json:"approved_items,omitempty"`
	RejectedItems   int      `json:"rejected_items,omitempty"`
	ApprovedKinds   []string `json:"approved_kinds,omitempty"`
	StopReason      string   `json:"stop_reason,omitempty"`
}

type workFollowupPromptContext struct {
	Goal                string
	WorkType            string
	RepoRoot            string
	BaselineSHA         string
	ChangedFiles        string
	DiffSummary         string
	VerificationSummary string
}

func buildLocalWorkFollowupPromptContext(manifest localWorkManifest, verification localWorkVerificationReport) (workFollowupPromptContext, error) {
	goalBytes, err := os.ReadFile(manifest.InputPath)
	if err != nil {
		return workFollowupPromptContext{}, err
	}
	return buildWorkFollowupPromptContext(
		strings.TrimSpace(string(goalBytes)),
		manifest.WorkType,
		manifest.RepoRoot,
		manifest.SandboxRepoPath,
		manifest.BaselineSHA,
		summarizeLocalVerification(verification),
	)
}

func buildGithubWorkFollowupPromptContext(manifest githubWorkManifest, verification localWorkVerificationReport) (workFollowupPromptContext, error) {
	goal := strings.TrimSpace(manifest.TargetTitle)
	if goal == "" {
		goal = fmt.Sprintf("Implement GitHub %s #%d for %s", manifest.TargetKind, manifest.TargetNumber, manifest.RepoSlug)
	} else {
		goal = fmt.Sprintf("Implement %s for %s", goal, manifest.RepoSlug)
	}
	if strings.TrimSpace(manifest.TargetURL) != "" {
		goal += "\n\nTarget URL: " + strings.TrimSpace(manifest.TargetURL)
	}
	return buildWorkFollowupPromptContext(
		goal,
		manifest.WorkType,
		manifest.RepoSlug,
		manifest.SandboxRepoPath,
		manifest.BaselineSHA,
		summarizeLocalVerification(verification),
	)
}

func buildWorkFollowupPromptContext(goal string, workType string, repoRoot string, sandboxRepoPath string, baselineSHA string, verificationSummary string) (workFollowupPromptContext, error) {
	context, err := buildReviewPromptContext(sandboxRepoPath, []string{baselineSHA}, reviewPromptContextOptions{
		ChangedFilesLimit: reviewPromptChangedFilesLimit,
		MaxHunksPerFile:   reviewPromptMaxHunksPerFile,
		MaxLinesPerFile:   reviewPromptMaxLinesPerFile,
		MaxCharsPerFile:   reviewPromptMaxCharsPerFile,
	})
	if err != nil {
		return workFollowupPromptContext{}, err
	}
	return workFollowupPromptContext{
		Goal:                strings.TrimSpace(goal),
		WorkType:            normalizeWorkType(workType),
		RepoRoot:            repoRoot,
		BaselineSHA:         baselineSHA,
		ChangedFiles:        context.ChangedFilesText,
		DiffSummary:         context.DiffSummary,
		VerificationSummary: verificationSummary,
	}, nil
}

func buildWorkFollowupPlannerPrompt(context workFollowupPromptContext, round int) string {
	lines := []string{
		"Plan any in-scope followups for this Nana work run and return JSON only.",
		`Schema: {"decision":"no_followups|followups","items":[{"title":"...","kind":"functional_change|test_coverage","summary":"...","rationale":"...","goal_alignment":"..."}]}`,
		"If there is nothing left that is directly required to finish the current goal, return {\"decision\":\"no_followups\",\"items\":[]}.",
		"",
		"Rules:",
		"- Only propose followups that are still required to complete the current goal.",
		"- Allowed followup kinds are functional_change and test_coverage.",
		"- Do not propose docs, style cleanups, unrelated refactors, infra work, or new scope expansion.",
		fmt.Sprintf("- Current followup round: %d/%d.", round, workFollowupMaxRounds),
		"",
		fmt.Sprintf("Work type: %s", workTypeDisplayName(context.WorkType)),
		fmt.Sprintf("Repo root: %s", context.RepoRoot),
		fmt.Sprintf("Baseline SHA: %s", context.BaselineSHA),
		fmt.Sprintf("Changed files: %s", context.ChangedFiles),
		fmt.Sprintf("Verification summary: %s", defaultString(context.VerificationSummary, "(none)")),
		"",
		"Original goal:",
		context.Goal,
		"",
		"Diff summary:",
		context.DiffSummary,
	}
	return capPromptChars(strings.Join(lines, "\n"), reviewPromptLocalCharLimit)
}

func buildWorkFollowupReviewerPrompt(context workFollowupPromptContext, plan workFollowupPlannerResult, round int) string {
	planPayload := string(mustMarshalJSON(plan))
	lines := []string{
		"Review this followup plan for scope discipline and return JSON only.",
		`Schema: {"decision":"no_followups|approved_followups","approved_items":[{"title":"...","kind":"functional_change|test_coverage","summary":"...","rationale":"...","goal_alignment":"..."}],"rejected_items":[{"title":"...","reason":"..."}],"summary":"..."}`,
		"If the proposed plan contains no in-scope followups, return {\"decision\":\"no_followups\",\"approved_items\":[],\"rejected_items\":[...],\"summary\":\"...\"}.",
		"",
		"Rules:",
		"- Approve only followups that are directly tied to the original goal.",
		"- Approved items may only be functional_change or test_coverage.",
		"- Reject anything unrelated to the goal, docs-only work, style-only cleanup, infra churn, or broader scope expansion.",
		"- If the work type is test_only, reject every functional_change item.",
		fmt.Sprintf("- Current followup round: %d/%d.", round, workFollowupMaxRounds),
		"",
		fmt.Sprintf("Work type: %s", workTypeDisplayName(context.WorkType)),
		"Original goal:",
		context.Goal,
		"",
		"Planner proposal JSON:",
		planPayload,
	}
	return capPromptChars(strings.Join(lines, "\n"), reviewPromptLocalCharLimit)
}

func buildWorkFollowupImplementationPrompt(context workFollowupPromptContext, approved []workFollowupItem, round int) string {
	lines := []string{
		"# NANA Followup Implementation",
		"",
		fmt.Sprintf("Followup round: %d/%d", round, workFollowupMaxRounds),
		fmt.Sprintf("Work type: %s", workTypeDisplayName(context.WorkType)),
		fmt.Sprintf("Repo root: %s", context.RepoRoot),
		fmt.Sprintf("Baseline SHA: %s", context.BaselineSHA),
		"",
		"Original goal:",
		context.Goal,
		"",
		"Approved followups:",
	}
	for _, item := range approved {
		lines = append(lines, fmt.Sprintf("- [%s] %s", item.Kind, item.Title))
		if strings.TrimSpace(item.Summary) != "" {
			lines = append(lines, "  Summary: "+strings.TrimSpace(item.Summary))
		}
		if strings.TrimSpace(item.Rationale) != "" {
			lines = append(lines, "  Rationale: "+strings.TrimSpace(item.Rationale))
		}
		if strings.TrimSpace(item.GoalAlignment) != "" {
			lines = append(lines, "  Goal alignment: "+strings.TrimSpace(item.GoalAlignment))
		}
	}
	lines = append(lines,
		"",
		"Contract:",
		"- Implement only the approved followup items listed above.",
		"- Do not expand scope beyond those items.",
		"- Do not submit, publish, push, or open PRs.",
		"- Add or update targeted tests when the approved followups require them.",
	)
	return capPromptChars(strings.Join(lines, "\n"), localWorkImplementPromptCharLimit)
}

func parseWorkFollowupPlannerResult(content []byte, workType string) (workFollowupPlannerResult, error) {
	var result workFollowupPlannerResult
	trimmed := bytes.TrimSpace(extractWorkFollowupJSONObject(content))
	if len(trimmed) == 0 {
		return workFollowupPlannerResult{Decision: workFollowupDecisionNoFollowups, Items: nil}, nil
	}
	if err := json.Unmarshal(trimmed, &result); err != nil {
		return workFollowupPlannerResult{}, fmt.Errorf("followup planner output did not match the expected JSON schema")
	}
	return validateWorkFollowupPlannerResult(result, workType)
}

func parseWorkFollowupReviewResult(content []byte, planner workFollowupPlannerResult, workType string) (workFollowupReviewResult, error) {
	var result workFollowupReviewResult
	trimmed := bytes.TrimSpace(extractWorkFollowupJSONObject(content))
	if len(trimmed) == 0 {
		return workFollowupReviewResult{Decision: workFollowupDecisionNoFollowups}, nil
	}
	if err := json.Unmarshal(trimmed, &result); err != nil {
		return workFollowupReviewResult{}, fmt.Errorf("followup reviewer output did not match the expected JSON schema")
	}
	return validateWorkFollowupReviewResult(result, planner, workType)
}

func validateWorkFollowupPlannerResult(result workFollowupPlannerResult, workType string) (workFollowupPlannerResult, error) {
	switch strings.TrimSpace(result.Decision) {
	case workFollowupDecisionNoFollowups:
		result.Items = nil
		return result, nil
	case workFollowupDecisionFollowups:
	default:
		return workFollowupPlannerResult{}, fmt.Errorf("followup planner returned invalid decision %q", result.Decision)
	}
	if len(result.Items) == 0 {
		return workFollowupPlannerResult{}, fmt.Errorf("followup planner returned decision %q without any items", result.Decision)
	}
	seen := map[string]struct{}{}
	for index, item := range result.Items {
		normalized, err := normalizeWorkFollowupItem(item, workType)
		if err != nil {
			return workFollowupPlannerResult{}, fmt.Errorf("followup planner item %d: %w", index+1, err)
		}
		key := normalized.Kind + "\x00" + strings.ToLower(strings.TrimSpace(normalized.Title))
		if _, ok := seen[key]; ok {
			return workFollowupPlannerResult{}, fmt.Errorf("followup planner returned duplicate item %q", normalized.Title)
		}
		seen[key] = struct{}{}
		result.Items[index] = normalized
	}
	return result, nil
}

func validateWorkFollowupReviewResult(result workFollowupReviewResult, planner workFollowupPlannerResult, workType string) (workFollowupReviewResult, error) {
	switch strings.TrimSpace(result.Decision) {
	case workFollowupDecisionNoFollowups:
		result.ApprovedItems = nil
	case workFollowupDecisionApprovedFollowup:
		if len(result.ApprovedItems) == 0 {
			return workFollowupReviewResult{}, fmt.Errorf("followup reviewer returned %q without approved items", result.Decision)
		}
	default:
		return workFollowupReviewResult{}, fmt.Errorf("followup reviewer returned invalid decision %q", result.Decision)
	}
	allowed := map[string]workFollowupItem{}
	for _, item := range planner.Items {
		allowed[item.Kind+"\x00"+strings.ToLower(strings.TrimSpace(item.Title))] = item
	}
	for index, item := range result.ApprovedItems {
		normalized, err := normalizeWorkFollowupItem(item, workType)
		if err != nil {
			return workFollowupReviewResult{}, fmt.Errorf("followup reviewer approved item %d: %w", index+1, err)
		}
		key := normalized.Kind + "\x00" + strings.ToLower(strings.TrimSpace(normalized.Title))
		if _, ok := allowed[key]; !ok {
			return workFollowupReviewResult{}, fmt.Errorf("followup reviewer approved item %q that was not in the planner output", normalized.Title)
		}
		result.ApprovedItems[index] = normalized
	}
	for index, item := range result.RejectedItems {
		item.Title = strings.TrimSpace(item.Title)
		item.Reason = strings.TrimSpace(item.Reason)
		if item.Title == "" {
			return workFollowupReviewResult{}, fmt.Errorf("followup reviewer rejected item %d is missing a title", index+1)
		}
		result.RejectedItems[index] = item
	}
	return result, nil
}

func normalizeWorkFollowupItem(item workFollowupItem, workType string) (workFollowupItem, error) {
	item.Title = strings.TrimSpace(item.Title)
	item.Kind = strings.TrimSpace(item.Kind)
	item.Summary = strings.TrimSpace(item.Summary)
	item.Rationale = strings.TrimSpace(item.Rationale)
	item.GoalAlignment = strings.TrimSpace(item.GoalAlignment)
	if item.Title == "" {
		return workFollowupItem{}, fmt.Errorf("missing title")
	}
	switch item.Kind {
	case workFollowupKindFunctionalChange, workFollowupKindTestCoverage:
	default:
		return workFollowupItem{}, fmt.Errorf("invalid kind %q", item.Kind)
	}
	if normalizeWorkType(workType) == workTypeTestOnly && item.Kind == workFollowupKindFunctionalChange {
		return workFollowupItem{}, fmt.Errorf("functional_change is not allowed for work type %s", workTypeTestOnly)
	}
	return item, nil
}

func extractWorkFollowupJSONObject(content []byte) []byte {
	text := string(content)
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		return []byte(text[start : end+1])
	}
	return nil
}

func workFollowupSummaryFromResults(round int, planner workFollowupPlannerResult, reviewer workFollowupReviewResult, stopReason string) workFollowupRoundSummary {
	kinds := []string{}
	for _, item := range reviewer.ApprovedItems {
		kinds = append(kinds, item.Kind)
	}
	return workFollowupRoundSummary{
		Round:           round,
		PlannerDecision: planner.Decision,
		ReviewDecision:  reviewer.Decision,
		ProposedItems:   len(planner.Items),
		ApprovedItems:   len(reviewer.ApprovedItems),
		RejectedItems:   len(reviewer.RejectedItems),
		ApprovedKinds:   uniqueStrings(kinds),
		StopReason:      strings.TrimSpace(stopReason),
	}
}

func workFollowupRoundDir(baseDir string, round int) string {
	return filepath.Join(baseDir, fmt.Sprintf("followup-round-%d", round))
}

func runLocalWorkFollowupRound(manifest localWorkManifest, codexArgs []string, iterationDir string, round int, verification localWorkVerificationReport) (workFollowupRoundSummary, []workFollowupItem, []workFollowupRejectedItem, error) {
	followupDir := workFollowupRoundDir(iterationDir, round)
	if err := os.MkdirAll(followupDir, 0o755); err != nil {
		return workFollowupRoundSummary{}, nil, nil, err
	}
	context, err := buildLocalWorkFollowupPromptContext(manifest, verification)
	if err != nil {
		return workFollowupRoundSummary{}, nil, nil, err
	}
	planner, err := loadOrRunLocalWorkFollowupPlanner(manifest, codexArgs, followupDir, round, context)
	if err != nil {
		return workFollowupRoundSummary{}, nil, nil, err
	}
	reviewer, err := loadOrRunLocalWorkFollowupReview(manifest, codexArgs, followupDir, round, context, planner)
	if err != nil {
		return workFollowupRoundSummary{}, nil, nil, err
	}
	return workFollowupSummaryFromResults(round, planner, reviewer, ""), reviewer.ApprovedItems, reviewer.RejectedItems, nil
}

func loadOrRunLocalWorkFollowupPlanner(manifest localWorkManifest, codexArgs []string, followupDir string, round int, context workFollowupPromptContext) (workFollowupPlannerResult, error) {
	resultPath := filepath.Join(followupDir, "planner-result.json")
	if fileExists(resultPath) {
		var stored workFollowupPlannerResult
		if err := readGithubJSON(resultPath, &stored); err == nil {
			return validateWorkFollowupPlannerResult(stored, manifest.WorkType)
		}
	}
	prompt := buildWorkFollowupPlannerPrompt(context, round)
	promptPath := filepath.Join(followupDir, "planner-prompt.md")
	stdoutPath := filepath.Join(followupDir, "planner-stdout.log")
	stderrPath := filepath.Join(followupDir, "planner-stderr.log")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		return workFollowupPlannerResult{}, err
	}
	result, err := runLocalWorkCodexPrompt(manifest, codexArgs, prompt, fmt.Sprintf("followup-planner-round-%d", round), filepath.Join(followupDir, "planner-checkpoint.json"))
	if err := os.WriteFile(stdoutPath, []byte(result.Stdout), 0o644); err != nil {
		return workFollowupPlannerResult{}, err
	}
	if err := os.WriteFile(stderrPath, []byte(result.Stderr), 0o644); err != nil {
		return workFollowupPlannerResult{}, err
	}
	if err != nil {
		return workFollowupPlannerResult{}, err
	}
	parsed, err := parseWorkFollowupPlannerResult([]byte(result.Stdout), manifest.WorkType)
	if err != nil {
		return workFollowupPlannerResult{}, err
	}
	if err := writeJSONArtifact(resultPath, parsed); err != nil {
		return workFollowupPlannerResult{}, err
	}
	return parsed, nil
}

func loadOrRunLocalWorkFollowupReview(manifest localWorkManifest, codexArgs []string, followupDir string, round int, context workFollowupPromptContext, planner workFollowupPlannerResult) (workFollowupReviewResult, error) {
	resultPath := filepath.Join(followupDir, "review-result.json")
	if fileExists(resultPath) {
		var stored workFollowupReviewResult
		if err := readGithubJSON(resultPath, &stored); err == nil {
			return validateWorkFollowupReviewResult(stored, planner, manifest.WorkType)
		}
	}
	prompt := buildWorkFollowupReviewerPrompt(context, planner, round)
	promptPath := filepath.Join(followupDir, "review-prompt.md")
	stdoutPath := filepath.Join(followupDir, "review-stdout.log")
	stderrPath := filepath.Join(followupDir, "review-stderr.log")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		return workFollowupReviewResult{}, err
	}
	result, err := runLocalWorkCodexPrompt(manifest, codexArgs, prompt, fmt.Sprintf("followup-reviewer-round-%d", round), filepath.Join(followupDir, "review-checkpoint.json"))
	if err := os.WriteFile(stdoutPath, []byte(result.Stdout), 0o644); err != nil {
		return workFollowupReviewResult{}, err
	}
	if err := os.WriteFile(stderrPath, []byte(result.Stderr), 0o644); err != nil {
		return workFollowupReviewResult{}, err
	}
	if err != nil {
		return workFollowupReviewResult{}, err
	}
	parsed, err := parseWorkFollowupReviewResult([]byte(result.Stdout), planner, manifest.WorkType)
	if err != nil {
		return workFollowupReviewResult{}, err
	}
	if err := writeJSONArtifact(resultPath, parsed); err != nil {
		return workFollowupReviewResult{}, err
	}
	return parsed, nil
}

func runGithubWorkFollowupLoop(manifestPath string, runDir string, manifest *githubWorkManifest, codexArgs []string) error {
	if manifest == nil {
		return fmt.Errorf("missing GitHub work manifest")
	}
	for {
		if err := runGithubWorkCompletionLoop(manifestPath, runDir, manifest, codexArgs); err != nil {
			return err
		}
		if strings.TrimSpace(manifest.FinalGateStatus) == "no-op" && strings.TrimSpace(manifest.CandidateAuditStatus) == "no-op" {
			return nil
		}
		nextRound := len(manifest.FollowupRounds) + 1
		summary, approved, err := runGithubWorkFollowupRound(manifestPath, runDir, manifest, codexArgs, nextRound)
		if summary.Round > 0 {
			manifest.FollowupRounds = append(manifest.FollowupRounds, summary)
			manifest.FollowupDecision = summary.ReviewDecision
			manifest.UpdatedAt = ISOTimeNow()
			if persistErr := persistGithubWorkManifest(manifestPath, *manifest); persistErr != nil {
				return persistErr
			}
		}
		if err != nil {
			return err
		}
		if summary.ReviewDecision == workFollowupDecisionNoFollowups {
			return nil
		}
		if nextRound >= workFollowupMaxRounds {
			manifest.ExecutionStatus = "failed"
			manifest.CurrentPhase = "followup-max-rounds"
			manifest.CurrentRound = nextRound
			manifest.LastError = fmt.Sprintf("GitHub work run %s exhausted followup rounds (%d) with approved followups still remaining", manifest.RunID, workFollowupMaxRounds)
			manifest.UpdatedAt = ISOTimeNow()
			if err := persistGithubWorkManifest(manifestPath, *manifest); err != nil {
				return err
			}
			return errors.New(manifest.LastError)
		}
		if err := runGithubFollowupImplementation(manifestPath, runDir, manifest, codexArgs, nextRound, approved); err != nil {
			return err
		}
	}
}

func runGithubWorkFollowupRound(manifestPath string, runDir string, manifest *githubWorkManifest, codexArgs []string, round int) (workFollowupRoundSummary, []workFollowupItem, error) {
	if manifest == nil {
		return workFollowupRoundSummary{}, nil, fmt.Errorf("missing GitHub work manifest")
	}
	followupDir := filepath.Join(runDir, "followups", fmt.Sprintf("round-%d", round))
	if err := os.MkdirAll(followupDir, 0o755); err != nil {
		return workFollowupRoundSummary{}, nil, err
	}
	localManifest := githubWorkLocalManifest(*manifest, manifestPath)
	verification := localWorkVerificationReport{Passed: true}
	if len(manifest.CompletionRounds) > 0 {
		last := manifest.CompletionRounds[len(manifest.CompletionRounds)-1]
		verification.Passed = last.VerificationPassed
		verification.FailedStages = nil
	}
	context, err := buildGithubWorkFollowupPromptContext(*manifest, verification)
	if err != nil {
		return workFollowupRoundSummary{}, nil, err
	}
	if err := setGithubWorkPhase(manifestPath, manifest, "followup-plan", round); err != nil {
		return workFollowupRoundSummary{}, nil, err
	}
	planner, err := loadOrRunLocalWorkFollowupPlanner(localManifest, codexArgs, followupDir, round, context)
	if err != nil {
		return workFollowupRoundSummary{}, nil, err
	}
	if err := setGithubWorkPhase(manifestPath, manifest, "followup-review", round); err != nil {
		return workFollowupRoundSummary{}, nil, err
	}
	reviewer, err := loadOrRunLocalWorkFollowupReview(localManifest, codexArgs, followupDir, round, context, planner)
	if err != nil {
		return workFollowupRoundSummary{}, nil, err
	}
	return workFollowupSummaryFromResults(round, planner, reviewer, ""), reviewer.ApprovedItems, nil
}

func runGithubFollowupImplementation(manifestPath string, runDir string, manifest *githubWorkManifest, codexArgs []string, round int, approved []workFollowupItem) error {
	if manifest == nil {
		return fmt.Errorf("missing GitHub work manifest")
	}
	followupDir := filepath.Join(runDir, "followups", fmt.Sprintf("round-%d", round))
	if err := os.MkdirAll(followupDir, 0o755); err != nil {
		return err
	}
	localManifest := githubWorkLocalManifest(*manifest, manifestPath)
	context, err := buildGithubWorkFollowupPromptContext(*manifest, localWorkVerificationReport{Passed: true})
	if err != nil {
		return err
	}
	prompt := buildWorkFollowupImplementationPrompt(context, approved, round)
	promptPath := filepath.Join(followupDir, "implementation-prompt.md")
	stdoutPath := filepath.Join(followupDir, "implementation-stdout.log")
	stderrPath := filepath.Join(followupDir, "implementation-stderr.log")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		return err
	}
	if err := setGithubWorkPhase(manifestPath, manifest, "followup-implement", round); err != nil {
		return err
	}
	result, err := runLocalWorkCodexPrompt(localManifest, codexArgs, prompt, fmt.Sprintf("github-followup-leader-round-%d", round), filepath.Join(followupDir, "implementation-checkpoint.json"))
	if err := os.WriteFile(stdoutPath, []byte(result.Stdout), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(stderrPath, []byte(result.Stderr), 0o644); err != nil {
		return err
	}
	return err
}
