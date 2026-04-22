package gocli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadStartWorkStateSyncsManualScoutFindingsBySourceIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	repo := createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	runID := "ui-scout-findings"
	artifactDir := filepath.Join(repo, ".nana", scoutArtifactRoot(uiScoutRole), runID)
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	report := scoutReport{
		Repo:        repoSlug,
		GeneratedAt: "2026-04-22T08:00:00Z",
		Proposals: []scoutFinding{{
			Title:    "Retry button copy is ambiguous",
			Summary:  "The approval retry button does not explain whether the whole job reruns.",
			Evidence: "approvals drawer copy",
			Severity: "high",
			Files:    []string{"internal/gocli/start_ui_assets/app.txt"},
		}},
	}
	if err := writeGithubJSON(filepath.Join(artifactDir, "proposals.json"), report); err != nil {
		t.Fatalf("write proposals: %v", err)
	}
	if err := writeStartWorkState(startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		UpdatedAt:  "2026-04-22T08:00:00Z",
		Issues:     map[string]startWorkIssueState{},
		PlannedItems: map[string]startWorkPlannedItem{
			"planned-scout": {
				ID:               "planned-scout",
				RepoSlug:         repoSlug,
				Title:            "Run UI scout",
				LaunchKind:       "manual_scout",
				FindingsHandling: startWorkFindingsHandlingManualReview,
				ScoutRole:        uiScoutRole,
				LaunchRunID:      runID,
				State:            startPlannedItemLaunched,
				CreatedAt:        "2026-04-22T08:00:00Z",
				UpdatedAt:        "2026-04-22T08:00:00Z",
			},
		},
	}); err != nil {
		t.Fatalf("write start state: %v", err)
	}

	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("read start state: %v", err)
	}
	if len(state.Findings) != 1 {
		t.Fatalf("expected one synced finding, got %+v", state.Findings)
	}
	var finding startWorkFinding
	for _, value := range state.Findings {
		finding = value
	}
	if finding.SourceKind != startWorkFindingSourceKindManualScout || finding.SourceID != runID || finding.ParentTaskKind != startWorkFindingParentTaskKindManualScout || finding.ParentTaskID != "planned-scout" {
		t.Fatalf("unexpected scout finding lineage: %+v", finding)
	}

	report.Proposals[0].Summary = "Updated scout summary from the same proposal source."
	if err := writeGithubJSON(filepath.Join(artifactDir, "proposals.json"), report); err != nil {
		t.Fatalf("rewrite proposals: %v", err)
	}
	updatedState, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("re-read start state: %v", err)
	}
	if len(updatedState.Findings) != 1 {
		t.Fatalf("expected one finding after resync, got %+v", updatedState.Findings)
	}
	for _, updated := range updatedState.Findings {
		if updated.Summary != report.Proposals[0].Summary {
			t.Fatalf("expected summary update from same source identity, got %+v", updated)
		}
	}
}

