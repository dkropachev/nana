package gocli

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	startUIDefaultAPIPort = 17653
	startUIDefaultWebPort = 17654
	startUIBindHost       = "127.0.0.1"
	startUIWorkRunLimit   = 10
)

type startUIRuntimeState struct {
	ProcessID int    `json:"process_id"`
	APIURL    string `json:"api_url"`
	WebURL    string `json:"web_url"`
	StartedAt string `json:"started_at"`
	StoppedAt string `json:"stopped_at,omitempty"`
}

type startUISupervisor struct {
	runtimePath string
	apiServer   *http.Server
	webServer   *http.Server
	apiURL      string
	webURL      string
}

type startUIAPI struct {
	cwd              string
	allowedWebOrigin string
	overviewCache    startUIOverviewCache
}

type startUIOverviewCache struct {
	mu        sync.Mutex
	signature string
	overview  startUIOverview
	ok        bool
}

type startUITotals struct {
	Repos            int `json:"repos"`
	IssuesQueued     int `json:"issues_queued"`
	IssuesInProgress int `json:"issues_in_progress"`
	ServiceQueued    int `json:"service_queued"`
	ServiceRunning   int `json:"service_running"`
	PlannedQueued    int `json:"planned_queued"`
	PlannedLaunching int `json:"planned_launching"`
	BlockedIssues    int `json:"blocked_issues"`
	ActiveWorkRuns   int `json:"active_work_runs"`
}

type startUIRepoSummary struct {
	RepoSlug          string              `json:"repo_slug"`
	RepoMode          string              `json:"repo_mode,omitempty"`
	IssuePickMode     string              `json:"issue_pick_mode,omitempty"`
	PRForwardMode     string              `json:"pr_forward_mode,omitempty"`
	UpdatedAt         string              `json:"updated_at,omitempty"`
	StatePath         string              `json:"state_path,omitempty"`
	SourcePath        string              `json:"source_path,omitempty"`
	IssueCounts       map[string]int      `json:"issue_counts"`
	ServiceTaskCounts map[string]int      `json:"service_task_counts"`
	PlannedItemCounts map[string]int      `json:"planned_item_counts"`
	LastRun           *startWorkLastRun   `json:"last_run,omitempty"`
	DefaultBranch     string              `json:"default_branch,omitempty"`
	Settings          *githubRepoSettings `json:"settings,omitempty"`
	State             *startWorkState     `json:"state,omitempty"`
}

type startUIOverview struct {
	GeneratedAt string               `json:"generated_at"`
	Totals      startUITotals        `json:"totals"`
	Repos       []startUIRepoSummary `json:"repos"`
	WorkRuns    []startUIWorkRun     `json:"work_runs"`
	HUD         HUDRenderContext     `json:"hud"`
}

type startUIWorkRun struct {
	RunID            string `json:"run_id"`
	Backend          string `json:"backend"`
	RepoKey          string `json:"repo_key,omitempty"`
	RepoName         string `json:"repo_name,omitempty"`
	Status           string `json:"status,omitempty"`
	CurrentPhase     string `json:"current_phase,omitempty"`
	CurrentIteration int    `json:"current_iteration,omitempty"`
	UpdatedAt        string `json:"updated_at"`
	TargetKind       string `json:"target_kind,omitempty"`
	TargetURL        string `json:"target_url,omitempty"`
	ArtifactPath     string `json:"artifact_path,omitempty"`
	PublicationState string `json:"publication_state,omitempty"`
}

type startUIIssuePatchRequest struct {
	Priority       *int    `json:"priority,omitempty"`
	ScheduleAt     *string `json:"schedule_at,omitempty"`
	DeferredReason *string `json:"deferred_reason,omitempty"`
	ClearSchedule  bool    `json:"clear_schedule,omitempty"`
}

type startUIPlannedItemRequest struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Priority    *int   `json:"priority,omitempty"`
	ScheduleAt  string `json:"schedule_at,omitempty"`
	LaunchKind  string `json:"launch_kind,omitempty"`
}

