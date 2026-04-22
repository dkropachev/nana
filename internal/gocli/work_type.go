package gocli

import (
	"fmt"
	"sort"
	"strings"
)

const (
	workTypeBugFix   = "bug_fix"
	workTypeRefactor = "refactor"
	workTypeFeature  = "feature"
	workTypeTestOnly = "test_only"
)

var supportedWorkTypes = []string{
	workTypeBugFix,
	workTypeRefactor,
	workTypeFeature,
	workTypeTestOnly,
}

type workTypeResolution struct {
	WorkType  string
	Source    string
	Ambiguous []string
}

func normalizeWorkType(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	normalized = strings.ReplaceAll(normalized, " ", "_")
	switch normalized {
	case workTypeBugFix:
		return workTypeBugFix
	case workTypeRefactor:
		return workTypeRefactor
	case workTypeFeature:
		return workTypeFeature
	case workTypeTestOnly:
		return workTypeTestOnly
	default:
		return ""
	}
}

func validWorkType(value string) bool {
	return normalizeWorkType(value) != ""
}

func workTypeDisplayName(value string) string {
	switch normalizeWorkType(value) {
	case workTypeBugFix:
		return "Bug fix"
	case workTypeRefactor:
		return "Refactor"
	case workTypeFeature:
		return "Feature"
	case workTypeTestOnly:
		return "Test only"
	default:
		return "Unknown"
	}
}

func workTypeCanonicalLabel(value string) string {
	switch normalizeWorkType(value) {
	case workTypeBugFix:
		return "nana:type:bug-fix"
	case workTypeRefactor:
		return "nana:type:refactor"
	case workTypeFeature:
		return "nana:type:feature"
	case workTypeTestOnly:
		return "nana:type:test-only"
	default:
		return ""
	}
}

func workTypeChoicesText() string {
	choices := make([]string, 0, len(supportedWorkTypes))
	for _, value := range supportedWorkTypes {
		choices = append(choices, value)
	}
	return strings.Join(choices, ", ")
}

func parseRequiredWorkType(value string, flag string) (string, error) {
	normalized := normalizeWorkType(value)
	if normalized == "" {
		return "", fmt.Errorf("invalid %s value %q. Expected one of %s", flag, strings.TrimSpace(value), workTypeChoicesText())
	}
	return normalized, nil
}

func resolveExplicitOrInferredWorkType(explicit string, inferred workTypeResolution, surface string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return parseRequiredWorkType(explicit, "--work-type")
	}
	if inferred.WorkType != "" {
		return inferred.WorkType, nil
	}
	if len(inferred.Ambiguous) > 0 {
		return "", fmt.Errorf("%s requires an explicit work type; inference was ambiguous (%s)", surface, strings.Join(inferred.Ambiguous, ", "))
	}
	return "", fmt.Errorf("%s requires a work type; pass --work-type or add a resolvable work-type label", surface)
}

func inferIssueWorkType(labels []string, texts ...string) workTypeResolution {
	return pickWorkTypeResolution(
		inferWorkTypeFromLabels(labels),
		inferWorkTypeFromText(texts...),
	)
}

func inferTrackedIssueWorkType(explicit string, labels []string, texts ...string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return parseRequiredWorkType(explicit, "work_type")
	}
	return resolveExplicitOrInferredWorkType("", inferIssueWorkType(labels, texts...), "tracked issue scheduling")
}

func inferScoutWorkType(role string, proposal scoutFinding) workTypeResolution {
	resolution := pickWorkTypeResolution(
		workTypeResolution{WorkType: normalizeWorkType(proposal.WorkType), Source: "scout.proposal"},
		inferWorkTypeFromLabels(proposal.Labels),
		inferWorkTypeFromText(proposal.Title, proposal.Summary, proposal.SuggestedNextStep, proposal.Rationale, proposal.Evidence, proposal.Impact),
	)
	if resolution.WorkType != "" {
		return resolution
	}
	fallback := defaultScoutRoleWorkType(strings.TrimSpace(role), "scout.role")
	if fallback.WorkType != "" {
		if len(resolution.Ambiguous) > 0 {
			fallback.Ambiguous = append([]string{}, resolution.Ambiguous...)
		}
		return fallback
	}
	return resolution
}

func inferPlannedItemWorkType(item startWorkPlannedItem) workTypeResolution {
	if normalized := normalizeWorkType(item.WorkType); normalized != "" {
		return workTypeResolution{WorkType: normalized, Source: "planned_item"}
	}
	labels := trackedIssueLabelsFromDescription(item.Description)
	return pickWorkTypeResolution(
		workTypeResolution{WorkType: trackedIssueWorkTypeFromDescription(item.Description), Source: "planned_item.description"},
		inferWorkTypeFromLabels(labels),
		inferWorkTypeFromText(item.Title, item.Description),
	)
}

