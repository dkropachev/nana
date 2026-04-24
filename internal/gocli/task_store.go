package gocli

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

type canonicalTaskRecord struct {
	Summary      startUITaskSummary
	ExecutorKind string
	QueueClass   string
	SourceKind   string
	SourceRef    string
	Payload      map[string]any
}

func listCanonicalTasks(cwd string) ([]startUITaskSummary, error) {
	items, err := withLocalWorkReadStore(func(store *localWorkDBStore) ([]startUITaskSummary, error) {
		return store.listCanonicalTasks()
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(items, compareStartUITaskSummary)
	return items, nil
}

func syncCanonicalTasks(cwd string) error {
	records, err := collectCanonicalTaskRecords(cwd)
	if err != nil {
		return err
	}
	return withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		return store.replaceCanonicalTasks(records)
	})
}

func syncCanonicalRepoTasksFromState(repoSlug string, state *startWorkState) error {
	if strings.TrimSpace(repoSlug) == "" || state == nil {
		return nil
	}
	records := collectCanonicalTaskRecordsForRepoState(repoSlug, state)
	return withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		return store.replaceCanonicalRepoTasks(repoSlug, records)
	})
}

func syncCanonicalWorkItemTask(item workItem) error {
	record := canonicalTaskRecordForWorkItem(item)
	return withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		return store.upsertCanonicalTaskRecord(record)
	})
}

func syncCanonicalInvestigationTask(manifest investigateManifest) error {
	record, ok, err := canonicalTaskRecordForInvestigation(manifest)
	if err != nil || !ok {
		return err
	}
	return withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		return store.upsertCanonicalTaskRecord(record)
	})
}

func syncCanonicalLocalWorkRunTask(manifest localWorkManifest) error {
	record, ok, err := canonicalTaskRecordForLocalWorkRun(manifest)
	if err != nil || !ok {
		return err
	}
	return withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		return store.upsertCanonicalTaskRecord(record)
	})
}

func syncCanonicalGithubWorkRunTask(manifestPath string, manifest githubWorkManifest) error {
	record, ok, err := canonicalTaskRecordForGithubWorkRun(manifestPath, manifest)
	if err != nil || !ok {
		return err
	}
	return withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		return store.upsertCanonicalTaskRecord(record)
	})
}

func collectCanonicalTaskRecords(cwd string) ([]canonicalTaskRecord, error) {
	repos, err := listStartUIRepoSummaries(true)
	if err != nil {
		return nil, err
	}
	investigations, err := listStartUIInvestigations(cwd)
	if err != nil {
		return nil, err
	}
	workRuns, err := loadStartUIWorkRuns(200)
	if err != nil {
		return nil, err
	}
	workItems, err := loadStartUIWorkItems(200, true, false)
	if err != nil {
		return nil, err
	}

	records := make([]canonicalTaskRecord, 0, len(investigations)+len(workItems)+len(workRuns)+len(repos)*8)
	linkedRunIDs := map[string]bool{}

	for _, repo := range repos {
		if repo.State == nil {
			continue
		}
		repoRecords := collectCanonicalTaskRecordsForRepoState(repo.RepoSlug, repo.State)
		for _, record := range repoRecords {
			if strings.TrimSpace(record.Summary.RunID) != "" {
				linkedRunIDs[record.Summary.RunID] = true
			}
			records = append(records, record)
		}
	}

	for _, item := range investigations {
		summary := startUITaskSummaryFromInvestigation(item)
		records = append(records, canonicalTaskRecord{
			Summary:      summary,
			ExecutorKind: "investigation",
			QueueClass:   "execution",
			SourceKind:   "investigation",
			SourceRef:    item.RunID,
			Payload: map[string]any{
				"query":  item.Query,
				"status": item.Status,
			},
		})
	}
	for _, item := range workItems {
		summary := startUITaskSummaryFromWorkItem(item)
		if strings.TrimSpace(summary.RunID) != "" {
			linkedRunIDs[summary.RunID] = true
		}
		records = append(records, canonicalTaskRecord{
			Summary:      summary,
			ExecutorKind: "work_item",
			QueueClass:   "execution",
			SourceKind:   "work_item",
			SourceRef:    item.ID,
			Payload: map[string]any{
				"status":      item.Status,
				"source_kind": item.SourceKind,
			},
		})
	}
	for _, run := range workRuns {
		if linkedRunIDs[strings.TrimSpace(run.RunID)] {
			continue
		}
		records = append(records, canonicalTaskRecord{
			Summary:      startUITaskSummaryFromWorkRun(run),
			ExecutorKind: defaultString(strings.TrimSpace(run.Backend), "work_run"),
			QueueClass:   "execution",
			SourceKind:   "work_run",
			SourceRef:    run.RunID,
			Payload: map[string]any{
				"backend": run.Backend,
				"status":  run.Status,
			},
		})
	}
	return records, nil
}