func launchStartUISupervisor(cwd string, options startOptions) (*startUISupervisor, error) {
	apiListener, apiURL, err := listenLoopbackPort(startUIBindHost, options.UIAPIPort)
	if err != nil {
		return nil, err
	}
	webListener, webURL, err := listenLoopbackPort(startUIBindHost, options.UIWebPort)
	if err != nil {
		_ = apiListener.Close()
		return nil, err
	}

	api := &startUIAPI{cwd: cwd, allowedWebOrigin: webURL}
	apiServer := &http.Server{Handler: api.routes()}
	webServer := &http.Server{Handler: startUIWebHandler(apiURL)}

	supervisor := &startUISupervisor{
		runtimePath: filepath.Join(githubNanaHome(), "start", "ui", "runtime.json"),
		apiServer:   apiServer,
		webServer:   webServer,
		apiURL:      apiURL,
		webURL:      webURL,
	}
	go func() {
		_ = apiServer.Serve(apiListener)
	}()
	go func() {
		_ = webServer.Serve(webListener)
	}()
	if err := writeGithubJSON(supervisor.runtimePath, startUIRuntimeState{
		ProcessID: os.Getpid(),
		APIURL:    apiURL,
		WebURL:    webURL,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		_ = supervisor.Close()
		return nil, err
	}
	fmt.Fprintf(os.Stdout, "[start-ui] API: %s\n", apiURL)
	fmt.Fprintf(os.Stdout, "[start-ui] Web: %s\n", webURL)
	return supervisor, nil
}

func (s *startUISupervisor) Close() error {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if s.apiServer != nil {
		_ = s.apiServer.Shutdown(ctx)
	}
	if s.webServer != nil {
		_ = s.webServer.Shutdown(ctx)
	}
	runtime := startUIRuntimeState{}
	_ = readGithubJSON(s.runtimePath, &runtime)
	runtime.ProcessID = os.Getpid()
	runtime.APIURL = s.apiURL
	runtime.WebURL = s.webURL
	runtime.StoppedAt = time.Now().UTC().Format(time.RFC3339)
	return writeGithubJSON(s.runtimePath, runtime)
}

func listenLoopbackPort(host string, preferredPort int) (net.Listener, string, error) {
	if preferredPort <= 0 {
		preferredPort = 0
	}
	tryPorts := []int{}
	if preferredPort == 0 {
		tryPorts = append(tryPorts, 0)
	} else {
		for port := preferredPort; port < preferredPort+50; port++ {
			tryPorts = append(tryPorts, port)
		}
	}
	var lastErr error
	for _, port := range tryPorts {
		address := net.JoinHostPort(host, strconv.Itoa(port))
		listener, err := net.Listen("tcp", address)
		if err == nil {
			resolvedPort := listener.Addr().(*net.TCPAddr).Port
			return listener, fmt.Sprintf("http://%s:%d", host, resolvedPort), nil
		}
		lastErr = err
	}
	return nil, "", lastErr
}

func (h *startUIAPI) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/overview", h.handleOverview)
	mux.HandleFunc("/api/v1/repos", h.handleRepos)
	mux.HandleFunc("/api/v1/repos/", h.handleRepoRoute)
	mux.HandleFunc("/api/v1/planned-items/", h.handlePlannedItemRoute)
	mux.HandleFunc("/api/v1/work/runs", h.handleWorkRuns)
	mux.HandleFunc("/api/v1/work/runs/", h.handleWorkRun)
	mux.HandleFunc("/api/v1/hud", h.handleHUD)
	mux.HandleFunc("/api/v1/events", h.handleEvents)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.applyCORS(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (h *startUIAPI) applyCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", h.allowedWebOrigin)
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func (h *startUIAPI) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	overview, err := h.buildOverview()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, overview)
}

func (h *startUIAPI) handleRepos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	repos, err := listStartUIRepoSummaries(true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, map[string]any{"repos": repos})
}

func (h *startUIAPI) handleRepoRoute(w http.ResponseWriter, r *http.Request) {
	repoSlug, tail, ok := parseStartUIRepoRoute(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch {
	case r.Method == http.MethodGet && tail == "start-state":
		summary, err := loadStartUIRepoSummary(repoSlug, true)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, os.ErrNotExist) {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSONResponse(w, summary)
	case r.Method == http.MethodPatch && strings.HasPrefix(tail, "issues/"):
		issueNumber, err := strconv.Atoi(strings.TrimPrefix(tail, "issues/"))
		if err != nil {
			http.Error(w, "invalid issue number", http.StatusBadRequest)
			return
		}
		var payload startUIIssuePatchRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		state, issue, err := patchStartUIIssue(repoSlug, issueNumber, payload)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.invalidateOverviewCache()
		writeJSONResponse(w, map[string]any{"state": state, "issue": issue})
	case r.Method == http.MethodPost && tail == "planned-items":
		var payload startUIPlannedItemRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		state, item, err := createStartUIPlannedItem(repoSlug, payload)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.invalidateOverviewCache()
		writeJSONResponse(w, map[string]any{"state": state, "planned_item": item})
	default:
		http.NotFound(w, r)
	}
}

func (h *startUIAPI) handlePlannedItemRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/launch-now") {
		http.NotFound(w, r)
		return
	}
	itemID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/planned-items/"), "/launch-now")
	if strings.TrimSpace(itemID) == "" {
		http.NotFound(w, r)
		return
	}
	repoSlug, state, item, err := findStartUIPlannedItem(itemID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	updatedState, updatedItem, launch, err := launchStartUIPlannedItemNow(repoSlug, state, item)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.invalidateOverviewCache()
	writeJSONResponse(w, map[string]any{"state": updatedState, "planned_item": updatedItem, "launch": launch})
}

func (h *startUIAPI) handleWorkRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	runs, err := loadStartUIWorkRuns(20)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, map[string]any{"runs": runs})
}

