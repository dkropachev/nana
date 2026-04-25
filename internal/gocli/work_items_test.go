package gocli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBuildWorkItemReplyPromptOmitsEmptyDefaultsAndCompactsBody(t *testing.T) {
	prompt := buildWorkItemReplyPrompt(workItem{
		ID:         "wi-1",
		Source:     "email",
		SourceKind: "task",
		Subject:    "Reply to this thread",
		Body:       strings.Repeat("body\n", 2000),
	}, nil, "")
	if strings.Contains(prompt, "Repo: (none)") || strings.Contains(prompt, "Target URL: (none)") {
		t.Fatalf("expected empty default fields to be omitted:\n%s", prompt)
	}
	if !strings.Contains(prompt, "... [truncated]") {
		t.Fatalf("expected large body to be truncated:\n%s", prompt)
	}
}

func TestWorkItemDropSilencesAndRestoreRequeues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	item, created, err := enqueueWorkItem(workItemInput{
		Source:     "email",
		SourceKind: "task",
		ExternalID: "mail-1",
		Subject:    "Triage this note",
		Body:       "This is only FYI.",
		Metadata: map[string]any{
			"task_mode": "reply",
		},
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}
	if !created {
		t.Fatalf("expected created item")
	}

	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	item.LatestDraft = &workItemDraft{
		Kind:                 "reply",
		Body:                 "No response needed.",
		Summary:              "Ignore this item.",
		SuggestedDisposition: "ignore",
		Confidence:           0.95,
	}
	item.Status = workItemStatusDraftReady
	if err := store.updateWorkItem(item); err != nil {
		t.Fatalf("updateWorkItem: %v", err)
	}
	store.Close()

	if err := dropWorkItemByID(item.ID, "test"); err != nil {
		t.Fatalf("dropWorkItemByID: %v", err)
	}
	detail, err := readWorkItemDetail(item.ID)
	if err != nil {
		t.Fatalf("readWorkItemDetail after drop: %v", err)
	}
	if detail.Item.Status != workItemStatusSilenced || !detail.Item.Hidden {
		t.Fatalf("expected silenced hidden item, got %+v", detail.Item)
	}

	visible, err := listWorkItems(workItemListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("listWorkItems visible: %v", err)
	}
	if len(visible) != 0 {
		t.Fatalf("expected hidden item to be excluded, got %+v", visible)
	}

	hidden, err := listWorkItems(workItemListOptions{Limit: 10, OnlyHidden: true, IncludeHidden: true})
	if err != nil {
		t.Fatalf("listWorkItems hidden: %v", err)
	}
	if len(hidden) != 1 || hidden[0].ID != item.ID {
		t.Fatalf("expected hidden item in hidden view, got %+v", hidden)
	}

	if err := restoreWorkItemByID(item.ID, "test"); err != nil {
		t.Fatalf("restoreWorkItemByID: %v", err)
	}
	restored, err := readWorkItemDetail(item.ID)
	if err != nil {
		t.Fatalf("readWorkItemDetail after restore: %v", err)
	}
	if restored.Item.Status != workItemStatusDraftReady || restored.Item.Hidden {
		t.Fatalf("expected restored draft-ready item, got %+v", restored.Item)
	}
}

func TestSyncGithubWorkItemsQueuesReviewRequestAndComment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "test-token")

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notifications":
			threads := []map[string]any{
				{
					"id":         "100",
					"reason":     "review_requested",
					"updated_at": "2026-04-13T10:00:00Z",
					"subject": map[string]any{
						"title": "Refine work items",
						"url":   server.URL + "/repos/acme/widget/pulls/7",
						"type":  "PullRequest",
					},
					"repository": map[string]any{"full_name": "acme/widget", "name": "widget", "owner": map[string]any{"login": "acme"}},
				},
				{
					"id":         "101",
					"reason":     "comment",
					"updated_at": "2026-04-13T11:00:00Z",
					"subject": map[string]any{
						"title":              "Refine work items",
						"url":                server.URL + "/repos/acme/widget/pulls/7",
						"latest_comment_url": server.URL + "/repos/acme/widget/issues/comments/11",
						"type":               "PullRequest",
					},
					"repository": map[string]any{"full_name": "acme/widget", "name": "widget", "owner": map[string]any{"login": "acme"}},
				},
			}
			_ = json.NewEncoder(w).Encode(threads)
		case "/repos/acme/widget/issues/comments/11":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":         11,
				"html_url":   "https://github.com/acme/widget/issues/7#issuecomment-11",
				"body":       "Please answer in the thread.",
				"updated_at": "2026-04-13T11:00:00Z",
				"user":       map[string]any{"login": "reviewer-a"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("GITHUB_API_URL", server.URL)

	result, err := syncGithubWorkItems(workItemSyncCommandOptions{Limit: 20})
	if err != nil {
		t.Fatalf("syncGithubWorkItems: %v", err)
	}
	if result.Queued != 2 || result.Created != 2 {
		t.Fatalf("unexpected sync result: %+v", result)
	}

	items, err := listWorkItems(workItemListOptions{Limit: 10, IncludeHidden: true})
	if err != nil {
		t.Fatalf("listWorkItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %+v", items)
	}
	kinds := []string{items[0].SourceKind, items[1].SourceKind}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, "review_request") || !strings.Contains(joined, "thread_comment") {
		t.Fatalf("expected review_request and thread_comment, got %+v", kinds)
	}
	for _, item := range items {
		if item.TargetURL != "https://github.com/acme/widget/pull/7" {
			t.Fatalf("unexpected target url for %+v", item)
		}
	}
}

