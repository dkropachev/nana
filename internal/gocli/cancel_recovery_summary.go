package gocli

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type cancelRecoveryModeSummary struct {
	Mode      string
	Phase     string
	StateFile string
}

type cancelRecoveryPlanArtifact struct {
	Path    string
	ModTime time.Time
}

func formatCancelRecoverySummary(cwd string, cancelled []cancelRecoveryModeSummary) string {
	if len(cancelled) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("Recovery summary:\n")
	sessionID := ReadCurrentSessionID(cwd)
	if sessionID == "" {
		sessionID = "n/a"
	}
	builder.WriteString("  Session: ")
	builder.WriteString(sessionID)
	builder.WriteByte('\n')

	builder.WriteString("  Affected state:\n")
	for _, item := range cancelled {
		builder.WriteString("    - ")
		builder.WriteString(item.Mode)
		if item.Phase != "" {
			builder.WriteString(" (was phase: ")
			builder.WriteString(item.Phase)
			builder.WriteByte(')')
		}
		if item.StateFile != "" {
			builder.WriteString(": ")
			builder.WriteString(item.StateFile)
		}
		builder.WriteByte('\n')
	}

	if artifact := latestRuntimeArtifactPath(cwd); artifact != "" {
		builder.WriteString("  Open artifacts:\n")
		builder.WriteString("    - ")
		builder.WriteString(artifact)
		builder.WriteByte('\n')
	} else {
		builder.WriteString("  Open artifacts: none\n")
	}

	plans := recentPlanArtifactPaths(cwd, 3)
	if len(plans) > 0 {
		builder.WriteString("  Pending plans:\n")
		for _, plan := range plans {
			builder.WriteString("    - ")
			builder.WriteString(plan)
			builder.WriteByte('\n')
		}
	} else {
		builder.WriteString("  Pending plans: none\n")
	}

	builder.WriteString("  Safe next commands:\n")
	for _, command := range safeCancelRecoveryCommands(cwd) {
		builder.WriteString("    - ")
		builder.WriteString(command)
		builder.WriteByte('\n')
	}
	return builder.String()
}

func recentPlanArtifactPaths(cwd string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(cwd, ".nana", "plans"))
	if err != nil {
		return nil
	}

	plans := []cancelRecoveryPlanArtifact{}
	for _, entry := range entries {
		if entry.IsDir() || !isPlanArtifactFile(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(cwd, ".nana", "plans", entry.Name())
		plans = append(plans, cancelRecoveryPlanArtifact{
			Path:    relativeRuntimePath(cwd, path),
			ModTime: info.ModTime(),
		})
	}

	sort.SliceStable(plans, func(i, j int) bool {
		if !plans[i].ModTime.Equal(plans[j].ModTime) {
			return plans[i].ModTime.After(plans[j].ModTime)
		}
		return plans[i].Path < plans[j].Path
	})

	if len(plans) > limit {
		plans = plans[:limit]
	}
	result := make([]string, 0, len(plans))
	for _, plan := range plans {
		result = append(result, plan.Path)
	}
	return result
}

func isPlanArtifactFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	return ext == ".md" || ext == ".json"
}

func safeCancelRecoveryCommands(cwd string) []string {
	commands := []string{"nana status", "nana doctor"}
	if _, err := os.Stat(filepath.Join(cwd, "nana-verify.json")); err == nil {
		commands = append(commands, "nana verify --json")
	}
	return commands
}
