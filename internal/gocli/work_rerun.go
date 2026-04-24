package gocli

import (
	"fmt"
	"os"
	"strings"
)

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

	options := localWorkStartOptions{
		Detach:                true,
		RepoPath:              manifest.RepoRoot,
		Task:                  task,
		WorkType:              normalizeWorkType(manifest.WorkType),
		MaxIterations:         manifest.MaxIterations,
		IntegrationPolicy:     defaultString(strings.TrimSpace(manifest.IntegrationPolicy), "final"),
		GroupingPolicy:        defaultString(strings.TrimSpace(manifest.GroupingPolicy), localWorkDefaultGroupingPolicy),
		ValidationParallelism: manifest.ValidationParallelism,
	}
	if options.MaxIterations <= 0 {
		options.MaxIterations = localWorkDefaultMaxIterations
	}
	if options.ValidationParallelism <= 0 {
		options.ValidationParallelism = localWorkValidationParallelism
	}
	if strings.TrimSpace(cwd) == "" {
		cwd = manifest.RepoRoot
	}
	return startLocalWorkWithRunID(cwd, options)
}