func TestSubmitWorkItemViaShellUsesDraftEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	outputPath := filepath.Join(home, "submit.log")
	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "slack-adapter",
		SourceKind: "task",
		ExternalID: "slack-1",
		Subject:    "Send a reply",
		Body:       "Need a follow-up",
		SubmitProfile: &workItemSubmitProfile{
			Type:    "shell",
			Command: "printf '%s\\n%s\\n' \"$NANA_WORK_ITEM_BODY\" \"$NANA_WORK_ITEM_SUMMARY\" > " + outputPath,
		},
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}

	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	item.LatestDraft = &workItemDraft{Kind: "reply", Body: "Adapter reply", Summary: "Reply summary"}
	item.Status = workItemStatusDraftReady
	item.LatestArtifactRoot = workItemAttemptDir(item.ID, 1)
	if err := os.MkdirAll(item.LatestArtifactRoot, 0o755); err != nil {
		t.Fatalf("mkdir attempt root: %v", err)
	}
	if err := store.updateWorkItem(item); err != nil {
		t.Fatalf("updateWorkItem: %v", err)
	}
	store.Close()

	submitted, err := submitWorkItemByID(item.ID, "test")
	if err != nil {
		t.Fatalf("submitWorkItemByID: %v", err)
	}
	if submitted.Status != workItemStatusSubmitted {
		t.Fatalf("expected submitted status, got %+v", submitted)
	}
	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read submit output: %v", err)
	}
	if string(content) != "Adapter reply\nReply summary\n" {
		t.Fatalf("unexpected submit output: %q", content)
	}
}

func TestSubmitWorkItemViaShellIgnoresOptionalArtifactWriteFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	outputPath := filepath.Join(home, "submit-optional.log")
	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "slack-adapter",
		SourceKind: "task",
		ExternalID: "slack-optional-artifact",
		Subject:    "Send a reply",
		SubmitProfile: &workItemSubmitProfile{
			Type:    "shell",
			Command: "printf 'ok\\n' > " + outputPath,
		},
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}

	brokenArtifactRoot := filepath.Join(home, "artifact-root-file")
	if err := os.WriteFile(brokenArtifactRoot, []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write broken artifact root: %v", err)
	}

	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	item.LatestDraft = &workItemDraft{Kind: "reply", Body: "Adapter reply", Summary: "Reply summary"}
	item.Status = workItemStatusDraftReady
	item.LatestArtifactRoot = brokenArtifactRoot
	if err := store.updateWorkItem(item); err != nil {
		t.Fatalf("updateWorkItem: %v", err)
	}
	store.Close()

	submitted, err := submitWorkItemByID(item.ID, "test")
	if err != nil {
		t.Fatalf("submitWorkItemByID: %v", err)
	}
	if submitted.Status != workItemStatusSubmitted {
		t.Fatalf("expected submitted status, got %+v", submitted)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("expected shell submit output despite artifact failure: %v", err)
	}
}

func TestRunWorkItemArtifactFailureLeavesPreFinalState(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)

	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "email",
		SourceKind: "task",
		ExternalID: "artifact-fail-run",
		Subject:    "Draft a reply",
		Body:       "Need an answer.",
		Metadata: map[string]any{
			"task_mode": "reply",
		},
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}

	oldRunner := workItemRunManagedPrompt
	oldArtifacts := workItemWriteDraftArtifacts
	workItemRunManagedPrompt = func(options codexManagedPromptOptions) (codexManagedPromptResult, error) {
		return codexManagedPromptResult{
			Stdout: `{"kind":"reply","body":"Response","summary":"Summary","suggested_disposition":"needs_review","confidence":0.6}`,
		}, nil
	}
	workItemWriteDraftArtifacts = func(item workItem, attemptDir string, rawOutput string) error {
		return fmt.Errorf("artifact write failed")
	}
	defer func() {
		workItemRunManagedPrompt = oldRunner
		workItemWriteDraftArtifacts = oldArtifacts
	}()

	_, err = runWorkItemByID(cwd, item.ID, nil, false)
	if err == nil || !strings.Contains(err.Error(), "artifact write failed") {
		t.Fatalf("expected artifact write failure, got %v", err)
	}

	detail, err := readWorkItemDetail(item.ID)
	if err != nil {
		t.Fatalf("readWorkItemDetail: %v", err)
	}
	if detail.Item.Status != workItemStatusRunning {
		t.Fatalf("expected item to remain in pre-final running state, got %+v", detail.Item)
	}
	if strings.TrimSpace(detail.Item.LatestArtifactRoot) != "" {
		t.Fatalf("expected no artifact root to be committed, got %+v", detail.Item)
	}
	if len(detail.Events) != 2 {
		t.Fatalf("expected only ingest and run_started events, got %+v", detail.Events)
	}
}

