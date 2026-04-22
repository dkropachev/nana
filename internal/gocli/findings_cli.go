package gocli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

const FindingsHelp = `nana findings - Repo-scoped findings inbox and markdown import

Usage:
  nana findings list --repo <owner/repo>
  nana findings import --repo <owner/repo> --file <path/to/file.md> [--promote <all|id1,id2>] [--drop <all|id1,id2>]
  nana findings import review --repo <owner/repo> --session <id> [--promote <all|id1,id2>] [--drop <all|id1,id2>]
  nana findings promote --repo <owner/repo> --finding <id>
  nana findings dismiss --repo <owner/repo> --finding <id>
  nana findings help
`

func Findings(cwd string, args []string) error {
	if len(args) == 0 || isHelpToken(args[0]) || args[0] == "help" {
		fmt.Fprint(os.Stdout, FindingsHelp)
		return nil
	}
	switch args[0] {
	case "list":
		options, err := parseFindingsListArgs(args[1:])
		if err != nil {
			return err
		}
		return findingsList(options)
	case "import":
		return findingsImportCommand(cwd, args[1:])
	case "promote":
		options, err := parseFindingsPromoteArgs(args[1:])
		if err != nil {
			return err
		}
		return findingsPromote(options)
	case "dismiss":
		options, err := parseFindingsPromoteArgs(args[1:])
		if err != nil {
			return err
		}
		return findingsDismiss(options)
	default:
		return fmt.Errorf("unknown findings subcommand: %s\n\n%s", args[0], FindingsHelp)
	}
}

type findingsListOptions struct {
	RepoSlug string
}

type findingsPromoteOptions struct {
	RepoSlug  string
	FindingID string
}

type findingsImportOptions struct {
	RepoSlug  string
	FilePath  string
	SessionID string
	Promote   string
	Drop      string
	Review    bool
}

func parseFindingsListArgs(args []string) (findingsListOptions, error) {
	options := findingsListOptions{}
	for index := 0; index < len(args); index++ {
		token := args[index]
		switch {
		case token == "--repo":
			value, err := requireFindingsFlagValue(args, index, "--repo")
			if err != nil {
				return findingsListOptions{}, err
			}
			options.RepoSlug = strings.TrimSpace(value)
			index++
		case strings.HasPrefix(token, "--repo="):
			options.RepoSlug = strings.TrimSpace(strings.TrimPrefix(token, "--repo="))
		default:
			return findingsListOptions{}, fmt.Errorf("unknown findings list option: %s\n\n%s", token, FindingsHelp)
		}
	}
	if strings.TrimSpace(options.RepoSlug) == "" {
		return findingsListOptions{}, fmt.Errorf("--repo is required\n\n%s", FindingsHelp)
	}
	return options, nil
}

func parseFindingsPromoteArgs(args []string) (findingsPromoteOptions, error) {
	options := findingsPromoteOptions{}
	for index := 0; index < len(args); index++ {
		token := args[index]
		switch {
		case token == "--repo":
			value, err := requireFindingsFlagValue(args, index, "--repo")
			if err != nil {
				return findingsPromoteOptions{}, err
			}
			options.RepoSlug = strings.TrimSpace(value)
			index++
		case strings.HasPrefix(token, "--repo="):
			options.RepoSlug = strings.TrimSpace(strings.TrimPrefix(token, "--repo="))
		case token == "--finding":
			value, err := requireFindingsFlagValue(args, index, "--finding")
			if err != nil {
				return findingsPromoteOptions{}, err
			}
			options.FindingID = strings.TrimSpace(value)
			index++
		case strings.HasPrefix(token, "--finding="):
			options.FindingID = strings.TrimSpace(strings.TrimPrefix(token, "--finding="))
		default:
			return findingsPromoteOptions{}, fmt.Errorf("unknown findings option: %s\n\n%s", token, FindingsHelp)
		}
	}
	if strings.TrimSpace(options.RepoSlug) == "" || strings.TrimSpace(options.FindingID) == "" {
		return findingsPromoteOptions{}, fmt.Errorf("--repo and --finding are required\n\n%s", FindingsHelp)
	}
	return options, nil
}

