package gocli

import (
	"path/filepath"
	"strings"
)

const (
	managedVerificationPlanFile = "verification-plan.json"
)

type managedVerificationPlan struct {
	Version         int                              `json:"version,omitempty"`
	Name            string                           `json:"name,omitempty"`
	Description     string                           `json:"description,omitempty"`
	Stages          []managedVerificationStage       `json:"stages,omitempty"`
	ChangedScope    *managedVerificationChangedScope `json:"changed_scope,omitempty"`
	Source          string                           `json:"source"`
	Lint            []string                         `json:"lint"`
	Compile         []string                         `json:"compile"`
	Unit            []string                         `json:"unit"`
	Integration     []string                         `json:"integration"`
	Benchmarks      []string                         `json:"benchmarks,omitempty"`
	Warnings        []string                         `json:"warnings,omitempty"`
	PlanFingerprint string                           `json:"plan_fingerprint,omitempty"`
	SourceFiles     []managedVerificationSourceFile  `json:"source_files,omitempty"`
}

type managedVerificationStage struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command"`
}

type managedVerificationChangedScope struct {
	Description string                                   `json:"description,omitempty"`
	FullCheck   managedVerificationChangedScopeFullCheck `json:"full_check"`
	Paths       []managedVerificationChangedScopePath    `json:"paths,omitempty"`
}

type managedVerificationChangedScopeFullCheck struct {
	Description string `json:"description,omitempty"`
	Command     string `json:"command"`
}

type managedVerificationChangedScopePath struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Patterns    []string `json:"patterns"`
	Stages      []string `json:"stages,omitempty"`
	Checks      []string `json:"checks,omitempty"`
}

type managedVerificationSourceFile struct {
	Path     string `json:"path"`
	Checksum string `json:"checksum"`
	Kind     string `json:"kind"`
}

type githubVerificationPlan = managedVerificationPlan
type githubVerificationSourceFile = managedVerificationSourceFile

func managedVerificationPlanPathForRepoRoot(repoRoot string) string {
	repoRoot = filepath.Clean(strings.TrimSpace(repoRoot))
	if repoRoot == "" {
		return filepath.Join(githubWorkReposRoot(), managedVerificationPlanFile)
	}
	if repoSlug := strings.TrimSpace(inferGithubRepoSlugFromRepo(repoRoot)); validRepoSlug(repoSlug) {
		return githubManagedPaths(repoSlug).RepoVerificationPlanPath
	}
	return filepath.Join(localWorkRepoDir(repoRoot), managedVerificationPlanFile)
}

func managedVerificationPlanPathForCWD(cwd string) (string, string, error) {
	repoRoot, err := resolveLocalWorkRepoRoot(cwd, "")
	if err != nil {
		return "", "", err
	}
	return repoRoot, managedVerificationPlanPathForRepoRoot(repoRoot), nil
}

func deriveManagedVerificationDefaults(plan *managedVerificationPlan, repoRoot string, repoSlug string) {
	if plan == nil {
		return
	}
	if plan.Version == 0 {
		plan.Version = 1
	}
	if strings.TrimSpace(plan.Name) == "" {
		if validRepoSlug(repoSlug) {
			plan.Name = repoSlug
		} else {
			plan.Name = filepath.Base(strings.TrimSpace(repoRoot))
		}
	}
	if strings.TrimSpace(plan.Description) == "" {
		plan.Description = "Canonical managed repository verification plan."
	}
	if len(plan.Stages) == 0 {
		plan.Stages = deriveManagedVerificationStages(*plan)
	}
}

func deriveManagedVerificationStages(plan managedVerificationPlan) []managedVerificationStage {
	stages := []managedVerificationStage{}
	appendStage := func(name string, description string, commands []string) {
		command := strings.Join(trimNonEmptyStrings(commands), " && ")
		if strings.TrimSpace(command) == "" {
			return
		}
		stages = append(stages, managedVerificationStage{
			Name:        name,
			Description: description,
			Command:     command,
		})
	}
	appendStage("lint", "Run lint/format verification checks.", plan.Lint)
	appendStage("compile", "Compile packages and tests without running test bodies.", plan.Compile)
	appendStage("unit", "Run the unit test suite.", plan.Unit)
	appendStage("integration", "Run integration checks.", plan.Integration)
	appendStage("benchmark", "Run benchmark checks.", plan.Benchmarks)
	return stages
}

func writeManagedVerificationPlan(repoRoot string, plan managedVerificationPlan) (string, error) {
	path := managedVerificationPlanPathForRepoRoot(repoRoot)
	if err := writeGithubJSON(path, plan); err != nil {
		return "", err
	}
	return path, nil
}