func TestParseWorkItemRunArgsAcceptsHiddenAttemptDir(t *testing.T) {
	options, err := parseWorkItemRunArgs([]string{"wi-1", "--attempt-dir", "/tmp/attempt-001", "--", "--model", "gpt-5.4"})
	if err != nil {
		t.Fatalf("parseWorkItemRunArgs: %v", err)
	}
	if options.ItemID != "wi-1" || options.AttemptDir != "/tmp/attempt-001" {
		t.Fatalf("unexpected parsed options: %+v", options)
	}
	if got := strings.Join(options.CodexArgs, " "); got != "--model gpt-5.4" {
		t.Fatalf("unexpected codex args: %q", got)
	}
}

func TestPatchWorkItemByIDPersistsWorkType(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "email",
		SourceKind: "task",
		ExternalID: "work-type-patch",
		Subject:    "Recover this execution",
		Metadata: map[string]any{
			"repo_root": t.TempDir(),
		},
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}

	workType := workTypeFeature
	updated, err := patchWorkItemByID(item.ID, &workType, "test")
	if err != nil {
		t.Fatalf("patchWorkItemByID: %v", err)
	}
	if updated.WorkType != workTypeFeature {
		t.Fatalf("expected patched work type, got %+v", updated)
	}

	detail, err := readWorkItemDetail(item.ID)
	if err != nil {
		t.Fatalf("readWorkItemDetail: %v", err)
	}
	if detail.Item.WorkType != workTypeFeature {
		t.Fatalf("expected persisted work type, got %+v", detail.Item)
	}
	if metadataString(detail.Item.Metadata, workItemMetadataWorkType) != workTypeFeature {
		t.Fatalf("expected metadata work type, got %+v", detail.Item.Metadata)
	}
	foundEvent := false
	for _, event := range detail.Events {
		if event.EventType == "work_type_updated" {
			foundEvent = true
			break
		}
	}
	if !foundEvent {
		t.Fatalf("expected work_type_updated event, got %+v", detail.Events)
	}
}

func TestRunWorkItemByIDUsesExplicitWorkTypeForGithubExecution(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)

	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "adapter",
		SourceKind: "task",
		ExternalID: "github-execution-work-type",
		RepoSlug:   "acme/widget",
		TargetURL:  "https://github.com/acme/widget/issues/1",
		Subject:    "Run live smoke task",
		WorkType:   workTypeTestOnly,
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}

	oldStart := workItemStartGithubExecution
	defer func() { workItemStartGithubExecution = oldStart }()

	captured := ""
	workItemStartGithubExecution = func(options githubWorkStartOptions) (githubWorkManifest, error) {
		captured = options.WorkType
		if err := writeWorkRunIndex(workRunIndexEntry{
			RunID:     "gh-exec-1",
			Backend:   "github",
			RepoKey:   "acme/widget",
			RepoSlug:  "acme/widget",
			UpdatedAt: ISOTimeNow(),
		}); err != nil {
			t.Fatalf("writeWorkRunIndex: %v", err)
		}
		return githubWorkManifest{RunID: "gh-exec-1"}, nil
	}

	result, err := runWorkItemByID(cwd, item.ID, nil, false)
	if err != nil {
		t.Fatalf("runWorkItemByID: %v", err)
	}
	if captured != workTypeTestOnly {
		t.Fatalf("expected github execution to use work type %q, got %q", workTypeTestOnly, captured)
	}
	if result.Item.LinkedRunID != "gh-exec-1" {
		t.Fatalf("expected linked GitHub run, got %+v", result.Item)
	}
}

func TestRunWorkItemByIDUsesExplicitWorkTypeForLocalExecution(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := t.TempDir()
	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "adapter",
		SourceKind: "task",
		ExternalID: "local-execution-work-type",
		Subject:    "Refine runtime plumbing",
		WorkType:   workTypeRefactor,
		Metadata: map[string]any{
			"repo_root": repoRoot,
		},
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}

	oldRunLocal := workItemRunLocalExecution
	defer func() { workItemRunLocalExecution = oldRunLocal }()

	var captured []string
	workItemRunLocalExecution = func(callCwd string, args []string, policy codexRateLimitPolicy) (string, error) {
		captured = append([]string{callCwd}, args...)
		return "lw-local-started", nil
	}

	result, err := runWorkItemByID(cwd, item.ID, nil, false)
	if err != nil {
		t.Fatalf("runWorkItemByID: %v", err)
	}
	if !slices.Contains(captured, "--work-type") || !slices.Contains(captured, workTypeRefactor) {
		t.Fatalf("expected local execution args to include explicit work type, got %+v", captured)
	}
	if result.Item.LatestDraft == nil || !strings.Contains(result.Item.LatestDraft.Summary, "Refactor") {
		t.Fatalf("expected local execution draft summary to mention work type, got %+v", result.Item.LatestDraft)
	}
}