func findingsImportCommand(cwd string, args []string) error {
	options, err := parseFindingsImportArgs(args)
	if err != nil {
		return err
	}
	if options.Review {
		return findingsImportReview(cwd, options)
	}
	return findingsImport(cwd, options)
}

func parseFindingsImportArgs(args []string) (findingsImportOptions, error) {
	options := findingsImportOptions{}
	parseArgs := args
	if len(parseArgs) > 0 && parseArgs[0] == "review" {
		options.Review = true
		parseArgs = parseArgs[1:]
	}
	for index := 0; index < len(parseArgs); index++ {
		token := parseArgs[index]
		switch {
		case token == "--repo":
			value, err := requireFindingsFlagValue(parseArgs, index, "--repo")
			if err != nil {
				return findingsImportOptions{}, err
			}
			options.RepoSlug = strings.TrimSpace(value)
			index++
		case strings.HasPrefix(token, "--repo="):
			options.RepoSlug = strings.TrimSpace(strings.TrimPrefix(token, "--repo="))
		case token == "--file":
			value, err := requireFindingsFlagValue(parseArgs, index, "--file")
			if err != nil {
				return findingsImportOptions{}, err
			}
			options.FilePath = strings.TrimSpace(value)
			index++
		case strings.HasPrefix(token, "--file="):
			options.FilePath = strings.TrimSpace(strings.TrimPrefix(token, "--file="))
		case token == "--session":
			value, err := requireFindingsFlagValue(parseArgs, index, "--session")
			if err != nil {
				return findingsImportOptions{}, err
			}
			options.SessionID = strings.TrimSpace(value)
			index++
		case strings.HasPrefix(token, "--session="):
			options.SessionID = strings.TrimSpace(strings.TrimPrefix(token, "--session="))
		case token == "--promote":
			value, err := requireFindingsFlagValue(parseArgs, index, "--promote")
			if err != nil {
				return findingsImportOptions{}, err
			}
			options.Promote = strings.TrimSpace(value)
			index++
		case strings.HasPrefix(token, "--promote="):
			options.Promote = strings.TrimSpace(strings.TrimPrefix(token, "--promote="))
		case token == "--drop":
			value, err := requireFindingsFlagValue(parseArgs, index, "--drop")
			if err != nil {
				return findingsImportOptions{}, err
			}
			options.Drop = strings.TrimSpace(value)
			index++
		case strings.HasPrefix(token, "--drop="):
			options.Drop = strings.TrimSpace(strings.TrimPrefix(token, "--drop="))
		default:
			return findingsImportOptions{}, fmt.Errorf("unknown findings import option: %s\n\n%s", token, FindingsHelp)
		}
	}
	if strings.TrimSpace(options.RepoSlug) == "" {
		return findingsImportOptions{}, fmt.Errorf("--repo is required\n\n%s", FindingsHelp)
	}
	if options.Review {
		if strings.TrimSpace(options.SessionID) == "" {
			return findingsImportOptions{}, fmt.Errorf("--session is required for `nana findings import review`\n\n%s", FindingsHelp)
		}
		return options, nil
	}
	if strings.TrimSpace(options.FilePath) == "" {
		return findingsImportOptions{}, fmt.Errorf("--file is required for `nana findings import`\n\n%s", FindingsHelp)
	}
	return options, nil
}

func findingsList(options findingsListOptions) error {
	response, err := loadStartUIFindings(options.RepoSlug)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Repo: %s\n", options.RepoSlug)
	if len(response.Items) == 0 {
		fmt.Fprintln(os.Stdout, "Findings: none")
		return nil
	}
	for _, finding := range response.Items {
		line := fmt.Sprintf("- %s [%s/%s] %s", finding.ID, finding.Status, normalizeGithubSeverity(finding.Severity), defaultString(finding.Title, "(untitled)"))
		if strings.TrimSpace(finding.SourceKind) != "" {
			line += fmt.Sprintf(" source=%s:%s:%s", finding.SourceKind, finding.SourceID, finding.SourceItemID)
		}
		if strings.TrimSpace(finding.ParentTaskID) != "" {
			line += fmt.Sprintf(" parent=%s/%s", defaultString(finding.ParentTaskKind, "(unknown)"), finding.ParentTaskID)
		}
		if strings.TrimSpace(finding.PromotedTaskID) != "" {
			line += fmt.Sprintf(" planned=%s", finding.PromotedTaskID)
		}
		fmt.Fprintln(os.Stdout, line)
	}
	return nil
}