func (h *startUIAPI) handleWorkRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	runID := strings.TrimPrefix(r.URL.Path, "/api/v1/work/runs/")
	detail, err := loadStartUIWorkRunDetail(runID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSONResponse(w, detail)
}

func (h *startUIAPI) handleHUD(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, err := h.loadHUD()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, ctx)
}

func (h *startUIAPI) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	lastHash := ""
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		payload, err := h.buildEventsPayload()
		if err == nil {
			hash := hashJSON(payload)
			if hash != lastHash {
				lastHash = hash
				data, _ := json.Marshal(payload)
				fmt.Fprintf(w, "event: state\ndata: %s\n\n", data)
				flusher.Flush()
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *startUIAPI) buildOverview() (startUIOverview, error) {
	return h.cachedOverview()
}

func (h *startUIAPI) cachedOverview() (startUIOverview, error) {
	signature, signatureErr := h.overviewSignature()
	h.overviewCache.mu.Lock()
	defer h.overviewCache.mu.Unlock()
	if signatureErr == nil && h.overviewCache.ok && h.overviewCache.signature == signature {
		return h.overviewCache.overview, nil
	}

	overview, err := h.buildOverviewUncached()
	if err != nil {
		return startUIOverview{}, err
	}
	if signatureErr == nil {
		refreshedSignature, err := h.overviewSignature()
		if err != nil {
			return overview, nil
		}
		if refreshedSignature != signature {
			signature = refreshedSignature
			overview, err = h.buildOverviewUncached()
			if err != nil {
				return startUIOverview{}, err
			}
			latestSignature, err := h.overviewSignature()
			if err != nil || latestSignature != signature {
				return overview, nil
			}
			signature = latestSignature
		} else {
			signature = refreshedSignature
		}
		h.overviewCache.signature = signature
		h.overviewCache.overview = overview
		h.overviewCache.ok = true
	}
	return overview, nil
}

func (h *startUIAPI) invalidateOverviewCache() {
	h.overviewCache.mu.Lock()
	defer h.overviewCache.mu.Unlock()
	h.overviewCache.signature = ""
	h.overviewCache.overview = startUIOverview{}
	h.overviewCache.ok = false
}

func (h *startUIAPI) overviewSignature() (string, error) {
	return startUIOverviewSignature(h.cwd)
}

func (h *startUIAPI) buildOverviewUncached() (startUIOverview, error) {
	repos, err := listStartUIRepoSummaries(false)
	if err != nil {
		return startUIOverview{}, err
	}
	workRuns, err := loadStartUIWorkRuns(startUIWorkRunLimit)
	if err != nil {
		return startUIOverview{}, err
	}
	hud, err := h.loadHUD()
	if err != nil {
		return startUIOverview{}, err
	}
	totals := startUITotals{Repos: len(repos)}
	for _, repo := range repos {
		totals.IssuesQueued += repo.IssueCounts[startWorkStatusQueued]
		totals.IssuesInProgress += repo.IssueCounts[startWorkStatusInProgress]
		totals.BlockedIssues += repo.IssueCounts[startWorkStatusBlocked]
		totals.ServiceQueued += repo.ServiceTaskCounts[startWorkServiceTaskQueued]
		totals.ServiceRunning += repo.ServiceTaskCounts[startWorkServiceTaskRunning]
		totals.PlannedQueued += repo.PlannedItemCounts[startPlannedItemQueued]
		totals.PlannedLaunching += repo.PlannedItemCounts[startPlannedItemLaunching]
	}
	for _, run := range workRuns {
		if run.Status == "running" || run.Status == "active" || run.Status == "in_progress" {
			totals.ActiveWorkRuns++
		}
	}
	return startUIOverview{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Totals:      totals,
		Repos:       repos,
		WorkRuns:    workRuns,
		HUD:         hud,
	}, nil
}

func startUIOverviewSignature(cwd string) (string, error) {
	var builder strings.Builder
	if err := appendStartUITwoLevelRepoFileSignature(&builder, "start-state", filepath.Join(githubNanaHome(), "start"), "state.json"); err != nil {
		return "", err
	}
	if err := appendStartUITwoLevelRepoFileSignature(&builder, "repo-settings", githubWorkReposRoot(), "settings.json"); err != nil {
		return "", err
	}
	workDBPath := localWorkDBPath()
	for _, path := range []string{workDBPath, workDBPath + "-wal", workDBPath + "-shm"} {
		if err := appendStartUIPathSignature(&builder, "work-db", path); err != nil {
			return "", err
		}
	}
	if err := appendStartUIWorkRunManifestSignatures(&builder, startUIWorkRunLimit); err != nil {
		return "", err
	}
	codexHome := ResolveCodexHomeForLaunch(cwd)
	for _, path := range []string{
		filepath.Join(cwd, ".nana", "hud-config.json"),
		filepath.Join(cwd, ".nana", "metrics.json"),
		managedAuthRegistryPathForHome(codexHome),
		managedAuthRuntimeStatePathForHome(codexHome),
	} {
		if err := appendStartUIPathSignature(&builder, "hud-file", path); err != nil {
			return "", err
		}
	}
	if err := appendStartUIHUDStateSignature(&builder, cwd); err != nil {
		return "", err
	}
	if err := appendStartUIGitSignature(&builder, cwd); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:]), nil
}