func TestRecoverWorkItemByIDRerunsLinkedLocalRunWithWorkTypeOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := t.TempDir()
	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "adapter",
		SourceKind: "task",
		ExternalID: "recover-linked-local",
		Subject:    "Recover the failed run",
		WorkType:   workTypeFeature,
		Metadata: map[string]any{
			"repo_root": repoRoot,
		},
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}
	item.LinkedRunID = "lw-old"
	item.Status = workItemStatusFailed
	if err := writeWorkRunIndex(workRunIndexEntry{
		RunID:     "lw-old",
		Backend:   "local",
		RepoRoot:  repoRoot,
		RepoSlug:  "acme/widget",
		UpdatedAt: ISOTimeNow(),
	}); err != nil {
		t.Fatalf("writeWorkRunIndex old run: %v", err)
	}
	if err := withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		return store.updateWorkItem(item)
	}); err != nil {
		t.Fatalf("updateWorkItem: %v", err)
	}

	inputPath := filepath.Join(t.TempDir(), "input-plan.md")
	if err := os.WriteFile(inputPath, []byte("Recover this task\n"), 0o644); err != nil {
		t.Fatalf("write input plan: %v", err)
	}

	oldResolve := workItemResolveLocalRun
	oldRerun := workItemRerunLocalExecution
	defer func() {
		workItemResolveLocalRun = oldResolve
		workItemRerunLocalExecution = oldRerun
	}()

	workItemResolveLocalRun = func(cwd string, selection localWorkRunSelection) (localWorkManifest, string, error) {
		return localWorkManifest{
			RunID:     "lw-old",
			RepoRoot:  repoRoot,
			InputPath: inputPath,
			WorkType:  workTypeTestOnly,
		}, repoRoot, nil
	}
	capturedWorkType := ""
	workItemRerunLocalExecution = func(cwd string, manifest localWorkManifest, workType string) (string, error) {
		capturedWorkType = workType
		if err := writeWorkRunIndex(workRunIndexEntry{
			RunID:     "lw-new",
			Backend:   "local",
			RepoRoot:  repoRoot,
			RepoSlug:  "acme/widget",
			UpdatedAt: ISOTimeNow(),
		}); err != nil {
			t.Fatalf("writeWorkRunIndex new run: %v", err)
		}
		return "lw-new", nil
	}

	updated, err := recoverWorkItemByID("", item.ID)
	if err != nil {
		t.Fatalf("recoverWorkItemByID: %v", err)
	}
	if capturedWorkType != workTypeFeature {
		t.Fatalf("expected recovery rerun to use patched work type, got %q", capturedWorkType)
	}
	if updated.LinkedRunID != "lw-new" || updated.WorkType != workTypeFeature {
		t.Fatalf("expected updated linked run and work type, got %+v", updated)
	}
	if updated.LatestDraft == nil || updated.LatestDraft.RunID != "lw-new" {
		t.Fatalf("expected recovery draft to point at new run, got %+v", updated.LatestDraft)
	}

	detail, err := readWorkItemDetail(item.ID)
	if err != nil {
		t.Fatalf("readWorkItemDetail: %v", err)
	}
	foundRecovered := false
	for _, event := range detail.Events {
		if event.EventType == "recovered" {
			foundRecovered = true
			if event.Payload["recovery_result"] != "restarted" || event.Payload["recovery_mode"] != workItemRecoveryModeLinkedLocalRerun {
				t.Fatalf("expected local recovery payload metadata, got %+v", event.Payload)
			}
			break
		}
	}
	if !foundRecovered {
		t.Fatalf("expected recovered event, got %+v", detail.Events)
	}
}