func findingsPromote(options findingsPromoteOptions) error {
	_, finding, item, err := promoteStartUIFinding(options.RepoSlug, options.FindingID)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Promoted %s -> %s\n", finding.ID, item.ID)
	return nil
}

func findingsDismiss(options findingsPromoteOptions) error {
	_, finding, err := dismissStartUIFinding(options.RepoSlug, options.FindingID, "")
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Dismissed %s (%s)\n", finding.ID, finding.Status)
	return nil
}

func findingsImport(cwd string, options findingsImportOptions) error {
	content, err := os.ReadFile(options.FilePath)
	if err != nil {
		return err
	}
	session, err := createStartUIFindingImportSession(options.RepoSlug, options.FilePath, string(content))
	if err != nil {
		return err
	}
	return findingsImportReviewSession(cwd, options, session)
}

func findingsImportReview(cwd string, options findingsImportOptions) error {
	session, err := loadStartUIFindingImportSession(options.RepoSlug, options.SessionID)
	if err != nil {
		return err
	}
	return findingsImportReviewSession(cwd, options, session)
}

func findingsImportReviewSession(_ string, options findingsImportOptions, session startWorkFindingImportSession) error {
	if strings.TrimSpace(session.CandidatesPath) == "" {
		if err := writeStartWorkFindingImportSessionArtifacts(&session); err != nil {
			return err
		}
	}
	if strings.TrimSpace(session.ParseStatus) == startWorkFindingImportParseFailed {
		fmt.Fprintf(os.Stdout, "Import session %s persisted with parse failure.\n", session.ID)
		if strings.TrimSpace(session.ParseError) != "" {
			fmt.Fprintf(os.Stdout, "Parse error: %s\n", session.ParseError)
		}
		return fmt.Errorf("findings import parse failed for session %s", session.ID)
	}
	if err := findingsOpenEditor(session.CandidatesPath); err != nil {
		return err
	}
	editedCandidates, err := readFindingsCandidatesArtifact(session.CandidatesPath)
	if err != nil {
		return err
	}
	_, session, err = replaceStartUIFindingImportSessionCandidates(options.RepoSlug, session.ID, editedCandidates)
	if err != nil {
		return err
	}
	printFindingsImportCandidates(session)
	promoteSelection := strings.TrimSpace(options.Promote)
	dropSelection := strings.TrimSpace(options.Drop)
	if promoteSelection == "" && dropSelection == "" && findingsCanPromptUser() {
		promoteSelection, dropSelection, err = findingsPromptImportActions(session)
		if err != nil {
			return err
		}
	}
	session, err = findingsApplyImportActions(options.RepoSlug, session, promoteSelection, dropSelection)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Reviewed import session %s\n", session.ID)
	return nil
}

func findingsApplyImportActions(repoSlug string, session startWorkFindingImportSession, promoteSelection string, dropSelection string) (startWorkFindingImportSession, error) {
	promoteIDs, err := findingsResolveCandidateSelection(session, promoteSelection, true)
	if err != nil {
		return startWorkFindingImportSession{}, err
	}
	for _, candidateID := range promoteIDs {
		_, updatedSession, _, promoteErr := promoteStartUIFindingImportCandidate(repoSlug, session.ID, candidateID)
		if promoteErr != nil {
			return startWorkFindingImportSession{}, promoteErr
		}
		session = updatedSession
	}
	dropIDs, err := findingsResolveCandidateSelection(session, dropSelection, false)
	if err != nil {
		return startWorkFindingImportSession{}, err
	}
	for _, candidateID := range dropIDs {
		candidate := findingsCandidateByID(session, candidateID)
		if candidate == nil || normalizeStartWorkFindingCandidateStatus(candidate.Status) != startWorkFindingCandidateStatusCandidate {
			continue
		}
		_, updatedSession, dropErr := dropStartUIFindingImportCandidate(repoSlug, session.ID, candidateID)
		if dropErr != nil {
			return startWorkFindingImportSession{}, dropErr
		}
		session = updatedSession
	}
	return session, nil
}