func TestReadStartWorkStateAutoPromotesTaskGeneratedFindings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	repo := createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	runID := "ui-scout-auto-promote"
	artifactDir := filepath.Join(repo, ".nana", scoutArtifactRoot(uiScoutRole), runID)
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	if err := writeGithubJSON(filepath.Join(artifactDir, "proposals.json"), scoutReport{
		Repo:        repoSlug,
		GeneratedAt: "2026-04-22T08:05:00Z",
		Proposals: []scoutFinding{{
			Title:    "Clarify retry wording",
			Summary:  "The retry wording should be explicit.",
			Evidence: "approvals drawer copy",
			Severity: "medium",
			Files:    []string{"internal/gocli/start_ui_assets/app.txt"},
		}},
	}); err != nil {
		t.Fatalf("write proposals: %v", err)
	}
	if err := writeStartWorkState(startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		UpdatedAt:  "2026-04-22T08:05:00Z",
		Issues:     map[string]startWorkIssueState{},
		PlannedItems: map[string]startWorkPlannedItem{
			"planned-scout": {
				ID:               "planned-scout",
				RepoSlug:         repoSlug,
				Title:            "Run UI scout",
				LaunchKind:       "manual_scout",
				FindingsHandling: startWorkFindingsHandlingAutoPromote,
				ScoutRole:        uiScoutRole,
				LaunchRunID:      runID,
				State:            startPlannedItemLaunched,
				CreatedAt:        "2026-04-22T08:05:00Z",
				UpdatedAt:        "2026-04-22T08:05:00Z",
			},
		},
	}); err != nil {
		t.Fatalf("write start state: %v", err)
	}

	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("read start state: %v", err)
	}
	if len(state.Findings) != 1 {
		t.Fatalf("expected one finding, got %+v", state.Findings)
	}
	if len(state.PlannedItems) != 2 {
		t.Fatalf("expected original task plus promoted task, got %+v", state.PlannedItems)
	}
	for _, finding := range state.Findings {
		if finding.Status != startWorkFindingStatusPromoted || finding.PromotedTaskID == "" {
			t.Fatalf("expected auto-promoted finding, got %+v", finding)
		}
		promotedTask, ok := state.PlannedItems[finding.PromotedTaskID]
		if !ok {
			t.Fatalf("expected promoted planned task %s, got %+v", finding.PromotedTaskID, state.PlannedItems)
		}
		if promotedTask.LaunchKind != "local_work" {
			t.Fatalf("expected promoted task to be local_work, got %+v", promotedTask)
		}
	}
}

