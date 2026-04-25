package gocli

import (
	"fmt"
	"os"
	"strings"
)

type localWorkRerunOptions struct {
	WorkType string
}

func rerunLocalWork(cwd string, selection localWorkRunSelection) (string, error) {
	manifest, _, err := resolveLocalWorkRun(cwd, selection)
	if err != nil {
		return "", err
	}
	return rerunLocalWorkManifest(cwd, manifest)
}

func localWorkRerunAllowed(manifest localWorkManifest) bool {
	if strings.TrimSpace(manifest.RepoRoot) == "" {
		return false
	}
	if strings.TrimSpace(normalizeWorkType(manifest.WorkType)) == "" {
		return false
	}
	if localWorkStopAllowed(manifest) {
		return false
	}
	inputPath := strings.TrimSpace(manifest.InputPath)
	if inputPath == "" {
		return false
	}
	info, err := os.Stat(inputPath)
	if err != nil || info.IsDir() {
		return false
	}
	content, err := os.ReadFile(inputPath)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(content)) != ""
}

func rerunLocalWorkManifest(cwd string, manifest localWorkManifest) (string, error) {
	return rerunLocalWorkManifestWithOptions(cwd, manifest, localWorkRerunOptions{})
}

func rerunLocalWorkManifestWithOptions(cwd string, manifest localWorkManifest, options localWorkRerunOptions) (string, error) {
	if localWorkStopAllowed(manifest) {
		return "", fmt.Errorf("work run %s is still active; stop it before rerunning", manifest.RunID)
	}
	if !localWorkRerunAllowed(manifest) {
		return "", fmt.Errorf("work run %s cannot be rerun from its current manifest state", manifest.RunID)
	}

	inputContent, err := os.ReadFile(strings.TrimSpace(manifest.InputPath))
	if err != nil {
		return "", err
	}
	task := strings.TrimSpace(string(inputContent))
	if task == "" {
		return "", fmt.Errorf("work run %s input plan is empty", manifest.RunID)
	}

	workType := normalizeWorkType(options.WorkType)
	if workType == "" {
		workType = normalizeWorkType(manifest.WorkType)
	}
	if workType == "" {
		return "", fmt.Errorf("work run %s is missing a valid work type for rerun", manifest.RunID)
	}

	startOptions := localWorkStartOptions{
		Detach:                true,
		RepoPath:              manifest.RepoRoot,
		Task:                  task,
		WorkType:              workType,
		MaxIterations:         manifest.MaxIterations,
		IntegrationPolicy:     defaultString(strings.TrimSpace(manifest.IntegrationPolicy), "final"),
		GroupingPolicy:        defaultString(strings.TrimSpace(manifest.GroupingPolicy), localWorkDefaultGroupingPolicy),
		ValidationParallelism: manifest.ValidationParallelism,
	}
	if startOptions.MaxIterations <= 0 {
		startOptions.MaxIterations = localWorkDefaultMaxIterations
	}
	if startOptions.ValidationParallelism <= 0 {
		startOptions.ValidationParallelism = localWorkValidationParallelism
	}
	if strings.TrimSpace(cwd) == "" {
		cwd = manifest.RepoRoot
	}
	return startLocalWorkWithRunID(cwd, startOptions)
}