func findingsResolveCandidateSelection(session startWorkFindingImportSession, selection string, promoteOnly bool) ([]string, error) {
	selection = strings.TrimSpace(selection)
	if selection == "" || strings.EqualFold(selection, "none") {
		return nil, nil
	}
	candidates := []string{}
	if strings.EqualFold(selection, "all") {
		for _, candidate := range session.Candidates {
			if normalizeStartWorkFindingCandidateStatus(candidate.Status) != startWorkFindingCandidateStatusCandidate {
				continue
			}
			candidates = append(candidates, candidate.CandidateID)
		}
		return candidates, nil
	}
	for _, raw := range strings.Split(selection, ",") {
		candidateID := strings.TrimSpace(raw)
		if candidateID == "" {
			continue
		}
		candidate := findingsCandidateByID(session, candidateID)
		if candidate == nil {
			return nil, fmt.Errorf("candidate %s was not found in session %s", candidateID, session.ID)
		}
		if normalizeStartWorkFindingCandidateStatus(candidate.Status) != startWorkFindingCandidateStatusCandidate && promoteOnly {
			return nil, fmt.Errorf("candidate %s is not available for promotion", candidateID)
		}
		candidates = append(candidates, candidateID)
	}
	return candidates, nil
}

func findingsCandidateByID(session startWorkFindingImportSession, candidateID string) *startWorkFindingImportCandidate {
	for index := range session.Candidates {
		if session.Candidates[index].CandidateID == candidateID {
			return &session.Candidates[index]
		}
	}
	return nil
}

func findingsCanPromptUser() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func findingsPromptImportActions(session startWorkFindingImportSession) (string, string, error) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Fprint(os.Stdout, "Promote candidates (all|none|id1,id2): ")
	promote, err := reader.ReadString('\n')
	if err != nil {
		return "", "", err
	}
	fmt.Fprint(os.Stdout, "Drop candidates (all|none|id1,id2): ")
	drop, err := reader.ReadString('\n')
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(promote), strings.TrimSpace(drop), nil
}

func printFindingsImportCandidates(session startWorkFindingImportSession) {
	fmt.Fprintf(os.Stdout, "Session: %s\n", session.ID)
	fmt.Fprintf(os.Stdout, "Candidates file: %s\n", session.CandidatesPath)
	fmt.Fprintf(os.Stdout, "Preview file: %s\n", session.PreviewPath)
	if len(session.Candidates) == 0 {
		fmt.Fprintln(os.Stdout, "Candidates: none")
		return
	}
	sort.SliceStable(session.Candidates, func(i, j int) bool {
		return session.Candidates[i].CandidateID < session.Candidates[j].CandidateID
	})
	for _, candidate := range session.Candidates {
		fmt.Fprintf(
			os.Stdout,
			"- %s [%s/%s/%s] %s\n",
			candidate.CandidateID,
			normalizeStartWorkFindingCandidateStatus(candidate.Status),
			normalizeGithubSeverity(candidate.Severity),
			defaultString(normalizeWorkType(candidate.WorkType), workTypeFeature),
			defaultString(candidate.Title, "(untitled)"),
		)
	}
}

func findingsOpenEditor(path string) error {
	editor := strings.TrimSpace(os.Getenv("EDITOR"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("VISUAL"))
	}
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("editor exited with status %d", exitErr.ExitCode())
		}
		return err
	}
	return nil
}

func readFindingsCandidatesArtifact(path string) ([]startWorkFindingImportCandidate, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Candidates []startWorkFindingImportCandidate `json:"candidates"`
	}
	if err := json.Unmarshal(content, &payload); err != nil {
		return nil, fmt.Errorf("invalid edited candidates.json: %w", err)
	}
	return validateStartWorkFindingImportCandidates(payload.Candidates)
}

func requireFindingsFlagValue(args []string, index int, flag string) (string, error) {
	if index+1 >= len(args) {
		return "", fmt.Errorf("missing value after %s\n\n%s", flag, FindingsHelp)
	}
	return args[index+1], nil
}