// appendStartUITwoLevelRepoFileSignature records only
// <root>/<owner>/<repo>/<fileName>, keeping cache-hit signature work
// proportional to watched state/settings files instead of checkout contents.
func appendStartUITwoLevelRepoFileSignature(builder *strings.Builder, label string, root string, fileName string) error {
	isDir, err := appendStartUIRootSignature(builder, label, root)
	if err != nil || !isDir {
		return err
	}
	files, err := listTwoLevelRepoFiles(root, fileName)
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := appendStartUIPathSignature(builder, label, file.Path); err != nil {
			return err
		}
	}
	return nil
}

var startUIHUDModeStateNames = []string{
	"ralph",
	"ultrawork",
	"autopilot",
	"ralplan",
	"deep-interview",
	"autoresearch",
	"ultraqa",
	"team",
}

func appendStartUIHUDStateSignature(builder *strings.Builder, cwd string) error {
	root := BaseStateDir(cwd)
	fmt.Fprintf(builder, "hud-state-root\t%s\n", root)
	for _, path := range []string{
		filepath.Join(root, "session.json"),
		filepath.Join(root, "hud-state.json"),
	} {
		if err := appendStartUIPathSignature(builder, "hud-state", path); err != nil {
			return err
		}
	}
	if err := appendStartUIHUDModeStatePathSignatures(builder, "hud-state", root); err != nil {
		return err
	}
	if sessionID := ReadCurrentSessionID(cwd); sessionID != "" {
		sessionDir := filepath.Join(root, "sessions", sessionID)
		fmt.Fprintf(builder, "hud-state-session\t%s\n", sessionDir)
		if err := appendStartUIHUDModeStatePathSignatures(builder, "hud-state-session", sessionDir); err != nil {
			return err
		}
	}
	return nil
}

func appendStartUIHUDModeStatePathSignatures(builder *strings.Builder, label string, dir string) error {
	for _, mode := range startUIHUDModeStateNames {
		if err := appendStartUIPathSignature(builder, label, filepath.Join(dir, mode+"-state.json")); err != nil {
			return err
		}
	}
	return nil
}

func appendStartUIRootSignature(builder *strings.Builder, label string, root string) (bool, error) {
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(builder, "%s\t%s\tmissing\n", label, root)
			return false, nil
		}
		return false, err
	}
	fmt.Fprintf(builder, "%s\t%s\t%d\t%d\t%t\n", label, root, info.Size(), info.ModTime().UnixNano(), info.IsDir())
	return info.IsDir(), nil
}

func appendStartUIPathSignature(builder *strings.Builder, label string, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(builder, "%s\t%s\tmissing\n", label, path)
			return nil
		}
		return err
	}
	fmt.Fprintf(builder, "%s\t%s\t%d\t%d\t%t\n", label, path, info.Size(), info.ModTime().UnixNano(), info.IsDir())
	return nil
}

func appendStartUIWorkRunManifestSignatures(builder *strings.Builder, limit int) error {
	workDBPath := localWorkDBPath()
	fmt.Fprintf(builder, "work-manifest-index\t%s\t%d\n", workDBPath, limit)
	if _, err := os.Stat(workDBPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(builder, "work-manifest-index\t%s\tmissing\n", workDBPath)
			return nil
		}
		return err
	}
	store, err := openLocalWorkDB()
	if err != nil {
		return err
	}
	defer store.Close()

	query := `SELECT backend, manifest_path FROM work_run_index ORDER BY updated_at DESC`
	args := []any{}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := store.db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	manifestPaths := map[string]bool{}
	for rows.Next() {
		var backend string
		var manifestPath sql.NullString
		if err := rows.Scan(&backend, &manifestPath); err != nil {
			return err
		}
		if backend != "github" || !manifestPath.Valid {
			continue
		}
		path := strings.TrimSpace(manifestPath.String)
		if path != "" {
			manifestPaths[path] = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	paths := make([]string, 0, len(manifestPaths))
	for path := range manifestPaths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := appendStartUIPathSignature(builder, "work-manifest", path); err != nil {
			return err
		}
	}
	return nil
}

