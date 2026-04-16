package gocli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/dkropachev/nana/internal/gocliassets"
)

const AgentsInitUsage = "Usage: nana agents-init [path] [--dry-run] [--force] [--verbose]\n       nana deepinit [path] [--dry-run] [--force] [--verbose]\n\nBootstrap lightweight AGENTS.md files for the target directory and its direct child directories.\n\nOptions:\n  --dry-run   Show planned file updates without writing files\n  --force     Overwrite existing unmanaged AGENTS.md files after taking a backup\n  --verbose   Print per-file actions and skip reasons\n  --help      Show this message"

const (
	managedMarker = "<!-- NANA:AGENTS-INIT:MANAGED -->"
	manualStart   = "<!-- NANA:AGENTS-INIT:MANUAL:START -->"
	manualEnd     = "<!-- NANA:AGENTS-INIT:MANUAL:END -->"
)

var ignoreDirectoryNames = map[string]bool{
	".git": true, ".nana": true, ".codex": true, "node_modules": true, "dist": true, "build": true,
	"coverage": true, ".next": true, ".nuxt": true, ".turbo": true, ".cache": true, "__pycache__": true,
	"vendor": true, "target": true, "tmp": true, "temp": true,
}

type AgentsInitOptions struct {
	TargetPath string
	DryRun     bool
	Force      bool
	Verbose    bool
}

type agentsInitSummary struct {
	Updated   int
	Unchanged int
	Skipped   int
	BackedUp  int
}

type sessionState struct {
	SessionID     string `json:"session_id"`
	StartedAt     string `json:"started_at"`
	CWD           string `json:"cwd"`
	PID           int    `json:"pid"`
	Platform      string `json:"platform"`
	PIDStartTicks int64  `json:"pid_start_ticks"`
	PIDCmdline    string `json:"pid_cmdline"`
}

func AgentsInit(repoRoot string, cwd string, args []string) error {
	if containsAny(args, "--help", "-h") {
		fmt.Fprintln(os.Stdout, AgentsInitUsage)
		return nil
	}

	options, err := parseAgentsInitArgs(args)
	if err != nil {
		return err
	}
	return runAgentsInit(repoRoot, cwd, options)
}

func parseAgentsInitArgs(args []string) (AgentsInitOptions, error) {
	options := AgentsInitOptions{}
	positionals := []string{}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			switch arg {
			case "--dry-run":
				options.DryRun = true
			case "--force":
				options.Force = true
			case "--verbose":
				options.Verbose = true
			default:
				return AgentsInitOptions{}, fmt.Errorf("unknown agents-init option: %s\n%s", arg, AgentsInitUsage)
			}
			continue
		}
		positionals = append(positionals, arg)
	}
	if len(positionals) > 1 {
		return AgentsInitOptions{}, fmt.Errorf("agents-init accepts at most one path argument.\n%s", AgentsInitUsage)
	}
	if len(positionals) == 1 {
		options.TargetPath = positionals[0]
	} else {
		options.TargetPath = "."
	}
	return options, nil
}