func TestRecoverWorkItemByIDResumesLinkedGithubRunWhenFeedbackResumeStateIsAvailable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	createLocalWorkRepoAt(t, sourcePath)

	runID := "gh-recover-resume"
	runDir := filepath.Join(githubWorkRepoRoot(repoSlug), "runs", runID)
	sandboxPath := filepath.Join(githubWorkRepoRoot(repoSlug), "sandboxes", "issue-42-"+runID)
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	createLocalWorkRepoAt(t, sandboxRepoPath)

	manifest := githubWorkManifest{
		RunID:              runID,
		RepoSlug:           repoSlug,
		RepoOwner:          "acme",
		RepoName:           "widget",
		ManagedRepoRoot:    githubWorkRepoRoot(repoSlug),
		SourcePath:         sourcePath,
		TargetURL:          "https://github.com/acme/widget/issues/42",
		TargetKind:         "issue",
		TargetNumber:       42,
		WorkType:           workTypeBugFix,
		UpdatedAt:          ISOTimeNow(),
		ExecutionStatus:    "failed",
		NextAction:         "continue_after_feedback",
		SandboxPath:        sandboxPath,
		SandboxRepoPath:    sandboxRepoPath,
		ReviewRequestState: "requested",
	}
	manifestPath := filepath.Join(runDir, "manifest.json")
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		t.Fatalf("writeGithubJSON manifest: %v", err)
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		t.Fatalf("indexGithubWorkRunManifest: %v", err)
	}

	newFeedback := githubFeedbackSnapshot{
		Actors: []string{"reviewer-a"},
		IssueComments: []githubIssueCommentPayload{{
			ID:        101,
			HTMLURL:   "https://github.com/acme/widget/issues/42#issuecomment-101",
			Body:      "Please keep going.",
			UpdatedAt: ISOTimeNow(),
			User:      githubActor{Login: "reviewer-a"},
		}},
	}
	prompt := buildGithubFeedbackContinuationPrompt(manifest, buildGithubFeedbackInstructions(manifest, newFeedback.Actors, newFeedback))
	if err := writeGithubJSON(githubFeedbackResumeStatePath(runDir), githubFeedbackResumeState{
		Version:           1,
		Actors:            append([]string{}, newFeedback.Actors...),
		NewFeedback:       newFeedback,
		PromptFingerprint: githubFeedbackResumeFingerprint(prompt, nil),
		UpdatedAt:         manifest.UpdatedAt,
	}); err != nil {
		t.Fatalf("writeGithubJSON resume state: %v", err)
	}
	if err := writeGithubJSON(filepath.Join(runDir, "leader-checkpoint.json"), codexStepCheckpoint{
		SessionID:         "leader-session-gh-recover",
		ResumeEligible:    true,
		PromptFingerprint: githubFeedbackResumeFingerprint(prompt, nil),
	}); err != nil {
		t.Fatalf("writeGithubJSON leader checkpoint: %v", err)
	}

	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "adapter",
		SourceKind: "task",
		ExternalID: "recover-linked-github-resume",
		RepoSlug:   repoSlug,
		TargetURL:  manifest.TargetURL,
		Subject:    "Recover linked GitHub work",
		WorkType:   workTypeFeature,
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}
	item.LinkedRunID = runID
	item.Status = workItemStatusFailed
	startUITestUpdateWorkItem(t, item)

	oldSync := workItemSyncGithubExecution
	defer func() { workItemSyncGithubExecution = oldSync }()

	var syncOptions githubWorkSyncOptions
	workItemSyncGithubExecution = func(options githubWorkSyncOptions) error {
		syncOptions = options
		return nil
	}

	updated, err := recoverWorkItemByID("", item.ID)
	if err != nil {
		t.Fatalf("recoverWorkItemByID: %v", err)
	}
	if syncOptions.RunID != runID {
		t.Fatalf("expected GitHub recovery to keep the linked run, got %+v", syncOptions)
	}
	if updated.LinkedRunID != runID || updated.WorkType != workTypeFeature {
		t.Fatalf("expected GitHub recovery to keep linked run and work type, got %+v", updated)
	}
	if updated.LatestDraft == nil || updated.LatestDraft.RunID != runID {
		t.Fatalf("expected GitHub recovery draft to point at the linked run, got %+v", updated.LatestDraft)
	}

	detail, err := readWorkItemDetail(item.ID)
	if err != nil {
		t.Fatalf("readWorkItemDetail: %v", err)
	}
	foundRecovered := false
	for _, event := range detail.Events {
		if event.EventType != "recovered" {
			continue
		}
		foundRecovered = true
		if event.Payload["recovery_result"] != "resumed" {
			t.Fatalf("expected resumed GitHub recovery payload, got %+v", event.Payload)
		}
		mode := event.Payload["recovery_mode"]
		if mode != workItemRecoveryModeGithubResume && mode != workItemRecoveryModeGithubSync {
			t.Fatalf("expected resumed GitHub recovery payload, got %+v", event.Payload)
		}
	}
	if !foundRecovered {
		t.Fatalf("expected recovered event, got %+v", detail.Events)
	}
}