func appendStartUIGitSignature(builder *strings.Builder, cwd string) error {
	topLevel, ok := startUIGitRevParsePath(cwd, "--show-toplevel")
	if !ok {
		fmt.Fprintf(builder, "git\t%s\tmissing\n", cwd)
		return nil
	}
	fmt.Fprintf(builder, "git-meta\ttoplevel\t%s\n", topLevel)
	if err := appendStartUIPathSignature(builder, "git", filepath.Join(topLevel, ".git")); err != nil {
		return err
	}
	if gitDir, ok := startUIGitRevParsePath(cwd, "--git-dir"); ok {
		fmt.Fprintf(builder, "git-meta\tgit-dir\t%s\n", gitDir)
		if err := appendStartUIPathSignature(builder, "git", gitDir); err != nil {
			return err
		}
	}
	if commonDir, ok := startUIGitRevParsePath(cwd, "--git-common-dir"); ok {
		fmt.Fprintf(builder, "git-meta\tgit-common-dir\t%s\n", commonDir)
		if err := appendStartUIPathSignature(builder, "git", commonDir); err != nil {
			return err
		}
	}

	for _, gitPath := range []string{"HEAD", "packed-refs", "config", "config.worktree"} {
		resolved, ok := startUIGitPath(cwd, gitPath)
		if !ok {
			continue
		}
		if err := appendStartUIPathSignature(builder, "git", resolved); err != nil {
			return err
		}
	}
	if refName, ok := startUIGitOutput(cwd, "symbolic-ref", "-q", "HEAD"); ok {
		fmt.Fprintf(builder, "git-meta\tHEAD-ref\t%s\n", refName)
		if refPath, ok := startUIGitPath(cwd, refName); ok {
			if err := appendStartUIPathSignature(builder, "git", refPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func startUIGitRevParsePath(cwd string, arg string) (string, bool) {
	if output, ok := startUIGitOutput(cwd, "rev-parse", "--path-format=absolute", arg); ok {
		return filepath.Clean(filepath.FromSlash(output)), true
	}
	if output, ok := startUIGitOutput(cwd, "rev-parse", arg); ok {
		return startUIResolveGitPath(cwd, output), true
	}
	return "", false
}

func startUIGitPath(cwd string, gitPath string) (string, bool) {
	if output, ok := startUIGitOutput(cwd, "rev-parse", "--path-format=absolute", "--git-path", gitPath); ok {
		return filepath.Clean(filepath.FromSlash(output)), true
	}
	if output, ok := startUIGitOutput(cwd, "rev-parse", "--git-path", gitPath); ok {
		return startUIResolveGitPath(cwd, output), true
	}
	return "", false
}

func startUIGitOutput(cwd string, args ...string) (string, bool) {
	output, err := readGitOutput(cwd, args...)
	if err != nil {
		return "", false
	}
	value := firstStartUINonEmptyLine(output)
	return value, value != ""
}

func firstStartUINonEmptyLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func startUIResolveGitPath(cwd string, path string) string {
	path = filepath.FromSlash(strings.TrimSpace(path))
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(cwd, path))
}

func (h *startUIAPI) buildEventsPayload() (map[string]any, error) {
	overview, err := h.buildOverview()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"generated_at": overview.GeneratedAt,
		"totals":       overview.Totals,
		"repos":        overview.Repos,
		"work_runs":    overview.WorkRuns,
		"hud":          overview.HUD,
	}, nil
}

func (h *startUIAPI) loadHUD() (HUDRenderContext, error) {
	config, err := readHUDConfig(h.cwd)
	if err != nil {
		return HUDRenderContext{}, err
	}
	return readAllHUDState(h.cwd, config)
}

func listStartUIRepoSummaries(includeState bool) ([]startUIRepoSummary, error) {
	repoSlugs, err := listStartUIRepoSlugs()
	if err != nil {
		return nil, err
	}
	summaries := make([]startUIRepoSummary, 0, len(repoSlugs))
	for _, repoSlug := range repoSlugs {
		summary, err := loadStartUIRepoSummary(repoSlug, includeState)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err == nil {
			summaries = append(summaries, summary)
		}
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].RepoSlug < summaries[j].RepoSlug
	})
	return summaries, nil
}

func listStartUIRepoSlugs() ([]string, error) {
	seen := map[string]bool{}
	repos, err := listOnboardedGithubRepos()
	if err != nil {
		return nil, err
	}
	for _, repo := range repos {
		seen[repo] = true
	}
	startRoot := filepath.Join(githubNanaHome(), "start")
	_ = filepath.WalkDir(startRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || entry.Name() != "state.json" {
			return nil
		}
		rel, err := filepath.Rel(startRoot, filepath.Dir(path))
		if err == nil {
			repoSlug := filepath.ToSlash(rel)
			if validRepoSlug(repoSlug) {
				seen[repoSlug] = true
			}
		}
		return nil
	})
	out := make([]string, 0, len(seen))
	for repo := range seen {
		out = append(out, repo)
	}
	sort.Strings(out)
	return out, nil
}