func TestReadStartWorkStateSyncsInvestigationAndCodingFindings(t *testing.T) {
	home := setLocalWorkDBProxyTestHome(t)

	repoSlug := "acme/widget"
	repo := createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	investigationRunID := "investigate-123"
	investigationDir := filepath.Join(repo, ".nana", "logs", "investigate", investigationRunID)
	if err := os.MkdirAll(investigationDir, 0o755); err != nil {
		t.Fatalf("mkdir investigation dir: %v", err)
	}
	if err := writeGithubJSON(filepath.Join(investigationDir, "manifest.json"), investigateManifest{
		Version:         1,
		RunID:           investigationRunID,
		Query:           "Investigate approval retry drift",
		WorkspaceRoot:   repo,
		RunDir:          investigationDir,
		Status:          investigateRunStatusCompleted,
		CreatedAt:       "2026-04-22T09:00:00Z",
		UpdatedAt:       "2026-04-22T09:10:00Z",
		CompletedAt:     "2026-04-22T09:10:00Z",
		FinalReportPath: filepath.Join(investigationDir, "final-report.json"),
	}); err != nil {
		t.Fatalf("write investigation manifest: %v", err)
	}
	if err := writeGithubJSON(filepath.Join(investigationDir, "final-report.json"), investigateReport{
		OverallStatus: investigateStatusConfirmed,
		Issues: []investigateIssue{{
			ID:                  "ISS-1",
			ShortExplanation:    "Retry copy disagrees between surfaces",
			DetailedExplanation: "The queue and drawer describe different retry semantics, which causes operator confusion.",
			Proofs: []investigateProof{{
				Kind:        "source_code",
				Title:       "Start UI text",
				Path:        "internal/gocli/start_ui_assets/app.txt",
				Line:        6400,
				WhyItProves: "The strings are inconsistent.",
			}},
		}},
	}); err != nil {
		t.Fatalf("write final report: %v", err)
	}

	repoID := localWorkRepoID(repo)
	localRunID := "lw-findings-1"
	sandboxPath := filepath.Join(home, "sandboxes", localRunID)
	sandboxRepoPath := createLocalWorkRepoAt(t, filepath.Join(sandboxPath, "repo"))
	runDir := localWorkRunDirByID(repoID, localRunID)
	iterationDir := localWorkIterationDir(runDir, 1)
	if err := os.MkdirAll(iterationDir, 0o755); err != nil {
		t.Fatalf("mkdir iteration dir: %v", err)
	}
	if err := writeGithubJSON(filepath.Join(iterationDir, "review-round-1-findings.json"), []githubPullReviewFinding{{
		Fingerprint: "review-1",
		Title:       "Retry label hides job scope",
		Path:        "internal/gocli/start_ui_assets/app.txt",
		Line:        6500,
		Severity:    "medium",
		Summary:     "The retry label is too vague.",
		Detail:      "Users cannot tell whether retry reruns the worker or just requeues the approval.",
	}}); err != nil {
		t.Fatalf("write review findings: %v", err)
	}
	if err := writeGithubJSON(filepath.Join(iterationDir, "verification-round-1-post-hardening.json"), localWorkVerificationReport{
		GeneratedAt: "2026-04-22T09:15:00Z",
		Passed:      false,
		FailedStages: []string{
			"lint",
		},
		Stages: []localWorkVerificationStageResult{{
			Name:   "lint",
			Status: "failed",
			Commands: []localWorkVerificationCommandResult{{
				Command:  "npm run lint",
				ExitCode: 1,
				Output:   "lint error in start_ui_assets/app.txt",
			}},
		}},
	}); err != nil {
		t.Fatalf("write verification report: %v", err)
	}
	manifest := localWorkManifest{
		Version:           4,
		RunID:             localRunID,
		CreatedAt:         "2026-04-22T09:11:00Z",
		UpdatedAt:         "2026-04-22T09:15:00Z",
		Status:            "failed",
		RepoRoot:          repo,
		RepoName:          filepath.Base(repo),
		RepoSlug:          repoSlug,
		RepoID:            repoID,
		SourceBranch:      "main",
		BaselineSHA:       strings.TrimSpace(runLocalWorkTestGitOutput(t, repo, "rev-parse", "HEAD")),
		SandboxPath:       sandboxPath,
		SandboxRepoPath:   sandboxRepoPath,
		InputPath:         filepath.Join(home, "task.md"),
		InputMode:         "task",
		WorkType:          workTypeFeature,
		IntegrationPolicy: "final",
		GroupingPolicy:    localWorkDefaultGroupingPolicy,
		MaxIterations:     1,
		Iterations: []localWorkIterationSummary{{
			Iteration:                1,
			StartedAt:                "2026-04-22T09:11:00Z",
			CompletedAt:              "2026-04-22T09:15:00Z",
			Status:                   "failed",
			VerificationPassed:       false,
			VerificationFailedStages: []string{"lint"},
			VerificationSummary:      "lint failed",
			ReviewRoundsUsed:         1,
			ReviewFindings:           1,
		}},
	}
	if err := writeLocalWorkManifest(manifest); err != nil {
		t.Fatalf("write local work manifest: %v", err)
	}

	if err := writeStartWorkState(startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		UpdatedAt:  "2026-04-22T09:15:00Z",
		Issues:     map[string]startWorkIssueState{},
		PlannedItems: map[string]startWorkPlannedItem{
			"planned-investigation": {
				ID:                 "planned-investigation",
				RepoSlug:           repoSlug,
				Title:              "Investigate retry drift",
				LaunchKind:         "investigation",
				FindingsHandling:   startWorkFindingsHandlingManualReview,
				InvestigationQuery: "Investigate retry drift",
				LaunchRunID:        investigationRunID,
				State:              startPlannedItemLaunched,
				CreatedAt:          "2026-04-22T09:00:00Z",
				UpdatedAt:          "2026-04-22T09:10:00Z",
			},
			"planned-coding": {
				ID:               "planned-coding",
				RepoSlug:         repoSlug,
				Title:            "Fix retry copy",
				LaunchKind:       "local_work",
				FindingsHandling: startWorkFindingsHandlingManualReview,
				WorkType:         workTypeFeature,
				LaunchRunID:      localRunID,
				State:            startPlannedItemLaunched,
				CreatedAt:        "2026-04-22T09:11:00Z",
				UpdatedAt:        "2026-04-22T09:15:00Z",
			},
		},
	}); err != nil {
		t.Fatalf("write start state: %v", err)
	}

	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("read start state: %v", err)
	}
	if len(state.Findings) != 3 {
		t.Fatalf("expected investigation + review + verification findings, got %+v", state.Findings)
	}
	foundInvestigation := false
	foundReview := false
	foundVerification := false
	for _, finding := range state.Findings {
		switch finding.SourceKind {
		case startWorkFindingSourceKindInvestigation:
			foundInvestigation = finding.ParentTaskKind == startWorkFindingParentTaskKindInvestigation && finding.SourceItemID == "ISS-1"
		case startWorkFindingSourceKindCoding:
			if strings.HasPrefix(finding.SourceItemID, "review:") {
				foundReview = finding.ParentTaskKind == startWorkFindingParentTaskKindCoding
			}
			if finding.SourceItemID == "verification:lint" {
				foundVerification = finding.ParentTaskKind == startWorkFindingParentTaskKindCoding
			}
		}
	}
	if !foundInvestigation || !foundReview || !foundVerification {
		t.Fatalf("expected investigation, review, and verification findings, got %+v", state.Findings)
	}
}