func inferPersistedScoutJobWorkType(job startWorkScoutJob) workTypeResolution {
	if normalized := normalizeWorkType(job.WorkType); normalized != "" {
		return workTypeResolution{WorkType: normalized, Source: "scout_job"}
	}
	resolution := pickWorkTypeResolution(
		inferWorkTypeFromLabels(job.Labels),
		inferWorkTypeFromText(job.Title, job.Summary, job.SuggestedNextStep, job.Rationale, job.Evidence, job.Impact),
	)
	if resolution.WorkType != "" {
		return resolution
	}
	fallback := defaultScoutRoleWorkType(strings.TrimSpace(job.Role), "scout_job.role")
	if fallback.WorkType != "" {
		if len(resolution.Ambiguous) > 0 {
			fallback.Ambiguous = append([]string{}, resolution.Ambiguous...)
		}
		return fallback
	}
	return resolution
}

func defaultScoutRoleWorkType(role string, source string) workTypeResolution {
	switch strings.TrimSpace(role) {
	case enhancementScoutRole:
		return workTypeResolution{WorkType: workTypeFeature, Source: source}
	case improvementScoutRole:
		return workTypeResolution{WorkType: workTypeRefactor, Source: source}
	case uiScoutRole:
		return workTypeResolution{WorkType: workTypeBugFix, Source: source}
	default:
		return workTypeResolution{}
	}
}

func inferStartWorkIssueType(issue startWorkIssuePayload, labels []string) workTypeResolution {
	return inferIssueWorkType(labels, issue.Title, issue.Body)
}

func inferStartWorkIssueStateType(issue startWorkIssueState) workTypeResolution {
	return inferIssueWorkType(issue.Labels, issue.Title, issue.SourceBody)
}

func trackedIssueLabelsFromDescription(description string) []string {
	lines := strings.Split(strings.TrimSpace(description), "\n")
	labels := []string{}
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "Labels:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "Labels:"))
		if value == "" {
			continue
		}
		for _, part := range strings.Split(value, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				labels = append(labels, trimmed)
			}
		}
	}
	return uniqueStrings(labels)
}

func trackedIssueWorkTypeFromDescription(description string) string {
	lines := strings.Split(strings.TrimSpace(description), "\n")
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "Work type:") {
			continue
		}
		return normalizeWorkType(strings.TrimSpace(strings.TrimPrefix(line, "Work type:")))
	}
	return ""
}

func inferWorkTypeFromLabels(labels []string) workTypeResolution {
	candidates := map[string]struct{}{}
	for _, label := range labels {
		normalized := strings.ToLower(strings.TrimSpace(label))
		switch normalized {
		case "nana:type:bug-fix", "nana:type:bug_fix", "bug", "bugfix", "fix", "fixes", "regression", "flaky":
			candidates[workTypeBugFix] = struct{}{}
		case "nana:type:refactor", "refactor", "cleanup", "tech-debt", "tech_debt", "chore", "hardening", "perf", "performance":
			candidates[workTypeRefactor] = struct{}{}
		case "nana:type:feature", "feature", "enhancement", "feat":
			candidates[workTypeFeature] = struct{}{}
		case "nana:type:test-only", "nana:type:test_only", "test", "tests", "testing", "test-only", "test_only", "coverage", "qa":
			candidates[workTypeTestOnly] = struct{}{}
		}
	}
	return workTypeResolutionFromCandidates("labels", candidates)
}

func inferWorkTypeFromText(texts ...string) workTypeResolution {
	candidates := map[string]struct{}{}
	for _, text := range texts {
		lower := strings.ToLower(strings.TrimSpace(text))
		if lower == "" {
			continue
		}
		if workTypeTextContainsAny(lower, "bug", "fix", "regression", "flaky", "broken", "crash", "failure", "failing", "repair", "incorrect") {
			candidates[workTypeBugFix] = struct{}{}
		}
		if workTypeTextContainsAny(lower, "refactor", "cleanup", "clean up", "restructure", "rename", "simplify", "consolidate", "optimize", "perf", "performance", "cache", "index") {
			candidates[workTypeRefactor] = struct{}{}
		}
		if workTypeTextContainsAny(lower, "feature", "enhancement", "add", "introduce", "implement", "support", "allow", "enable", "expose", "create", "provide") {
			candidates[workTypeFeature] = struct{}{}
		}
		if workTypeTextContainsAny(lower, "test", "coverage", "smoke", "assertion", "harness") {
			candidates[workTypeTestOnly] = struct{}{}
		}
	}
	return workTypeResolutionFromCandidates("text", candidates)
}

func pickWorkTypeResolution(resolutions ...workTypeResolution) workTypeResolution {
	for _, resolution := range resolutions {
		if resolution.WorkType != "" {
			return resolution
		}
		if len(resolution.Ambiguous) > 0 {
			return resolution
		}
	}
	return workTypeResolution{}
}

func workTypeResolutionFromCandidates(source string, candidates map[string]struct{}) workTypeResolution {
	if len(candidates) == 0 {
		return workTypeResolution{}
	}
	values := make([]string, 0, len(candidates))
	for value := range candidates {
		values = append(values, value)
	}
	sort.Strings(values)
	if len(values) == 1 {
		return workTypeResolution{WorkType: values[0], Source: source}
	}
	return workTypeResolution{Source: source, Ambiguous: values}
}

func workTypeTextContainsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
