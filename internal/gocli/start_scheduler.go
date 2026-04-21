package gocli

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	startTaskQueueService        = "service"
	startTaskQueueImplementation = "implementation"

	startTaskKindIssueSync     = "issue-sync"
	startTaskKindScout         = "scout"
	startTaskKindTriage        = "triage"
	startTaskKindIssue         = "implementation"
	startTaskKindScoutJob      = "scout-job"
	startTaskKindReconcile     = "reconcile"
	startTaskKindPlannedLaunch = "planned-launch"
)

type startRepoTask struct {
	Key           string
	Queue         string
	Kind          string
	IssueKey      string
	Issue         startWorkIssueState
	ScoutJobID    string
	ScoutJob      startWorkScoutJob
	PlannedItemID string
	PlannedItem   startWorkPlannedItem
	ScoutRole     string
	RunID         string
}

type startWorkReconcileResult struct {
	Status            string
	BlockedReason     string
	RunID             string
	PublishedPRNumber int
	PublishedPRURL    string
	PublicationState  string
	ShouldRetry       bool
}

type startRepoTaskResult struct {
	Task          startRepoTask
	Triage        *startWorkTriageResult
	Reconcile     *startWorkReconcileResult
	Launch        *startWorkLaunchResult
	PlannedLaunch *startPlannedLaunchResult
	Err           error
}

type startRepoTaskCompletion struct {
	repoSlug string
	result   startRepoTaskResult
}

type startRepoCoordinator struct {
	cwd                 string
	repoSlug            string
	cycleID             string
	globalOptions       startOptions
	workOptions         startWorkOptions
	state               *startWorkState
	openPRs             int
	running             map[string]startRepoTask
	scoutRoles          []string
	nextQueue           string
	refreshState        bool
	startedIssueNumbers []int
	serviceStartedCount int
	implStartedCount    int
	lastSkippedReason   string
}

func (c *startRepoCoordinator) withPersistedStateLock(fn func() error) error {
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	if strings.TrimSpace(c.repoSlug) != "" {
		state, err := readStartWorkStateUnlocked(c.repoSlug)
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		} else {
			c.state = state
		}
	}
	return fn()
}

var startRunIssueTriage = func(repoSlug string, issue startWorkIssueState, codexArgs []string) (startWorkTriageResult, error) {
	return runStartWorkIssueTriage(repoSlug, issue, codexArgs)
}

var startRunScoutRole = func(cwd string, options ImproveOptions, role string) error {
	options.RateLimitPolicy = codexRateLimitPolicyReturnPause
	return runScout(cwd, options, role)
}

var startRunIssueReconcile = func(repoSlug string, publishTarget string, issue startWorkIssueState) (startWorkReconcileResult, error) {
	return runStartWorkIssueReconcile(repoSlug, publishTarget, issue)
}

var startSyncRepoState = startWorkSyncRepoState

var startRunIssueSync = func(options startWorkOptions) error {
	_, state, _, _, err := startSyncRepoState(options)
	if err != nil {
		return err
	}
	return writeStartWorkStatePreservingPlannedItems(*state)
}

var startRunPlannedItemLaunch = func(cwd string, repoSlug string, workOptions startWorkOptions, item startWorkPlannedItem) (startPlannedLaunchResult, error) {
	return startLaunchScheduledPlannedItem(cwd, repoSlug, workOptions, item)
}

var startRunScoutJobLaunch = func(repoSlug string, job startWorkScoutJob, codexArgs []string) (startWorkLaunchResult, error) {
	return runStartWorkScoutJob(repoSlug, job, codexArgs)
}

func runStartRepoCyclesSharedWorkers(cwd string, repos []string, options startOptions) error {
	maxWorkers := options.Parallel
	if maxWorkers <= 0 {
		maxWorkers = 1
	}
	errs := []string{}
	repoOrder := []string{}
	coordinators := map[string]*startRepoCoordinator{}
	for _, repoSlug := range repos {
		prepared, err := prepareStartRepoCycle(repoSlug, options)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", repoSlug, err))
			continue
		}
		if prepared == nil {
			continue
		}
		repoOrder = append(repoOrder, repoSlug)
		coordinators[repoSlug] = newStartRepoCoordinator(cwd, repoSlug, prepared.workOptions, options)
	}
	results := make(chan startRepoTaskCompletion, maxWorkers)
	running := 0
	nextRepoIndex := 0
	for {
		for running < maxWorkers {
			repoSlug, coordinator, task, ok, err := nextStartRepoTask(repoOrder, coordinators, nextRepoIndex)
			if err != nil {
				errs = append(errs, err.Error())
				delete(coordinators, repoSlug)
				repoOrder = slices.DeleteFunc(repoOrder, func(candidate string) bool {
					return candidate == repoSlug
				})
				if nextRepoIndex >= len(repoOrder) {
					nextRepoIndex = 0
				}
				continue
			}
			if !ok {
				break
			}
			nextRepoIndex = nextStartRepoIndex(repoOrder, repoSlug)
			if err := coordinator.markTaskStarted(task); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", repoSlug, err))
				delete(coordinators, repoSlug)
				repoOrder = slices.DeleteFunc(repoOrder, func(candidate string) bool {
					return candidate == repoSlug
				})
				if nextRepoIndex >= len(repoOrder) {
					nextRepoIndex = 0
				}
				continue
			}
			coordinator.running[task.Key] = task
			coordinator.launchTaskCompletion(task, results)
			running++
		}
		if running == 0 {
			break
		}
		completion := <-results
		coordinator := coordinators[completion.repoSlug]
		delete(coordinator.running, completion.result.Task.Key)
		if err := coordinator.applyTaskResult(completion.result); err != nil {
			errs = append(errs, err.Error())
		}
		running--
	}
	for _, repoSlug := range repoOrder {
		if err := coordinators[repoSlug].completeRun(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", repoSlug, err))
		}
		if err := finalizeStartRepoCycle(repoSlug, options); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", repoSlug, err))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	slices.Sort(errs)
	return fmt.Errorf("start repo failures:\n%s", strings.Join(errs, "\n"))
}

func nextStartRepoTask(repoOrder []string, coordinators map[string]*startRepoCoordinator, startIndex int) (string, *startRepoCoordinator, startRepoTask, bool, error) {
	if len(repoOrder) == 0 {
		return "", nil, startRepoTask{}, false, nil
	}
	for offset := 0; offset < len(repoOrder); offset++ {
		repoSlug := repoOrder[(startIndex+offset)%len(repoOrder)]
		coordinator := coordinators[repoSlug]
		if coordinator == nil {
			continue
		}
		task, ok, err := coordinator.nextTask()
		if err != nil {
			return repoSlug, coordinator, startRepoTask{}, false, fmt.Errorf("%s: %w", repoSlug, err)
		}
		if ok {
			return repoSlug, coordinator, task, true, nil
		}
	}
	return "", nil, startRepoTask{}, false, nil
}

func nextStartRepoIndex(repoOrder []string, repoSlug string) int {
	if len(repoOrder) == 0 {
		return 0
	}
	for index, candidate := range repoOrder {
		if candidate == repoSlug {
			return (index + 1) % len(repoOrder)
		}
	}
	return 0
}

