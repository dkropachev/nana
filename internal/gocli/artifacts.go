package gocli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const ArtifactsHelp = `nana artifacts - List repo-local NANA artifacts

Usage:
  nana artifacts list [--repo <path>] [--json]
  nana artifacts help

Scans the repo's .nana/ directory without writing files and groups durable
artifacts such as notes, project memory, context snapshots, interviews, specs,
plans, logs, state, scout reports, and helper outputs by type, timestamp, and
originating mode.
`

type artifactsOptions struct {
	RepoPath string
	JSON     bool
}

type nanaArtifactIndex struct {
	RepoRoot  string         `json:"repo_root"`
	NanaDir   string         `json:"nana_dir"`
	Artifacts []nanaArtifact `json:"artifacts"`
}

type nanaArtifact struct {
	Type      string `json:"type"`
	Mode      string `json:"mode,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
	Path      string `json:"path"`
	Summary   string `json:"summary,omitempty"`
	Files     int    `json:"files,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`

	sortTime time.Time
}

func Artifacts(cwd string, args []string) error {
	return artifactsWithIO(cwd, args, os.Stdout)
}

func artifactsWithIO(cwd string, args []string, stdout io.Writer) error {
	if len(args) == 0 || isHelpToken(args[0]) {
		fmt.Fprint(stdout, ArtifactsHelp)
		return nil
	}

	switch args[0] {
	case "list":
		if wantsArtifactsListHelp(args[1:]) {
			fmt.Fprint(stdout, ArtifactsHelp)
			return nil
		}
		options, err := parseArtifactsOptions(args[1:])
		if err != nil {
			return err
		}
		repoRoot, err := resolveArtifactsRepoRoot(cwd, options.RepoPath)
		if err != nil {
			return err
		}
		index, err := buildNanaArtifactIndex(repoRoot)
		if err != nil {
			return err
		}
		if options.JSON {
			encoder := json.NewEncoder(stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(index)
		}
		printNanaArtifactIndex(stdout, index)
		return nil
	default:
		return fmt.Errorf("Unknown artifacts subcommand: %s\n\n%s", args[0], ArtifactsHelp)
	}
}

func wantsArtifactsListHelp(args []string) bool {
	for index := 0; index < len(args); index++ {
		token := args[index]
		if token == "--repo" {
			index++
			continue
		}
		if isHelpToken(token) {
			return true
		}
	}
	return false
}

func parseArtifactsOptions(args []string) (artifactsOptions, error) {
	var options artifactsOptions
	for index := 0; index < len(args); index++ {
		token := args[index]
		switch {
		case token == "--repo":
			if index+1 >= len(args) {
				return options, fmt.Errorf("Missing value after --repo.\n%s", ArtifactsHelp)
			}
			options.RepoPath = strings.TrimSpace(args[index+1])
			index++
		case strings.HasPrefix(token, "--repo="):
			options.RepoPath = strings.TrimSpace(strings.TrimPrefix(token, "--repo="))
		case token == "--json":
			options.JSON = true
		default:
			return options, fmt.Errorf("Unknown artifacts list option: %s\n\n%s", token, ArtifactsHelp)
		}
	}
	return options, nil
}

func resolveArtifactsRepoRoot(cwd string, repoPath string) (string, error) {
	target := cwd
	if strings.TrimSpace(repoPath) != "" {
		if filepath.IsAbs(repoPath) {
			target = repoPath
		} else {
			target = filepath.Join(cwd, repoPath)
		}
	}
	target = filepath.Clean(target)
	info, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		target = filepath.Dir(target)
	}
	if output, err := readGitOutput(target, "rev-parse", "--show-toplevel"); err == nil {
		if root := strings.TrimSpace(output); root != "" {
			return filepath.Clean(root), nil
		}
	}
	return target, nil
}