func TestRecoverWorkItemByIDRestartsLinkedGithubRunWhenResumeIsUnavailable(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	createLocalWorkRepoAt(t, sourcePath)

	oldRunID := "gh-recover-old"
	runDir := filepath.Join(githubWorkRepoRoot(repoSlug), "runs", oldRunID)
	sandboxPath := filepath.Join(githubWorkRepoRoot(repoSlug), "sandboxes", "issue-7-"+oldRunID)
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	createLocalWorkRepoAt(t, sandboxRepoPath)

	manifest := githubWorkManifest{
		RunID:           oldRunID,
		RepoSlug:        repoSlug,
		RepoOwner:       "acme",
		RepoName:        "widget",
		ManagedRepoRoot: githubWorkRepoRoot(repoSlug),
		SourcePath:      sourcePath,
		TargetURL:       "https://github.com/acme/widget/issues/7",
		TargetKind:      "issue",
		TargetNumber:    7,
		WorkType:        workTypeTestOnly,
		UpdatedAt:       ISOTimeNow(),
		ExecutionStatus: "failed",
		NextAction:      "waiting for approval",
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
	}
	manifestPath := filepath.Join(runDir, "manifest.json")
	if err := writeGithubJSON(manifestPath, manifest); err != nil {
		t.Fatalf("writeGithubJSON manifest: %v", err)
	}
	if err := indexGithubWorkRunManifest(manifestPath, manifest); err != nil {
		t.Fatalf("indexGithubWorkRunManifest: %v", err)
	}

	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "adapter",
		SourceKind: "task",
		ExternalID: "recover-linked-github-restart",
		RepoSlug:   repoSlug,
		TargetURL:  manifest.TargetURL,
		Subject:    "Restart linked GitHub work",
		WorkType:   workTypeFeature,
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}
	item.LinkedRunID = oldRunID
	item.Status = workItemStatusFailed
	startUITestUpdateWorkItem(t, item)

	oldStart := workItemStartGithubExecution
	defer func() { workItemStartGithubExecution = oldStart }()

	capturedWorkType := ""
	workItemStartGithubExecution = func(options githubWorkStartOptions) (githubWorkManifest, error) {
		capturedWorkType = options.WorkType
		if err := writeWorkRunIndex(workRunIndexEntry{
			RunID:        "gh-recover-new",
			Backend:      "github",
			RepoKey:      repoSlug,
			RepoSlug:     repoSlug,
			ManifestPath: filepath.Join(githubWorkRepoRoot(repoSlug), "runs", "gh-recover-new", "manifest.json"),
			UpdatedAt:    ISOTimeNow(),
		}); err != nil {
			t.Fatalf("writeWorkRunIndex: %v", err)
		}
		return githubWorkManifest{RunID: "gh-recover-new"}, nil
	}

	updated, err := recoverWorkItemByID(cwd, item.ID)
	if err != nil {
		t.Fatalf("recoverWorkItemByID: %v", err)
	}
	if capturedWorkType != workTypeFeature {
		t.Fatalf("expected restarted GitHub recovery to use selected work type, got %q", capturedWorkType)
	}
	if updated.LinkedRunID != "gh-recover-new" || updated.WorkType != workTypeFeature {
		t.Fatalf("expected restarted GitHub recovery to relink the item, got %+v", updated)
	}
	if updated.LatestDraft == nil || updated.LatestDraft.RunID != "gh-recover-new" {
		t.Fatalf("expected restarted GitHub recovery draft to point at the new run, got %+v", updated.LatestDraft)
	}

	detail, err := readWorkItemDetail(item.ID)
	if err != nil {
		t.Fatalf("readWorkItemDetail: %v", err)
	}
	foundRecovered := false
	for _, event := range detail.Events {
		if event.EventType != "recovered" {
			continue
		}
		foundRecovered = true
		if event.Payload["recovery_result"] != "restarted" || event.Payload["recovery_mode"] != workItemRecoveryModeGithubRestart {
			t.Fatalf("expected restarted GitHub recovery payload, got %+v", event.Payload)
		}
		if event.Payload["previous_run_id"] != oldRunID || event.Payload["run_id"] != "gh-recover-new" {
			t.Fatalf("expected GitHub recovery payload to relink runs, got %+v", event.Payload)
		}
	}
	if !foundRecovered {
		t.Fatalf("expected recovered event, got %+v", detail.Events)
	}
}

func TestRunWorkItemByIDWithOptionsReusesAttemptDir(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)

	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "email",
		SourceKind: "task",
		ExternalID: "resume-attempt",
		Subject:    "Draft a reply",
		Body:       "Need an answer.",
		Metadata: map[string]any{
			"task_mode": "reply",
		},
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}

	attemptDir := workItemAttemptDir(item.ID, 1)
	if err := os.MkdirAll(attemptDir, 0o755); err != nil {
		t.Fatalf("mkdir attempt dir: %v", err)
	}

	oldRunner := workItemRunManagedPrompt
	workItemRunManagedPrompt = func(options codexManagedPromptOptions) (codexManagedPromptResult, error) {
		return codexManagedPromptResult{
			Stdout: `{"kind":"reply","body":"Response","summary":"Summary","suggested_disposition":"needs_review","confidence":0.6}`,
		}, nil
	}
	defer func() {
		workItemRunManagedPrompt = oldRunner
	}()

	result, err := runWorkItemByIDWithOptions(cwd, workItemRunCommandOptions{
		ItemID:     item.ID,
		AttemptDir: attemptDir,
	}, false)
	if err != nil {
		t.Fatalf("runWorkItemByIDWithOptions: %v", err)
	}
	if result.Item.LatestArtifactRoot != attemptDir {
		t.Fatalf("expected reused attempt dir, got %+v", result.Item)
	}

	detail, err := readWorkItemDetail(item.ID)
	if err != nil {
		t.Fatalf("readWorkItemDetail: %v", err)
	}
	if detail.Item.LatestArtifactRoot != attemptDir || detail.Item.Status != workItemStatusDraftReady {
		t.Fatalf("unexpected item after resumed attempt: %+v", detail.Item)
	}
	foundResumeEvent := false
	for _, event := range detail.Events {
		if event.EventType == "run_resumed" {
			foundResumeEvent = true
			break
		}
	}
	if !foundResumeEvent {
		t.Fatalf("expected run_resumed event, got %+v", detail.Events)
	}
}

