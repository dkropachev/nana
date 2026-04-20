package gocli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	workItemStatusQueued       = "queued"
	workItemStatusRunning      = "running"
	workItemStatusDraftReady   = "draft_ready"
	workItemStatusSubmitted    = "submitted"
	workItemStatusDropped      = "dropped"
	workItemStatusSilenced     = "silenced"
	workItemStatusFailed       = "failed"
	workItemStatusPaused       = "paused"
	workItemStatusNeedsRouting = "needs_routing"
)

const WorkItemsHelp = `nana work items - Cross-source work-item queue and draft workflow

Usage:
  nana work items list [--json] [--limit <n>] [--status <value>] [--hidden|--all]
  nana work items show <item-id> [--json]
  nana work items intake --stdin-json
  nana work items sync-github [--repo <owner/repo>] [--limit <n>] [--auto-run] [-- codex-args...]
  nana work items run <item-id> [-- codex-args...]
  nana work items submit <item-id>
  nana work items fix <item-id> --instruction <text> [-- codex-args...]
  nana work items drop <item-id>
  nana work items restore <item-id>
  nana work items help

Behavior:
  - intake reads a normalized work-item JSON payload from stdin
  - sync-github ingests GitHub review requests and thread comments into the queue
  - run generates a draft review/reply/execution result without publishing by default
  - reply drafting acquires a read lock on the selected repo checkout or linked sandbox before invoking Codex
  - rate-limited runs pause instead of failing and become runnable again after retry_after
  - submit publishes the current draft through GitHub or an adapter submit profile
  - drop records a terminal disposition; high-confidence ignore drafts are silenced
  - hidden items stay in audit history and are excluded from default list/UI views
`

type workItem struct {
	ID                 string                 `json:"id"`
	DedupeKey          string                 `json:"dedupe_key"`
	Source             string                 `json:"source"`
	SourceKind         string                 `json:"source_kind"`
	ExternalID         string                 `json:"external_id"`
	ThreadKey          string                 `json:"thread_key,omitempty"`
	RepoSlug           string                 `json:"repo_slug,omitempty"`
	TargetURL          string                 `json:"target_url,omitempty"`
	LinkedRunID        string                 `json:"linked_run_id,omitempty"`
	Subject            string                 `json:"subject"`
	Body               string                 `json:"body,omitempty"`
	Author             string                 `json:"author,omitempty"`
	ReceivedAt         string                 `json:"received_at,omitempty"`
	Status             string                 `json:"status"`
	Priority           int                    `json:"priority,omitempty"`
	AutoRun            bool                   `json:"auto_run,omitempty"`
	AutoSubmit         bool                   `json:"auto_submit,omitempty"`
	Hidden             bool                   `json:"hidden,omitempty"`
	HiddenReason       string                 `json:"hidden_reason,omitempty"`
	SubmitProfile      *workItemSubmitProfile `json:"submit_profile,omitempty"`
	Metadata           map[string]any         `json:"metadata,omitempty"`
	LatestDraft        *workItemDraft         `json:"latest_draft,omitempty"`
	LatestArtifactRoot string                 `json:"latest_artifact_root,omitempty"`
	LatestActionAt     string                 `json:"latest_action_at,omitempty"`
	PauseReason        string                 `json:"pause_reason,omitempty"`
	PauseUntil         string                 `json:"pause_until,omitempty"`
	CreatedAt          string                 `json:"created_at"`
	UpdatedAt          string                 `json:"updated_at"`
}

type workItemCodexTarget struct {
	RepoPath string
	LockKind string
}

const (
	workItemCodexLockSource  = "source"
	workItemCodexLockSandbox = "sandbox"
)

type workItemInput struct {
	Source        string                 `json:"source"`
	SourceKind    string                 `json:"source_kind"`
	ExternalID    string                 `json:"external_id"`
	ThreadKey     string                 `json:"thread_key,omitempty"`
	RepoSlug      string                 `json:"repo_slug,omitempty"`
	TargetURL     string                 `json:"target_url,omitempty"`
	Subject       string                 `json:"subject"`
	Body          string                 `json:"body,omitempty"`
	Author        string                 `json:"author,omitempty"`
	ReceivedAt    string                 `json:"received_at,omitempty"`
	Priority      int                    `json:"priority,omitempty"`
	AutoRun       bool                   `json:"auto_run,omitempty"`
	AutoSubmit    bool                   `json:"auto_submit,omitempty"`
	SubmitProfile *workItemSubmitProfile `json:"submit_profile,omitempty"`
	Metadata      map[string]any         `json:"metadata,omitempty"`
}

type workItemSubmitProfile struct {
	Type     string         `json:"type"`
	Command  string         `json:"command,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type workItemDraft struct {
	Kind                 string                       `json:"kind"`
	Body                 string                       `json:"body,omitempty"`
	ReviewEvent          string                       `json:"review_event,omitempty"`
	InlineComments       []workItemDraftInlineComment `json:"inline_comments,omitempty"`
	Summary              string                       `json:"summary,omitempty"`
	SuggestedDisposition string                       `json:"suggested_disposition,omitempty"`
	Confidence           float64                      `json:"confidence,omitempty"`
	RunID                string                       `json:"run_id,omitempty"`
}

type workItemDraftInlineComment struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
	Body string `json:"body"`
}

type workItemEvent struct {
	ID        int64          `json:"id"`
	ItemID    string         `json:"item_id"`
	CreatedAt string         `json:"created_at"`
	EventType string         `json:"event_type"`
	Actor     string         `json:"actor,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
}