func TestFindingImportSessionPromotionAndDrop(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeFindingsTestExecutable(t, filepath.Join(fakeBin, "codex"), "#!/bin/sh\ncat >/dev/null\nprintf '{\"candidates\":[{\"candidate_id\":\"cand-1\",\"title\":\"Fix retry wording\",\"summary\":\"Clarify retry scope\",\"detail\":\"The retry label should explain whether the whole worker reruns.\",\"severity\":\"medium\",\"work_type\":\"feature\"},{\"candidate_id\":\"cand-2\",\"title\":\"Drop this\",\"summary\":\"Optional\",\"detail\":\"This candidate will be dropped.\",\"severity\":\"low\",\"work_type\":\"test_only\"}]}'\n")
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	session, err := createStartUIFindingImportSession(repoSlug, "notes.md", "# Findings\n\n- retry wording")
	if err != nil {
		t.Fatalf("create import session: %v", err)
	}
	if session.ParseStatus != startWorkFindingImportParseParsed || len(session.Candidates) != 2 {
		t.Fatalf("unexpected import session: %+v", session)
	}

	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("read start state: %v", err)
	}
	if len(state.Findings) != 0 {
		t.Fatalf("expected no real findings before promotion, got %+v", state.Findings)
	}

	_, promotedSession, finding, err := promoteStartUIFindingImportCandidate(repoSlug, session.ID, "cand-1")
	if err != nil {
		t.Fatalf("promote candidate: %v", err)
	}
	if finding.SourceKind != startWorkFindingSourceKindManualImport || finding.SourceID != session.ID || finding.SourceItemID != "cand-1" {
		t.Fatalf("unexpected promoted finding: %+v", finding)
	}
	if finding.ParentTaskID != "" || finding.ParentTaskKind != "" {
		t.Fatalf("manual import finding should not have parent task lineage: %+v", finding)
	}
	if promotedSession.Candidates[0].Status != startWorkFindingCandidateStatusPromoted {
		t.Fatalf("expected promoted candidate status, got %+v", promotedSession.Candidates[0])
	}

	_, droppedSession, err := dropStartUIFindingImportCandidate(repoSlug, session.ID, "cand-2")
	if err != nil {
		t.Fatalf("drop candidate: %v", err)
	}
	if droppedSession.Candidates[1].Status != startWorkFindingCandidateStatusDropped {
		t.Fatalf("expected dropped candidate status, got %+v", droppedSession.Candidates[1])
	}
	if _, _, _, err := promoteStartUIFindingImportCandidate(repoSlug, session.ID, "cand-2"); err == nil {
		t.Fatal("expected dropped candidate promotion to fail")
	}
}