func canonicalTaskRecordForWorkItem(item workItem) canonicalTaskRecord {
	summary := startUITaskSummaryFromWorkItemState(item)
	return canonicalTaskRecord{
		Summary:      summary,
		ExecutorKind: "work_item",
		QueueClass:   "execution",
		SourceKind:   "work_item",
		SourceRef:    item.ID,
		Payload: map[string]any{
			"status":      item.Status,
			"source_kind": item.SourceKind,
		},
	}
}

func canonicalTaskRecordForInvestigation(manifest investigateManifest) (canonicalTaskRecord, bool, error) {
	summary, err := startUITaskSummaryFromInvestigationManifest(manifest)
	if err != nil {
		return canonicalTaskRecord{}, false, err
	}
	return canonicalTaskRecord{
		Summary:      summary,
		ExecutorKind: "investigation",
		QueueClass:   "execution",
		SourceKind:   "investigation",
		SourceRef:    manifest.RunID,
		Payload: map[string]any{
			"query":  manifest.Query,
			"status": manifest.Status,
		},
	}, true, nil
}

func canonicalTaskRecordForLocalWorkRun(manifest localWorkManifest) (canonicalTaskRecord, bool, error) {
	sourcePathIndex, err := listStartUIRepoSourcePathIndex()
	if err != nil {
		return canonicalTaskRecord{}, false, err
	}
	entry := localWorkRunIndexEntry(manifest)
	run, err := startUIWorkRunFromLocalManifest(entry, manifest, sourcePathIndex)
	if err != nil {
		return canonicalTaskRecord{}, false, err
	}
	return canonicalTaskRecord{
		Summary:      startUITaskSummaryFromWorkRun(run),
		ExecutorKind: defaultString(strings.TrimSpace(run.Backend), "work_run"),
		QueueClass:   "execution",
		SourceKind:   "work_run",
		SourceRef:    run.RunID,
		Payload: map[string]any{
			"backend": run.Backend,
			"status":  run.Status,
		},
	}, true, nil
}

func canonicalTaskRecordForGithubWorkRun(manifestPath string, manifest githubWorkManifest) (canonicalTaskRecord, bool, error) {
	entry := workRunIndexEntry{
		RunID:        manifest.RunID,
		Backend:      "github",
		RepoKey:      manifest.RepoSlug,
		RepoRoot:     strings.TrimSpace(manifest.ManagedRepoRoot),
		RepoName:     manifest.RepoName,
		RepoSlug:     manifest.RepoSlug,
		ManifestPath: manifestPath,
		UpdatedAt:    defaultString(strings.TrimSpace(manifest.UpdatedAt), ISOTimeNow()),
		TargetKind:   manifest.TargetKind,
	}
	sourcePathIndex, err := listStartUIRepoSourcePathIndex()
	if err != nil {
		return canonicalTaskRecord{}, false, err
	}
	run, err := startUIWorkRunFromIndex(entry, sourcePathIndex)
	if err != nil {
		return canonicalTaskRecord{}, false, err
	}
	return canonicalTaskRecord{
		Summary:      startUITaskSummaryFromWorkRun(run),
		ExecutorKind: defaultString(strings.TrimSpace(run.Backend), "work_run"),
		QueueClass:   "execution",
		SourceKind:   "work_run",
		SourceRef:    run.RunID,
		Payload: map[string]any{
			"backend": run.Backend,
			"status":  run.Status,
		},
	}, true, nil
}