func runAgentsInit(repoRoot string, cwd string, options AgentsInitOptions) error {
	targetDir := filepath.Clean(filepath.Join(cwd, options.TargetPath))
	relTarget, err := filepath.Rel(cwd, targetDir)
	if err != nil {
		return err
	}
	if strings.HasPrefix(relTarget, "..") {
		return fmt.Errorf("agents-init target must stay inside the current working directory: %s", options.TargetPath)
	}
	info, err := os.Stat(targetDir)
	if err != nil {
		return fmt.Errorf("agents-init target not found: %s", options.TargetPath)
	}
	if !info.IsDir() {
		return fmt.Errorf("agents-init target must be a directory: %s", options.TargetPath)
	}

	plannedDirs, err := resolveTargetDirectories(targetDir)
	if err != nil {
		return err
	}
	summary := agentsInitSummary{}
	backupRoot := filepath.Join(cwd, ".nana", "backups", "agents-init", strings.ReplaceAll(time.Now().UTC().Format(time.RFC3339), ":", "-"))
	rootSessionGuardActive := isActiveSession(cwd)

	fmt.Fprintln(os.Stdout, "nana AGENTS bootstrap")
	fmt.Fprintln(os.Stdout, "===========================")
	fmt.Fprintln(os.Stdout)
	fmt.Fprintf(os.Stdout, "Target: %s\n", options.TargetPath)
	childrenCount := len(plannedDirs) - 1
	suffix := "ies"
	if childrenCount == 1 {
		suffix = "y"
	}
	fmt.Fprintf(os.Stdout, "Scope: target directory + %d direct child director%s\n\n", maxInt(childrenCount, 0), suffix)

	for index, dir := range plannedDirs {
		destinationPath := filepath.Join(dir, "AGENTS.md")
		existingContent := readOptionalFile(destinationPath)
		isRootTarget := index == 0
		relativeDir := filepath.ToSlash(mustRelative(cwd, dir))
		if relativeDir == "" || relativeDir == "." {
			relativeDir = "."
		}

		var content string
		if isRootTarget && targetDir == cwd {
			content, err = renderManagedProjectRootAgents(repoRoot, existingContent)
		} else {
			content, err = renderManagedDirectoryAgents(cwd, targetDir, dir, existingContent, filepath.Dir(dir) == targetDir)
		}
		if err != nil {
			return err
		}

		rootOverlayRisk := rootSessionGuardActive && dir == cwd && fileExists(destinationPath) && existingContent != content
		decision, err := syncManagedAgentsFile(cwd, destinationPath, content, options, &summary, backupRoot, rootOverlayRisk)
		if err != nil {
			return err
		}
		if options.Verbose || decision != "unchanged" {
			label := map[string]string{"updated": "updated", "unchanged": "unchanged", "skipped": "skipped"}[decision]
			if options.DryRun && decision == "updated" {
				label = "would update"
			}
			reason := ""
			if rootOverlayRisk {
				reason = " (active nana session detected for project root AGENTS.md)"
			} else if decision == "skipped" && !isManagedAgentsInitFile(existingContent) && fileExists(destinationPath) && !options.Force {
				reason = " (existing unmanaged AGENTS.md (re-run with --force to adopt it))"
			}
			fmt.Fprintf(os.Stdout, "  %s %s/AGENTS.md%s\n", label, relativeDir, reason)
		}
	}

	fmt.Fprintln(os.Stdout, "\nGuardrails:")
	fmt.Fprintln(os.Stdout, "- Generates the target directory and its direct child directories only.")
	fmt.Fprintln(os.Stdout, "- Skips generated/vendor/build directories via a fixed exclusion list.")
	fmt.Fprintln(os.Stdout, "- Preserves manual notes only for files already managed by nana agents-init.")
	fmt.Fprintln(os.Stdout, "- Never overwrites unmanaged AGENTS.md files unless you pass --force.")
	fmt.Fprintln(os.Stdout, "- Avoids rewriting project-root AGENTS.md while an active nana session is running.")
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "Summary:")
	fmt.Fprintf(os.Stdout, "  updated=%d, unchanged=%d, backed_up=%d, skipped=%d\n", summary.Updated, summary.Unchanged, summary.BackedUp, summary.Skipped)
	return nil
}

func resolveTargetDirectories(targetDir string) ([]string, error) {
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return nil, err
	}
	childDirs := []string{}
	for _, entry := range entries {
		if entry.IsDir() && !ignoreDirectoryNames[entry.Name()] {
			childDirs = append(childDirs, filepath.Join(targetDir, entry.Name()))
		}
	}
	sort.Strings(childDirs)
	return append([]string{targetDir}, childDirs...), nil
}

func renderManagedProjectRootAgents(repoRoot string, existingContent string) (string, error) {
	templatePath := filepath.Join(repoRoot, "templates", "AGENTS.md")
	templateBytes, err := os.ReadFile(templatePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		templates, embeddedErr := gocliassets.Templates()
		if embeddedErr != nil {
			return "", embeddedErr
		}
		content, ok := templates["AGENTS.md"]
		if !ok {
			return "", fmt.Errorf("embedded AGENTS template missing")
		}
		templateBytes = []byte(content)
	}
	template := strings.ReplaceAll(string(templateBytes), "~/.codex", "./.codex")
	manual := extractManualSection(existingContent, "## Local Notes\n- Add repo-specific architecture notes, workflow conventions, and verification commands here.\n- This block is preserved by `nana agents-init` refreshes.")
	return wrapManagedContent(template, manual), nil
}

func renderManagedDirectoryAgents(cwd string, targetDir string, dir string, existingContent string, assumeParentAgents bool) (string, error) {
	files, directories, err := snapshotDirectory(dir)
	if err != nil {
		return "", err
	}
	manual := extractManualSection(existingContent, "## Local Notes\n- Add subtree-specific constraints, ownership notes, and test commands here.\n- Keep notes scoped to this directory and its children.")
	title := filepath.Base(dir)
	relativeDir := filepath.ToSlash(mustRelative(cwd, dir))
	if relativeDir == "" {
		relativeDir = "."
	}
	parentReference := renderParentReference(dir, assumeParentAgents)
	body := fmt.Sprintf("%s# %s\n\nThis AGENTS.md scopes guidance to `%s`. Parent AGENTS guidance still applies unless this file narrows it for this subtree.\n\n## Bootstrap Guardrails\n- This is a lightweight scaffold generated by `nana agents-init`.\n- Refresh updates the layout summary below and preserves the manual notes block.\n- Keep only directory-specific guidance here; do not duplicate the root orchestration brain.\n\n## Current Layout\n\n### Files\n%s\n\n### Subdirectories\n%s",
		parentReference,
		title,
		relativeDir,
		formatList(files, "", 12),
		formatList(directories, "/", 12),
	)
	return wrapManagedContent(body, manual), nil
}