func newStartRepoCoordinator(cwd string, repoSlug string, workOptions startWorkOptions, options startOptions) *startRepoCoordinator {
	return &startRepoCoordinator{
		cwd:           cwd,
		repoSlug:      repoSlug,
		cycleID:       strconv.FormatInt(time.Now().UnixNano(), 10),
		globalOptions: options,
		workOptions:   workOptions,
		running:       map[string]startRepoTask{},
		nextQueue:     startTaskQueueService,
		refreshState:  true,
	}
}

func runStartRepoSchedulerCycle(cwd string, repoSlug string, workOptions startWorkOptions, options startOptions) error {
	coordinator := newStartRepoCoordinator(cwd, repoSlug, workOptions, options)
	return coordinator.run()
}

func (c *startRepoCoordinator) run() error {
	if err := c.refreshRepoState(); err != nil {
		return err
	}
	results := make(chan startRepoTaskResult, max(c.workOptions.Parallel, 1))
	errs := []string{}
	for {
		if err := c.reconcileStaleLocalRuns(); err != nil {
			return err
		}
		if c.refreshState {
			if err := c.refreshRepoState(); err != nil {
				return err
			}
		} else {
			if err := c.reloadPersistedState(); err != nil {
				return err
			}
		}
		serviceQueue := c.buildServiceQueue()
		implementationQueue, skippedReason := c.buildImplementationQueue()
		if len(serviceQueue) == 0 && skippedReason != "" {
			c.lastSkippedReason = skippedReason
		}
		available := c.availableWorkerSlots()
		for available > 0 {
			task, remainingService, remainingImpl, ok := c.selectNextTask(serviceQueue, implementationQueue)
			if !ok {
				break
			}
			serviceQueue = remainingService
			implementationQueue = remainingImpl
			if err := c.markTaskStarted(task); err != nil {
				return err
			}
			c.running[task.Key] = task
			c.launchTask(task, results)
			available--
		}
		if len(c.running) == 0 {
			break
		}
		result := <-results
		delete(c.running, result.Task.Key)
		if err := c.applyTaskResult(result); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if err := c.completeRun(); err != nil {
		return err
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("repo task failures:\n%s", strings.Join(errs, "\n"))
}

func (c *startRepoCoordinator) reloadPersistedState() error {
	state, err := readStartWorkState(c.repoSlug)
	if err != nil {
		return err
	}
	c.state = state
	return nil
}

func (c *startRepoCoordinator) refreshRepoState() error {
	updatedOptions, state, openPRs, _, err := startSyncRepoState(c.workOptions)
	if err != nil {
		return err
	}
	c.workOptions = updatedOptions
	c.state = state
	c.openPRs = openPRs
	if persisted, persistErr := readStartWorkState(c.repoSlug); persistErr == nil {
		if c.state.PlannedItems == nil {
			c.state.PlannedItems = map[string]startWorkPlannedItem{}
		}
		for itemID, item := range persisted.PlannedItems {
			if _, ok := c.state.PlannedItems[itemID]; !ok {
				c.state.PlannedItems[itemID] = item
			}
		}
		if c.state.ScoutJobs == nil {
			c.state.ScoutJobs = map[string]startWorkScoutJob{}
		}
		for jobID, job := range persisted.ScoutJobs {
			if _, ok := c.state.ScoutJobs[jobID]; !ok {
				c.state.ScoutJobs[jobID] = job
			}
		}
	} else if !os.IsNotExist(persistErr) {
		return persistErr
	}
	if repoPath := strings.TrimSpace(githubManagedPaths(c.repoSlug).SourcePath); repoPath != "" {
		if syncErr := withSourceReadLock(repoPath, repoAccessLockOwner{
			Backend: "start-scheduler",
			RunID:   sanitizePathToken(c.repoSlug),
			Purpose: "sync-scout-jobs",
			Label:   "start-sync-scout-jobs",
		}, func() error {
			_, err := syncStartWorkScoutJobsIntoState(repoPath, c.state)
			return err
		}); syncErr != nil {
			return syncErr
		}
	}
	c.scoutRoles, err = startRepoDueScoutRoles(c.repoSlug, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := c.syncServiceTasks(); err != nil {
		return err
	}
	c.refreshState = false
	return nil
}

func (c *startRepoCoordinator) reconcileStaleLocalRuns() error {
	cleaned, manifests, err := cleanupStaleLocalWorkRunsForRepoDetailed(githubManagedPaths(c.repoSlug).SourcePath)
	if err != nil {
		return err
	}
	handled := map[string]bool{}
	if cleaned > 0 {
		if err := c.withPersistedStateLock(func() error {
			resumed, requeued, recoveredRunIDs, updated, err := recoverStartWorkScoutJobsFromStaleManifests(c.repoSlug, c.state, manifests, c.workOptions.CodexArgs)
			if err != nil {
				return err
			}
			for runID := range recoveredRunIDs {
				handled[runID] = true
			}
			if !updated {
				return nil
			}
			c.state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			if err := writeStartWorkStateUnlocked(*c.state); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "[start] %s: recovered stale scout jobs resumed=%d requeued=%d\n", c.repoSlug, resumed, requeued)
			return nil
		}); err != nil {
			return err
		}
	}
	for _, manifest := range manifests {
		if handled[strings.TrimSpace(manifest.RunID)] {
			continue
		}
		fmt.Fprintf(
			os.Stdout,
			"[start] %s: stale local work run %s marked failed unexpectedly (phase=%s updated_at=%s reason=%s).\n",
			c.repoSlug,
			manifest.RunID,
			defaultString(strings.TrimSpace(manifest.CurrentPhase), "-"),
			defaultString(strings.TrimSpace(manifest.UpdatedAt), "-"),
			defaultString(strings.TrimSpace(manifest.LastError), "-"),
		)
	}
	if cleaned > 0 {
		c.refreshState = true
	}
	return nil
}

func (c *startRepoCoordinator) nextTask() (startRepoTask, bool, error) {
	if c.refreshState {
		if err := c.refreshRepoState(); err != nil {
			return startRepoTask{}, false, err
		}
	} else {
		if err := c.reloadPersistedState(); err != nil {
			return startRepoTask{}, false, err
		}
	}
	serviceQueue := c.buildServiceQueue()
	implementationQueue, skippedReason := c.buildImplementationQueue()
	if len(serviceQueue) == 0 && skippedReason != "" {
		c.lastSkippedReason = skippedReason
	}
	task, _, _, ok := c.selectNextTask(serviceQueue, implementationQueue)
	return task, ok, nil
}

func (c *startRepoCoordinator) syncServiceTasks() error {
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	if strings.TrimSpace(c.repoSlug) != "" {
		persisted, err := readStartWorkStateUnlocked(c.repoSlug)
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		} else {
			if c.state.PlannedItems == nil {
				c.state.PlannedItems = map[string]startWorkPlannedItem{}
			}
			for itemID, item := range persisted.PlannedItems {
				if _, ok := c.state.PlannedItems[itemID]; !ok {
					c.state.PlannedItems[itemID] = item
				}
			}
		}
	}
	if c.state.ServiceTasks == nil {
		c.state.ServiceTasks = map[string]startWorkServiceTask{}
	}
	nowTime := time.Now().UTC()
	now := nowTime.Format(time.RFC3339)
	mutated := false
	dueScoutRoles := map[string]bool{}
	for _, role := range c.scoutRoles {
		dueScoutRoles[role] = true
	}
	for key, task := range c.state.ServiceTasks {
		if task.Status == startWorkServiceTaskRunning {
			task.Status = startWorkServiceTaskQueued
			task.StartedAt = ""
			task.UpdatedAt = now
			c.state.ServiceTasks[key] = task
			mutated = true
		}
		if task.Kind == startTaskKindScout && task.Status == startWorkServiceTaskQueued && !dueScoutRoles[task.ScoutRole] {
			task.Status = startWorkServiceTaskCompleted
			task.ResultSummary = "schedule_wait"
			task.WaitCycle = ""
			task.WaitUntil = ""
			task.StartedAt = ""
			task.CompletedAt = now
			task.UpdatedAt = now
			task.Generation = c.cycleID
			c.state.ServiceTasks[key] = task
			mutated = true
		}
		if task.Kind == startTaskKindPlannedLaunch {
			item, ok := c.state.PlannedItems[task.PlannedItemID]
			if !ok {
				delete(c.state.ServiceTasks, key)
				mutated = true
				continue
			}
			if startWorkPlannedItemLooksScoutDerived(item) {
				if strings.TrimSpace(item.State) == startPlannedItemLaunching {
					continue
				}
				delete(c.state.ServiceTasks, key)
				mutated = true
				continue
			}
			if strings.TrimSpace(item.State) == startPlannedItemLaunching {
				continue
			}
			if !startWorkPlannedItemDue(item, nowTime) {
				delete(c.state.ServiceTasks, key)
				mutated = true
			}
		}
	}
	syncTaskID := startServiceTaskKey(startTaskKindIssueSync, c.cycleID)
	issueKeys := make([]string, 0, len(c.state.Issues))
	for issueKey := range c.state.Issues {
		issueKeys = append(issueKeys, issueKey)
	}
	slices.SortFunc(issueKeys, compareStartIssueKeys)
	scoutTaskKeys := []string{}
	for _, role := range c.scoutRoles {
		scoutTaskID := startServiceTaskKey(startTaskKindScout, role)
		scoutTaskKeys = append(scoutTaskKeys, scoutTaskID)
		if c.upsertServiceTask(startWorkServiceTask{
			ID:         scoutTaskID,
			Kind:       startTaskKindScout,
			Queue:      startTaskQueueService,
			Status:     startWorkServiceTaskQueued,
			ScoutRole:  role,
			Generation: c.cycleID,
		}, now) {
			mutated = true
		}
	}
	if len(scoutTaskKeys) > 0 {
		if c.upsertServiceTask(startWorkServiceTask{
			ID:             syncTaskID,
			Kind:           startTaskKindIssueSync,
			Queue:          startTaskQueueService,
			Status:         startWorkServiceTaskQueued,
			Generation:     c.cycleID,
			DependencyKeys: append([]string{}, scoutTaskKeys...),
		}, now) {
			mutated = true
		}
	}
	for _, issueKey := range issueKeys {
		issue := c.state.Issues[issueKey]
		if c.shouldQueueTriage(issueKey, issue) {
			if c.upsertServiceTask(startWorkServiceTask{
				ID:             startServiceTaskKey(startTaskKindTriage, issueKey),
				Kind:           startTaskKindTriage,
				Queue:          startTaskQueueService,
				Status:         startWorkServiceTaskQueued,
				IssueKey:       issueKey,
				Fingerprint:    issue.SourceFingerprint,
				DependencyKeys: c.triageDependencyKeys(syncTaskID),
			}, now) {
				mutated = true
			}
		}
		if c.shouldQueueReconcile(issueKey, issue) {
			if c.upsertServiceTask(startWorkServiceTask{
				ID:          startServiceTaskKey(startTaskKindReconcile, issueKey),
				Kind:        startTaskKindReconcile,
				Queue:       startTaskQueueService,
				Status:      startWorkServiceTaskQueued,
				IssueKey:    issueKey,
				Fingerprint: issue.SourceFingerprint,
				RunID:       strings.TrimSpace(issue.LastRunID),
			}, now) {
				mutated = true
			}
		}
	}
	plannedIDs := make([]string, 0, len(c.state.PlannedItems))
	for itemID := range c.state.PlannedItems {
		plannedIDs = append(plannedIDs, itemID)
	}
	slices.Sort(plannedIDs)
	for _, itemID := range plannedIDs {
		item := c.state.PlannedItems[itemID]
		if startWorkPlannedItemLooksScoutDerived(item) {
			continue
		}
		if strings.TrimSpace(item.State) != startPlannedItemLaunching && !startWorkPlannedItemDue(item, nowTime) {
			continue
		}
		if c.upsertServiceTask(startWorkServiceTask{
			ID:            startServiceTaskKey(startTaskKindPlannedLaunch, itemID),
			Kind:          startTaskKindPlannedLaunch,
			Queue:         startTaskQueueService,
			Status:        startWorkServiceTaskQueued,
			PlannedItemID: itemID,
			Fingerprint:   startWorkPlannedItemFingerprint(item),
		}, now) {
			mutated = true
		}
	}
	if mutated {
		return writeStartWorkStateUnlocked(*c.state)
	}
	return writeStartWorkStateUnlocked(*c.state)
}

func (c *startRepoCoordinator) upsertServiceTask(desired startWorkServiceTask, now string) bool {
	current := c.state.ServiceTasks[desired.ID]
	switch desired.Kind {
	case startTaskKindScout:
		if current.ID != "" && current.Generation == desired.Generation && current.Status != startWorkServiceTaskFailed {
			return false
		}
		if current.ID != "" && current.Generation == desired.Generation && current.Status == startWorkServiceTaskFailed && current.Attempts >= serviceTaskRetryLimit(desired.Kind) {
			return false
		}
	case startTaskKindIssueSync, startTaskKindTriage, startTaskKindReconcile, startTaskKindPlannedLaunch:
		if current.ID != "" && current.Status == startWorkServiceTaskRunning {
			return false
		}
		if current.ID != "" && current.Status == startWorkServiceTaskCompleted && current.Fingerprint == desired.Fingerprint && current.RunID == desired.RunID {
			return false
		}
		if current.ID != "" && current.Status == startWorkServiceTaskQueued && current.Fingerprint == desired.Fingerprint && current.RunID == desired.RunID {
			return false
		}
		if current.ID != "" && current.Status == startWorkServiceTaskFailed && current.Fingerprint == desired.Fingerprint && current.RunID == desired.RunID && current.Attempts >= serviceTaskRetryLimit(desired.Kind) {
			return false
		}
	}
	desired.Attempts = current.Attempts
	desired.LastError = current.LastError
	desired.ResultSummary = current.ResultSummary
	desired.WaitCycle = current.WaitCycle
	desired.WaitUntil = current.WaitUntil
	desired.UpdatedAt = now
	desired.StartedAt = ""
	desired.CompletedAt = ""
	c.state.ServiceTasks[desired.ID] = desired
	return true
}

func (c *startRepoCoordinator) buildServiceQueue() []startRepoTask {
	if c.state.ServiceTasks == nil {
		return nil
	}
	taskKeys := make([]string, 0, len(c.state.ServiceTasks))
	for key, task := range c.state.ServiceTasks {
		if task.Queue != startTaskQueueService || task.Status != startWorkServiceTaskQueued {
			continue
		}
		if task.WaitCycle != "" && task.WaitCycle == c.cycleID {
			continue
		}
		if taskWaitUntilPending(task, time.Now().UTC()) {
			continue
		}
		if !c.serviceTaskReady(task) {
			continue
		}
		taskKeys = append(taskKeys, key)
	}
	slices.SortFunc(taskKeys, func(a, b string) int {
		left := c.state.ServiceTasks[a]
		right := c.state.ServiceTasks[b]
		if serviceTaskPriority(left.Kind) != serviceTaskPriority(right.Kind) {
			return serviceTaskPriority(left.Kind) - serviceTaskPriority(right.Kind)
		}
		if left.IssueKey != "" || right.IssueKey != "" {
			return compareStartIssueKeys(left.IssueKey, right.IssueKey)
		}
		return strings.Compare(a, b)
	})
	queue := make([]startRepoTask, 0, len(taskKeys))
	for _, key := range taskKeys {
		task := c.state.ServiceTasks[key]
		repoTask := startRepoTask{
			Key:           task.ID,
			Queue:         task.Queue,
			Kind:          task.Kind,
			IssueKey:      task.IssueKey,
			PlannedItemID: task.PlannedItemID,
			ScoutRole:     task.ScoutRole,
			RunID:         task.RunID,
		}
		if task.IssueKey != "" {
			repoTask.Issue = c.state.Issues[task.IssueKey]
		}
		if task.PlannedItemID != "" {
			repoTask.PlannedItem = c.state.PlannedItems[task.PlannedItemID]
		}
		queue = append(queue, repoTask)
	}
	return queue
}

func (c *startRepoCoordinator) buildImplementationQueue() ([]startRepoTask, string) {
	issues, skippedReason := startWorkBuildImplementationQueue(c.state, c.workOptions, c.openPRs)
	tasks := make([]startRepoTask, 0, len(issues))
	for _, issue := range issues {
		issueKey := fmt.Sprintf("%d", issue.SourceNumber)
		taskKey := startServiceTaskKey(startTaskKindIssue, issueKey)
		if c.running[taskKey].Key != "" {
			continue
		}
		tasks = append(tasks, startRepoTask{
			Key:      taskKey,
			Queue:    startTaskQueueImplementation,
			Kind:     startTaskKindIssue,
			IssueKey: issueKey,
			Issue:    issue,
		})
	}
	scoutTasks := c.buildScoutJobQueue()
	tasks = append(tasks, scoutTasks...)
	if len(tasks) == 0 {
		return nil, skippedReason
	}
	return tasks, ""
}

func (c *startRepoCoordinator) buildScoutJobQueue() []startRepoTask {
	if c.state == nil || c.state.ScoutJobs == nil {
		return nil
	}
	jobIDs := make([]string, 0, len(c.state.ScoutJobs))
	for jobID, job := range c.state.ScoutJobs {
		if job.Status != startScoutJobQueued || job.Destination != improvementDestinationLocal {
			continue
		}
		if strings.TrimSpace(job.WorkType) == "" {
			continue
		}
		if scoutJobPausePending(job, time.Now().UTC()) {
			continue
		}
		taskKey := startServiceTaskKey(startTaskKindScoutJob, jobID)
		if c.running[taskKey].Key != "" {
			continue
		}
		jobIDs = append(jobIDs, jobID)
	}
	slices.SortFunc(jobIDs, func(leftID, rightID string) int {
		left := c.state.ScoutJobs[leftID]
		right := c.state.ScoutJobs[rightID]
		if left.UpdatedAt != right.UpdatedAt {
			return strings.Compare(left.UpdatedAt, right.UpdatedAt)
		}
		if left.ArtifactPath != right.ArtifactPath {
			return strings.Compare(left.ArtifactPath, right.ArtifactPath)
		}
		return strings.Compare(left.Title, right.Title)
	})
	tasks := make([]startRepoTask, 0, len(jobIDs))
	for _, jobID := range jobIDs {
		job := c.state.ScoutJobs[jobID]
		tasks = append(tasks, startRepoTask{
			Key:        startServiceTaskKey(startTaskKindScoutJob, jobID),
			Queue:      startTaskQueueImplementation,
			Kind:       startTaskKindScoutJob,
			ScoutJobID: jobID,
			ScoutJob:   job,
		})
	}
	return tasks
}

func (c *startRepoCoordinator) availableWorkerSlots() int {
	available := c.workOptions.Parallel - startWorkImplementationInProgress(c.state)
	for _, task := range c.state.ServiceTasks {
		if task.Queue == startTaskQueueService && task.Status == startWorkServiceTaskRunning {
			available--
		}
	}
	return available
}

func (c *startRepoCoordinator) selectNextTask(serviceQueue []startRepoTask, implementationQueue []startRepoTask) (startRepoTask, []startRepoTask, []startRepoTask, bool) {
	if len(serviceQueue) == 0 && len(implementationQueue) == 0 {
		return startRepoTask{}, serviceQueue, implementationQueue, false
	}
	if len(serviceQueue) == 0 {
		task := implementationQueue[0]
		c.nextQueue = startTaskQueueService
		return task, serviceQueue, implementationQueue[1:], true
	}
	if len(implementationQueue) == 0 {
		task := serviceQueue[0]
		c.nextQueue = startTaskQueueImplementation
		return task, serviceQueue[1:], implementationQueue, true
	}
	if c.nextQueue == startTaskQueueImplementation {
		task := implementationQueue[0]
		c.nextQueue = startTaskQueueService
		return task, serviceQueue, implementationQueue[1:], true
	}
	task := serviceQueue[0]
	c.nextQueue = startTaskQueueImplementation
	return task, serviceQueue[1:], implementationQueue, true
}

func (c *startRepoCoordinator) shouldQueueTriage(issueKey string, issue startWorkIssueState) bool {
	if issue.State != "open" || issue.ForkNumber <= 0 {
		return false
	}
	if strings.TrimSpace(issue.PrioritySource) == "manual_label" {
		return false
	}
	if issue.Status == startWorkStatusCompleted || issue.Status == startWorkStatusNotActioned {
		return false
	}
	if !startWorkAutomationAllowsIssue(c.workOptions.ImplementMode, issue.Labels, "implement") {
		return false
	}
	if c.running[startServiceTaskKey(startTaskKindIssue, issueKey)].Key != "" {
		return false
	}
	return !startWorkIssueHasFreshPriority(issue)
}

func (c *startRepoCoordinator) shouldQueueReconcile(issueKey string, issue startWorkIssueState) bool {
	if issue.Status == startWorkStatusReconciling {
		return true
	}
	if issue.Status != startWorkStatusInProgress {
		return false
	}
	return c.running[startServiceTaskKey(startTaskKindIssue, issueKey)].Key == ""
}

func (c *startRepoCoordinator) markTaskStarted(task startRepoTask) error {
	return c.withPersistedStateLock(func() error {
		now := time.Now().UTC().Format(time.RFC3339)
		switch task.Kind {
		case startTaskKindIssue:
			issue := c.state.Issues[task.IssueKey]
			issue.Status = startWorkStatusInProgress
			issue.BlockedReason = ""
			issue.LastRunError = ""
			issue.ScheduleAt = ""
			issue.DeferredReason = ""
			issue.LastRunUpdatedAt = now
			issue.UpdatedAt = now
			c.state.Issues[task.IssueKey] = issue
			c.startedIssueNumbers = append(c.startedIssueNumbers, issue.SourceNumber)
			c.implStartedCount++
		case startTaskKindScoutJob:
			job := c.state.ScoutJobs[task.ScoutJobID]
			job.Status = startScoutJobRunning
			job.LastError = ""
			job.PauseUntil = ""
			job.PauseReason = ""
			job.UpdatedAt = now
			job.Attempts++
			c.state.ScoutJobs[task.ScoutJobID] = job
			c.implStartedCount++
		case startTaskKindPlannedLaunch:
			item := c.state.PlannedItems[task.PlannedItemID]
			item.State = startPlannedItemLaunching
			item.LastError = ""
			item.ScheduleAt = ""
			item.DeferredReason = ""
			item.UpdatedAt = now
			c.state.PlannedItems[task.PlannedItemID] = item
			serviceTask := c.state.ServiceTasks[task.Key]
			serviceTask.Status = startWorkServiceTaskRunning
			serviceTask.Attempts++
			serviceTask.StartedAt = now
			serviceTask.UpdatedAt = now
			serviceTask.LastError = ""
			serviceTask.WaitCycle = ""
			serviceTask.WaitUntil = ""
			serviceTask.WaitUntil = ""
			c.state.ServiceTasks[task.Key] = serviceTask
			c.serviceStartedCount++
		default:
			serviceTask := c.state.ServiceTasks[task.Key]
			serviceTask.Status = startWorkServiceTaskRunning
			serviceTask.Attempts++
			serviceTask.StartedAt = now
			serviceTask.UpdatedAt = now
			serviceTask.LastError = ""
			serviceTask.WaitCycle = ""
			serviceTask.WaitUntil = ""
			serviceTask.WaitUntil = ""
			c.state.ServiceTasks[task.Key] = serviceTask
			c.serviceStartedCount++
		}
		return writeStartWorkStateUnlocked(*c.state)
	})
}

func (c *startRepoCoordinator) launchTask(task startRepoTask, results chan<- startRepoTaskResult) {
	go func() {
		results <- c.executeTask(task)
	}()
}

func (c *startRepoCoordinator) launchTaskCompletion(task startRepoTask, results chan<- startRepoTaskCompletion) {
	go func() {
		results <- startRepoTaskCompletion{repoSlug: c.repoSlug, result: c.executeTask(task)}
	}()
}

func (c *startRepoCoordinator) executeTask(task startRepoTask) startRepoTaskResult {
	switch task.Kind {
	case startTaskKindIssueSync:
		return startRepoTaskResult{
			Task: task,
			Err:  startRunIssueSync(c.workOptions),
		}
	case startTaskKindTriage:
		triage, err := startRunIssueTriage(c.repoSlug, task.Issue, c.workOptions.CodexArgs)
		return startRepoTaskResult{Task: task, Triage: &triage, Err: err}
	case startTaskKindScout:
		return startRepoTaskResult{
			Task: task,
			Err: startRunScoutRole(c.cwd, ImproveOptions{
				Target:    c.repoSlug,
				Focus:     []string{"ux", "perf"},
				CodexArgs: append([]string{}, c.workOptions.CodexArgs...),
			}, task.ScoutRole),
		}
	case startTaskKindReconcile:
		reconcile, err := startRunIssueReconcile(c.repoSlug, c.workOptions.PublishTarget, task.Issue)
		return startRepoTaskResult{Task: task, Reconcile: &reconcile, Err: err}
	case startTaskKindIssue:
		issueURL := task.Issue.SourceURL
		if c.workOptions.PublishTarget == "fork" {
			issueURL = task.Issue.ForkURL
		}
		launch, err := startWorkRunGithubWork(issueURL, c.workOptions.PublishTarget, c.workOptions.CodexArgs)
		return startRepoTaskResult{Task: task, Launch: &launch, Err: err}
	case startTaskKindScoutJob:
		launch, err := startRunScoutJobLaunch(c.repoSlug, task.ScoutJob, c.workOptions.CodexArgs)
		return startRepoTaskResult{Task: task, Launch: &launch, Err: err}
	case startTaskKindPlannedLaunch:
		launch, err := startRunPlannedItemLaunch(c.cwd, c.repoSlug, c.workOptions, task.PlannedItem)
		return startRepoTaskResult{Task: task, PlannedLaunch: &launch, Err: err}
	default:
		return startRepoTaskResult{Task: task, Err: fmt.Errorf("unknown task kind %s", task.Kind)}
	}
}

func (c *startRepoCoordinator) applyTaskResult(result startRepoTaskResult) error {
	return c.withPersistedStateLock(func() error {
		now := time.Now().UTC().Format(time.RFC3339)
		switch result.Task.Kind {
		case startTaskKindIssue:
			issue := c.state.Issues[result.Task.IssueKey]
			if result.Launch != nil {
				issue.LastRunID = strings.TrimSpace(result.Launch.RunID)
			}
			if pauseErr, ok := isCodexRateLimitPauseError(result.Err); ok {
				issue.Status = startWorkStatusQueued
				issue.BlockedReason = ""
				issue.LastRunError = codexPauseInfoMessage(pauseErr.Info)
				issue.DeferredReason = defaultString(strings.TrimSpace(pauseErr.Info.Reason), "rate limited")
				issue.ScheduleAt = strings.TrimSpace(pauseErr.Info.RetryAfter)
				issue.ScheduleUpdatedAt = now
				issue.LastRunUpdatedAt = now
				issue.UpdatedAt = now
				c.state.Issues[result.Task.IssueKey] = issue
				return writeStartWorkStateUnlocked(*c.state)
			}
			issue.LastRunUpdatedAt = now
			if result.Err != nil {
				issue.LastRunError = result.Err.Error()
			} else {
				issue.LastRunError = ""
			}
			issue.Status = startWorkStatusReconciling
			issue.UpdatedAt = now
			c.state.Issues[result.Task.IssueKey] = issue
			serviceTask := c.state.ServiceTasks[startServiceTaskKey(startTaskKindReconcile, result.Task.IssueKey)]
			serviceTask.ID = startServiceTaskKey(startTaskKindReconcile, result.Task.IssueKey)
			serviceTask.Kind = startTaskKindReconcile
			serviceTask.Queue = startTaskQueueService
			serviceTask.Status = startWorkServiceTaskQueued
			serviceTask.IssueKey = result.Task.IssueKey
			serviceTask.Fingerprint = issue.SourceFingerprint
			serviceTask.RunID = issue.LastRunID
			serviceTask.WaitCycle = ""
			serviceTask.WaitUntil = ""
			serviceTask.UpdatedAt = now
			c.state.ServiceTasks[serviceTask.ID] = serviceTask
			if err := writeStartWorkStateUnlocked(*c.state); err != nil {
				return err
			}
			if result.Err != nil {
				return fmt.Errorf("%s %s: %w", c.repoSlug, result.Task.Key, result.Err)
			}
			return nil
		case startTaskKindScoutJob:
			job := c.state.ScoutJobs[result.Task.ScoutJobID]
			if result.Launch != nil && strings.TrimSpace(result.Launch.RunID) != "" {
				job.RunID = strings.TrimSpace(result.Launch.RunID)
			}
			if pauseErr, ok := isCodexRateLimitPauseError(result.Err); ok {
				job.Status = startScoutJobQueued
				job.LastError = codexPauseInfoMessage(pauseErr.Info)
				job.PauseReason = defaultString(strings.TrimSpace(pauseErr.Info.Reason), "rate limited")
				job.PauseUntil = strings.TrimSpace(pauseErr.Info.RetryAfter)
				job.UpdatedAt = now
				c.state.ScoutJobs[result.Task.ScoutJobID] = job
				if err := writeStartWorkStateUnlocked(*c.state); err != nil {
					return err
				}
				return nil
			}
			if result.Err != nil {
				job.Status = startScoutJobFailed
				job.LastError = result.Err.Error()
				job.UpdatedAt = now
				c.state.ScoutJobs[result.Task.ScoutJobID] = job
				fmt.Fprintf(os.Stdout, "[start] %s: scout job %s failed to launch: %s.\n", c.repoSlug, result.Task.ScoutJobID, result.Err.Error())
				if err := writeStartWorkStateUnlocked(*c.state); err != nil {
					return err
				}
				return fmt.Errorf("%s %s: %w", c.repoSlug, result.Task.Key, result.Err)
			}
			job.Status = startScoutJobRunning
			job.LastError = ""
			job.UpdatedAt = now
			c.state.ScoutJobs[result.Task.ScoutJobID] = job
			fmt.Fprintf(os.Stdout, "[start] %s: scout job %s launched local work run %s.\n", c.repoSlug, result.Task.ScoutJobID, defaultString(strings.TrimSpace(job.RunID), "-"))
		case startTaskKindIssueSync:
			serviceTask := c.state.ServiceTasks[result.Task.Key]
			if result.Err != nil {
				return c.handleServiceTaskFailure(serviceTask, result.Err, now)
			}
			serviceTask.Status = startWorkServiceTaskCompleted
			serviceTask.ResultSummary = "synced"
			serviceTask.WaitCycle = ""
			serviceTask.WaitUntil = ""
			serviceTask.CompletedAt = now
			serviceTask.UpdatedAt = now
			c.state.ServiceTasks[result.Task.Key] = serviceTask
			c.refreshState = true
		case startTaskKindTriage:
			serviceTask := c.state.ServiceTasks[result.Task.Key]
			issue := c.state.Issues[result.Task.IssueKey]
			if result.Err != nil {
				if serviceTask.Attempts < serviceTaskRetryLimit(serviceTask.Kind) {
					serviceTask.Status = startWorkServiceTaskQueued
					serviceTask.LastError = result.Err.Error()
					serviceTask.ResultSummary = "retrying"
					serviceTask.WaitCycle = ""
					serviceTask.WaitUntil = ""
					serviceTask.StartedAt = ""
					serviceTask.UpdatedAt = now
					c.state.ServiceTasks[result.Task.Key] = serviceTask
					issue.TriageStatus = startWorkTriageQueued
					issue.TriageError = result.Err.Error()
					issue.TriageUpdatedAt = now
					issue.UpdatedAt = now
					c.state.Issues[result.Task.IssueKey] = issue
					return writeStartWorkStateUnlocked(*c.state)
				}
				serviceTask.Status = startWorkServiceTaskFailed
				serviceTask.LastError = result.Err.Error()
				serviceTask.WaitCycle = ""
				serviceTask.WaitUntil = ""
				serviceTask.CompletedAt = now
				serviceTask.UpdatedAt = now
				c.state.ServiceTasks[result.Task.Key] = serviceTask
				issue.TriageStatus = startWorkTriageFailed
				issue.TriageError = result.Err.Error()
				issue.TriageUpdatedAt = now
				issue.UpdatedAt = now
				c.state.Issues[result.Task.IssueKey] = issue
				if err := writeStartWorkStateUnlocked(*c.state); err != nil {
					return err
				}
				return fmt.Errorf("%s %s: %w", c.repoSlug, result.Task.Key, result.Err)
			}
			serviceTask.Status = startWorkServiceTaskCompleted
			serviceTask.ResultSummary = startWorkPriorityLabel(result.Triage.Priority)
			serviceTask.WaitCycle = ""
			serviceTask.WaitUntil = ""
			serviceTask.CompletedAt = now
			serviceTask.UpdatedAt = now
			serviceTask.Fingerprint = issue.SourceFingerprint
			c.state.ServiceTasks[result.Task.Key] = serviceTask
			issue.Priority = result.Triage.Priority
			issue.PrioritySource = "triage"
			issue.TriageStatus = startWorkTriageCompleted
			issue.TriageRationale = result.Triage.Rationale
			issue.TriageFingerprint = issue.SourceFingerprint
			issue.TriageUpdatedAt = now
			issue.TriageError = ""
			issue.UpdatedAt = now
			c.state.Issues[result.Task.IssueKey] = issue
		case startTaskKindReconcile:
			serviceTask := c.state.ServiceTasks[result.Task.Key]
			issue := c.state.Issues[result.Task.IssueKey]
			if result.Err != nil {
				if serviceTask.Attempts < serviceTaskRetryLimit(serviceTask.Kind) {
					serviceTask.Status = startWorkServiceTaskQueued
					serviceTask.LastError = result.Err.Error()
					serviceTask.ResultSummary = "retrying"
					serviceTask.StartedAt = ""
					serviceTask.UpdatedAt = now
					c.state.ServiceTasks[result.Task.Key] = serviceTask
					issue.Status = startWorkStatusReconciling
					issue.BlockedReason = ""
					issue.UpdatedAt = now
					c.state.Issues[result.Task.IssueKey] = issue
					return writeStartWorkStateUnlocked(*c.state)
				}
				serviceTask.Status = startWorkServiceTaskFailed
				serviceTask.LastError = result.Err.Error()
				serviceTask.CompletedAt = now
				serviceTask.UpdatedAt = now
				c.state.ServiceTasks[result.Task.Key] = serviceTask
				issue.Status = startWorkStatusBlocked
				issue.BlockedReason = result.Err.Error()
				issue.UpdatedAt = now
				c.state.Issues[result.Task.IssueKey] = issue
				if err := writeStartWorkStateUnlocked(*c.state); err != nil {
					return err
				}
				return fmt.Errorf("%s %s: %w", c.repoSlug, result.Task.Key, result.Err)
			}
			if result.Reconcile != nil && result.Reconcile.ShouldRetry {
				if serviceTask.Attempts < serviceTaskRetryLimit(serviceTask.Kind) {
					serviceTask.Status = startWorkServiceTaskQueued
					serviceTask.LastError = result.Reconcile.BlockedReason
					serviceTask.ResultSummary = "waiting"
					serviceTask.WaitCycle = c.cycleID
					serviceTask.WaitUntil = ""
					serviceTask.StartedAt = ""
					serviceTask.UpdatedAt = now
					serviceTask.RunID = result.Reconcile.RunID
					c.state.ServiceTasks[result.Task.Key] = serviceTask
					issue.Status = startWorkStatusReconciling
					issue.BlockedReason = ""
					issue.LastRunID = result.Reconcile.RunID
					issue.LastRunUpdatedAt = now
					issue.UpdatedAt = now
					c.state.Issues[result.Task.IssueKey] = issue
					return writeStartWorkStateUnlocked(*c.state)
				}
				issue.Status = startWorkStatusBlocked
				issue.BlockedReason = defaultString(result.Reconcile.BlockedReason, "reconcile retries exhausted")
			}
			serviceTask.Status = startWorkServiceTaskCompleted
			serviceTask.ResultSummary = result.Reconcile.Status
			serviceTask.WaitCycle = ""
			serviceTask.WaitUntil = ""
			serviceTask.CompletedAt = now
			serviceTask.UpdatedAt = now
			serviceTask.RunID = result.Reconcile.RunID
			c.state.ServiceTasks[result.Task.Key] = serviceTask
			issue.Status = result.Reconcile.Status
			issue.BlockedReason = result.Reconcile.BlockedReason
			issue.LastRunID = result.Reconcile.RunID
			issue.LastRunUpdatedAt = now
			issue.PublishedPRNumber = result.Reconcile.PublishedPRNumber
			issue.PublishedPRURL = result.Reconcile.PublishedPRURL
			issue.PublicationState = result.Reconcile.PublicationState
			issue.UpdatedAt = now
			c.state.Issues[result.Task.IssueKey] = issue
		case startTaskKindScout:
			serviceTask := c.state.ServiceTasks[result.Task.Key]
			if result.Err != nil {
				return c.handleServiceTaskFailure(serviceTask, result.Err, now)
			}
			serviceTask.Status = startWorkServiceTaskCompleted
			serviceTask.WaitCycle = ""
			serviceTask.WaitUntil = ""
			serviceTask.CompletedAt = now
			serviceTask.UpdatedAt = now
			c.state.ServiceTasks[result.Task.Key] = serviceTask
			c.refreshState = true
		case startTaskKindPlannedLaunch:
			serviceTask := c.state.ServiceTasks[result.Task.Key]
			item := c.state.PlannedItems[result.Task.PlannedItemID]
			scoutJobID, scoutJob, scoutJobOK := findScoutJobByLegacyPlannedItemID(c.state, result.Task.PlannedItemID)
			if pauseErr, ok := isCodexRateLimitPauseError(result.Err); ok {
				serviceTask.Status = startWorkServiceTaskQueued
				serviceTask.LastError = codexPauseInfoMessage(pauseErr.Info)
				serviceTask.ResultSummary = "paused_rate_limit"
				serviceTask.WaitCycle = ""
				serviceTask.WaitUntil = strings.TrimSpace(pauseErr.Info.RetryAfter)
				serviceTask.StartedAt = ""
				serviceTask.CompletedAt = ""
				serviceTask.UpdatedAt = now
				c.state.ServiceTasks[result.Task.Key] = serviceTask
				item.State = startPlannedItemQueued
				item.LaunchRunID = defaultString(item.LaunchRunID, result.PlannedLaunch.RunID)
				item.LastError = codexPauseInfoMessage(pauseErr.Info)
				item.ScheduleAt = strings.TrimSpace(pauseErr.Info.RetryAfter)
				item.DeferredReason = defaultString(strings.TrimSpace(pauseErr.Info.Reason), "rate limited")
				item.UpdatedAt = now
				c.state.PlannedItems[result.Task.PlannedItemID] = item
				if scoutJobOK {
					scoutJob.Status = startScoutJobQueued
					scoutJob.LastError = codexPauseInfoMessage(pauseErr.Info)
					scoutJob.PauseReason = defaultString(strings.TrimSpace(pauseErr.Info.Reason), "rate limited")
					scoutJob.PauseUntil = strings.TrimSpace(pauseErr.Info.RetryAfter)
					scoutJob.UpdatedAt = now
					c.state.ScoutJobs[scoutJobID] = scoutJob
				}
				return writeStartWorkStateUnlocked(*c.state)
			}
			if result.Err != nil {
				serviceTask.Status = startWorkServiceTaskFailed
				serviceTask.LastError = result.Err.Error()
				serviceTask.CompletedAt = now
				serviceTask.UpdatedAt = now
				c.state.ServiceTasks[result.Task.Key] = serviceTask
				item.State = startPlannedItemFailed
				item.LastError = result.Err.Error()
				item.UpdatedAt = now
				c.state.PlannedItems[result.Task.PlannedItemID] = item
				if scoutJobOK {
					scoutJob.Status = startScoutJobFailed
					scoutJob.LastError = result.Err.Error()
					scoutJob.UpdatedAt = now
					c.state.ScoutJobs[scoutJobID] = scoutJob
				}
				if err := writeStartWorkStateUnlocked(*c.state); err != nil {
					return err
				}
				return fmt.Errorf("%s %s: %w", c.repoSlug, result.Task.Key, result.Err)
			}
			serviceTask.Status = startWorkServiceTaskCompleted
			serviceTask.CompletedAt = now
			serviceTask.UpdatedAt = now
			serviceTask.ResultSummary = defaultString(result.PlannedLaunch.Status, "launched")
			serviceTask.RunID = result.PlannedLaunch.RunID
			c.state.ServiceTasks[result.Task.Key] = serviceTask
			item.State = startPlannedItemLaunched
			item.LaunchRunID = result.PlannedLaunch.RunID
			item.LaunchIssueNumber = result.PlannedLaunch.IssueNumber
			item.LaunchIssueURL = result.PlannedLaunch.IssueURL
			item.LaunchResult = defaultString(result.PlannedLaunch.Result, result.PlannedLaunch.Status)
			item.LastError = ""
			item.UpdatedAt = now
			c.state.PlannedItems[result.Task.PlannedItemID] = item
			if scoutJobOK {
				scoutJob.Status = startScoutJobCompleted
				scoutJob.LastError = ""
				startWorkScoutJobClearRecovery(&scoutJob)
				scoutJob.UpdatedAt = now
				if strings.TrimSpace(result.PlannedLaunch.RunID) != "" {
					scoutJob.RunID = strings.TrimSpace(result.PlannedLaunch.RunID)
				}
				c.state.ScoutJobs[scoutJobID] = scoutJob
			}
			c.refreshState = true
		default:
			return fmt.Errorf("unknown start task kind %s", result.Task.Kind)
		}
		return writeStartWorkStateUnlocked(*c.state)
	})
}

func (c *startRepoCoordinator) serviceTaskReady(task startWorkServiceTask) bool {
	for _, dependencyKey := range task.DependencyKeys {
		dependency := c.state.ServiceTasks[dependencyKey]
		if dependency.ID == "" {
			return false
		}
		if dependency.Status != startWorkServiceTaskCompleted {
			return false
		}
	}
	return true
}

func taskWaitUntilPending(task startWorkServiceTask, now time.Time) bool {
	waitUntil, ok := startWorkScheduleParsed(task.WaitUntil)
	return ok && waitUntil.After(now)
}

func scoutJobPausePending(job startWorkScoutJob, now time.Time) bool {
	waitUntil, ok := startWorkScheduleParsed(job.PauseUntil)
	return ok && waitUntil.After(now)
}

func (c *startRepoCoordinator) triageDependencyKeys(syncTaskID string) []string {
	if syncTaskID == "" {
		return nil
	}
	task := c.state.ServiceTasks[syncTaskID]
	if task.ID == "" {
		return nil
	}
	if task.Status == startWorkServiceTaskCompleted {
		return nil
	}
	return []string{syncTaskID}
}

func (c *startRepoCoordinator) handleServiceTaskFailure(task startWorkServiceTask, taskErr error, now string) error {
	if pauseErr, ok := isCodexRateLimitPauseError(taskErr); ok {
		task.Status = startWorkServiceTaskQueued
		task.LastError = codexPauseInfoMessage(pauseErr.Info)
		task.ResultSummary = "paused_rate_limit"
		task.WaitCycle = ""
		task.WaitUntil = strings.TrimSpace(pauseErr.Info.RetryAfter)
		task.StartedAt = ""
		task.CompletedAt = ""
		task.UpdatedAt = now
		c.state.ServiceTasks[task.ID] = task
		return writeStartWorkStateUnlocked(*c.state)
	}
	if task.Attempts < serviceTaskRetryLimit(task.Kind) {
		task.Status = startWorkServiceTaskQueued
		task.LastError = taskErr.Error()
		task.ResultSummary = "retrying"
		task.WaitCycle = ""
		task.WaitUntil = ""
		task.StartedAt = ""
		task.UpdatedAt = now
		c.state.ServiceTasks[task.ID] = task
		return writeStartWorkStateUnlocked(*c.state)
	}
	task.Status = startWorkServiceTaskFailed
	task.LastError = taskErr.Error()
	task.WaitCycle = ""
	task.WaitUntil = ""
	task.CompletedAt = now
	task.UpdatedAt = now
	c.state.ServiceTasks[task.ID] = task
	if err := writeStartWorkStateUnlocked(*c.state); err != nil {
		return err
	}
	return fmt.Errorf("%s %s: %w", c.repoSlug, task.ID, taskErr)
}

func (c *startRepoCoordinator) persistLastRun() error {
	if c.state == nil {
		return nil
	}
	return c.withPersistedStateLock(func() error {
		c.state.LastRun = &startWorkLastRun{
			StartedIssueNumbers:        append([]int{}, c.startedIssueNumbers...),
			SkippedReason:              c.lastSkippedReason,
			OpenForkPRs:                c.openPRs,
			ParallelLimit:              c.workOptions.Parallel,
			GlobalParallelLimit:        c.globalOptions.Parallel,
			RepoWorkerLimit:            c.workOptions.Parallel,
			OpenPRCap:                  c.workOptions.MaxOpenPR,
			ServiceStartedCount:        c.serviceStartedCount,
			ImplementationStartedCount: c.implStartedCount,
			UpdatedAt:                  time.Now().UTC().Format(time.RFC3339),
		}
		c.state.UpdatedAt = c.state.LastRun.UpdatedAt
		return writeStartWorkStateUnlocked(*c.state)
	})
}

func (c *startRepoCoordinator) capacitySnapshot() (active int, limit int, runnableService int, runnableImplementation int, blockedService int) {
	limit = c.workOptions.Parallel
	if limit <= 0 {
		limit = 1
	}
	if c.state == nil {
		return 0, limit, 0, 0, 0
	}
	serviceQueue := c.buildServiceQueue()
	implementationQueue, _ := c.buildImplementationQueue()
	queuedServiceTotal := 0
	for _, task := range c.state.ServiceTasks {
		if task.Queue == startTaskQueueService && task.Status == startWorkServiceTaskQueued {
			queuedServiceTotal++
		}
	}
	active = limit - c.availableWorkerSlots()
	if active < 0 {
		active = 0
	}
	runnableService = len(serviceQueue)
	runnableImplementation = len(implementationQueue)
	blockedService = queuedServiceTotal - runnableService
	if blockedService < 0 {
		blockedService = 0
	}
	return active, limit, runnableService, runnableImplementation, blockedService
}

func (c *startRepoCoordinator) completeRun() error {
	if err := c.persistLastRun(); err != nil {
		return err
	}
	active, limit, runnableService, runnableImplementation, blockedService := c.capacitySnapshot()
	fmt.Fprintf(os.Stdout, "[start] %s: service started=%d implementation started=%d.\n", c.repoSlug, c.serviceStartedCount, c.implStartedCount)
	fmt.Fprintf(os.Stdout, "[start] %s: worker utilization=%d/%d runnable service=%d runnable implementation=%d blocked service=%d.\n", c.repoSlug, active, limit, runnableService, runnableImplementation, blockedService)
	return nil
}

func startRepoSupportedScoutRoles(repoSlug string) ([]string, error) {
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	if info, err := os.Stat(sourcePath); err == nil && info.IsDir() {
		roles := []string{}
		if lockErr := withSourceReadLock(sourcePath, repoAccessLockOwner{
			Backend: "start-scheduler",
			RunID:   sanitizePathToken(repoSlug),
			Purpose: "supported-scout-roles",
			Label:   "start-supported-scout-roles",
		}, func() error {
			roles = supportedScoutRoles(sourcePath)
			return nil
		}); lockErr != nil {
			return nil, lockErr
		}
		return roles, nil
	}
	repoPath, err := ensureImproveGithubCheckout(repoSlug)
	if err != nil {
		return nil, err
	}
	return supportedScoutRolesWithReadLock(repoPath, repoAccessLockOwner{
		Backend: "start-scheduler",
		RunID:   sanitizePathToken(repoSlug),
		Purpose: "supported-scout-roles",
		Label:   "start-supported-scout-roles",
	})
}

func startRepoDueScoutRoles(repoSlug string, now time.Time) ([]string, error) {
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	repoPath := sourcePath
	if info, err := os.Stat(sourcePath); err != nil || !info.IsDir() {
		var checkoutErr error
		repoPath, checkoutErr = ensureImproveGithubCheckout(repoSlug)
		if checkoutErr != nil {
			return nil, checkoutErr
		}
	}
	due := []string{}
	err := withSourceReadLock(repoPath, repoAccessLockOwner{
		Backend: "start-scheduler",
		RunID:   sanitizePathToken(repoSlug),
		Purpose: "due-scout-roles",
		Label:   "start-due-scout-roles",
	}, func() error {
		supported := supportedScoutRoles(repoPath)
		due = make([]string, 0, len(supported))
		for _, role := range supported {
			policy := readScoutPolicy(repoPath, role)
			decision, err := scoutScheduleDecisionForRole(repoPath, repoSlug, role, policy, now)
			if err != nil {
				return err
			}
			if decision.Due {
				due = append(due, role)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return due, nil
}

func compareStartIssueKeys(a string, b string) int {
	left, errLeft := strconv.Atoi(a)
	right, errRight := strconv.Atoi(b)
	switch {
	case errLeft == nil && errRight == nil:
		return left - right
	case a == "" && b == "":
		return 0
	case a == "":
		return -1
	case b == "":
		return 1
	default:
		return strings.Compare(a, b)
	}
}

func serviceTaskPriority(kind string) int {
	switch kind {
	case startTaskKindReconcile:
		return 0
	case startTaskKindIssueSync:
		return 1
	case startTaskKindPlannedLaunch:
		return 2
	case startTaskKindTriage:
		return 3
	case startTaskKindScout:
		return 4
	default:
		return 5
	}
}

func startServiceTaskKey(kind string, target string) string {
	return kind + ":" + target
}

func serviceTaskRetryLimit(kind string) int {
	switch kind {
	case startTaskKindScout:
		return 2
	default:
		return 3
	}
}