func collectCanonicalTaskRecordsForRepoState(repoSlug string, state *startWorkState) []canonicalTaskRecord {
	if state == nil {
		return nil
	}
	records := []canonicalTaskRecord{}
	for _, issue := range state.Issues {
		if !startUIIssueShouldMaterializeTask(issue) {
			continue
		}
		summary := startUITaskSummaryFromIssue(startUIIssueQueueItemFromState(repoSlug, issue))
		records = append(records, canonicalTaskRecord{
			Summary:      summary,
			ExecutorKind: "issue",
			QueueClass:   "execution",
			SourceKind:   "start_issue",
			SourceRef:    fmt.Sprintf("%s#%d", repoSlug, issue.SourceNumber),
			Payload: map[string]any{
				"issue_key":      strconv.Itoa(issue.SourceNumber),
				"status":         issue.Status,
				"schedule_at":    issue.ScheduleAt,
				"blocked_reason": issue.BlockedReason,
			},
		})
	}
	for _, item := range state.PlannedItems {
		summary := startUITaskSummaryFromPlannedItem(item)
		records = append(records, canonicalTaskRecord{
			Summary:      summary,
			ExecutorKind: defaultString(strings.TrimSpace(item.LaunchKind), "planned_item"),
			QueueClass:   "service",
			SourceKind:   "planned_item",
			SourceRef:    item.ID,
			Payload: map[string]any{
				"launch_kind": item.LaunchKind,
				"schedule_at": item.ScheduleAt,
			},
		})
	}
	for _, job := range state.ScoutJobs {
		summary := startUITaskSummaryFromScoutJob(repoSlug, job)
		records = append(records, canonicalTaskRecord{
			Summary:      summary,
			ExecutorKind: "scout_job",
			QueueClass:   "execution",
			SourceKind:   "scout_job",
			SourceRef:    repoSlug + ":" + job.ID,
			Payload: map[string]any{
				"role":          job.Role,
				"artifact_path": job.ArtifactPath,
				"status":        job.Status,
			},
		})
	}
	for _, task := range state.ServiceTasks {
		if strings.TrimSpace(task.Status) == startWorkServiceTaskCompleted {
			continue
		}
		if strings.TrimSpace(task.Kind) == startTaskKindPlannedLaunch {
			continue
		}
		summary := startUITaskSummaryFromServiceTask(repoSlug, state, task)
		records = append(records, canonicalTaskRecord{
			Summary:      summary,
			ExecutorKind: "service_task",
			QueueClass:   "service",
			SourceKind:   "service_task",
			SourceRef:    repoSlug + ":" + task.ID,
			Payload: map[string]any{
				"kind":       task.Kind,
				"issue_key":  task.IssueKey,
				"planned_id": task.PlannedItemID,
				"wait_until": task.WaitUntil,
			},
		})
	}
	return records
}

func startUIIssueShouldMaterializeTask(issue startWorkIssueState) bool {
	switch strings.TrimSpace(issue.Status) {
	case startWorkStatusQueued, startWorkStatusInProgress, startWorkStatusReconciling, startWorkStatusBlocked:
		return true
	default:
		return false
	}
}