func snapshotDirectory(dir string) ([]string, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	files := []string{}
	directories := []string{}
	for _, entry := range entries {
		if entry.Name() == "AGENTS.md" {
			continue
		}
		if entry.IsDir() {
			if ignoreDirectoryNames[entry.Name()] {
				continue
			}
			directories = append(directories, entry.Name())
		} else {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)
	sort.Strings(directories)
	return files, directories, nil
}

func formatList(items []string, suffix string, limit int) string {
	if len(items) == 0 {
		return "- None"
	}
	lines := []string{}
	for index, item := range items {
		if index >= limit {
			lines = append(lines, fmt.Sprintf("- ...and %d more", len(items)-limit))
			break
		}
		lines = append(lines, fmt.Sprintf("- `%s%s`", item, suffix))
	}
	return strings.Join(lines, "\n")
}

func renderParentReference(dir string, assumeParentAgents bool) string {
	parentAgentsPath := filepath.Join(filepath.Dir(dir), "AGENTS.md")
	if !assumeParentAgents && !fileExists(parentAgentsPath) {
		return ""
	}
	relativePath := filepath.ToSlash(mustRelative(dir, parentAgentsPath))
	return fmt.Sprintf("<!-- Parent: %s -->\n", relativePath)
}

func extractManualSection(existingContent string, fallbackBody string) string {
	if existingContent == "" {
		return strings.TrimSpace(fallbackBody)
	}
	start := strings.Index(existingContent, manualStart)
	end := strings.Index(existingContent, manualEnd)
	if start == -1 || end == -1 || end < start {
		return strings.TrimSpace(fallbackBody)
	}
	section := strings.TrimSpace(existingContent[start+len(manualStart) : end])
	if section == "" {
		return strings.TrimSpace(fallbackBody)
	}
	return section
}

func wrapManagedContent(body string, manualBody string) string {
	return fmt.Sprintf("%s\n%s\n\n%s\n%s\n%s\n", managedMarker, strings.TrimRight(body, "\n"), manualStart, strings.TrimSpace(manualBody), manualEnd)
}

func isManagedAgentsInitFile(content string) bool {
	return strings.Contains(content, managedMarker)
}

func syncManagedAgentsFile(cwd string, destinationPath string, content string, options AgentsInitOptions, summary *agentsInitSummary, backupRoot string, skipRootSession bool) (string, error) {
	exists := fileExists(destinationPath)
	existingContent := readOptionalFile(destinationPath)
	if skipRootSession {
		summary.Skipped++
		return "skipped", nil
	}
	if !exists {
		if !options.DryRun {
			if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(destinationPath, []byte(content), 0o644); err != nil {
				return "", err
			}
		}
		summary.Updated++
		return "updated", nil
	}
	if existingContent == content {
		summary.Unchanged++
		return "unchanged", nil
	}
	if !isManagedAgentsInitFile(existingContent) && !options.Force {
		summary.Skipped++
		return "skipped", nil
	}
	if exists {
		if err := ensureBackup(cwd, destinationPath, backupRoot, options.DryRun); err != nil {
			return "", err
		}
		summary.BackedUp++
	}
	if !options.DryRun {
		if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(destinationPath, []byte(content), 0o644); err != nil {
			return "", err
		}
	}
	summary.Updated++
	return "updated", nil
}

func ensureBackup(cwd string, destinationPath string, backupRoot string, dryRun bool) error {
	relativePath, err := filepath.Rel(cwd, destinationPath)
	if err != nil {
		return err
	}
	safeRelativePath := relativePath
	if strings.HasPrefix(relativePath, "..") || relativePath == "" {
		safeRelativePath = strings.TrimLeft(destinationPath, string(filepath.Separator))
	}
	backupPath := filepath.Join(backupRoot, safeRelativePath)
	if dryRun {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
		return err
	}
	content, err := os.ReadFile(destinationPath)
	if err != nil {
		return err
	}
	return os.WriteFile(backupPath, content, 0o644)
}

func isActiveSession(cwd string) bool {
	path := filepath.Join(BaseStateDir(cwd), "session.json")
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var state sessionState
	if err := json.Unmarshal(content, &state); err != nil {
		return false
	}
	if state.PID <= 0 {
		return false
	}
	process, err := os.FindProcess(state.PID)
	if err != nil {
		return false
	}
	return process.Signal(os.Signal(syscall.Signal(0))) == nil
}

func readOptionalFile(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(content)
}

func containsAny(values []string, targets ...string) bool {
	for _, value := range values {
		for _, target := range targets {
			if value == target {
				return true
			}
		}
	}
	return false
}

func mustRelative(base string, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return rel
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