type workItemLink struct {
	ItemID   string         `json:"item_id"`
	LinkType string         `json:"link_type"`
	TargetID string         `json:"target_id"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type workItemDetail struct {
	Item      workItem           `json:"item"`
	Events    []workItemEvent    `json:"events,omitempty"`
	Links     []workItemLink     `json:"links,omitempty"`
	LinkedRun *workRunIndexEntry `json:"linked_run,omitempty"`
}

type workItemListOptions struct {
	Limit         int
	Status        string
	IncludeHidden bool
	OnlyHidden    bool
	RepoSlug      string
}

type workItemsListCommandOptions struct {
	JSON          bool
	Limit         int
	Status        string
	IncludeHidden bool
	OnlyHidden    bool
}

type workItemSyncCommandOptions struct {
	RepoSlug  string
	Limit     int
	AutoRun   bool
	CodexArgs []string
}

type workItemFixCommandOptions struct {
	ItemID      string
	Instruction string
	CodexArgs   []string
}

type workItemExecutionResult struct {
	Item  workItem
	Draft *workItemDraft
	Links []workItemLink
}

func workItemsCommand(cwd string, args []string) error {
	if len(args) == 0 || isHelpToken(args[0]) {
		fmt.Fprint(os.Stdout, WorkItemsHelp)
		return nil
	}
	switch args[0] {
	case "list":
		options, err := parseWorkItemsListArgs(args[1:])
		if err != nil {
			return err
		}
		return listWorkItemsCommand(options)
	case "show":
		itemID, jsonOutput, err := parseWorkItemShowArgs(args[1:])
		if err != nil {
			return err
		}
		return showWorkItemCommand(itemID, jsonOutput)
	case "intake":
		if err := ensureStdinJSONArg(args[1:]); err != nil {
			return err
		}
		return intakeWorkItemCommand(os.Stdin)
	case "sync-github":
		options, err := parseWorkItemSyncArgs(args[1:])
		if err != nil {
			return err
		}
		_, err = syncGithubWorkItems(options)
		return err
	case "run":
		itemID, codexArgs, err := parseWorkItemRunArgs(args[1:])
		if err != nil {
			return err
		}
		_, err = runWorkItemByID(cwd, itemID, codexArgs, false)
		return err
	case "submit":
		itemID, err := parseSingleWorkItemArg(args[1:], "submit")
		if err != nil {
			return err
		}
		_, err = submitWorkItemByID(itemID, "user")
		return err
	case "fix":
		options, err := parseWorkItemFixArgs(args[1:])
		if err != nil {
			return err
		}
		_, err = fixWorkItemByID(cwd, options)
		return err
	case "drop":
		itemID, err := parseSingleWorkItemArg(args[1:], "drop")
		if err != nil {
			return err
		}
		return dropWorkItemByID(itemID, "user")
	case "restore":
		itemID, err := parseSingleWorkItemArg(args[1:], "restore")
		if err != nil {
			return err
		}
		return restoreWorkItemByID(itemID, "user")
	default:
		return fmt.Errorf("Unknown work items subcommand: %s\n\n%s", args[0], WorkItemsHelp)
	}
}

func ensureStdinJSONArg(args []string) error {
	for _, token := range args {
		if token == "--stdin-json" {
			return nil
		}
	}
	return fmt.Errorf("Usage: nana work items intake --stdin-json\n\n%s", WorkItemsHelp)
}

func parseWorkItemsListArgs(args []string) (workItemsListCommandOptions, error) {
	options := workItemsListCommandOptions{Limit: 20}
	for index := 0; index < len(args); index++ {
		token := args[index]
		switch {
		case token == "--json":
			options.JSON = true
		case token == "--all":
			options.IncludeHidden = true
		case token == "--hidden":
			options.OnlyHidden = true
			options.IncludeHidden = true
		case token == "--limit":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return workItemsListCommandOptions{}, fmt.Errorf("Missing value after --limit.\n\n%s", WorkItemsHelp)
			}
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || parsed <= 0 {
				return workItemsListCommandOptions{}, fmt.Errorf("Invalid --limit value %q.\n\n%s", value, WorkItemsHelp)
			}
			options.Limit = parsed
			index++
		case strings.HasPrefix(token, "--limit="):
			value := strings.TrimSpace(strings.TrimPrefix(token, "--limit="))
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed <= 0 {
				return workItemsListCommandOptions{}, fmt.Errorf("Invalid --limit value %q.\n\n%s", value, WorkItemsHelp)
			}
			options.Limit = parsed
		case token == "--status":
			value, err := requireFlagValue(args, index, token)
			if err != nil {
				return workItemsListCommandOptions{}, fmt.Errorf("Missing value after --status.\n\n%s", WorkItemsHelp)
			}
			options.Status = strings.TrimSpace(value)
			index++
		case strings.HasPrefix(token, "--status="):
			options.Status = strings.TrimSpace(strings.TrimPrefix(token, "--status="))
		default:
			return workItemsListCommandOptions{}, fmt.Errorf("Unknown work items list option: %s\n\n%s", token, WorkItemsHelp)
		}
	}
	return options, nil
}

func parseWorkItemShowArgs(args []string) (string, bool, error) {
	if len(args) == 0 {
		return "", false, fmt.Errorf("Usage: nana work items show <item-id> [--json]\n\n%s", WorkItemsHelp)
	}
	itemID := ""
	jsonOutput := false
	for _, token := range args {
		switch token {
		case "--json":
			jsonOutput = true
		default:
			if itemID != "" {
				return "", false, fmt.Errorf("Usage: nana work items show <item-id> [--json]\n\n%s", WorkItemsHelp)
			}
			itemID = strings.TrimSpace(token)
		}
	}
	if itemID == "" {
		return "", false, fmt.Errorf("Usage: nana work items show <item-id> [--json]\n\n%s", WorkItemsHelp)
	}
	return itemID, jsonOutput, nil
}

func parseWorkItemSyncArgs(args []string) (workItemSyncCommandOptions, error) {
	options := workItemSyncCommandOptions{Limit: 50}
	passthroughIndex := len(args)
	for index, token := range args {
		if token == "--" {
			passthroughIndex = index
			break
		}
	}
	parseArgs := args[:passthroughIndex]
	if passthroughIndex < len(args) {
		options.CodexArgs = append([]string{}, args[passthroughIndex+1:]...)
	}
	for index := 0; index < len(parseArgs); index++ {
		token := parseArgs[index]
		switch {
		case token == "--repo":
			value, err := requireFlagValue(parseArgs, index, token)
			if err != nil {
				return workItemSyncCommandOptions{}, fmt.Errorf("Missing value after --repo.\n\n%s", WorkItemsHelp)
			}
			repoSlug, err := resolveGithubRepoSlugLocator(value)
			if err != nil {
				return workItemSyncCommandOptions{}, err
			}
			options.RepoSlug = repoSlug
			index++
		case strings.HasPrefix(token, "--repo="):
			repoSlug, err := resolveGithubRepoSlugLocator(strings.TrimPrefix(token, "--repo="))
			if err != nil {
				return workItemSyncCommandOptions{}, err
			}
			options.RepoSlug = repoSlug
		case token == "--limit":
			value, err := requireFlagValue(parseArgs, index, token)
			if err != nil {
				return workItemSyncCommandOptions{}, fmt.Errorf("Missing value after --limit.\n\n%s", WorkItemsHelp)
			}
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || parsed <= 0 {
				return workItemSyncCommandOptions{}, fmt.Errorf("Invalid --limit value %q.\n\n%s", value, WorkItemsHelp)
			}
			options.Limit = parsed
			index++
		case strings.HasPrefix(token, "--limit="):
			value := strings.TrimSpace(strings.TrimPrefix(token, "--limit="))
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed <= 0 {
				return workItemSyncCommandOptions{}, fmt.Errorf("Invalid --limit value %q.\n\n%s", value, WorkItemsHelp)
			}
			options.Limit = parsed
		case token == "--auto-run":
			options.AutoRun = true
		default:
			return workItemSyncCommandOptions{}, fmt.Errorf("Unknown work items sync option: %s\n\n%s", token, WorkItemsHelp)
		}
	}
	return options, nil
}

func parseWorkItemRunArgs(args []string) (string, []string, error) {
	passthroughIndex := len(args)
	for index, token := range args {
		if token == "--" {
			passthroughIndex = index
			break
		}
	}
	parseArgs := args[:passthroughIndex]
	codexArgs := []string{}
	if passthroughIndex < len(args) {
		codexArgs = append(codexArgs, args[passthroughIndex+1:]...)
	}
	itemID, err := parseSingleWorkItemArg(parseArgs, "run")
	return itemID, codexArgs, err
}

func parseWorkItemFixArgs(args []string) (workItemFixCommandOptions, error) {
	options := workItemFixCommandOptions{}
	passthroughIndex := len(args)
	for index, token := range args {
		if token == "--" {
			passthroughIndex = index
			break
		}
	}
	parseArgs := args[:passthroughIndex]
	if passthroughIndex < len(args) {
		options.CodexArgs = append([]string{}, args[passthroughIndex+1:]...)
	}
	for index := 0; index < len(parseArgs); index++ {
		token := parseArgs[index]
		switch {
		case token == "--instruction":
			value, err := requireFlagValue(parseArgs, index, token)
			if err != nil {
				return workItemFixCommandOptions{}, fmt.Errorf("Missing value after --instruction.\n\n%s", WorkItemsHelp)
			}
			options.Instruction = strings.TrimSpace(value)
			index++
		case strings.HasPrefix(token, "--instruction="):
			options.Instruction = strings.TrimSpace(strings.TrimPrefix(token, "--instruction="))
		default:
			if options.ItemID != "" {
				return workItemFixCommandOptions{}, fmt.Errorf("Usage: nana work items fix <item-id> --instruction <text> [-- codex-args...]\n\n%s", WorkItemsHelp)
			}
			options.ItemID = strings.TrimSpace(token)
		}
	}
	if options.ItemID == "" || options.Instruction == "" {
		return workItemFixCommandOptions{}, fmt.Errorf("Usage: nana work items fix <item-id> --instruction <text> [-- codex-args...]\n\n%s", WorkItemsHelp)
	}
	return options, nil
}

func parseSingleWorkItemArg(args []string, verb string) (string, error) {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return "", fmt.Errorf("Usage: nana work items %s <item-id>\n\n%s", verb, WorkItemsHelp)
	}
	return strings.TrimSpace(args[0]), nil
}

func listWorkItemsCommand(options workItemsListCommandOptions) error {
	items, err := listWorkItems(workItemListOptions{
		Limit:         options.Limit,
		Status:        options.Status,
		IncludeHidden: options.IncludeHidden,
		OnlyHidden:    options.OnlyHidden,
	})
	if err != nil {
		return localWorkReadCommandError(err)
	}
	if options.JSON {
		_, err := os.Stdout.Write(mustMarshalJSON(map[string]any{"items": items}))
		return err
	}
	if len(items) == 0 {
		fmt.Fprintln(os.Stdout, "[work-item] No items matched the requested filter.")
		return nil
	}
	for _, item := range items {
		hiddenSuffix := ""
		if item.Hidden {
			hiddenSuffix = " hidden"
		}
		pauseSuffix := ""
		if strings.TrimSpace(item.PauseUntil) != "" {
			pauseSuffix = " retry_after=" + strings.TrimSpace(item.PauseUntil)
		}
		fmt.Fprintf(os.Stdout, "[work-item] %s %s/%s status=%s%s subject=%q repo=%s target=%s updated=%s\n",
			item.ID,
			defaultString(item.Source, "-"),
			defaultString(item.SourceKind, "-"),
			defaultString(item.Status, "-"),
			hiddenSuffix+pauseSuffix,
			item.Subject,
			defaultString(item.RepoSlug, "-"),
			defaultString(item.TargetURL, "-"),
			defaultString(item.UpdatedAt, "-"),
		)
	}
	return nil
}

func showWorkItemCommand(itemID string, jsonOutput bool) error {
	detail, err := readWorkItemDetail(itemID)
	if err != nil {
		return localWorkReadCommandError(err)
	}
	if jsonOutput {
		_, err := os.Stdout.Write(mustMarshalJSON(detail))
		return err
	}
	item := detail.Item
	fmt.Fprintf(os.Stdout, "[work-item] Id: %s\n", item.ID)
	fmt.Fprintf(os.Stdout, "[work-item] Source: %s/%s\n", defaultString(item.Source, "-"), defaultString(item.SourceKind, "-"))
	fmt.Fprintf(os.Stdout, "[work-item] Status: %s\n", defaultString(item.Status, "-"))
	if strings.TrimSpace(item.PauseUntil) != "" {
		fmt.Fprintf(os.Stdout, "[work-item] Pause until: %s", item.PauseUntil)
		if strings.TrimSpace(item.PauseReason) != "" {
			fmt.Fprintf(os.Stdout, " reason=%s", item.PauseReason)
		}
		fmt.Fprintln(os.Stdout)
	}
	if item.Hidden {
		fmt.Fprintf(os.Stdout, "[work-item] Hidden: true (%s)\n", defaultString(item.HiddenReason, "no reason recorded"))
	}
	fmt.Fprintf(os.Stdout, "[work-item] Subject: %s\n", defaultString(item.Subject, "(none)"))
	if strings.TrimSpace(item.Body) != "" {
		fmt.Fprintf(os.Stdout, "[work-item] Body: %s\n", item.Body)
	}
	fmt.Fprintf(os.Stdout, "[work-item] Repo: %s\n", defaultString(item.RepoSlug, "-"))
	fmt.Fprintf(os.Stdout, "[work-item] Target: %s\n", defaultString(item.TargetURL, "-"))
	if strings.TrimSpace(item.LinkedRunID) != "" {
		fmt.Fprintf(os.Stdout, "[work-item] Linked run: %s\n", item.LinkedRunID)
	}
	fmt.Fprintf(os.Stdout, "[work-item] Artifact root: %s\n", defaultString(item.LatestArtifactRoot, "-"))
	if item.LatestDraft != nil {
		fmt.Fprintf(os.Stdout, "[work-item] Draft kind: %s\n", defaultString(item.LatestDraft.Kind, "-"))
		fmt.Fprintf(os.Stdout, "[work-item] Draft summary: %s\n", defaultString(item.LatestDraft.Summary, "(none)"))
		if strings.TrimSpace(item.LatestDraft.ReviewEvent) != "" {
			fmt.Fprintf(os.Stdout, "[work-item] Review event: %s\n", item.LatestDraft.ReviewEvent)
		}
		if strings.TrimSpace(item.LatestDraft.Body) != "" {
			fmt.Fprintf(os.Stdout, "[work-item] Draft body:\n%s\n", item.LatestDraft.Body)
		}
		for _, comment := range item.LatestDraft.InlineComments {
			fmt.Fprintf(os.Stdout, "[work-item] Inline comment: %s:%d %s\n", comment.Path, comment.Line, comment.Body)
		}
	}
	for _, event := range detail.Events {
		fmt.Fprintf(os.Stdout, "[work-item] Event %d: %s at %s actor=%s\n", event.ID, event.EventType, event.CreatedAt, defaultString(event.Actor, "-"))
	}
	return nil
}

func intakeWorkItemCommand(reader io.Reader) error {
	content, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	var input workItemInput
	if err := json.Unmarshal(content, &input); err != nil {
		return fmt.Errorf("work item intake requires valid JSON: %w", err)
	}
	item, _, err := enqueueWorkItem(input, "intake")
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "[work-item] queued %s status=%s source=%s/%s\n", item.ID, item.Status, item.Source, item.SourceKind)
	return nil
}

func listWorkItems(options workItemListOptions) ([]workItem, error) {
	return withLocalWorkReadStore(func(store *localWorkDBStore) ([]workItem, error) {
		return store.listWorkItems(options)
	})
}

func readWorkItemDetail(itemID string) (workItemDetail, error) {
	return withLocalWorkReadStore(func(store *localWorkDBStore) (workItemDetail, error) {
		item, err := store.readWorkItem(itemID)
		if err != nil {
			return workItemDetail{}, err
		}
		events, err := store.readWorkItemEvents(itemID)
		if err != nil {
			return workItemDetail{}, err
		}
		links, err := store.readWorkItemLinks(itemID)
		if err != nil {
			return workItemDetail{}, err
		}
		var linkedRun *workRunIndexEntry
		if strings.TrimSpace(item.LinkedRunID) != "" {
			entry, err := readWorkRunIndex(item.LinkedRunID)
			if err == nil {
				linkedRun = &entry
			}
		}
		return workItemDetail{
			Item:      item,
			Events:    events,
			Links:     links,
			LinkedRun: linkedRun,
		}, nil
	})
}

func enqueueWorkItem(input workItemInput, actor string) (workItem, bool, error) {
	item, err := buildWorkItemFromInput(input)
	if err != nil {
		return workItem{}, false, err
	}
	var created bool
	item, err = withLocalWorkWriteStore(func(store *localWorkDBStore) (workItem, error) {
		item, created, err = store.upsertWorkItemWithLinks(item, actor, map[string]any{
			"source":      item.Source,
			"source_kind": item.SourceKind,
			"external_id": item.ExternalID,
			"target_url":  item.TargetURL,
		}, buildDefaultWorkItemLinks(item))
		return item, err
	})
	if err != nil {
		return workItem{}, false, err
	}
	if err := writeWorkItemSourceSnapshot(item); err != nil {
		return workItem{}, false, err
	}
	return item, created, nil
}

func buildWorkItemFromInput(input workItemInput) (workItem, error) {
	now := ISOTimeNow()
	item := workItem{
		ID:            fmt.Sprintf("wi-%d", time.Now().UnixNano()),
		Source:        strings.TrimSpace(input.Source),
		SourceKind:    strings.TrimSpace(input.SourceKind),
		ExternalID:    strings.TrimSpace(input.ExternalID),
		ThreadKey:     strings.TrimSpace(input.ThreadKey),
		RepoSlug:      strings.TrimSpace(input.RepoSlug),
		TargetURL:     strings.TrimSpace(input.TargetURL),
		Subject:       strings.TrimSpace(input.Subject),
		Body:          strings.TrimSpace(input.Body),
		Author:        strings.TrimSpace(input.Author),
		ReceivedAt:    defaultString(strings.TrimSpace(input.ReceivedAt), now),
		Status:        workItemStatusQueued,
		Priority:      input.Priority,
		AutoRun:       input.AutoRun,
		AutoSubmit:    input.AutoSubmit,
		SubmitProfile: input.SubmitProfile,
		Metadata:      cloneWorkItemMetadata(input.Metadata),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if item.Source == "" || item.SourceKind == "" || item.ExternalID == "" || item.Subject == "" {
		return workItem{}, fmt.Errorf("work item intake requires source, source_kind, external_id, and subject")
	}
	if item.Priority < 0 || item.Priority > 5 {
		item.Priority = 3
	}
	item.DedupeKey = computeWorkItemDedupeKey(item)
	item.LinkedRunID = resolveWorkItemLinkedRunID(item)
	if item.SourceKind == "task" && metadataString(item.Metadata, "task_mode") != "reply" && item.LinkedRunID == "" && strings.TrimSpace(item.TargetURL) == "" && strings.TrimSpace(metadataString(item.Metadata, "repo_root")) == "" {
		item.Status = workItemStatusNeedsRouting
	}
	return item, nil
}

func computeWorkItemDedupeKey(item workItem) string {
	parts := []string{
		strings.ToLower(strings.TrimSpace(item.Source)),
		strings.ToLower(strings.TrimSpace(item.SourceKind)),
		strings.TrimSpace(item.ExternalID),
		strings.TrimSpace(item.ThreadKey),
		strings.TrimSpace(item.TargetURL),
	}
	return strings.Join(parts, "|")
}

func buildDefaultWorkItemLinks(item workItem) []workItemLink {
	links := []workItemLink{}
	if strings.TrimSpace(item.LinkedRunID) != "" {
		links = append(links, workItemLink{
			ItemID:   item.ID,
			LinkType: "run",
			TargetID: item.LinkedRunID,
		})
	}
	if strings.TrimSpace(item.TargetURL) != "" {
		links = append(links, workItemLink{
			ItemID:   item.ID,
			LinkType: "target_url",
			TargetID: item.TargetURL,
		})
	}
	return links
}

func resolveWorkItemLinkedRunID(item workItem) string {
	if strings.TrimSpace(item.TargetURL) == "" && strings.TrimSpace(item.RepoSlug) == "" {
		return ""
	}
	runID, err := withLocalWorkReadStore(func(store *localWorkDBStore) (string, error) {
		return store.findLinkedRunID(item.RepoSlug, item.TargetURL)
	})
	if err != nil {
		return ""
	}
	return runID
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func cloneWorkItemMetadata(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	content, err := json.Marshal(input)
	if err != nil {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := json.Unmarshal(content, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func clearWorkItemPauseMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	cloned := cloneWorkItemMetadata(metadata)
	delete(cloned, "pause_reason")
	delete(cloned, "pause_until")
	if len(cloned) == 0 {
		return nil
	}
	return cloned
}

func workItemPauseFields(item *workItem) {
	if item == nil {
		return
	}
	item.PauseReason = strings.TrimSpace(item.PauseReason)
	item.PauseUntil = strings.TrimSpace(item.PauseUntil)
}

func workItemsRoot() string {
	return filepath.Join(workHomeRoot(), "items")
}

func workItemRoot(itemID string) string {
	return filepath.Join(workItemsRoot(), itemID)
}

func workItemAttemptDir(itemID string, attempt int) string {
	return filepath.Join(workItemRoot(itemID), fmt.Sprintf("attempt-%03d", attempt))
}

func nextWorkItemAttemptDir(itemID string) (string, int, error) {
	root := workItemRoot(itemID)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", 0, err
	}
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return "", 0, err
	}
	next := 1
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "attempt-") {
			continue
		}
		parsed, err := strconv.Atoi(strings.TrimPrefix(entry.Name(), "attempt-"))
		if err == nil && parsed >= next {
			next = parsed + 1
		}
	}
	return workItemAttemptDir(itemID, next), next, nil
}

func writeWorkItemSourceSnapshot(item workItem) error {
	root := workItemRoot(item.ID)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	return writeGithubJSON(filepath.Join(root, "source.json"), item)
}

func writeWorkItemDraftArtifacts(item workItem, attemptDir string, rawOutput string) error {
	if err := os.MkdirAll(attemptDir, 0o755); err != nil {
		return err
	}
	if err := writeGithubJSON(filepath.Join(attemptDir, "item.json"), item); err != nil {
		return err
	}
	if item.LatestDraft != nil {
		if err := writeGithubJSON(filepath.Join(attemptDir, "draft.json"), item.LatestDraft); err != nil {
			return err
		}
	}
	if strings.TrimSpace(rawOutput) != "" {
		return os.WriteFile(filepath.Join(attemptDir, "raw-output.txt"), []byte(strings.TrimSpace(rawOutput)+"\n"), 0o644)
	}
	return nil
}

var workItemWriteDraftArtifacts = writeWorkItemDraftArtifacts

func writeOptionalWorkItemArtifact(path string, content []byte) {
	if strings.TrimSpace(path) == "" {
		return
	}
	_ = os.WriteFile(path, content, 0o644)
}

func runWorkItemByID(cwd string, itemID string, codexArgs []string, background bool) (workItemExecutionResult, error) {
	item, err := withLocalWorkReadStore(func(store *localWorkDBStore) (workItem, error) {
		return store.readWorkItem(itemID)
	})
	if err != nil {
		return workItemExecutionResult{}, err
	}
	attemptDir, _, err := nextWorkItemAttemptDir(item.ID)
	if err != nil {
		return workItemExecutionResult{}, err
	}
	item, err = withLocalWorkWriteStore(func(store *localWorkDBStore) (workItem, error) {
		return store.startWorkItemAttempt(itemID, attemptDir, ternaryString(background, "auto", "user"))
	})
	if err != nil {
		return workItemExecutionResult{}, err
	}
	result, rawOutput, err := executeWorkItem(cwd, item, attemptDir, codexArgs)
	if err != nil {
		if pauseErr, ok := isCodexRateLimitPauseError(err); ok {
			item.Status = workItemStatusPaused
			item.PauseReason = strings.TrimSpace(pauseErr.Info.Reason)
			item.PauseUntil = strings.TrimSpace(pauseErr.Info.RetryAfter)
			item.Metadata = clearWorkItemPauseMetadata(item.Metadata)
			item.LatestArtifactRoot = attemptDir
			item.UpdatedAt = ISOTimeNow()
			item.LatestActionAt = item.UpdatedAt
			if artifactErr := workItemWriteDraftArtifacts(item, attemptDir, rawOutput); artifactErr != nil {
				return workItemExecutionResult{}, artifactErr
			}
			if _, persistErr := withLocalWorkWriteStore(func(store *localWorkDBStore) (workItem, error) {
				if err := store.updateWorkItemWithEvent(item, "run_paused", "system", map[string]any{
					"reason":      pauseErr.Info.Reason,
					"retry_after": pauseErr.Info.RetryAfter,
					"attempt_dir": attemptDir,
				}); err != nil {
					return workItem{}, err
				}
				return item, nil
			}); persistErr != nil {
				return workItemExecutionResult{}, persistErr
			}
			return workItemExecutionResult{Item: item, Draft: item.LatestDraft, Links: buildDefaultWorkItemLinks(item)}, nil
		}
		item.Status = workItemStatusFailed
		item.PauseReason = ""
		item.PauseUntil = ""
		item.Metadata = clearWorkItemPauseMetadata(item.Metadata)
		item.LatestArtifactRoot = attemptDir
		item.UpdatedAt = ISOTimeNow()
		item.LatestActionAt = item.UpdatedAt
		if artifactErr := workItemWriteDraftArtifacts(item, attemptDir, rawOutput); artifactErr != nil {
			return workItemExecutionResult{}, artifactErr
		}
		if _, persistErr := withLocalWorkWriteStore(func(store *localWorkDBStore) (workItem, error) {
			if err := store.updateWorkItemWithEvent(item, "run_failed", "system", map[string]any{"error": err.Error(), "attempt_dir": attemptDir}); err != nil {
				return workItem{}, err
			}
			return item, nil
		}); persistErr != nil {
			return workItemExecutionResult{}, persistErr
		}
		return workItemExecutionResult{}, err
	}
	item = result.Item
	item.LatestArtifactRoot = attemptDir
	item.LatestActionAt = ISOTimeNow()
	item.UpdatedAt = item.LatestActionAt
	if item.Status != workItemStatusPaused {
		item.PauseReason = ""
		item.PauseUntil = ""
		item.Metadata = clearWorkItemPauseMetadata(item.Metadata)
	}
	if item.LatestDraft != nil && item.Status == "" {
		item.Status = workItemStatusDraftReady
	}
	if shouldSilenceWorkItem(item) {
		item.Status = workItemStatusSilenced
		item.Hidden = true
		item.HiddenReason = defaultString(item.HiddenReason, "ai_ignore_high_confidence")
	}
	if item.Status == "" {
		item.Status = workItemStatusDraftReady
	}
	eventType := "draft_ready"
	if item.Status == workItemStatusSilenced {
		eventType = "silenced"
	}
	if err := workItemWriteDraftArtifacts(item, attemptDir, rawOutput); err != nil {
		return workItemExecutionResult{}, err
	}
	if _, persistErr := withLocalWorkWriteStore(func(store *localWorkDBStore) (workItem, error) {
		if err := store.updateWorkItemWithLinksAndEvent(item, result.Links, eventType, ternaryString(background, "auto", "user"), map[string]any{
			"status":      item.Status,
			"attempt_dir": attemptDir,
			"draft_kind":  valueOrEmptyDraftKind(item.LatestDraft),
		}); err != nil {
			return workItem{}, err
		}
		return item, nil
	}); persistErr != nil {
		return workItemExecutionResult{}, persistErr
	}
	if item.AutoSubmit && !item.Hidden && item.Status == workItemStatusDraftReady {
		submitted, err := submitWorkItemByID(item.ID, "auto")
		if err != nil {
			return workItemExecutionResult{}, err
		}
		item = submitted
	}
	return workItemExecutionResult{Item: item, Draft: item.LatestDraft, Links: result.Links}, nil
}

func executeWorkItem(cwd string, item workItem, attemptDir string, codexArgs []string) (workItemExecutionResult, string, error) {
	switch {
	case item.Source == "github" && item.SourceKind == "review_request":
		return executeGithubReviewRequestWorkItem(item, attemptDir, codexArgs)
	case item.Source == "github" && item.SourceKind == "thread_comment":
		draft, rawOutput, err := draftWorkItemReply(cwd, item, attemptDir, codexArgs, nil, "")
		if err != nil {
			return workItemExecutionResult{}, rawOutput, err
		}
		item.LatestDraft = draft
		item.Status = workItemStatusDraftReady
		return workItemExecutionResult{Item: item, Draft: draft, Links: buildDefaultWorkItemLinks(item)}, rawOutput, nil
	default:
		return executeGenericTaskWorkItem(cwd, item, attemptDir, codexArgs)
	}
}

func executeGenericTaskWorkItem(cwd string, item workItem, attemptDir string, codexArgs []string) (workItemExecutionResult, string, error) {
	taskMode := metadataString(item.Metadata, "task_mode")
	switch {
	case strings.TrimSpace(item.TargetURL) != "":
		target, err := parseGithubTargetURL(item.TargetURL)
		if err != nil {
			return workItemExecutionResult{}, "", err
		}
		run, err := startGithubWork(githubWorkStartOptions{
			Target:           target,
			CreatePR:         true,
			CreatePRExplicit: true,
			RateLimitPolicy:  codexRateLimitPolicyReturnPause,
		})
		if err != nil {
			item.LinkedRunID = run.RunID
			return workItemExecutionResult{}, "", err
		}
		item.LinkedRunID = run.RunID
		item.LatestDraft = &workItemDraft{
			Kind:                 "execution",
			Body:                 fmt.Sprintf("Started Nana work run %s for %s.", run.RunID, item.TargetURL),
			Summary:              fmt.Sprintf("Started work run %s", run.RunID),
			SuggestedDisposition: "needs_review",
			Confidence:           0.99,
			RunID:                run.RunID,
		}
		return workItemExecutionResult{Item: item, Draft: item.LatestDraft, Links: buildDefaultWorkItemLinks(item)}, "", nil
	case metadataString(item.Metadata, "repo_root") != "":
		repoRoot := metadataString(item.Metadata, "repo_root")
		task := strings.TrimSpace(item.Subject)
		if strings.TrimSpace(item.Body) != "" {
			task += "\n\n" + strings.TrimSpace(item.Body)
		}
		if _, err := runLocalWorkCommandWithOptions(cwd, []string{"start", "--repo", repoRoot, "--task", task}, codexRateLimitPolicyReturnPause); err != nil {
			return workItemExecutionResult{}, "", err
		}
		item.LatestDraft = &workItemDraft{
			Kind:                 "execution",
			Body:                 fmt.Sprintf("Started Nana local work for %s.", defaultString(repoRoot, "the requested repo")),
			Summary:              "Started local work run",
			SuggestedDisposition: "needs_review",
			Confidence:           0.95,
		}
		return workItemExecutionResult{Item: item, Draft: item.LatestDraft, Links: buildDefaultWorkItemLinks(item)}, "", nil
	case taskMode == "reply":
		draft, rawOutput, err := draftWorkItemReply(cwd, item, attemptDir, codexArgs, nil, "")
		if err != nil {
			return workItemExecutionResult{}, rawOutput, err
		}
		item.LatestDraft = draft
		return workItemExecutionResult{Item: item, Draft: draft, Links: buildDefaultWorkItemLinks(item)}, rawOutput, nil
	default:
		item.Status = workItemStatusNeedsRouting
		item.LatestDraft = &workItemDraft{
			Kind:                 "execution",
			Body:                 "Nana could not safely route this item to a repo or existing run. Add target_url or metadata.repo_root, or fix the draft with explicit routing instructions.",
			Summary:              "Needs routing",
			SuggestedDisposition: "needs_review",
			Confidence:           0.9,
		}
		return workItemExecutionResult{Item: item, Draft: item.LatestDraft, Links: buildDefaultWorkItemLinks(item)}, "", nil
	}
}

func draftWorkItemReply(cwd string, item workItem, attemptDir string, codexArgs []string, existing *workItemDraft, instruction string) (*workItemDraft, string, error) {
	target, err := workItemCodexTargetForItem(cwd, item)
	if err != nil {
		return nil, "", err
	}
	prompt := buildWorkItemReplyPrompt(item, existing, instruction)
	rawOutput, err := runWorkItemCodexPrompt(target, attemptDir, prompt, codexArgs)
	if err != nil {
		return nil, rawOutput, err
	}
	draft, err := parseWorkItemDraft(rawOutput)
	if err != nil {
		return nil, rawOutput, err
	}
	if strings.TrimSpace(draft.Kind) == "" {
		draft.Kind = "reply"
	}
	return draft, rawOutput, writeGithubJSON(filepath.Join(attemptDir, "prompt.json"), map[string]any{
		"prompt_kind": "reply",
		"instruction": instruction,
	})
}

func buildWorkItemReplyPrompt(item workItem, existing *workItemDraft, instruction string) string {
	lines := []string{
		"Draft a response for this Nana work item. Return JSON only.",
		`Schema: {"kind":"reply|execution","body":"...","summary":"...","suggested_disposition":"submit|needs_review|ignore","confidence":0.0}`,
		fmt.Sprintf("Item id: %s", item.ID),
		fmt.Sprintf("Source: %s/%s", item.Source, item.SourceKind),
	}
	if repoSlug := strings.TrimSpace(item.RepoSlug); repoSlug != "" {
		lines = append(lines, fmt.Sprintf("Repo: %s", repoSlug))
	}
	if targetURL := strings.TrimSpace(item.TargetURL); targetURL != "" {
		lines = append(lines, fmt.Sprintf("Target URL: %s", targetURL))
	}
	if subject := compactPromptValue(item.Subject, 0, 200); subject != "" {
		lines = append(lines, fmt.Sprintf("Subject: %s", subject))
	}
	if body := compactPromptValue(item.Body, 80, 6000); body != "" {
		lines = append(lines, "Body:", body)
	}
	if strings.TrimSpace(item.Author) != "" {
		lines = append(lines, fmt.Sprintf("Author: %s", item.Author))
	}
	if strings.TrimSpace(item.LinkedRunID) != "" {
		lines = append(lines, fmt.Sprintf("Linked run: %s", item.LinkedRunID))
	}
	if existing != nil {
		lines = append(lines,
			"",
			"Current draft:",
			defaultString(compactPromptValue(existing.Body, 40, 3000), "(empty)"),
		)
	}
	if strings.TrimSpace(instruction) != "" {
		lines = append(lines,
			"",
			"Apply this fix instruction:",
			compactPromptValue(instruction, 40, 3000),
		)
	}
	lines = append(lines,
		"",
		"Write a concise, user-facing result.",
		"Use suggested_disposition=ignore only when the item is clearly not worth attention.",
	)
	return strings.Join(lines, "\n") + "\n"
}

func parseWorkItemDraft(raw string) (*workItemDraft, error) {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("work item output did not contain JSON object")
	}
	var draft workItemDraft
	if err := json.Unmarshal([]byte(raw[start:end+1]), &draft); err != nil {
		return nil, err
	}
	if strings.TrimSpace(draft.Body) == "" && strings.TrimSpace(draft.Summary) == "" {
		return nil, fmt.Errorf("work item draft was empty")
	}
	if draft.Confidence < 0 {
		draft.Confidence = 0
	}
	if draft.Confidence > 1 {
		draft.Confidence = 1
	}
	return &draft, nil
}

var workItemRunManagedPrompt = runManagedCodexPrompt

func runWorkItemCodexPrompt(target workItemCodexTarget, attemptDir string, prompt string, codexArgs []string) (string, error) {
	repoPath := strings.TrimSpace(target.RepoPath)
	if repoPath == "" {
		return "", fmt.Errorf("work item Codex repo path is required")
	}
	lockOwner := repoAccessLockOwner{
		Backend: "work-item",
		RunID:   sanitizePathToken(filepath.Base(attemptDir)),
		Purpose: "draft-reply",
		Label:   "work-item-draft",
	}
	lockWith := withSourceReadLock
	if strings.TrimSpace(target.LockKind) == workItemCodexLockSandbox {
		lockWith = withSandboxReadLock
	}
	output := ""
	err := lockWith(repoPath, lockOwner, func() error {
		codexHome, err := ensureScopedCodexHome(ResolveCodexHomeForLaunch(repoPath), filepath.Join(workItemsRoot(), "_codex-home", sanitizePathToken(filepath.Base(repoPath))))
		if err != nil {
			return err
		}
		normalizedCodexArgs, fastMode := NormalizeCodexLaunchArgsWithFast(codexArgs)
		prompt = prefixCodexFastPrompt(prompt, fastMode)
		transport := promptTransportForSize(prompt, structuredPromptStdinThreshold)
		result, err := workItemRunManagedPrompt(codexManagedPromptOptions{
			CommandDir:       repoPath,
			InstructionsRoot: repoPath,
			CodexHome:        codexHome,
			FreshArgsPrefix:  []string{"exec", "-C", repoPath},
			CommonArgs:       normalizedCodexArgs,
			Prompt:           prompt,
			PromptTransport:  transport,
			CheckpointPath:   filepath.Join(attemptDir, "reply-checkpoint.json"),
			StepKey:          "work-item-reply",
			ResumeStrategy:   codexResumeSamePrompt,
			Env: append(buildGithubCodexEnv(NotifyTempContract{}, codexHome, strings.TrimSpace(os.Getenv("GITHUB_API_URL"))),
				"NANA_PROJECT_AGENTS_ROOT="+repoPath,
			),
			RateLimitPolicy: codexRateLimitPolicyReturnPause,
		})
		output = strings.TrimSpace(result.Stdout)
		if err != nil {
			if _, ok := isCodexRateLimitPauseError(err); ok {
				return err
			}
			return fmt.Errorf("%v\n%s", err, result.Stderr)
		}
		return nil
	})
	return output, err
}

func workItemCodexTargetForItem(cwd string, item workItem) (workItemCodexTarget, error) {
	if strings.TrimSpace(item.LinkedRunID) != "" {
		if strings.HasPrefix(item.LinkedRunID, "gh-") {
			manifest, _, err := resolveGithubWorkRun(localWorkRunSelection{RunID: item.LinkedRunID})
			if err == nil && strings.TrimSpace(manifest.SandboxRepoPath) != "" {
				return workItemCodexTarget{RepoPath: manifest.SandboxRepoPath, LockKind: workItemCodexLockSandbox}, nil
			}
		}
		if strings.HasPrefix(item.LinkedRunID, "lw-") {
			manifest, _, err := resolveLocalWorkRun(cwd, localWorkRunSelection{RunID: item.LinkedRunID})
			if err == nil && strings.TrimSpace(manifest.SandboxRepoPath) != "" {
				return workItemCodexTarget{RepoPath: manifest.SandboxRepoPath, LockKind: workItemCodexLockSandbox}, nil
			}
		}
	}
	if strings.TrimSpace(item.RepoSlug) != "" {
		repoPath := githubManagedPaths(item.RepoSlug).SourcePath
		if info, err := os.Stat(repoPath); err == nil && info.IsDir() {
			return workItemCodexTarget{RepoPath: repoPath, LockKind: workItemCodexLockSource}, nil
		}
	}
	if repoRoot := metadataString(item.Metadata, "repo_root"); repoRoot != "" {
		return workItemCodexTarget{RepoPath: repoRoot, LockKind: workItemCodexLockSource}, nil
	}
	if cwd == "" {
		cwd = "."
	}
	return workItemCodexTarget{RepoPath: cwd, LockKind: workItemCodexLockSource}, nil
}

func submitWorkItemByID(itemID string, actor string) (workItem, error) {
	item, err := withLocalWorkReadStore(func(store *localWorkDBStore) (workItem, error) {
		return store.readWorkItem(itemID)
	})
	if err != nil {
		return workItem{}, err
	}
	if item.LatestDraft == nil {
		return workItem{}, fmt.Errorf("work item %s does not have a draft to submit", itemID)
	}
	switch {
	case item.Source == "github" && item.SourceKind == "review_request":
		if err := submitGithubReviewRequestDraft(item); err != nil {
			return workItem{}, err
		}
	case item.Source == "github" && item.SourceKind == "thread_comment":
		if err := submitGithubThreadCommentDraft(item); err != nil {
			return workItem{}, err
		}
	case item.SubmitProfile != nil && strings.EqualFold(strings.TrimSpace(item.SubmitProfile.Type), "shell"):
		if err := submitWorkItemViaShell(item); err != nil {
			return workItem{}, err
		}
	}
	item.Status = workItemStatusSubmitted
	item.Hidden = false
	item.HiddenReason = ""
	item.UpdatedAt = ISOTimeNow()
	item.LatestActionAt = item.UpdatedAt
	if _, err := withLocalWorkWriteStore(func(store *localWorkDBStore) (workItem, error) {
		if err := store.updateWorkItemWithEvent(item, "submitted", actor, map[string]any{
			"draft_kind": valueOrEmptyDraftKind(item.LatestDraft),
		}); err != nil {
			return workItem{}, err
		}
		return item, nil
	}); err != nil {
		return workItem{}, err
	}
	return item, nil
}

func fixWorkItemByID(cwd string, options workItemFixCommandOptions) (workItem, error) {
	item, err := withLocalWorkReadStore(func(store *localWorkDBStore) (workItem, error) {
		return store.readWorkItem(options.ItemID)
	})
	if err != nil {
		return workItem{}, err
	}
	if item.LatestDraft == nil {
		return workItem{}, fmt.Errorf("work item %s has no existing draft to fix", item.ID)
	}
	attemptDir, _, err := nextWorkItemAttemptDir(item.ID)
	if err != nil {
		return workItem{}, err
	}
	draft, rawOutput, err := draftWorkItemReply(cwd, item, attemptDir, options.CodexArgs, item.LatestDraft, options.Instruction)
	if err != nil {
		if pauseErr, ok := isCodexRateLimitPauseError(err); ok {
			item.Status = workItemStatusPaused
			item.PauseReason = strings.TrimSpace(pauseErr.Info.Reason)
			item.PauseUntil = strings.TrimSpace(pauseErr.Info.RetryAfter)
			item.Metadata = clearWorkItemPauseMetadata(item.Metadata)
			item.LatestArtifactRoot = attemptDir
			item.UpdatedAt = ISOTimeNow()
			item.LatestActionAt = item.UpdatedAt
			if artifactErr := workItemWriteDraftArtifacts(item, attemptDir, rawOutput); artifactErr != nil {
				return workItem{}, artifactErr
			}
			if _, persistErr := withLocalWorkWriteStore(func(store *localWorkDBStore) (workItem, error) {
				return item, store.updateWorkItem(item)
			}); persistErr != nil {
				return workItem{}, persistErr
			}
			return item, nil
		}
		item.Status = workItemStatusFailed
		item.PauseReason = ""
		item.PauseUntil = ""
		item.Metadata = clearWorkItemPauseMetadata(item.Metadata)
		item.LatestArtifactRoot = attemptDir
		item.UpdatedAt = ISOTimeNow()
		item.LatestActionAt = item.UpdatedAt
		if artifactErr := workItemWriteDraftArtifacts(item, attemptDir, rawOutput); artifactErr != nil {
			return workItem{}, artifactErr
		}
		if _, persistErr := withLocalWorkWriteStore(func(store *localWorkDBStore) (workItem, error) {
			return item, store.updateWorkItem(item)
		}); persistErr != nil {
			return workItem{}, persistErr
		}
		return workItem{}, err
	}
	item.LatestDraft = draft
	item.Status = workItemStatusDraftReady
	item.PauseReason = ""
	item.PauseUntil = ""
	item.Metadata = clearWorkItemPauseMetadata(item.Metadata)
	item.Hidden = false
	item.HiddenReason = ""
	item.LatestArtifactRoot = attemptDir
	item.LatestActionAt = ISOTimeNow()
	item.UpdatedAt = item.LatestActionAt
	if shouldSilenceWorkItem(item) {
		item.Status = workItemStatusSilenced
		item.Hidden = true
		item.HiddenReason = defaultString(item.HiddenReason, "ai_ignore_high_confidence")
	}
	if err := workItemWriteDraftArtifacts(item, attemptDir, rawOutput); err != nil {
		return workItem{}, err
	}
	if _, err := withLocalWorkWriteStore(func(store *localWorkDBStore) (workItem, error) {
		if err := store.updateWorkItemWithEvent(item, "draft_fixed", "user", map[string]any{
			"instruction": options.Instruction,
			"attempt_dir": attemptDir,
		}); err != nil {
			return workItem{}, err
		}
		return item, nil
	}); err != nil {
		return workItem{}, err
	}
	return item, nil
}

func dropWorkItemByID(itemID string, actor string) error {
	return withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		item, err := store.readWorkItem(itemID)
		if err != nil {
			return err
		}
		item.UpdatedAt = ISOTimeNow()
		item.LatestActionAt = item.UpdatedAt
		if shouldSilenceWorkItem(item) {
			item.Status = workItemStatusSilenced
			item.Hidden = true
			item.HiddenReason = defaultString(item.HiddenReason, "ai_ignore_high_confidence")
		} else {
			item.Status = workItemStatusDropped
			item.Hidden = false
			item.HiddenReason = ""
		}
		return store.updateWorkItemWithEvent(item, "dropped", actor, map[string]any{"status": item.Status})
	})
}

func restoreWorkItemByID(itemID string, actor string) error {
	return withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		item, err := store.readWorkItem(itemID)
		if err != nil {
			return err
		}
		item.Hidden = false
		item.HiddenReason = ""
		if item.LatestDraft != nil {
			item.Status = workItemStatusDraftReady
		} else {
			item.Status = workItemStatusQueued
		}
		item.UpdatedAt = ISOTimeNow()
		item.LatestActionAt = item.UpdatedAt
		return store.updateWorkItemWithEvent(item, "restored", actor, map[string]any{"status": item.Status})
	})
}

func dispatchQueuedWorkItems(repoSlug string, codexArgs []string) (int, error) {
	items, err := withLocalWorkReadStore(func(store *localWorkDBStore) ([]workItem, error) {
		return store.listAutoRunnableWorkItems(repoSlug, 10)
	})
	if err != nil {
		return 0, err
	}
	started := 0
	for _, item := range items {
		if _, err := runWorkItemByID("", item.ID, codexArgs, true); err != nil {
			return started, err
		}
		started++
	}
	return started, nil
}

func shouldSilenceWorkItem(item workItem) bool {
	if item.LatestDraft == nil {
		return false
	}
	disposition := strings.ToLower(strings.TrimSpace(item.LatestDraft.SuggestedDisposition))
	switch disposition {
	case "ignore", "silence":
		return item.LatestDraft.Confidence >= 0.9
	default:
		return false
	}
}

func valueOrEmptyDraftKind(draft *workItemDraft) string {
	if draft == nil {
		return ""
	}
	return draft.Kind
}

func ternaryString(condition bool, left string, right string) string {
	if condition {
		return left
	}
	return right
}

func submitWorkItemViaShell(item workItem) error {
	if item.SubmitProfile == nil || strings.TrimSpace(item.SubmitProfile.Command) == "" {
		return fmt.Errorf("work item %s does not have a shell submit command", item.ID)
	}
	itemJSONPath := filepath.Join(workItemRoot(item.ID), "source.json")
	draftJSONPath := ""
	if strings.TrimSpace(item.LatestArtifactRoot) != "" {
		draftJSONPath = filepath.Join(item.LatestArtifactRoot, "draft.json")
	}
	cmd := exec.Command("sh", "-lc", item.SubmitProfile.Command)
	cmd.Env = append(os.Environ(),
		"NANA_WORK_ITEM_ID="+item.ID,
		"NANA_WORK_ITEM_SOURCE="+item.Source,
		"NANA_WORK_ITEM_SOURCE_KIND="+item.SourceKind,
		"NANA_WORK_ITEM_REPO_SLUG="+item.RepoSlug,
		"NANA_WORK_ITEM_TARGET_URL="+item.TargetURL,
		"NANA_WORK_ITEM_SUBJECT="+item.Subject,
		"NANA_WORK_ITEM_BODY="+safeDraftBody(item.LatestDraft),
		"NANA_WORK_ITEM_SUMMARY="+safeDraftSummary(item.LatestDraft),
		"NANA_WORK_ITEM_JSON="+itemJSONPath,
		"NANA_WORK_ITEM_DRAFT_JSON="+draftJSONPath,
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v\n%s%s", err, stdout.String(), stderr.String())
	}
	if strings.TrimSpace(item.LatestArtifactRoot) != "" {
		writeOptionalWorkItemArtifact(filepath.Join(item.LatestArtifactRoot, "submit-shell.stdout.log"), stdout.Bytes())
	}
	return nil
}

func safeDraftBody(draft *workItemDraft) string {
	if draft == nil {
		return ""
	}
	return draft.Body
}

func safeDraftSummary(draft *workItemDraft) string {
	if draft == nil {
		return ""
	}
	return draft.Summary
}

func workItemPausePending(item workItem, now time.Time) bool {
	workItemPauseFields(&item)
	retryAt, ok := parseManagedAuthTime(item.PauseUntil)
	return ok && retryAt.After(now)
}

func (s *localWorkDBStore) listWorkItems(options workItemListOptions) ([]workItem, error) {
	limit := options.Limit
	if limit <= 0 {
		limit = 20
	}
	clauses := []string{"1=1"}
	args := []any{}
	if strings.TrimSpace(options.Status) != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, strings.TrimSpace(options.Status))
	}
	if strings.TrimSpace(options.RepoSlug) != "" {
		clauses = append(clauses, "repo_slug = ?")
		args = append(args, strings.TrimSpace(options.RepoSlug))
	}
	if options.OnlyHidden {
		clauses = append(clauses, "hidden = 1")
	} else if !options.IncludeHidden {
		clauses = append(clauses, "hidden = 0")
	}
	query := `SELECT id, dedupe_key, source, source_kind, external_id, thread_key, repo_slug, target_url, linked_run_id, subject, body, author, received_at, status, priority, auto_run, auto_submit, hidden, hidden_reason, submit_profile_json, metadata_json, latest_draft_json, latest_artifact_root, latest_action_at, pause_reason, pause_until, created_at, updated_at FROM work_items WHERE ` + strings.Join(clauses, " AND ") + ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []workItem{}
	for rows.Next() {
		item, err := scanWorkItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *localWorkDBStore) listAutoRunnableWorkItems(repoSlug string, limit int) ([]workItem, error) {
	if limit <= 0 {
		limit = 10
	}
	args := []any{}
	query := `SELECT id, dedupe_key, source, source_kind, external_id, thread_key, repo_slug, target_url, linked_run_id, subject, body, author, received_at, status, priority, auto_run, auto_submit, hidden, hidden_reason, submit_profile_json, metadata_json, latest_draft_json, latest_artifact_root, latest_action_at, pause_reason, pause_until, created_at, updated_at FROM work_items WHERE auto_run = 1 AND hidden = 0 AND status IN (?, ?)`
	args = append(args, workItemStatusQueued, workItemStatusPaused)
	if strings.TrimSpace(repoSlug) != "" {
		query += ` AND repo_slug = ?`
		args = append(args, strings.TrimSpace(repoSlug))
	}
	query += ` ORDER BY priority ASC, updated_at ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []workItem{}
	for rows.Next() {
		item, err := scanWorkItem(rows)
		if err != nil {
			return nil, err
		}
		if item.Status == workItemStatusPaused && workItemPausePending(item, time.Now().UTC()) {
			continue
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *localWorkDBStore) readWorkItem(itemID string) (workItem, error) {
	row := s.db.QueryRow(`SELECT id, dedupe_key, source, source_kind, external_id, thread_key, repo_slug, target_url, linked_run_id, subject, body, author, received_at, status, priority, auto_run, auto_submit, hidden, hidden_reason, submit_profile_json, metadata_json, latest_draft_json, latest_artifact_root, latest_action_at, pause_reason, pause_until, created_at, updated_at FROM work_items WHERE id = ?`, itemID)
	item, err := scanWorkItem(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return workItem{}, fmt.Errorf("work item %s was not found", itemID)
		}
		return workItem{}, err
	}
	return item, nil
}

func (s *localWorkDBStore) findLinkedRunID(repoSlug string, targetURL string) (string, error) {
	rows, err := s.db.Query(`SELECT run_id, backend, repo_key, repo_root, repo_name, repo_slug, manifest_path, updated_at, target_kind FROM work_run_index WHERE backend = 'github' AND repo_key = ? ORDER BY updated_at DESC LIMIT 20`, nullableString(repoSlug))
	if err != nil {
		return "", err
	}
	defer rows.Close()
	targetURL = strings.TrimSpace(targetURL)
	for rows.Next() {
		entry, err := scanWorkRunIndexEntry(rows)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(entry.ManifestPath) == "" {
			continue
		}
		manifest, err := readGithubWorkManifest(entry.ManifestPath)
		if err != nil {
			continue
		}
		switch targetURL {
		case strings.TrimSpace(manifest.TargetURL), strings.TrimSpace(manifest.PublishedPRURL):
			return manifest.RunID, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "", sql.ErrNoRows
}

func (s *localWorkDBStore) upsertWorkItem(item workItem, actor string, payload map[string]any) (workItem, bool, error) {
	return s.upsertWorkItemWithLinks(item, actor, payload, nil)
}

func (s *localWorkDBStore) upsertWorkItemWithLinks(item workItem, actor string, payload map[string]any, links []workItemLink) (workItem, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return workItem{}, false, err
	}
	defer tx.Rollback()
	var existingID string
	row := tx.QueryRow(`SELECT id FROM work_items WHERE dedupe_key = ?`, item.DedupeKey)
	err = row.Scan(&existingID)
	created := errors.Is(err, sql.ErrNoRows)
	if err != nil && !created {
		return workItem{}, false, err
	}
	if !created {
		existing, err := readWorkItemTx(tx, existingID)
		if err != nil {
			return workItem{}, false, err
		}
		item.ID = existing.ID
		item.CreatedAt = existing.CreatedAt
		item.Status = existing.Status
		item.Hidden = existing.Hidden
		item.HiddenReason = existing.HiddenReason
		item.LatestDraft = existing.LatestDraft
		item.LatestArtifactRoot = existing.LatestArtifactRoot
		item.LatestActionAt = existing.LatestActionAt
	}
	for index := range links {
		links[index].ItemID = item.ID
	}
	if err := writeWorkItemTx(tx, item); err != nil {
		return workItem{}, false, err
	}
	if err := appendWorkItemEventTx(tx, item.ID, ternaryString(created, "ingested", "refreshed"), actor, payload); err != nil {
		return workItem{}, false, err
	}
	if links != nil {
		if err := replaceWorkItemLinksTx(tx, item.ID, links); err != nil {
			return workItem{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return workItem{}, false, err
	}
	return item, created, nil
}

func (s *localWorkDBStore) updateWorkItem(item workItem) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := writeWorkItemTx(tx, item); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *localWorkDBStore) appendWorkItemEvent(itemID string, eventType string, actor string, payload map[string]any) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := appendWorkItemEventTx(tx, itemID, eventType, actor, payload); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *localWorkDBStore) readWorkItemEvents(itemID string) ([]workItemEvent, error) {
	rows, err := s.db.Query(`SELECT id, item_id, created_at, event_type, actor, payload_json FROM work_item_events WHERE item_id = ? ORDER BY created_at DESC, id DESC`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []workItemEvent{}
	for rows.Next() {
		var event workItemEvent
		var actor sql.NullString
		var payloadRaw string
		if err := rows.Scan(&event.ID, &event.ItemID, &event.CreatedAt, &event.EventType, &actor, &payloadRaw); err != nil {
			return nil, err
		}
		event.Actor = actor.String
		if strings.TrimSpace(payloadRaw) != "" {
			_ = json.Unmarshal([]byte(payloadRaw), &event.Payload)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *localWorkDBStore) replaceWorkItemLinks(itemID string, links []workItemLink) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := replaceWorkItemLinksTx(tx, itemID, links); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *localWorkDBStore) updateWorkItemWithEvent(item workItem, eventType string, actor string, payload map[string]any) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := writeWorkItemTx(tx, item); err != nil {
		return err
	}
	if err := appendWorkItemEventTx(tx, item.ID, eventType, actor, payload); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *localWorkDBStore) updateWorkItemWithLinksAndEvent(item workItem, links []workItemLink, eventType string, actor string, payload map[string]any) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := writeWorkItemTx(tx, item); err != nil {
		return err
	}
	if err := replaceWorkItemLinksTx(tx, item.ID, links); err != nil {
		return err
	}
	if err := appendWorkItemEventTx(tx, item.ID, eventType, actor, payload); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *localWorkDBStore) startWorkItemAttempt(itemID string, attemptDir string, actor string) (workItem, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return workItem{}, err
	}
	defer tx.Rollback()
	item, err := readWorkItemTx(tx, itemID)
	if err != nil {
		return workItem{}, err
	}
	item.Status = workItemStatusRunning
	item.Hidden = false
	item.HiddenReason = ""
	item.LatestActionAt = ISOTimeNow()
	item.UpdatedAt = item.LatestActionAt
	if err := writeWorkItemTx(tx, item); err != nil {
		return workItem{}, err
	}
	if err := appendWorkItemEventTx(tx, item.ID, "run_started", actor, map[string]any{"attempt_dir": attemptDir}); err != nil {
		return workItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return workItem{}, err
	}
	return item, nil
}

func (s *localWorkDBStore) readWorkItemLinks(itemID string) ([]workItemLink, error) {
	rows, err := s.db.Query(`SELECT item_id, link_type, target_id, metadata_json FROM work_item_links WHERE item_id = ? ORDER BY link_type, target_id`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	links := []workItemLink{}
	for rows.Next() {
		var link workItemLink
		var metadataRaw string
		if err := rows.Scan(&link.ItemID, &link.LinkType, &link.TargetID, &metadataRaw); err != nil {
			return nil, err
		}
		if strings.TrimSpace(metadataRaw) != "" {
			_ = json.Unmarshal([]byte(metadataRaw), &link.Metadata)
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

func writeWorkItemTx(tx *sql.Tx, item workItem) error {
	workItemPauseFields(&item)
	item.Metadata = clearWorkItemPauseMetadata(item.Metadata)
	submitProfileJSON, err := marshalNullableJSON(item.SubmitProfile)
	if err != nil {
		return err
	}
	metadataJSON, err := marshalNullableJSON(item.Metadata)
	if err != nil {
		return err
	}
	draftJSON, err := marshalNullableJSON(item.LatestDraft)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT INTO work_items(id, dedupe_key, source, source_kind, external_id, thread_key, repo_slug, target_url, linked_run_id, subject, body, author, received_at, status, priority, auto_run, auto_submit, hidden, hidden_reason, submit_profile_json, metadata_json, latest_draft_json, latest_artifact_root, latest_action_at, pause_reason, pause_until, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   dedupe_key=excluded.dedupe_key,
		   source=excluded.source,
		   source_kind=excluded.source_kind,
		   external_id=excluded.external_id,
		   thread_key=excluded.thread_key,
		   repo_slug=excluded.repo_slug,
		   target_url=excluded.target_url,
		   linked_run_id=excluded.linked_run_id,
		   subject=excluded.subject,
		   body=excluded.body,
		   author=excluded.author,
		   received_at=excluded.received_at,
		   status=excluded.status,
		   priority=excluded.priority,
		   auto_run=excluded.auto_run,
		   auto_submit=excluded.auto_submit,
		   hidden=excluded.hidden,
		   hidden_reason=excluded.hidden_reason,
		   submit_profile_json=excluded.submit_profile_json,
		   metadata_json=excluded.metadata_json,
		   latest_draft_json=excluded.latest_draft_json,
		   latest_artifact_root=excluded.latest_artifact_root,
		   latest_action_at=excluded.latest_action_at,
		   pause_reason=excluded.pause_reason,
		   pause_until=excluded.pause_until,
		   created_at=excluded.created_at,
		   updated_at=excluded.updated_at`,
		item.ID,
		item.DedupeKey,
		item.Source,
		item.SourceKind,
		item.ExternalID,
		nullableString(item.ThreadKey),
		nullableString(item.RepoSlug),
		nullableString(item.TargetURL),
		nullableString(item.LinkedRunID),
		item.Subject,
		nullableString(item.Body),
		nullableString(item.Author),
		item.ReceivedAt,
		item.Status,
		defaultInt(item.Priority, 3),
		boolToInt(item.AutoRun),
		boolToInt(item.AutoSubmit),
		boolToInt(item.Hidden),
		nullableString(item.HiddenReason),
		submitProfileJSON,
		metadataJSON,
		draftJSON,
		nullableString(item.LatestArtifactRoot),
		nullableString(item.LatestActionAt),
		nullableString(item.PauseReason),
		nullableString(item.PauseUntil),
		item.CreatedAt,
		item.UpdatedAt,
	)
	return err
}

func appendWorkItemEventTx(tx *sql.Tx, itemID string, eventType string, actor string, payload map[string]any) error {
	payloadJSON, err := marshalNullableJSON(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT INTO work_item_events(item_id, created_at, event_type, actor, payload_json) VALUES(?, ?, ?, ?, ?)`,
		itemID,
		ISOTimeNow(),
		eventType,
		nullableString(actor),
		defaultString(payloadJSON, "{}"),
	)
	return err
}

func writeWorkItemLinkTx(tx *sql.Tx, link workItemLink) error {
	metadataJSON, err := marshalNullableJSON(link.Metadata)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO work_item_links(item_id, link_type, target_id, metadata_json) VALUES(?, ?, ?, ?)`,
		link.ItemID,
		link.LinkType,
		link.TargetID,
		defaultString(metadataJSON, "{}"),
	)
	return err
}

func replaceWorkItemLinksTx(tx *sql.Tx, itemID string, links []workItemLink) error {
	if _, err := tx.Exec(`DELETE FROM work_item_links WHERE item_id = ?`, itemID); err != nil {
		return err
	}
	for _, link := range links {
		if err := writeWorkItemLinkTx(tx, link); err != nil {
			return err
		}
	}
	return nil
}

func marshalNullableJSON(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	content, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if string(content) == "null" {
		return "", nil
	}
	return string(content), nil
}

type workItemScanner interface {
	Scan(dest ...interface{}) error
}

func scanWorkItem(row workItemScanner) (workItem, error) {
	var item workItem
	var threadKey, repoSlug, targetURL, linkedRunID, body, author, hiddenReason sql.NullString
	var submitProfileRaw, metadataRaw, latestDraftRaw, latestArtifactRoot, latestActionAt, pauseReason, pauseUntil sql.NullString
	var autoRun, autoSubmit, hidden int
	if err := row.Scan(
		&item.ID,
		&item.DedupeKey,
		&item.Source,
		&item.SourceKind,
		&item.ExternalID,
		&threadKey,
		&repoSlug,
		&targetURL,
		&linkedRunID,
		&item.Subject,
		&body,
		&author,
		&item.ReceivedAt,
		&item.Status,
		&item.Priority,
		&autoRun,
		&autoSubmit,
		&hidden,
		&hiddenReason,
		&submitProfileRaw,
		&metadataRaw,
		&latestDraftRaw,
		&latestArtifactRoot,
		&latestActionAt,
		&pauseReason,
		&pauseUntil,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return workItem{}, err
	}
	item.ThreadKey = threadKey.String
	item.RepoSlug = repoSlug.String
	item.TargetURL = targetURL.String
	item.LinkedRunID = linkedRunID.String
	item.Body = body.String
	item.Author = author.String
	item.AutoRun = autoRun == 1
	item.AutoSubmit = autoSubmit == 1
	item.Hidden = hidden == 1
	item.HiddenReason = hiddenReason.String
	item.LatestArtifactRoot = latestArtifactRoot.String
	item.LatestActionAt = latestActionAt.String
	item.PauseReason = pauseReason.String
	item.PauseUntil = pauseUntil.String
	if strings.TrimSpace(submitProfileRaw.String) != "" {
		var profile workItemSubmitProfile
		if err := json.Unmarshal([]byte(submitProfileRaw.String), &profile); err == nil {
			item.SubmitProfile = &profile
		}
	}
	if strings.TrimSpace(metadataRaw.String) != "" {
		_ = json.Unmarshal([]byte(metadataRaw.String), &item.Metadata)
	}
	if strings.TrimSpace(latestDraftRaw.String) != "" {
		var draft workItemDraft
		if err := json.Unmarshal([]byte(latestDraftRaw.String), &draft); err == nil {
			item.LatestDraft = &draft
		}
	}
	workItemPauseFields(&item)
	return item, nil
}

func readWorkItemTx(tx *sql.Tx, itemID string) (workItem, error) {
	row := tx.QueryRow(`SELECT id, dedupe_key, source, source_kind, external_id, thread_key, repo_slug, target_url, linked_run_id, subject, body, author, received_at, status, priority, auto_run, auto_submit, hidden, hidden_reason, submit_profile_json, metadata_json, latest_draft_json, latest_artifact_root, latest_action_at, pause_reason, pause_until, created_at, updated_at FROM work_items WHERE id = ?`, itemID)
	item, err := scanWorkItem(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return workItem{}, fmt.Errorf("work item %s was not found", itemID)
		}
		return workItem{}, err
	}
	return item, nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