func (s *localWorkDBStore) replaceCanonicalTasks(records []canonicalTaskRecord) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM tasks`); err != nil {
		return err
	}
	for _, record := range records {
		payloadJSON, err := marshalNullableJSON(record.Payload)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO tasks(
				id, repo_slug, kind, executor_kind, queue_class, status, raw_status, priority, scheduled_at,
				title, summary, description, external_url, work_type, run_id, item_id, source_kind, source_ref,
				payload_json, created_at, updated_at
			) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			record.Summary.ID,
			nullableString(record.Summary.RepoSlug),
			record.Summary.Kind,
			record.ExecutorKind,
			record.QueueClass,
			record.Summary.Status,
			nullableString(record.Summary.RawStatus),
			clampTaskPriority(record.Summary.Priority),
			nullableString(record.Summary.ScheduleAt),
			record.Summary.Title,
			nullableString(record.Summary.Summary),
			nullableString(record.Summary.Description),
			nullableString(record.Summary.ExternalURL),
			nullableString(record.Summary.WorkType),
			nullableString(record.Summary.RunID),
			nullableString(record.Summary.ID),
			record.SourceKind,
			record.SourceRef,
			defaultString(payloadJSON, "{}"),
			defaultString(record.Summary.CreatedAt, ISOTimeNow()),
			defaultString(record.Summary.UpdatedAt, defaultString(record.Summary.CreatedAt, ISOTimeNow())),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *localWorkDBStore) upsertCanonicalTaskRecord(record canonicalTaskRecord) error {
	payloadJSON, err := marshalNullableJSON(record.Payload)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO tasks(
			id, repo_slug, kind, executor_kind, queue_class, status, raw_status, priority, scheduled_at,
			title, summary, description, external_url, work_type, run_id, item_id, source_kind, source_ref,
			payload_json, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			repo_slug=excluded.repo_slug,
			kind=excluded.kind,
			executor_kind=excluded.executor_kind,
			queue_class=excluded.queue_class,
			status=excluded.status,
			raw_status=excluded.raw_status,
			priority=excluded.priority,
			scheduled_at=excluded.scheduled_at,
			title=excluded.title,
			summary=excluded.summary,
			description=excluded.description,
			external_url=excluded.external_url,
			work_type=excluded.work_type,
			run_id=excluded.run_id,
			item_id=excluded.item_id,
			source_kind=excluded.source_kind,
			source_ref=excluded.source_ref,
			payload_json=excluded.payload_json,
			created_at=excluded.created_at,
			updated_at=excluded.updated_at`,
		record.Summary.ID,
		nullableString(record.Summary.RepoSlug),
		record.Summary.Kind,
		record.ExecutorKind,
		record.QueueClass,
		record.Summary.Status,
		nullableString(record.Summary.RawStatus),
		clampTaskPriority(record.Summary.Priority),
		nullableString(record.Summary.ScheduleAt),
		record.Summary.Title,
		nullableString(record.Summary.Summary),
		nullableString(record.Summary.Description),
		nullableString(record.Summary.ExternalURL),
		nullableString(record.Summary.WorkType),
		nullableString(record.Summary.RunID),
		nullableString(record.Summary.ID),
		record.SourceKind,
		record.SourceRef,
		defaultString(payloadJSON, "{}"),
		defaultString(record.Summary.CreatedAt, ISOTimeNow()),
		defaultString(record.Summary.UpdatedAt, defaultString(record.Summary.CreatedAt, ISOTimeNow())),
	)
	return err
}

