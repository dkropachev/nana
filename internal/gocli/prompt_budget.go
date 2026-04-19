package gocli

import (
	"fmt"
	"strings"
)

const (
	structuredPromptStdinThreshold = 8 * 1024

	reviewPromptChangedFilesLimit = 20
	reviewPromptMaxHunksPerFile   = 2
	reviewPromptMaxLinesPerFile   = 40
	reviewPromptMaxCharsPerFile   = 1200
	reviewPromptLocalCharLimit    = 24_000
	reviewPromptGithubCharLimit   = 32_000

	githubInstructionCharLimit = 24_000
	githubRolePromptCharLimit  = 8_000

	localWorkImplementPromptCharLimit  = 24_000
	localWorkPlanPayloadCharLimit      = 12_000
	localWorkPromptSurfaceCharLimit    = 6_000
	localWorkHardeningPromptCharLimit  = 32_000
	localWorkGroupingPromptCharLimit   = 20_000
	localWorkValidationPromptCharLimit = 24_000

	investigateRolePromptCharLimit       = 6_000
	investigatePromptCharLimit           = 24_000
	investigateValidatorPayloadCharLimit = 12_000
	investigateMaxPromptServers          = 20
	investigateMaxPromptViolations       = 10
	investigateMaxValidatorIssues        = 10
	investigateMaxValidatorProofs        = 20

	githubFeedbackInstructionCharLimit = 24_000
	githubFeedbackBodyCharLimit        = 600
	githubFeedbackIssueCommentLimit    = 5
	githubFeedbackReviewLimit          = 5
	githubFeedbackReviewCommentLimit   = 10

	agentsPromptCharLimit       = 8 * 1024
	embeddedRolePromptCharLimit = 3 * 1024
)

type reviewPromptContextOptions struct {
	ChangedFilesLimit int
	MaxHunksPerFile   int
	MaxLinesPerFile   int
	MaxCharsPerFile   int
}

type reviewPromptContext struct {
	ChangedFiles     []string
	ChangedFilesText string
	Shortstat        string
	DiffSummary      string
}

type promptDiffFileSection struct {
	Path  string
	Lines []string
}

func promptTransportForSize(prompt string, threshold int) codexPromptTransport {
	if threshold <= 0 {
		threshold = structuredPromptStdinThreshold
	}
	if len(strings.TrimSpace(prompt)) > threshold {
		return codexPromptTransportStdin
	}
	return codexPromptTransportArg
}

func compactPromptValue(value string, lineLimit int, charLimit int) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	lineTruncated := false
	if lineLimit > 0 {
		limited, truncated := tailLinesLimitedWithNotice(trimmed, lineLimit)
		trimmed = limited
		lineTruncated = truncated
	}
	if charLimit > 0 && len(trimmed) > charLimit {
		return strings.TrimSpace(trimmed[:charLimit]) + "... [truncated]"
	}
	if lineTruncated {
		return trimmed + "\n... [truncated]"
	}
	return trimmed
}

func compactPromptHeadValue(value string, lineLimit int, charLimit int) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	lineTruncated := false
	if lineLimit > 0 {
		limited, truncated := headLinesLimitedWithNotice(trimmed, lineLimit)
		trimmed = limited
		lineTruncated = truncated
	}
	if charLimit > 0 && len(trimmed) > charLimit {
		return strings.TrimSpace(trimmed[:charLimit]) + "... [truncated]"
	}
	if lineTruncated {
		return trimmed + "\n... [truncated]"
	}
	return trimmed
}

func capPromptChars(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	const notice = "\n\n[Prompt truncated to fit runtime limits]\n"
	if limit <= len(notice) {
		return notice[:limit]
	}
	return value[:limit-len(notice)] + notice
}

func tailLinesLimited(content string, limit int) string {
	limited, _ := tailLinesLimitedWithNotice(content, limit)
	return limited
}

func headLinesLimited(content string, limit int) string {
	limited, _ := headLinesLimitedWithNotice(content, limit)
	return limited
}