func buildNanaArtifactIndex(repoRoot string) (nanaArtifactIndex, error) {
	repoRoot = filepath.Clean(repoRoot)
	nanaDir := filepath.Join(repoRoot, ".nana")
	index := nanaArtifactIndex{
		RepoRoot: repoRoot,
		NanaDir:  nanaDir,
	}
	info, err := os.Lstat(nanaDir)
	if os.IsNotExist(err) {
		index.Artifacts = []nanaArtifact{}
		return index, nil
	}
	if err != nil {
		return index, err
	}
	if !info.IsDir() {
		return index, fmt.Errorf("%s is not a directory", nanaDir)
	}

	artifacts := make([]nanaArtifact, 0)
	add := func(items []nanaArtifact, err error) error {
		if err != nil {
			return err
		}
		artifacts = append(artifacts, items...)
		return nil
	}

	if artifact, ok := fileArtifact(repoRoot, filepath.Join(nanaDir, "notepad.md"), "notes", "note", markdownFileSummary); ok {
		artifacts = append(artifacts, artifact)
	}
	if artifact, ok := fileArtifact(repoRoot, filepath.Join(nanaDir, "project-memory.json"), "project-memory", "memory", jsonKeysSummary); ok {
		artifacts = append(artifacts, artifact)
	}
	if err := add(collectDurableFileArtifacts(repoRoot, nanaDir, "context", "context", inferContextMode)); err != nil {
		return index, err
	}
	if err := add(collectDurableFileArtifacts(repoRoot, nanaDir, "interviews", "interviews", inferInterviewMode)); err != nil {
		return index, err
	}
	if err := add(collectDurableFileArtifacts(repoRoot, nanaDir, "specs", "specs", inferSpecMode)); err != nil {
		return index, err
	}
	if err := add(collectPlanArtifacts(repoRoot, nanaDir)); err != nil {
		return index, err
	}
	if err := add(collectLogArtifacts(repoRoot, nanaDir)); err != nil {
		return index, err
	}
	if err := add(collectStateArtifacts(repoRoot, nanaDir)); err != nil {
		return index, err
	}
	if err := add(collectScoutArtifacts(repoRoot, nanaDir, "enhancements", "enhancements", "enhance")); err != nil {
		return index, err
	}
	if err := add(collectScoutArtifacts(repoRoot, nanaDir, "improvements", "improvements", "improve")); err != nil {
		return index, err
	}
	if err := add(collectScoutArtifacts(repoRoot, nanaDir, "ui-findings", "ui-findings", "ui-scout")); err != nil {
		return index, err
	}
	if err := add(collectGenericArtifacts(repoRoot, nanaDir)); err != nil {
		return index, err
	}

	sortNanaArtifacts(artifacts)
	index.Artifacts = artifacts
	return index, nil
}

func collectDurableFileArtifacts(repoRoot string, nanaDir string, dirName string, artifactType string, inferMode func(string, string) string) ([]nanaArtifact, error) {
	root := filepath.Join(nanaDir, dirName)
	if !artifactDirExists(root) {
		return nil, nil
	}
	var artifacts []nanaArtifact
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if artifact, ok := fileArtifact(repoRoot, path, artifactType, inferMode(root, path), durableFileSummary); ok {
			artifacts = append(artifacts, artifact)
		}
		return nil
	})
	return artifacts, err
}

func collectPlanArtifacts(repoRoot string, nanaDir string) ([]nanaArtifact, error) {
	root := filepath.Join(nanaDir, "plans")
	if !artifactDirExists(root) {
		return nil, nil
	}
	var artifacts []nanaArtifact
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		if artifact, ok := fileArtifact(repoRoot, path, "plans", inferPlanMode(entry.Name()), markdownFileSummary); ok {
			artifacts = append(artifacts, artifact)
		}
		return nil
	})
	return artifacts, err
}

func collectLogArtifacts(repoRoot string, nanaDir string) ([]nanaArtifact, error) {
	root := filepath.Join(nanaDir, "logs")
	if !artifactDirExists(root) {
		return nil, nil
	}
	var artifacts []nanaArtifact
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.IsDir() {
			manifestPath := filepath.Join(path, "manifest.json")
			if artifactFileExists(manifestPath) {
				artifact := directoryArtifact(repoRoot, path, "logs", inferLogMode(root, path))
				applyManifestMetadata(&artifact, manifestPath)
				artifacts = append(artifacts, artifact)
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		if artifact, ok := fileArtifact(repoRoot, path, "logs", inferLogMode(root, path), logFileSummary); ok {
			artifacts = append(artifacts, artifact)
		}
		return nil
	})
	return artifacts, err
}