func (s *localWorkDBStore) replaceCanonicalRepoTasks(repoSlug string, records []canonicalTaskRecord) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM tasks WHERE repo_slug = ? AND source_kind IN ('start_issue', 'planned_item', 'scout_job', 'service_task')`, repoSlug); err != nil {
		return err
	}
	for _, record := range records {
		payloadJSON, err := marshalNullableJSON(record.Payload)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO tasks(
				id, repo_slug, kind, executor_kind, queue_class, status, raw_status, priority, scheduled_at,
				title, summary, description, external_url, work_type, run_id, item_id, source_kind, source_ref,
				payload_json, created_at, updated_at
			) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			record.Summary.ID,
			nullableString(record.Summary.RepoSlug),
			record.Summary.Kind,
			record.ExecutorKind,
			record.QueueClass,
			record.Summary.Status,
			nullableString(record.Summary.RawStatus),
			clampTaskPriority(record.Summary.Priority),
			nullableString(record.Summary.ScheduleAt),
			record.Summary.Title,
			nullableString(record.Summary.Summary),
			nullableString(record.Summary.Description),
			nullableString(record.Summary.ExternalURL),
			nullableString(record.Summary.WorkType),
			nullableString(record.Summary.RunID),
			nullableString(record.Summary.ID),
			record.SourceKind,
			record.SourceRef,
			defaultString(payloadJSON, "{}"),
			defaultString(record.Summary.CreatedAt, ISOTimeNow()),
			defaultString(record.Summary.UpdatedAt, defaultString(record.Summary.CreatedAt, ISOTimeNow())),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *localWorkDBStore) listCanonicalTasks() ([]startUITaskSummary, error) {
	rows, err := s.db.Query(`SELECT id, kind, repo_slug, title, summary, description, status, raw_status, priority, scheduled_at, created_at, updated_at, external_url, work_type, run_id FROM tasks`)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	items := []startUITaskSummary{}
	for rows.Next() {
		var item startUITaskSummary
		var repoSlug sql.NullString
		var summary sql.NullString
		var description sql.NullString
		var rawStatus sql.NullString
		var scheduleAt sql.NullString
		var createdAt sql.NullString
		var updatedAt sql.NullString
		var externalURL sql.NullString
		var workType sql.NullString
		var runID sql.NullString
		if err := rows.Scan(
			&item.ID,
			&item.Kind,
			&repoSlug,
			&item.Title,
			&summary,
			&description,
			&item.Status,
			&rawStatus,
			&item.Priority,
			&scheduleAt,
			&createdAt,
			&updatedAt,
			&externalURL,
			&workType,
			&runID,
		); err != nil {
			return nil, err
		}
		item.RepoSlug = strings.TrimSpace(repoSlug.String)
		item.Summary = strings.TrimSpace(summary.String)
		item.Description = strings.TrimSpace(description.String)
		item.RawStatus = strings.TrimSpace(rawStatus.String)
		item.ScheduleAt = strings.TrimSpace(scheduleAt.String)
		item.CreatedAt = strings.TrimSpace(createdAt.String)
		item.UpdatedAt = strings.TrimSpace(updatedAt.String)
		item.ExternalURL = strings.TrimSpace(externalURL.String)
		item.WorkType = strings.TrimSpace(workType.String)
		item.RunID = strings.TrimSpace(runID.String)
		normalizeCanonicalTaskSummaryFromStore(&item)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *localWorkDBStore) listCanonicalTasksForRepo(repoSlug string, statuses ...string) ([]startUITaskSummary, error) {
	query := `SELECT id, kind, repo_slug, title, summary, description, status, raw_status, priority, scheduled_at, created_at, updated_at, external_url, work_type, run_id FROM tasks WHERE repo_slug = ?`
	args := []any{repoSlug}
	if len(statuses) > 0 {
		placeholders := make([]string, 0, len(statuses))
		for _, status := range statuses {
			placeholders = append(placeholders, "?")
			args = append(args, status)
		}
		query += ` AND status IN (` + strings.Join(placeholders, ",") + `)`
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []startUITaskSummary{}
	for rows.Next() {
		var item startUITaskSummary
		var repoSlugValue sql.NullString
		var summary sql.NullString
		var description sql.NullString
		var rawStatus sql.NullString
		var scheduleAt sql.NullString
		var createdAt sql.NullString
		var updatedAt sql.NullString
		var externalURL sql.NullString
		var workType sql.NullString
		var runID sql.NullString
		if err := rows.Scan(
			&item.ID,
			&item.Kind,
			&repoSlugValue,
			&item.Title,
			&summary,
			&description,
			&item.Status,
			&rawStatus,
			&item.Priority,
			&scheduleAt,
			&createdAt,
			&updatedAt,
			&externalURL,
			&workType,
			&runID,
		); err != nil {
			return nil, err
		}
		item.RepoSlug = strings.TrimSpace(repoSlugValue.String)
		item.Summary = strings.TrimSpace(summary.String)
		item.Description = strings.TrimSpace(description.String)
		item.RawStatus = strings.TrimSpace(rawStatus.String)
		item.ScheduleAt = strings.TrimSpace(scheduleAt.String)
		item.CreatedAt = strings.TrimSpace(createdAt.String)
		item.UpdatedAt = strings.TrimSpace(updatedAt.String)
		item.ExternalURL = strings.TrimSpace(externalURL.String)
		item.WorkType = strings.TrimSpace(workType.String)
		item.RunID = strings.TrimSpace(runID.String)
		normalizeCanonicalTaskSummaryFromStore(&item)
		items = append(items, item)
	}
	slices.SortFunc(items, compareStartUITaskSummary)
	return items, rows.Err()
}

func normalizeCanonicalTaskSummaryFromStore(item *startUITaskSummary) {
	if item == nil {
		return
	}
	if strings.TrimSpace(item.Kind) == "work_run" && strings.TrimSpace(item.RawStatus) == "failed" {
		item.Status = startUITaskStatusFailed
	}
	item.CanOpenJob = strings.TrimSpace(item.RunID) != ""
	item.PriorityLabel = startWorkPriorityLabel(item.Priority)
	item.AttentionState = startUITaskAttentionStateForStatus(item.Status)
}

func syncTaskTemplatesFromState(repoSlug string) error {
	if strings.TrimSpace(repoSlug) == "" {
		return nil
	}
	state, err := readStartWorkState(repoSlug)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not exist") {
			return nil
		}
		return err
	}
	if len(state.TaskTemplates) == 0 {
		return nil
	}
	return withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		for _, template := range state.TaskTemplates {
			if err := store.writeTaskTemplate(repoSlug, template); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *localWorkDBStore) writeTaskTemplate(repoSlug string, template startWorkTaskTemplate) error {
	now := defaultString(strings.TrimSpace(template.UpdatedAt), ISOTimeNow())
	createdAt := defaultString(strings.TrimSpace(template.CreatedAt), now)
	_, err := s.db.Exec(
		`INSERT INTO task_templates(id, repo_slug, name, description, launch_kind_hint, scout_role_hint, work_type_hint, default_priority, built_in, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   repo_slug=excluded.repo_slug,
		   name=excluded.name,
		   description=excluded.description,
		   launch_kind_hint=excluded.launch_kind_hint,
		   scout_role_hint=excluded.scout_role_hint,
		   work_type_hint=excluded.work_type_hint,
		   default_priority=excluded.default_priority,
		   built_in=excluded.built_in,
		   created_at=excluded.created_at,
		   updated_at=excluded.updated_at`,
		template.ID,
		repoSlug,
		template.Name,
		nullableString(template.Description),
		nullableString(template.LaunchKindHint),
		nullableString(template.ScoutRoleHint),
		nullableString(template.WorkTypeHint),
		clampTaskPriority(template.DefaultPriority),
		0,
		createdAt,
		now,
	)
	return err
}

func (s *localWorkDBStore) listTaskTemplates(repoSlug string) ([]startWorkTaskTemplate, error) {
	rows, err := s.db.Query(`SELECT id, name, description, launch_kind_hint, scout_role_hint, work_type_hint, default_priority, created_at, updated_at FROM task_templates WHERE repo_slug = ? ORDER BY name ASC, id ASC`, repoSlug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []startWorkTaskTemplate{}
	for rows.Next() {
		var item startWorkTaskTemplate
		var description sql.NullString
		var launchKindHint sql.NullString
		var scoutRoleHint sql.NullString
		var workTypeHint sql.NullString
		if err := rows.Scan(
			&item.ID,
			&item.Name,
			&description,
			&launchKindHint,
			&scoutRoleHint,
			&workTypeHint,
			&item.DefaultPriority,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		item.Description = strings.TrimSpace(description.String)
		item.LaunchKindHint = strings.TrimSpace(launchKindHint.String)
		item.ScoutRoleHint = strings.TrimSpace(scoutRoleHint.String)
		item.WorkTypeHint = strings.TrimSpace(workTypeHint.String)
		items = append(items, item)
	}
	return items, rows.Err()
}