func loadStartUIRepoSummary(repoSlug string, includeState bool) (startUIRepoSummary, error) {
	settings, _ := readGithubRepoSettings(githubRepoSettingsPath(repoSlug))
	state, err := readStartWorkState(repoSlug)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return startUIRepoSummary{}, err
	}
	summary := startUIRepoSummary{
		RepoSlug:          repoSlug,
		RepoMode:          resolvedGithubRepoMode(settings),
		IssuePickMode:     resolvedGithubIssuePickMode(settings),
		PRForwardMode:     resolvedGithubPRForwardMode(settings),
		StatePath:         startWorkStatePath(repoSlug),
		SourcePath:        githubManagedPaths(repoSlug).SourcePath,
		IssueCounts:       map[string]int{},
		ServiceTaskCounts: map[string]int{},
		PlannedItemCounts: map[string]int{},
		Settings:          settings,
	}
	if state == nil {
		return summary, nil
	}
	summary.UpdatedAt = state.UpdatedAt
	summary.LastRun = state.LastRun
	summary.DefaultBranch = state.DefaultBranch
	if includeState {
		summary.State = state
	}
	for _, issue := range state.Issues {
		summary.IssueCounts[issue.Status]++
	}
	for _, task := range state.ServiceTasks {
		summary.ServiceTaskCounts[task.Status]++
	}
	for _, item := range state.PlannedItems {
		summary.PlannedItemCounts[item.State]++
	}
	return summary, nil
}