func collectStateArtifacts(repoRoot string, nanaDir string) ([]nanaArtifact, error) {
	root := filepath.Join(nanaDir, "state")
	if !artifactDirExists(root) {
		return nil, nil
	}
	var artifacts []nanaArtifact
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			return nil
		}
		if artifact, ok := fileArtifact(repoRoot, path, "state", inferStateMode(path), stateSummary); ok {
			artifacts = append(artifacts, artifact)
		}
		return nil
	})
	return artifacts, err
}

func collectScoutArtifacts(repoRoot string, nanaDir string, dirName string, artifactType string, mode string) ([]nanaArtifact, error) {
	root := filepath.Join(nanaDir, dirName)
	if !artifactDirExists(root) {
		return nil, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	artifacts := make([]nanaArtifact, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		artifact := directoryArtifact(repoRoot, path, artifactType, mode)
		applyScoutMetadata(&artifact, filepath.Join(path, "proposals.json"))
		artifacts = append(artifacts, artifact)
	}
	return artifacts, nil
}

func collectGenericArtifacts(repoRoot string, nanaDir string) ([]nanaArtifact, error) {
	root := filepath.Join(nanaDir, "artifacts")
	if !artifactDirExists(root) {
		return nil, nil
	}
	var artifacts []nanaArtifact
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		if artifact, ok := fileArtifact(repoRoot, path, "artifacts", inferGenericArtifactMode(entry.Name()), markdownFileSummary); ok {
			artifacts = append(artifacts, artifact)
		}
		return nil
	})
	return artifacts, err
}

func fileArtifact(repoRoot string, path string, artifactType string, mode string, summarize func(string) string) (nanaArtifact, bool) {
	info, ok := artifactRegularFileInfo(path)
	if !ok {
		return nanaArtifact{}, false
	}
	timestamp := info.ModTime().UTC()
	return nanaArtifact{
		Type:      artifactType,
		Mode:      mode,
		Timestamp: timestamp.Format(time.RFC3339),
		Path:      artifactRelPath(repoRoot, path),
		Summary:   summarize(path),
		SizeBytes: info.Size(),
		sortTime:  timestamp,
	}, true
}

func directoryArtifact(repoRoot string, path string, artifactType string, mode string) nanaArtifact {
	info, _ := os.Stat(path)
	var timestamp time.Time
	if info != nil {
		timestamp = info.ModTime().UTC()
	}
	artifact := nanaArtifact{
		Type:      artifactType,
		Mode:      mode,
		Path:      artifactRelPath(repoRoot, path),
		Files:     countRegularFiles(path),
		Timestamp: timestamp.Format(time.RFC3339),
		sortTime:  timestamp,
	}
	return artifact
}

func applyScoutMetadata(artifact *nanaArtifact, proposalsPath string) {
	if !artifactFileExists(proposalsPath) {
		return
	}
	var report scoutReport
	if err := readGithubJSON(proposalsPath, &report); err != nil {
		return
	}
	if timestamp, ok := parseArtifactTimestamp(report.GeneratedAt); ok {
		setArtifactTime(artifact, timestamp)
	}
	count := len(report.Proposals)
	if count == 0 {
		artifact.Summary = "0 proposals"
		return
	}
	firstTitle := strings.TrimSpace(report.Proposals[0].Title)
	if firstTitle == "" {
		artifact.Summary = fmt.Sprintf("%d proposals", count)
		return
	}
	artifact.Summary = fmt.Sprintf("%d proposals: %s", count, truncateArtifactSummary(firstTitle))
}

func applyManifestMetadata(artifact *nanaArtifact, manifestPath string) {
	if !artifactFileExists(manifestPath) {
		return
	}
	var manifest map[string]any
	if err := readGithubJSON(manifestPath, &manifest); err != nil {
		return
	}
	if timestamp, ok := firstTimestampField(manifest, "completed_at", "updated_at", "generated_at", "created_at"); ok {
		setArtifactTime(artifact, timestamp)
	}
	parts := []string{}
	if runID := jsonString(manifest, "run_id"); runID != "" {
		parts = append(parts, "run "+runID)
	}
	if status := jsonString(manifest, "status"); status != "" {
		parts = append(parts, "status "+status)
	}
	if phase := jsonString(manifest, "current_phase"); phase != "" {
		parts = append(parts, "phase "+phase)
	}
	for _, key := range []string{"query", "task", "reason"} {
		if value := jsonString(manifest, key); value != "" {
			parts = append(parts, truncateArtifactSummary(value))
			break
		}
	}
	if len(parts) > 0 {
		artifact.Summary = strings.Join(parts, "; ")
	}
}

