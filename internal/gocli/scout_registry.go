package gocli

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

type scoutPolicy struct {
	Version          int      `json:"version"`
	Mode             string   `json:"mode,omitempty"`
	Schedule         string   `json:"schedule,omitempty"`
	IssueDestination string   `json:"issue_destination,omitempty"`
	ForkRepo         string   `json:"fork_repo,omitempty"`
	Labels           []string `json:"labels,omitempty"`
	SessionLimit     int      `json:"session_limit,omitempty"`
}

type scoutReport struct {
	Version     int            `json:"version"`
	Repo        string         `json:"repo,omitempty"`
	GeneratedAt string         `json:"generated_at,omitempty"`
	Proposals   []scoutFinding `json:"proposals"`
}

type scoutFinding struct {
	Title             string   `json:"title"`
	Area              string   `json:"area,omitempty"`
	Summary           string   `json:"summary"`
	Rationale         string   `json:"rationale,omitempty"`
	Evidence          string   `json:"evidence,omitempty"`
	Impact            string   `json:"impact,omitempty"`
	SuggestedNextStep string   `json:"suggested_next_step,omitempty"`
	Confidence        string   `json:"confidence,omitempty"`
	Files             []string `json:"files,omitempty"`
	Labels            []string `json:"labels,omitempty"`
	Page              string   `json:"page,omitempty"`
	Route             string   `json:"route,omitempty"`
	Severity          string   `json:"severity,omitempty"`
	TargetKind        string   `json:"target_kind,omitempty"`
	Screenshots       []string `json:"screenshots,omitempty"`
}

type scoutIssueResult struct {
	Title  string
	URL    string
	DryRun bool
}

type improvementPolicy = scoutPolicy
type improvementReport = scoutReport
type improvementProposal = scoutFinding
type improvementIssueResult = scoutIssueResult

type scoutRoleSpec struct {
	Role                      string
	ConfigKey                 string
	DisplayLabel              string
	ArtifactRoot              string
	OutputPrefix              string
	BaseLabel                 string
	IssueHeading              string
	DefaultArea               string
	ResultPlural              string
	ItemCountNoun             string
	DraftWording              string
	IssueWording              string
	IncludedInDefaultRepoRole bool
	SupportsSessionLimit      bool
	UsesPreflight             bool
}

var scoutRoleRegistry = []scoutRoleSpec{
	{
		Role:                      improvementScoutRole,
		ConfigKey:                 "improvement",
		DisplayLabel:              "Improvement Scout",
		ArtifactRoot:              "improvements",
		OutputPrefix:              "improve",
		BaseLabel:                 "improvement",
		IssueHeading:              "Improvement",
		DefaultArea:               "Improvement",
		ResultPlural:              "improvements",
		ItemCountNoun:             "proposal(s)",
		DraftWording:              "These are improvement proposals, not enhancement requests.",
		IssueWording:              "This is an improvement proposal, not an enhancement request.",
		IncludedInDefaultRepoRole: true,
	},
	{
		Role:                      enhancementScoutRole,
		ConfigKey:                 "enhancement",
		DisplayLabel:              "Enhancement Scout",
		ArtifactRoot:              "enhancements",
		OutputPrefix:              "enhance",
		BaseLabel:                 "enhancement",
		IssueHeading:              "Enhancement",
		DefaultArea:               "Enhancement",
		ResultPlural:              "enhancements",
		ItemCountNoun:             "proposal(s)",
		DraftWording:              "These are enhancement proposals intended to help the repo move forward.",
		IssueWording:              "This is an enhancement proposal intended to help the repo move forward.",
		IncludedInDefaultRepoRole: true,
	},
	{
		Role:                 uiScoutRole,
		ConfigKey:            "ui",
		DisplayLabel:         "UI Scout",
		ArtifactRoot:         "ui-findings",
		OutputPrefix:         "ui-scout",
		BaseLabel:            "ui",
		IssueHeading:         "UI Finding",
		DefaultArea:          "UI",
		ResultPlural:         "ui findings",
		ItemCountNoun:        "finding(s)",
		DraftWording:         "These are issue-style UI findings intended to be audited, prioritized, and fixed.",
		IssueWording:         "This is a UI audit finding discovered by ui-scout.",
		SupportsSessionLimit: true,
		UsesPreflight:        true,
	},
}

var supportedScoutRoleOrder = scoutRegistryRoles()

func scoutRegistryRoles() []string {
	roles := make([]string, 0, len(scoutRoleRegistry))
	for _, spec := range scoutRoleRegistry {
		roles = append(roles, spec.Role)
	}
	return roles
}

func scoutDefaultRepoRoles() []string {
	roles := []string{}
	for _, spec := range scoutRoleRegistry {
		if spec.IncludedInDefaultRepoRole {
			roles = append(roles, spec.Role)
		}
	}
	return roles
}

func scoutRoleSpecFor(role string) scoutRoleSpec {
	for _, spec := range scoutRoleRegistry {
		if spec.Role == role {
			return spec
		}
	}
	return scoutRoleSpec{
		Role:          role,
		ConfigKey:     strings.TrimSuffix(role, "-scout"),
		DisplayLabel:  role,
		ArtifactRoot:  "improvements",
		OutputPrefix:  "improve",
		BaseLabel:     "improvement",
		IssueHeading:  "Improvement",
		DefaultArea:   "Improvement",
		ResultPlural:  "improvements",
		ItemCountNoun: "proposal(s)",
		DraftWording:  "These are improvement proposals, not enhancement requests.",
		IssueWording:  "This is an improvement proposal, not an enhancement request.",
	}
}

func scoutRoleSpecForConfigKey(configKey string) (scoutRoleSpec, bool) {
	for _, spec := range scoutRoleRegistry {
		if spec.ConfigKey == configKey {
			return spec, true
		}
	}
	return scoutRoleSpec{}, false
}

func scoutRoleConfigKeys() []string {
	keys := make([]string, 0, len(scoutRoleRegistry))
	for _, spec := range scoutRoleRegistry {
		keys = append(keys, spec.ConfigKey)
	}
	return keys
}

func scoutRoleSupportsSessionLimit(role string) bool {
	return scoutRoleSpecFor(role).SupportsSessionLimit
}

func scoutRoleUsesPreflight(role string) bool {
	return scoutRoleSpecFor(role).UsesPreflight
}

func scoutDisplayLabel(role string) string {
	return scoutRoleSpecFor(role).DisplayLabel
}

func scoutPolicyFileName(role string) string {
	return scoutRoleSpecFor(role).ConfigKey + "-policy.json"
}

func scoutLegacyPolicyPaths(repoPath string, role string) []string {
	key := scoutRoleSpecFor(role).ConfigKey
	return []string{
		filepath.Join(repoPath, ".github", fmt.Sprintf("nana-%s-policy.json", key)),
		filepath.Join(repoPath, ".nana", fmt.Sprintf("%s-policy.json", key)),
	}
}

func scoutRoleMatchesToken(role string, token string) bool {
	spec := scoutRoleSpecFor(role)
	normalized := strings.ToLower(strings.TrimSpace(token))
	return normalized == spec.ConfigKey || normalized == spec.Role || normalized == spec.ConfigKey+"s"
}

func scoutRoleListIncludes(roles []string, target string) bool {
	return slices.Contains(roles, target)
}