func tailLinesLimitedWithNotice(content string, limit int) (string, bool) {
	if limit <= 0 {
		return content, false
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= limit {
		return content, false
	}
	return strings.Join(lines[len(lines)-limit:], "\n"), true
}

func headLinesLimitedWithNotice(content string, limit int) (string, bool) {
	if limit <= 0 {
		return content, false
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= limit {
		return content, false
	}
	return strings.Join(lines[:limit], "\n"), true
}

func limitPromptList[T any](items []T, limit int) []T {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return append([]T(nil), items[:limit]...)
}

func mergePromptDocuments(parts ...string) string {
	seen := map[string]bool{}
	merged := []string{}
	for _, part := range parts {
		for _, block := range splitPromptBlocks(part) {
			normalized := normalizePromptBlock(block)
			if normalized == "" || seen[normalized] {
				continue
			}
			seen[normalized] = true
			merged = append(merged, strings.TrimSpace(block))
		}
	}
	return strings.TrimSpace(strings.Join(merged, "\n\n"))
}

func splitPromptBlocks(content string) []string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n\n")
}

func normalizePromptBlock(block string) string {
	lines := strings.Split(strings.TrimSpace(block), "\n")
	normalizedLines := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		if trimmed == "<!-- nana:generated:agents-md -->" {
			continue
		}
		trimmed = strings.ReplaceAll(trimmed, "./.nana/codex-home-investigate", "{{CODEX_HOME}}")
		trimmed = strings.ReplaceAll(trimmed, "./.codex", "{{CODEX_HOME}}")
		trimmed = strings.ReplaceAll(trimmed, "~/.codex", "{{CODEX_HOME}}")
		if strings.HasPrefix(trimmed, "Run `nana setup` to install prompts, skills, hooks") {
			trimmed = "Run `nana setup` to install prompts, skills, hooks, and runtime guidance. Run `nana doctor` to verify installation."
		}
		normalizedLines = append(normalizedLines, trimmed)
	}
	return strings.TrimSpace(strings.Join(normalizedLines, "\n"))
}

func buildReviewPromptContext(repoPath string, diffRefs []string, opts reviewPromptContextOptions) (reviewPromptContext, error) {
	changedFilesOutput, err := githubGitOutput(repoPath, append([]string{"diff", "--name-only"}, diffRefs...)...)
	if err != nil {
		return reviewPromptContext{}, err
	}
	changedFiles := collectTrimmedLines(changedFilesOutput)
	shortstatOutput, _ := githubGitOutput(repoPath, append([]string{"diff", "--shortstat"}, diffRefs...)...)
	diffOutput, err := githubGitOutput(repoPath, append([]string{"diff", "--unified=3"}, diffRefs...)...)
	if err != nil {
		return reviewPromptContext{}, err
	}
	return reviewPromptContext{
		ChangedFiles:     changedFiles,
		ChangedFilesText: summarizePromptChangedFiles(changedFiles, opts.ChangedFilesLimit),
		Shortstat:        strings.TrimSpace(shortstatOutput),
		DiffSummary:      summarizeUnifiedDiffForPrompt(diffOutput, opts),
	}, nil
}