func TestFindingsImportCommandPersistsSessionArtifacts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeFindingsTestExecutable(t, filepath.Join(fakeBin, "codex"), "#!/bin/sh\ncat >/dev/null\nprintf '{\"candidates\":[{\"candidate_id\":\"cand-1\",\"title\":\"Fix retry wording\",\"summary\":\"Clarify retry scope\",\"detail\":\"The retry label should explain whether the whole worker reruns.\",\"severity\":\"medium\",\"work_type\":\"feature\"}]}'\n")
	editorPath := filepath.Join(fakeBin, "editor-noop")
	writeFindingsTestExecutable(t, editorPath, "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("EDITOR", editorPath)

	markdownPath := filepath.Join(home, "findings.md")
	if err := os.WriteFile(markdownPath, []byte("# Findings\n\nRetry wording"), 0o644); err != nil {
		t.Fatalf("write markdown: %v", err)
	}
	if err := Findings(".", []string{"import", "--repo", repoSlug, "--file", markdownPath, "--promote", "none", "--drop", "none"}); err != nil {
		t.Fatalf("Findings import: %v", err)
	}

	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("read start state: %v", err)
	}
	if len(state.ImportSessions) != 1 {
		t.Fatalf("expected one import session, got %+v", state.ImportSessions)
	}
	for _, session := range state.ImportSessions {
		for _, path := range []string{session.SourceMarkdownPath, session.CandidatesPath, session.PreviewPath} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("expected import artifact %s: %v", path, err)
			}
		}
	}
}

func TestFindingsImportCommandKeepsSessionOnInvalidEditedJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeFindingsTestExecutable(t, filepath.Join(fakeBin, "codex"), "#!/bin/sh\ncat >/dev/null\nprintf '{\"candidates\":[{\"candidate_id\":\"cand-1\",\"title\":\"Fix retry wording\",\"summary\":\"Clarify retry scope\",\"detail\":\"The retry label should explain whether the whole worker reruns.\",\"severity\":\"medium\",\"work_type\":\"feature\"}]}'\n")
	editorPath := filepath.Join(fakeBin, "editor-break-json")
	writeFindingsTestExecutable(t, editorPath, "#!/bin/sh\nprintf '{invalid json' > \"$1\"\n")
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	t.Setenv("EDITOR", editorPath)

	markdownPath := filepath.Join(home, "findings.md")
	if err := os.WriteFile(markdownPath, []byte("# Findings\n\nRetry wording"), 0o644); err != nil {
		t.Fatalf("write markdown: %v", err)
	}
	if err := Findings(".", []string{"import", "--repo", repoSlug, "--file", markdownPath, "--promote", "none", "--drop", "none"}); err == nil {
		t.Fatal("expected invalid edited candidates.json to fail")
	}

	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("read start state: %v", err)
	}
	if len(state.ImportSessions) != 1 {
		t.Fatalf("expected import session to remain persisted, got %+v", state.ImportSessions)
	}
}