func setArtifactTime(artifact *nanaArtifact, timestamp time.Time) {
	timestamp = timestamp.UTC()
	artifact.sortTime = timestamp
	artifact.Timestamp = timestamp.Format(time.RFC3339)
}

func firstTimestampField(values map[string]any, keys ...string) (time.Time, bool) {
	for _, key := range keys {
		if timestamp, ok := parseArtifactTimestamp(jsonString(values, key)); ok {
			return timestamp, true
		}
	}
	return time.Time{}, false
}

func parseArtifactTimestamp(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if timestamp, err := time.Parse(layout, value); err == nil {
			return timestamp, true
		}
	}
	if number, err := strconv.ParseInt(value, 10, 64); err == nil && number > 0 {
		switch {
		case number > 1_000_000_000_000_000_000:
			return time.Unix(0, number), true
		case number > 1_000_000_000_000:
			return time.UnixMilli(number), true
		case number > 1_000_000_000:
			return time.Unix(number, 0), true
		}
	}
	return time.Time{}, false
}

func markdownFileSummary(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
		line = strings.TrimPrefix(line, "- ")
		if line != "" {
			return truncateArtifactSummary(line)
		}
	}
	return ""
}

func jsonKeysSummary(path string) string {
	var values map[string]any
	if err := readGithubJSON(path, &values); err != nil {
		return ""
	}
	if len(values) == 0 {
		return "empty JSON object"
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	visible := keys
	if len(visible) > 4 {
		visible = visible[:4]
	}
	summary := fmt.Sprintf("%d keys: %s", len(keys), strings.Join(visible, ", "))
	if len(keys) > len(visible) {
		summary += ", ..."
	}
	return summary
}

func logFileSummary(path string) string {
	if strings.EqualFold(filepath.Base(path), "manifest.json") {
		artifact := nanaArtifact{}
		applyManifestMetadata(&artifact, path)
		return artifact.Summary
	}
	return markdownFileSummary(path)
}

func durableFileSummary(path string) string {
	if strings.EqualFold(filepath.Ext(path), ".json") {
		return jsonKeysSummary(path)
	}
	return markdownFileSummary(path)
}

func stateSummary(path string) string {
	var values map[string]any
	if err := readGithubJSON(path, &values); err != nil {
		return jsonKeysSummary(path)
	}
	parts := []string{}
	if sessionID := jsonString(values, "session_id"); sessionID != "" {
		parts = append(parts, "session "+sessionID)
	}
	if active, ok := values["active"].(bool); ok {
		if active {
			parts = append(parts, "active")
		} else {
			parts = append(parts, "inactive")
		}
	}
	if status := jsonString(values, "status"); status != "" {
		parts = append(parts, "status "+status)
	}
	if phase := jsonString(values, "current_phase"); phase != "" {
		parts = append(parts, "phase "+phase)
	}
	if len(parts) == 0 {
		return jsonKeysSummary(path)
	}
	return strings.Join(parts, "; ")
}

func inferContextMode(root string, path string) string {
	name := strings.ToLower(filepath.Base(path))
	switch {
	case strings.HasPrefix(name, "autopilot-"):
		return "autopilot"
	case strings.HasPrefix(name, "deep-interview-"):
		return "deep-interview"
	case strings.HasPrefix(name, "ralplan-"):
		return "ralplan"
	default:
		return "context"
	}
}

func inferInterviewMode(root string, path string) string {
	return "deep-interview"
}

func inferSpecMode(root string, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = filepath.Base(path)
	}
	rel = strings.ToLower(filepath.ToSlash(rel))
	switch {
	case strings.HasPrefix(rel, "deep-interview-"), strings.Contains(rel, "/deep-interview-"):
		return "deep-interview"
	case strings.HasPrefix(rel, "autoresearch-"), strings.Contains(rel, "/autoresearch-"):
		return "autoresearch"
	default:
		return "spec"
	}
}

func jsonString(values map[string]any, key string) string {
	value, ok := values[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return ""
	}
}

func inferPlanMode(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasPrefix(lower, "prd-"), strings.HasPrefix(lower, "test-spec-"):
		return "ralplan"
	default:
		return "plan"
	}
}