func loadStartUIWorkRuns(limit int) ([]startUIWorkRun, error) {
	store, err := openLocalWorkDB()
	if err != nil {
		return nil, err
	}
	defer store.Close()
	rows, err := store.db.Query(`SELECT run_id, backend, repo_key, repo_root, repo_name, manifest_path, updated_at, target_kind FROM work_run_index ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	runs := []startUIWorkRun{}
	for rows.Next() {
		entry, err := scanWorkRunIndexEntry(rows)
		if err != nil {
			return nil, err
		}
		run, err := startUIWorkRunFromIndex(entry)
		if err != nil {
			continue
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func startUIWorkRunFromIndex(entry workRunIndexEntry) (startUIWorkRun, error) {
	switch entry.Backend {
	case "local":
		manifest, err := readLocalWorkManifestByRunID(entry.RunID)
		if err != nil {
			return startUIWorkRun{}, err
		}
		return startUIWorkRun{
			RunID:            entry.RunID,
			Backend:          entry.Backend,
			RepoKey:          manifest.RepoID,
			RepoName:         manifest.RepoName,
			Status:           manifest.Status,
			CurrentPhase:     manifest.CurrentPhase,
			CurrentIteration: manifest.CurrentIteration,
			UpdatedAt:        manifest.UpdatedAt,
			ArtifactPath:     localWorkRunDirByID(manifest.RepoID, manifest.RunID),
		}, nil
	case "github":
		manifest, err := readGithubWorkManifest(entry.ManifestPath)
		if err != nil {
			return startUIWorkRun{}, err
		}
		return startUIWorkRun{
			RunID:            entry.RunID,
			Backend:          entry.Backend,
			RepoKey:          manifest.RepoSlug,
			RepoName:         manifest.RepoName,
			Status:           defaultString(manifest.PublicationState, "active"),
			CurrentPhase:     defaultString(manifest.NextAction, manifest.TargetKind),
			UpdatedAt:        manifest.UpdatedAt,
			TargetKind:       manifest.TargetKind,
			TargetURL:        manifest.TargetURL,
			ArtifactPath:     filepath.Dir(entry.ManifestPath),
			PublicationState: manifest.PublicationState,
		}, nil
	default:
		return startUIWorkRun{}, fmt.Errorf("unsupported backend %q", entry.Backend)
	}
}

func loadStartUIWorkRunDetail(runID string) (map[string]any, error) {
	entry, err := readWorkRunIndex(runID)
	if err != nil {
		return nil, err
	}
	summary, err := startUIWorkRunFromIndex(entry)
	if err != nil {
		return nil, err
	}
	switch entry.Backend {
	case "local":
		manifest, err := readLocalWorkManifestByRunID(runID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"summary": summary, "manifest": manifest}, nil
	case "github":
		manifest, err := readGithubWorkManifest(entry.ManifestPath)
		if err != nil {
			return nil, err
		}
		status, err := buildGithubWorkStatusSnapshot(manifest, filepath.Dir(entry.ManifestPath))
		if err != nil {
			return nil, err
		}
		return map[string]any{"summary": summary, "manifest": manifest, "status": status}, nil
	default:
		return nil, fmt.Errorf("unsupported backend %q", entry.Backend)
	}
}

func patchStartUIIssue(repoSlug string, issueNumber int, payload startUIIssuePatchRequest) (*startWorkState, startWorkIssueState, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	startWorkStateFileMu.Lock()
	state, err := readStartWorkStateUnlocked(repoSlug)
	if err != nil {
		startWorkStateFileMu.Unlock()
		return nil, startWorkIssueState{}, err
	}
	key := strconv.Itoa(issueNumber)
	issue, ok := state.Issues[key]
	if !ok {
		startWorkStateFileMu.Unlock()
		return nil, startWorkIssueState{}, fmt.Errorf("issue #%d is not tracked in start state", issueNumber)
	}
	if payload.Priority != nil {
		if *payload.Priority < 0 || *payload.Priority > 5 {
			startWorkStateFileMu.Unlock()
			return nil, startWorkIssueState{}, fmt.Errorf("priority must be between P0 and P5")
		}
		issue.ManualPriority = *payload.Priority
		issue.ManualPriorityUpdatedAt = now
		issue.Priority = *payload.Priority
		issue.PrioritySource = "manual_override"
		issue.TriageStatus = startWorkTriageCompleted
		issue.TriageRationale = "manual override " + startWorkPriorityLabel(*payload.Priority)
		issue.TriageFingerprint = issue.SourceFingerprint
		issue.TriageUpdatedAt = now
	}
	if payload.ClearSchedule {
		issue.ScheduleAt = ""
		issue.ScheduleUpdatedAt = now
		issue.DeferredReason = ""
	}
	if payload.ScheduleAt != nil {
		value := strings.TrimSpace(*payload.ScheduleAt)
		if value != "" {
			if _, err := time.Parse(time.RFC3339, value); err != nil {
				startWorkStateFileMu.Unlock()
				return nil, startWorkIssueState{}, fmt.Errorf("schedule_at must be RFC3339")
			}
		}
		issue.ScheduleAt = value
		issue.ScheduleUpdatedAt = now
	}
	if payload.DeferredReason != nil {
		issue.DeferredReason = strings.TrimSpace(*payload.DeferredReason)
	}
	issue.UpdatedAt = now
	state.Issues[key] = issue
	state.UpdatedAt = now
	if err := writeStartWorkStateUnlocked(*state); err != nil {
		startWorkStateFileMu.Unlock()
		return nil, startWorkIssueState{}, err
	}
	startWorkStateFileMu.Unlock()
	if payload.Priority != nil {
		if nextLabels, mirrorErr := mirrorStartWorkIssuePriority(repoSlug, issue.SourceNumber, issue.Labels, *payload.Priority); mirrorErr == nil && len(nextLabels) > 0 {
			startWorkStateFileMu.Lock()
			defer startWorkStateFileMu.Unlock()
			refreshedState, readErr := readStartWorkStateUnlocked(repoSlug)
			if readErr == nil {
				refreshedIssue := refreshedState.Issues[key]
				refreshedIssue.Labels = nextLabels
				refreshedIssue.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
				refreshedState.Issues[key] = refreshedIssue
				refreshedState.UpdatedAt = refreshedIssue.UpdatedAt
				_ = writeStartWorkStateUnlocked(*refreshedState)
				state = refreshedState
				issue = refreshedIssue
			}
		}
	}
	return state, issue, nil
}

func createStartUIPlannedItem(repoSlug string, payload startUIPlannedItemRequest) (*startWorkState, startWorkPlannedItem, error) {
	title := strings.TrimSpace(payload.Title)
	if title == "" {
		return nil, startWorkPlannedItem{}, fmt.Errorf("title is required")
	}
	scheduleAt := strings.TrimSpace(payload.ScheduleAt)
	if scheduleAt != "" {
		if _, err := time.Parse(time.RFC3339, scheduleAt); err != nil {
			return nil, startWorkPlannedItem{}, fmt.Errorf("schedule_at must be RFC3339")
		}
	}
	priority := 3
	if payload.Priority != nil {
		priority = *payload.Priority
	}
	if priority < 0 || priority > 5 {
		priority = 3
	}
	now := time.Now().UTC().Format(time.RFC3339)
	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	state, err := ensureStartUIStateUnlocked(repoSlug)
	if err != nil {
		return nil, startWorkPlannedItem{}, err
	}
	itemID := fmt.Sprintf("planned-%d", time.Now().UnixNano())
	item := startWorkPlannedItem{
		ID:          itemID,
		RepoSlug:    repoSlug,
		Title:       title,
		Description: strings.TrimSpace(payload.Description),
		LaunchKind:  strings.TrimSpace(payload.LaunchKind),
		Priority:    priority,
		ScheduleAt:  scheduleAt,
		State:       startPlannedItemQueued,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if item.LaunchKind == "" {
		settings, _ := readGithubRepoSettings(githubRepoSettingsPath(repoSlug))
		if resolvedGithubRepoMode(settings) == "local" {
			item.LaunchKind = "local_work"
		} else {
			item.LaunchKind = "github_issue"
		}
	}
	if state.PlannedItems == nil {
		state.PlannedItems = map[string]startWorkPlannedItem{}
	}
	state.PlannedItems[item.ID] = item
	state.UpdatedAt = now
	if err := writeStartWorkStateUnlocked(*state); err != nil {
		return nil, startWorkPlannedItem{}, err
	}
	return state, item, nil
}

func findStartUIPlannedItem(itemID string) (string, *startWorkState, startWorkPlannedItem, error) {
	repos, err := listStartUIRepoSlugs()
	if err != nil {
		return "", nil, startWorkPlannedItem{}, err
	}
	for _, repoSlug := range repos {
		state, stateErr := readStartWorkState(repoSlug)
		if stateErr != nil {
			continue
		}
		if item, ok := state.PlannedItems[itemID]; ok {
			return repoSlug, state, item, nil
		}
	}
	return "", nil, startWorkPlannedItem{}, fmt.Errorf("planned item %s was not found", itemID)
}

func launchStartUIPlannedItemNow(repoSlug string, state *startWorkState, item startWorkPlannedItem) (*startWorkState, startWorkPlannedItem, startPlannedLaunchResult, error) {
	startWorkStateFileMu.Lock()
	freshState, err := readStartWorkStateUnlocked(repoSlug)
	if err != nil {
		startWorkStateFileMu.Unlock()
		return nil, startWorkPlannedItem{}, startPlannedLaunchResult{}, err
	}
	freshItem := freshState.PlannedItems[item.ID]
	if strings.TrimSpace(freshItem.State) != startPlannedItemQueued && strings.TrimSpace(freshItem.State) != startPlannedItemFailed {
		startWorkStateFileMu.Unlock()
		return nil, startWorkPlannedItem{}, startPlannedLaunchResult{}, fmt.Errorf("planned item %s is not launchable from state %s", item.ID, freshItem.State)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	freshItem.State = startPlannedItemLaunching
	freshItem.LastError = ""
	freshItem.UpdatedAt = now
	freshState.PlannedItems[item.ID] = freshItem
	freshState.UpdatedAt = now
	if err := writeStartWorkStateUnlocked(*freshState); err != nil {
		startWorkStateFileMu.Unlock()
		return nil, startWorkPlannedItem{}, startPlannedLaunchResult{}, err
	}
	startWorkStateFileMu.Unlock()

	settings, _ := readGithubRepoSettings(githubRepoSettingsPath(repoSlug))
	workOptions := startWorkOptions{
		RepoSlug:       repoSlug,
		PublishTarget:  repoModeToPublishTarget(resolvedGithubRepoMode(settings)),
		RepoMode:       resolvedGithubRepoMode(settings),
		IssuePickMode:  resolvedGithubIssuePickMode(settings),
		PRForwardMode:  resolvedGithubPRForwardMode(settings),
		ForkIssuesMode: defaultString(normalizeGithubAutomationMode(settings.ForkIssuesMode), issuePickModeToAutomationMode(resolvedGithubIssuePickMode(settings))),
		ImplementMode:  defaultString(normalizeGithubAutomationMode(settings.ImplementMode), issuePickModeToAutomationMode(resolvedGithubIssuePickMode(settings))),
		Parallel:       startWorkDefaultParallel,
		MaxOpenPR:      startWorkDefaultOpenPRCap,
	}
	launch, launchErr := startLaunchPlannedItem(githubManagedPaths(repoSlug).SourcePath, repoSlug, workOptions, freshItem)

	startWorkStateFileMu.Lock()
	defer startWorkStateFileMu.Unlock()
	updatedState, err := readStartWorkStateUnlocked(repoSlug)
	if err != nil {
		return nil, startWorkPlannedItem{}, startPlannedLaunchResult{}, err
	}
	updatedItem := updatedState.PlannedItems[item.ID]
	if launchErr != nil {
		updatedItem.State = startPlannedItemFailed
		updatedItem.LastError = launchErr.Error()
	} else {
		updatedItem.State = startPlannedItemLaunched
		updatedItem.LaunchRunID = launch.RunID
		updatedItem.LaunchIssueNumber = launch.IssueNumber
		updatedItem.LaunchIssueURL = launch.IssueURL
		updatedItem.LaunchResult = defaultString(launch.Result, launch.Status)
		updatedItem.LastError = ""
	}
	updatedItem.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	updatedState.PlannedItems[item.ID] = updatedItem
	updatedState.UpdatedAt = updatedItem.UpdatedAt
	if err := writeStartWorkStateUnlocked(*updatedState); err != nil {
		return nil, startWorkPlannedItem{}, startPlannedLaunchResult{}, err
	}
	return updatedState, updatedItem, launch, launchErr
}

func ensureStartUIStateUnlocked(repoSlug string) (*startWorkState, error) {
	state, err := readStartWorkStateUnlocked(repoSlug)
	if err == nil {
		return state, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	state = &startWorkState{
		Version:        startWorkStateVersion,
		SourceRepo:     repoSlug,
		CreatedAt:      now,
		UpdatedAt:      now,
		Issues:         map[string]startWorkIssueState{},
		ServiceTasks:   map[string]startWorkServiceTask{},
		Promotions:     map[string]startWorkPromotion{},
		PromotionSkips: map[string]startWorkPromotionSkip{},
		PlannedItems:   map[string]startWorkPlannedItem{},
	}
	return state, nil
}

func parseStartUIRepoRoute(path string) (string, string, bool) {
	trimmed := strings.TrimPrefix(path, "/api/v1/repos/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 3 {
		return "", "", false
	}
	repoSlug := parts[0] + "/" + parts[1]
	return repoSlug, strings.Join(parts[2:], "/"), true
}

func writeJSONResponse(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}

func hashJSON(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