func collectTrimmedLines(content string) []string {
	lines := []string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func summarizePromptChangedFiles(changedFiles []string, limit int) string {
	if len(changedFiles) == 0 {
		return "(none)"
	}
	if limit <= 0 || len(changedFiles) <= limit {
		return strings.Join(changedFiles, ", ")
	}
	return fmt.Sprintf("%s ... (+%d more)", strings.Join(changedFiles[:limit], ", "), len(changedFiles)-limit)
}

func summarizeUnifiedDiffForPrompt(diff string, opts reviewPromptContextOptions) string {
	sections := splitUnifiedDiffSections(diff)
	if len(sections) == 0 {
		return "(no textual diff)"
	}
	fileLimit := opts.ChangedFilesLimit
	if fileLimit <= 0 {
		fileLimit = reviewPromptChangedFilesLimit
	}
	rendered := []string{}
	for index, section := range sections {
		if index >= fileLimit {
			rendered = append(rendered, fmt.Sprintf("[... omitted %d additional changed file(s) ...]", len(sections)-fileLimit))
			break
		}
		rendered = append(rendered, summarizePromptDiffFileSection(section, opts))
	}
	return strings.TrimSpace(strings.Join(rendered, "\n\n"))
}

func splitUnifiedDiffSections(diff string) []promptDiffFileSection {
	lines := strings.Split(strings.TrimSpace(diff), "\n")
	sections := []promptDiffFileSection{}
	var current *promptDiffFileSection
	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			if current != nil {
				sections = append(sections, *current)
			}
			current = &promptDiffFileSection{
				Path:  parsePromptDiffPath(line),
				Lines: []string{line},
			}
			continue
		}
		if current == nil {
			continue
		}
		if strings.HasPrefix(line, "+++ b/") {
			current.Path = strings.TrimPrefix(line, "+++ b/")
		}
		if current.Path == "" && strings.HasPrefix(line, "--- a/") {
			current.Path = strings.TrimPrefix(line, "--- a/")
		}
		current.Lines = append(current.Lines, line)
	}
	if current != nil {
		sections = append(sections, *current)
	}
	return sections
}

func parsePromptDiffPath(header string) string {
	fields := strings.Fields(strings.TrimSpace(header))
	if len(fields) >= 4 {
		right := strings.TrimPrefix(fields[3], "b/")
		if right != "/dev/null" {
			return right
		}
		left := strings.TrimPrefix(fields[2], "a/")
		if left != "/dev/null" {
			return left
		}
	}
	return ""
}

func summarizePromptDiffFileSection(section promptDiffFileSection, opts reviewPromptContextOptions) string {
	maxHunks := opts.MaxHunksPerFile
	if maxHunks <= 0 {
		maxHunks = reviewPromptMaxHunksPerFile
	}
	maxLines := opts.MaxLinesPerFile
	if maxLines <= 0 {
		maxLines = reviewPromptMaxLinesPerFile
	}
	maxChars := opts.MaxCharsPerFile
	if maxChars <= 0 {
		maxChars = reviewPromptMaxCharsPerFile
	}

	path := strings.TrimSpace(section.Path)
	if path == "" {
		path = "(unknown path)"
	}
	out := []string{fmt.Sprintf("File: %s", path)}
	hunksSeen := 0
	linesUsed := 0
	omittedHunks := 0
	omittedLines := 0
	includedBinary := false

	for index := 0; index < len(section.Lines); index++ {
		line := section.Lines[index]
		switch {
		case strings.HasPrefix(line, "@@"):
			if hunksSeen >= maxHunks {
				omittedHunks++
				for index+1 < len(section.Lines) && !strings.HasPrefix(section.Lines[index+1], "@@") {
					index++
				}
				continue
			}
			hunksSeen++
			out = append(out, line)
		case strings.HasPrefix(line, "Binary files ") || strings.HasPrefix(line, "GIT binary patch"):
			out = append(out, line, "[binary diff omitted]")
			includedBinary = true
			index = len(section.Lines)
		default:
			if hunksSeen == 0 || includedBinary {
				continue
			}
			if strings.HasPrefix(line, "diff --git ") {
				break
			}
			if linesUsed >= maxLines {
				omittedLines++
				continue
			}
			out = append(out, line)
			linesUsed++
		}
	}

	if omittedHunks > 0 {
		out = append(out, fmt.Sprintf("[... omitted %d additional hunk(s) ...]", omittedHunks))
	}
	if omittedLines > 0 {
		out = append(out, fmt.Sprintf("[... omitted %d additional diff line(s) ...]", omittedLines))
	}
	return compactPromptValue(strings.Join(out, "\n"), 0, maxChars)
}