func TestResolveWorkItemLinkedRunIDUsesMatchingRunIndex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	runID := "gh-linked-resolution"
	runDir := filepath.Join(githubWorkRepoRoot(repoSlug), "runs", runID)
	manifestPath := filepath.Join(runDir, "manifest.json")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := writeGithubJSON(manifestPath, githubWorkManifest{
		Version:        1,
		RunID:          runID,
		RepoSlug:       repoSlug,
		RepoOwner:      "acme",
		RepoName:       "widget",
		TargetURL:      "https://github.com/acme/widget/pull/7",
		PublishedPRURL: "https://github.com/acme/widget/pull/7",
		UpdatedAt:      ISOTimeNow(),
	}); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := writeWorkRunIndex(workRunIndexEntry{
		RunID:        runID,
		Backend:      "github",
		RepoKey:      repoSlug,
		RepoRoot:     githubWorkRepoRoot(repoSlug),
		RepoName:     "widget",
		RepoSlug:     repoSlug,
		ManifestPath: manifestPath,
		UpdatedAt:    ISOTimeNow(),
		TargetKind:   "pull_request",
	}); err != nil {
		t.Fatalf("writeWorkRunIndex: %v", err)
	}

	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "github",
		SourceKind: "review_request",
		ExternalID: "notification-100",
		RepoSlug:   repoSlug,
		TargetURL:  "https://github.com/acme/widget/pull/7",
		Subject:    "Review request",
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}
	if item.LinkedRunID != runID {
		t.Fatalf("expected linked run %s, got %+v", runID, item)
	}
}

func TestWorkItemCodexTargetForItemUsesManagedSourceCheckout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoSlug := "acme/widget"
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	if err := os.MkdirAll(sourcePath, 0o755); err != nil {
		t.Fatalf("mkdir source path: %v", err)
	}

	target, err := workItemCodexTargetForItem("", workItem{RepoSlug: repoSlug})
	if err != nil {
		t.Fatalf("workItemCodexTargetForItem: %v", err)
	}
	if target.RepoPath != sourcePath || target.LockKind != workItemCodexLockSource {
		t.Fatalf("unexpected Codex target: %+v", target)
	}
}

func TestWorkItemCodexTargetForItemUsesLinkedSandbox(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runID := "gh-linked-run"
	repoRoot := githubWorkRepoRoot("acme/widget")
	runDir := filepath.Join(repoRoot, "runs", runID)
	sandboxPath := filepath.Join(repoRoot, "sandboxes", "issue-1-"+runID)
	sandboxRepoPath := filepath.Join(sandboxPath, "repo")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.MkdirAll(sandboxRepoPath, 0o755); err != nil {
		t.Fatalf("mkdir sandbox repo: %v", err)
	}
	if err := writeGithubJSON(filepath.Join(runDir, "manifest.json"), githubWorkManifest{
		Version:         1,
		RunID:           runID,
		RepoSlug:        "acme/widget",
		RepoOwner:       "acme",
		RepoName:        "widget",
		SandboxPath:     sandboxPath,
		SandboxRepoPath: sandboxRepoPath,
		TargetKind:      "issue",
		TargetNumber:    1,
		TargetURL:       "https://github.com/acme/widget/issues/1",
		UpdatedAt:       ISOTimeNow(),
	}); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	target, err := workItemCodexTargetForItem("", workItem{LinkedRunID: runID})
	if err != nil {
		t.Fatalf("workItemCodexTargetForItem: %v", err)
	}
	if target.RepoPath != sandboxRepoPath || target.LockKind != workItemCodexLockSandbox {
		t.Fatalf("unexpected linked-run target: %+v", target)
	}
}