func TestFindingsCLIListPromoteDismissAndReviewSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoSlug := "acme/widget"
	createLocalWorkRepoAt(t, githubManagedPaths(repoSlug).SourcePath)
	if err := writeStartWorkState(startWorkState{
		Version:    startWorkStateVersion,
		SourceRepo: repoSlug,
		UpdatedAt:  "2026-04-22T11:00:00Z",
		Issues:     map[string]startWorkIssueState{},
		Findings: map[string]startWorkFinding{
			"finding-1": {
				ID:           "finding-1",
				RepoSlug:     repoSlug,
				SourceKind:   startWorkFindingSourceKindManualImport,
				SourceID:     "import-1",
				SourceItemID: "cand-1",
				Title:        "Clarify retry wording",
				Summary:      "The retry wording should be explicit.",
				Severity:     "medium",
				WorkType:     workTypeFeature,
				Status:       startWorkFindingStatusOpen,
				CreatedAt:    "2026-04-22T11:00:00Z",
				UpdatedAt:    "2026-04-22T11:00:00Z",
			},
		},
	}); err != nil {
		t.Fatalf("write start state: %v", err)
	}

	listOutput, err := captureStdout(t, func() error {
		return Findings(".", []string{"list", "--repo", repoSlug})
	})
	if err != nil {
		t.Fatalf("findings list: %v", err)
	}
	if !strings.Contains(listOutput, "finding-1") || !strings.Contains(listOutput, "manual_import:import-1:cand-1") {
		t.Fatalf("unexpected findings list output: %s", listOutput)
	}

	promoteOutput, err := captureStdout(t, func() error {
		return Findings(".", []string{"promote", "--repo", repoSlug, "--finding", "finding-1"})
	})
	if err != nil {
		t.Fatalf("findings promote: %v", err)
	}
	if !strings.Contains(promoteOutput, "Promoted finding-1 -> planned-") {
		t.Fatalf("unexpected promote output: %s", promoteOutput)
	}

	dismissOutput, err := captureStdout(t, func() error {
		return Findings(".", []string{"dismiss", "--repo", repoSlug, "--finding", "finding-1"})
	})
	if err != nil {
		t.Fatalf("findings dismiss: %v", err)
	}
	if !strings.Contains(dismissOutput, "Dismissed finding-1") {
		t.Fatalf("unexpected dismiss output: %s", dismissOutput)
	}

	state, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("read start state after promote/dismiss: %v", err)
	}
	if state.Findings["finding-1"].Status != startWorkFindingStatusDismissed || state.Findings["finding-1"].PromotedTaskID == "" {
		t.Fatalf("unexpected finding state after promote+dismiss: %+v", state.Findings["finding-1"])
	}

	fakeBin := filepath.Join(home, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	editorPath := filepath.Join(fakeBin, "editor-noop")
	writeFindingsTestExecutable(t, editorPath, "#!/bin/sh\nexit 0\n")
	t.Setenv("EDITOR", editorPath)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	session := startWorkFindingImportSession{
		ID:               "import-review-1",
		RepoSlug:         repoSlug,
		InputFilePath:    "notes.md",
		MarkdownSnapshot: "# Findings\n\nRetry wording",
		ParseStatus:      startWorkFindingImportParseParsed,
		Candidates: []startWorkFindingImportCandidate{{
			CandidateID: "cand-1",
			Title:       "Clarify retry wording",
			Summary:     "The retry wording should be explicit.",
			Detail:      "Use explicit retry wording in the queue and drawer.",
			Severity:    "medium",
			WorkType:    workTypeFeature,
			Status:      startWorkFindingCandidateStatusCandidate,
		}},
		CreatedAt: "2026-04-22T11:10:00Z",
		UpdatedAt: "2026-04-22T11:10:00Z",
	}
	if err := writeStartWorkFindingImportSessionArtifacts(&session); err != nil {
		t.Fatalf("write session artifacts: %v", err)
	}
	state.ImportSessions = map[string]startWorkFindingImportSession{session.ID: session}
	state.UpdatedAt = "2026-04-22T11:10:00Z"
	if err := writeStartWorkState(*state); err != nil {
		t.Fatalf("rewrite start state with import session: %v", err)
	}

	reviewOutput, err := captureStdout(t, func() error {
		return Findings(".", []string{"import", "review", "--repo", repoSlug, "--session", session.ID, "--promote", "none", "--drop", "none"})
	})
	if err != nil {
		t.Fatalf("findings import review: %v", err)
	}
	if !strings.Contains(reviewOutput, "Reviewed import session "+session.ID) {
		t.Fatalf("unexpected import review output: %s", reviewOutput)
	}

	updatedState, err := readStartWorkState(repoSlug)
	if err != nil {
		t.Fatalf("read start state after review: %v", err)
	}
	if len(updatedState.ImportSessions) != 1 {
		t.Fatalf("expected same persisted import session after review, got %+v", updatedState.ImportSessions)
	}
	if _, ok := updatedState.ImportSessions[session.ID]; !ok {
		t.Fatalf("expected session %s to remain persisted", session.ID)
	}
}

func writeFindingsTestExecutable(t testing.TB, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}