func inferLogMode(logRoot string, path string) string {
	rel, err := filepath.Rel(logRoot, path)
	if err != nil {
		return "logs"
	}
	rel = filepath.ToSlash(rel)
	parts := strings.Split(rel, "/")
	if len(parts) == 0 || parts[0] == "." || parts[0] == "" {
		return "logs"
	}
	first := strings.ToLower(parts[0])
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.Contains(first, "investigate"):
		return "investigate"
	case strings.Contains(first, "hook") || strings.HasPrefix(base, "hooks-"):
		return "hooks"
	default:
		return first
	}
}

func inferStateMode(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	name = strings.TrimSuffix(name, "-state")
	if name == "" || name == "session" {
		return "state"
	}
	return name
}

func inferGenericArtifactMode(name string) string {
	parts := strings.SplitN(name, "-", 2)
	if len(parts) > 0 && strings.TrimSpace(parts[0]) != "" {
		return strings.ToLower(parts[0])
	}
	return "artifact"
}

func artifactRelPath(repoRoot string, path string) string {
	rel, err := filepath.Rel(repoRoot, path)
	if err != nil {
		return filepath.ToSlash(filepath.Clean(path))
	}
	return filepath.ToSlash(rel)
}

func countRegularFiles(root string) int {
	count := 0
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if _, ok := artifactRegularFileInfo(path); ok {
			count++
		}
		return nil
	})
	return count
}

func artifactFileExists(path string) bool {
	_, ok := artifactRegularFileInfo(path)
	return ok
}

func artifactRegularFileInfo(path string) (os.FileInfo, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, false
	}
	return info, true
}

func artifactDirExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

func truncateArtifactSummary(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	const max = 120
	if len(value) <= max {
		return value
	}
	return strings.TrimSpace(utf8BytePrefix(value, max-1)) + "…"
}

func utf8BytePrefix(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	cutoff := 0
	for index := range value {
		if index > maxBytes {
			break
		}
		cutoff = index
	}
	return value[:cutoff]
}

func sortNanaArtifacts(artifacts []nanaArtifact) {
	sort.SliceStable(artifacts, func(i, j int) bool {
		left := artifactTypeRank(artifacts[i].Type)
		right := artifactTypeRank(artifacts[j].Type)
		if left != right {
			return left < right
		}
		if !artifacts[i].sortTime.Equal(artifacts[j].sortTime) {
			return artifacts[i].sortTime.After(artifacts[j].sortTime)
		}
		return artifacts[i].Path < artifacts[j].Path
	})
}

func artifactTypeRank(value string) int {
	order := []string{"notes", "project-memory", "context", "interviews", "specs", "plans", "logs", "state", "enhancements", "improvements", "ui-findings", "artifacts"}
	for index, current := range order {
		if value == current {
			return index
		}
	}
	return len(order)
}

func printNanaArtifactIndex(stdout io.Writer, index nanaArtifactIndex) {
	fmt.Fprintf(stdout, "NANA artifacts in %s\n", index.NanaDir)
	if len(index.Artifacts) == 0 {
		fmt.Fprintln(stdout, "No artifacts found.")
		return
	}

	groups := map[string][]nanaArtifact{}
	types := []string{}
	for _, artifact := range index.Artifacts {
		if _, ok := groups[artifact.Type]; !ok {
			types = append(types, artifact.Type)
		}
		groups[artifact.Type] = append(groups[artifact.Type], artifact)
	}
	sort.SliceStable(types, func(i, j int) bool {
		left := artifactTypeRank(types[i])
		right := artifactTypeRank(types[j])
		if left != right {
			return left < right
		}
		return types[i] < types[j]
	})

	for _, artifactType := range types {
		items := groups[artifactType]
		fmt.Fprintf(stdout, "\n%s (%d)\n", artifactType, len(items))
		for _, artifact := range items {
			timestamp := defaultString(artifact.Timestamp, "n/a")
			mode := defaultString(artifact.Mode, "n/a")
			fmt.Fprintf(stdout, "  %s  %-12s  %s", timestamp, mode, artifact.Path)
			if artifact.Summary != "" {
				fmt.Fprintf(stdout, " — %s", artifact.Summary)
			}
			if artifact.Files > 0 {
				fmt.Fprintf(stdout, " (%d files)", artifact.Files)
			}
			fmt.Fprintln(stdout)
		}
	}
}
