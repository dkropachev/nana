package gocli

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	startTaskQueueService        = "service"
	startTaskQueueImplementation = "implementation"

	startTaskKindIssueSync = "issue-sync"
	startTaskKindScout     = "scout"
	startTaskKindTriage    = "triage"
	startTaskKindIssue     = "implementation"
	startTaskKindReconcile = "reconcile"
)

type startRepoTask struct {
	Key       string
	Queue     string
	Kind      string
	IssueKey  string
	Issue     startWorkIssueState
	ScoutRole string
	RunID     string
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
	Task      startRepoTask
	Triage    *startWorkTriageResult
	Reconcile *startWorkReconcileResult
	Launch    *startWorkLaunchResult
	Err       error
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

var startRunIssueTriage = func(repoSlug string, issue startWorkIssueState, codexArgs []string) (startWorkTriageResult, error) {
	return runStartWorkIssueTriage(repoSlug, issue, codexArgs)
}

var startRunScoutRole = func(cwd string, options ImproveOptions, role string) error {
	return runScout(cwd, options, role)
}

var startRunIssueReconcile = func(repoSlug string, publishTarget string, issue startWorkIssueState) (startWorkReconcileResult, error) {
	return runStartWorkIssueReconcile(repoSlug, publishTarget, issue)
}

var startRunIssueSync = func(options startWorkOptions) error {
	_, state, _, _, err := startWorkSyncRepoState(options)
	if err != nil {
		return err
	}
	return writeStartWorkState(*state)
}

func runStartRepoCycles(cwd string, repos []string, options startOptions) error {
	parallel := options.Parallel
	if parallel <= 0 {
		parallel = 1
	}
	sem := make(chan struct{}, parallel)
	errs := []string{}
	var errsMu sync.Mutex
	var wg sync.WaitGroup
	for _, repoSlug := range repos {
		repoSlug := repoSlug
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() {
				<-sem
			}()
			if err := startRunRepoCycle(cwd, repoSlug, options); err != nil {
				errsMu.Lock()
				errs = append(errs, fmt.Sprintf("%s: %v", repoSlug, err))
				errsMu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(errs) == 0 {
		return nil
	}
	slices.Sort(errs)
	return fmt.Errorf("start repo failures:\n%s", strings.Join(errs, "\n"))
}

func runStartRepoSchedulerCycle(cwd string, repoSlug string, workOptions startWorkOptions, options startOptions) error {
	coordinator := &startRepoCoordinator{
		cwd:           cwd,
		repoSlug:      repoSlug,
		cycleID:       strconv.FormatInt(time.Now().UnixNano(), 10),
		globalOptions: options,
		workOptions:   workOptions,
		running:       map[string]startRepoTask{},
		nextQueue:     startTaskQueueService,
		refreshState:  true,
	}
	return coordinator.run()
}

func (c *startRepoCoordinator) run() error {
	if err := c.refreshRepoState(); err != nil {
		return err
	}
	results := make(chan startRepoTaskResult, max(c.workOptions.Parallel, 1))
	errs := []string{}
	for {
		if c.refreshState {
			if err := c.refreshRepoState(); err != nil {
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
	if err := c.persistLastRun(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "[start] %s: service started=%d implementation started=%d.\n", c.repoSlug, c.serviceStartedCount, c.implStartedCount)
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("repo task failures:\n%s", strings.Join(errs, "\n"))
}

func (c *startRepoCoordinator) refreshRepoState() error {
	updatedOptions, state, openPRs, _, err := startWorkSyncRepoState(c.workOptions)
	if err != nil {
		return err
	}
	c.workOptions = updatedOptions
	c.state = state
	c.openPRs = openPRs
	c.scoutRoles = startRepoSupportedScoutRoles(c.repoSlug)
	if err := c.syncServiceTasks(); err != nil {
		return err
	}
	c.refreshState = false
	return nil
}

func (c *startRepoCoordinator) syncServiceTasks() error {
	if c.state.ServiceTasks == nil {
		c.state.ServiceTasks = map[string]startWorkServiceTask{}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	mutated := false
	for key, task := range c.state.ServiceTasks {
		if task.Status == startWorkServiceTaskRunning {
			task.Status = startWorkServiceTaskQueued
			task.StartedAt = ""
			task.UpdatedAt = now
			c.state.ServiceTasks[key] = task
			mutated = true
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
	if mutated {
		return writeStartWorkState(*c.state)
	}
	return nil
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
	case startTaskKindIssueSync, startTaskKindTriage, startTaskKindReconcile:
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
			Key:       task.ID,
			Queue:     task.Queue,
			Kind:      task.Kind,
			IssueKey:  task.IssueKey,
			ScoutRole: task.ScoutRole,
			RunID:     task.RunID,
		}
		if task.IssueKey != "" {
			repoTask.Issue = c.state.Issues[task.IssueKey]
		}
		queue = append(queue, repoTask)
	}
	return queue
}

func (c *startRepoCoordinator) buildImplementationQueue() ([]startRepoTask, string) {
	issues, skippedReason := startWorkBuildImplementationQueue(c.state, c.workOptions, c.openPRs)
	if len(issues) == 0 {
		return nil, skippedReason
	}
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
	if len(tasks) == 0 {
		return nil, skippedReason
	}
	return tasks, ""
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
	now := time.Now().UTC().Format(time.RFC3339)
	switch task.Kind {
	case startTaskKindIssue:
		issue := c.state.Issues[task.IssueKey]
		issue.Status = startWorkStatusInProgress
		issue.BlockedReason = ""
		issue.LastRunError = ""
		issue.LastRunUpdatedAt = now
		issue.UpdatedAt = now
		c.state.Issues[task.IssueKey] = issue
		c.startedIssueNumbers = append(c.startedIssueNumbers, issue.SourceNumber)
		c.implStartedCount++
	default:
		serviceTask := c.state.ServiceTasks[task.Key]
		serviceTask.Status = startWorkServiceTaskRunning
		serviceTask.Attempts++
		serviceTask.StartedAt = now
		serviceTask.UpdatedAt = now
		serviceTask.LastError = ""
		c.state.ServiceTasks[task.Key] = serviceTask
		c.serviceStartedCount++
	}
	return writeStartWorkState(*c.state)
}

func (c *startRepoCoordinator) launchTask(task startRepoTask, results chan<- startRepoTaskResult) {
	go func() {
		switch task.Kind {
		case startTaskKindIssueSync:
			results <- startRepoTaskResult{
				Task: task,
				Err:  startRunIssueSync(c.workOptions),
			}
		case startTaskKindTriage:
			triage, err := startRunIssueTriage(c.repoSlug, task.Issue, c.workOptions.CodexArgs)
			results <- startRepoTaskResult{Task: task, Triage: &triage, Err: err}
		case startTaskKindScout:
			results <- startRepoTaskResult{
				Task: task,
				Err: startRunScoutRole(c.cwd, ImproveOptions{
					Target:    c.repoSlug,
					Focus:     []string{"ux", "perf"},
					CodexArgs: append([]string{}, c.workOptions.CodexArgs...),
				}, task.ScoutRole),
			}
		case startTaskKindReconcile:
			reconcile, err := startRunIssueReconcile(c.repoSlug, c.workOptions.PublishTarget, task.Issue)
			results <- startRepoTaskResult{Task: task, Reconcile: &reconcile, Err: err}
		case startTaskKindIssue:
			issueURL := task.Issue.SourceURL
			if c.workOptions.PublishTarget == "fork" {
				issueURL = task.Issue.ForkURL
			}
			launch, err := startWorkRunGithubWork(issueURL, c.workOptions.PublishTarget, c.workOptions.CodexArgs)
			results <- startRepoTaskResult{Task: task, Launch: &launch, Err: err}
		default:
			results <- startRepoTaskResult{Task: task, Err: fmt.Errorf("unknown task kind %s", task.Kind)}
		}
	}()
}

func (c *startRepoCoordinator) applyTaskResult(result startRepoTaskResult) error {
	now := time.Now().UTC().Format(time.RFC3339)
	switch result.Task.Kind {
	case startTaskKindIssue:
		issue := c.state.Issues[result.Task.IssueKey]
		if result.Launch != nil {
			issue.LastRunID = strings.TrimSpace(result.Launch.RunID)
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
		serviceTask.UpdatedAt = now
		c.state.ServiceTasks[serviceTask.ID] = serviceTask
		if err := writeStartWorkState(*c.state); err != nil {
			return err
		}
		if result.Err != nil {
			return fmt.Errorf("%s %s: %w", c.repoSlug, result.Task.Key, result.Err)
		}
		return nil
	case startTaskKindIssueSync:
		serviceTask := c.state.ServiceTasks[result.Task.Key]
		if result.Err != nil {
			return c.handleServiceTaskFailure(serviceTask, result.Err, now)
		}
		serviceTask.Status = startWorkServiceTaskCompleted
		serviceTask.ResultSummary = "synced"
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
				serviceTask.StartedAt = ""
				serviceTask.UpdatedAt = now
				c.state.ServiceTasks[result.Task.Key] = serviceTask
				issue.TriageStatus = startWorkTriageQueued
				issue.TriageError = result.Err.Error()
				issue.TriageUpdatedAt = now
				issue.UpdatedAt = now
				c.state.Issues[result.Task.IssueKey] = issue
				return writeStartWorkState(*c.state)
			}
			serviceTask.Status = startWorkServiceTaskFailed
			serviceTask.LastError = result.Err.Error()
			serviceTask.CompletedAt = now
			serviceTask.UpdatedAt = now
			c.state.ServiceTasks[result.Task.Key] = serviceTask
			issue.TriageStatus = startWorkTriageFailed
			issue.TriageError = result.Err.Error()
			issue.TriageUpdatedAt = now
			issue.UpdatedAt = now
			c.state.Issues[result.Task.IssueKey] = issue
			if err := writeStartWorkState(*c.state); err != nil {
				return err
			}
			return fmt.Errorf("%s %s: %w", c.repoSlug, result.Task.Key, result.Err)
		}
		serviceTask.Status = startWorkServiceTaskCompleted
		serviceTask.ResultSummary = startWorkPriorityLabel(result.Triage.Priority)
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
				return writeStartWorkState(*c.state)
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
			if err := writeStartWorkState(*c.state); err != nil {
				return err
			}
			return fmt.Errorf("%s %s: %w", c.repoSlug, result.Task.Key, result.Err)
		}
		if result.Reconcile != nil && result.Reconcile.ShouldRetry {
			if serviceTask.Attempts < serviceTaskRetryLimit(serviceTask.Kind) {
				serviceTask.Status = startWorkServiceTaskQueued
				serviceTask.LastError = result.Reconcile.BlockedReason
				serviceTask.ResultSummary = "waiting"
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
				return writeStartWorkState(*c.state)
			}
			issue.Status = startWorkStatusBlocked
			issue.BlockedReason = defaultString(result.Reconcile.BlockedReason, "reconcile retries exhausted")
		}
		serviceTask.Status = startWorkServiceTaskCompleted
		serviceTask.ResultSummary = result.Reconcile.Status
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
		serviceTask.CompletedAt = now
		serviceTask.UpdatedAt = now
		c.state.ServiceTasks[result.Task.Key] = serviceTask
		c.refreshState = true
	default:
		return fmt.Errorf("unknown start task kind %s", result.Task.Kind)
	}
	return writeStartWorkState(*c.state)
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
	if task.Attempts < serviceTaskRetryLimit(task.Kind) {
		task.Status = startWorkServiceTaskQueued
		task.LastError = taskErr.Error()
		task.ResultSummary = "retrying"
		task.StartedAt = ""
		task.UpdatedAt = now
		c.state.ServiceTasks[task.ID] = task
		return writeStartWorkState(*c.state)
	}
	task.Status = startWorkServiceTaskFailed
	task.LastError = taskErr.Error()
	task.CompletedAt = now
	task.UpdatedAt = now
	c.state.ServiceTasks[task.ID] = task
	if err := writeStartWorkState(*c.state); err != nil {
		return err
	}
	return fmt.Errorf("%s %s: %w", c.repoSlug, task.ID, taskErr)
}

func (c *startRepoCoordinator) persistLastRun() error {
	if c.state == nil {
		return nil
	}
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
	return writeStartWorkState(*c.state)
}

func startRepoSupportedScoutRoles(repoSlug string) []string {
	sourcePath := githubManagedPaths(repoSlug).SourcePath
	if info, err := os.Stat(sourcePath); err == nil && info.IsDir() {
		return supportedScoutRoles(sourcePath)
	}
	repoPath, err := ensureImproveGithubCheckout(repoSlug)
	if err != nil {
		return nil
	}
	return supportedScoutRoles(repoPath)
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
	case startTaskKindTriage:
		return 2
	case startTaskKindScout:
		return 3
	default:
		return 4
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