func TestRunWorkItemCodexPromptBlocksWhenSourceWriteLockHeld(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := setRepoAccessLockTestTiming(t, 200*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond, time.Second)
	defer restore()

	repoPath := filepath.Join(home, "repo")
	attemptDir := filepath.Join(home, "attempt")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := os.MkdirAll(attemptDir, 0o755); err != nil {
		t.Fatalf("mkdir attempt dir: %v", err)
	}

	oldRunner := workItemRunManagedPrompt
	called := false
	workItemRunManagedPrompt = func(options codexManagedPromptOptions) (codexManagedPromptResult, error) {
		called = true
		return codexManagedPromptResult{}, nil
	}
	defer func() { workItemRunManagedPrompt = oldRunner }()

	lock, err := acquireSourceWriteLock(repoPath, repoAccessLockOwner{
		Backend: "test",
		RunID:   "work-item-source-writer",
		Purpose: "source-setup",
		Label:   "work-item-source-writer",
	})
	if err != nil {
		t.Fatalf("acquire source lock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	_, err = runWorkItemCodexPrompt(workItemCodexTarget{RepoPath: repoPath, LockKind: workItemCodexLockSource}, attemptDir, "draft a reply", nil)
	if err == nil || !strings.Contains(err.Error(), "repo read lock busy") {
		t.Fatalf("expected source lock conflict, got %v", err)
	}
	if called {
		t.Fatalf("expected prompt runner not to be called while source lock is held")
	}
}

func TestRepairLocalWorkDBMigratesLegacyPauseFieldsAndDelayAutoRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "email",
		SourceKind: "task",
		ExternalID: "mail-pause-1",
		Subject:    "Wait for retry window",
		AutoRun:    true,
		Metadata: map[string]any{
			"task_mode": "reply",
		},
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}
	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	legacyMetadata := map[string]any{
		"task_mode":    "reply",
		"pause_reason": "rate limited",
		"pause_until":  time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
	encoded, err := json.Marshal(legacyMetadata)
	if err != nil {
		t.Fatalf("marshal legacy metadata: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE work_items SET status = ?, metadata_json = ?, pause_reason = NULL, pause_until = NULL WHERE id = ?`,
		workItemStatusPaused, string(encoded), item.ID); err != nil {
		t.Fatalf("seed legacy paused row: %v", err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 0`); err != nil {
		t.Fatalf("downgrade schema version: %v", err)
	}
	store.Close()

	if _, err := readWorkItemDetail(item.ID); err == nil {
		t.Fatal("expected readWorkItemDetail to require repair for legacy schema")
	} else {
		var schemaErr *localWorkDBSchemaError
		if !errors.As(err, &schemaErr) {
			t.Fatalf("expected schema repair error, got %v", err)
		}
	}

	report, err := repairLocalWorkDB()
	if err != nil {
		t.Fatalf("repairLocalWorkDB: %v", err)
	}
	if !report.Changed {
		t.Fatalf("expected repair to change the DB, got %+v", report)
	}

	detail, err := readWorkItemDetail(item.ID)
	if err != nil {
		t.Fatalf("readWorkItemDetail after repair: %v", err)
	}
	if detail.Item.PauseReason != "rate limited" || detail.Item.PauseUntil == "" {
		t.Fatalf("expected hydrated pause fields after repair, got %+v", detail.Item)
	}

	store, err = openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB after repair: %v", err)
	}
	reloaded, err := store.readWorkItem(item.ID)
	if err != nil {
		t.Fatalf("readWorkItem after repair: %v", err)
	}
	if reloaded.Metadata != nil {
		if _, ok := reloaded.Metadata["pause_reason"]; ok {
			t.Fatalf("expected legacy pause_reason metadata to be cleared, got %+v", reloaded.Metadata)
		}
		if _, ok := reloaded.Metadata["pause_until"]; ok {
			t.Fatalf("expected legacy pause_until metadata to be cleared, got %+v", reloaded.Metadata)
		}
	}
	runnable, err := store.listAutoRunnableWorkItems("", 10)
	store.Close()
	if err != nil {
		t.Fatalf("listAutoRunnableWorkItems: %v", err)
	}
	if len(runnable) != 0 {
		t.Fatalf("expected paused item with future retry to stay unrunnable, got %+v", runnable)
	}
}

func TestWorkItemPauseFieldsPersistExplicitlyInDB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	item, _, err := enqueueWorkItem(workItemInput{
		Source:     "email",
		SourceKind: "task",
		ExternalID: "mail-pause-2",
		Subject:    "Persist explicit pause fields",
	}, "test")
	if err != nil {
		t.Fatalf("enqueueWorkItem: %v", err)
	}
	store, err := openLocalWorkDB()
	if err != nil {
		t.Fatalf("openLocalWorkDB: %v", err)
	}
	item.Status = workItemStatusPaused
	item.PauseReason = "rate limited"
	item.PauseUntil = time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	item.Metadata = nil
	if err := store.updateWorkItem(item); err != nil {
		t.Fatalf("updateWorkItem: %v", err)
	}
	reloaded, err := store.readWorkItem(item.ID)
	store.Close()
	if err != nil {
		t.Fatalf("readWorkItem: %v", err)
	}
	if reloaded.PauseReason != "rate limited" || reloaded.PauseUntil == "" {
		t.Fatalf("expected explicit pause fields from DB, got %+v", reloaded)
	}
	if reloaded.Metadata != nil {
		if _, ok := reloaded.Metadata["pause_reason"]; ok {
			t.Fatalf("expected new rows to avoid pause_reason metadata dual-write, got %+v", reloaded.Metadata)
		}
		if _, ok := reloaded.Metadata["pause_until"]; ok {
			t.Fatalf("expected new rows to avoid pause_until metadata dual-write, got %+v", reloaded.Metadata)
		}
	}
}
