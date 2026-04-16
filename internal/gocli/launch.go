package gocli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	MadmaxFlag         = "--madmax"
	CodexBypassFlag    = "--dangerously-bypass-approvals-and-sandbox"
	CodexFastFlag      = "--fast"
	HighReasoningFlag  = "--high"
	XHighReasoningFlag = "--xhigh"
	SparkFlag          = "--spark"
	MadmaxSparkFlag    = "--madmax-spark"
	ConfigFlag         = "-c"
	LongConfigFlag     = "--config"

	ModelInstructionsFileKey = "model_instructions_file"
	NotifyTempEnv            = "NANA_NOTIFY_TEMP"
	NotifyTempContractEnv    = "NANA_NOTIFY_TEMP_CONTRACT"
)

type CLIInvocation struct {
	Command    string
	LaunchArgs []string
}

type NotifyTempContract struct {
	Active             bool     `json:"active"`
	Selectors          []string `json:"selectors"`
	CanonicalSelectors []string `json:"canonicalSelectors"`
	Warnings           []string `json:"warnings"`
	Source             string   `json:"source"`
}

type ParseNotifyTempContractResult struct {
	Contract        NotifyTempContract
	PassthroughArgs []string
}

type WorktreeMode struct {
	Enabled  bool
	Detached bool
	Name     string
}

type ParsedWorktreeMode struct {
	Mode          WorktreeMode
	RemainingArgs []string
}

type GitWorktreeEntry struct {
	Path      string
	Head      string
	BranchRef string
	Detached  bool
}

func ResolveCLIInvocation(args []string) CLIInvocation {
	if len(args) == 0 {
		return CLIInvocation{Command: "launch", LaunchArgs: nil}
	}

	firstArg := args[0]
	switch firstArg {
	case "--help", "-h", "help":
		return CLIInvocation{Command: "help", LaunchArgs: nil}
	case "--version", "-v", "version":
		return CLIInvocation{Command: "version", LaunchArgs: nil}
	}

	if strings.HasPrefix(firstArg, "-") {
		return CLIInvocation{Command: "launch", LaunchArgs: args}
	}

	switch firstArg {
	case "launch":
		return CLIInvocation{Command: "launch", LaunchArgs: args[1:]}
	case "resume":
		return CLIInvocation{Command: "resume", LaunchArgs: args[1:]}
	case "exec":
		return CLIInvocation{Command: "exec", LaunchArgs: args[1:]}
	case "implement":
		return CLIInvocation{Command: "implement", LaunchArgs: args[1:]}
	case "investigate":
		return CLIInvocation{Command: "investigate", LaunchArgs: args[1:]}
	case "start":
		return CLIInvocation{Command: "start", LaunchArgs: nil}
	case "next":
		return CLIInvocation{Command: "next", LaunchArgs: nil}
	case "improve":
		return CLIInvocation{Command: "improve", LaunchArgs: nil}
	case "enhance":
		return CLIInvocation{Command: "enhance", LaunchArgs: nil}
	case "ui-scout":
		return CLIInvocation{Command: "ui-scout", LaunchArgs: nil}
	case "sync":
		return CLIInvocation{Command: "sync", LaunchArgs: args[1:]}
	case "issue":
		return CLIInvocation{Command: "issue", LaunchArgs: nil}
	case "review":
		return CLIInvocation{Command: "review", LaunchArgs: nil}
	case "review-rules":
		return CLIInvocation{Command: "review-rules", LaunchArgs: nil}
	case "repo":
		return CLIInvocation{Command: "repo", LaunchArgs: nil}
	case "work":
		return CLIInvocation{Command: "work", LaunchArgs: nil}
	case "work-on":
		return CLIInvocation{Command: "work-on", LaunchArgs: nil}
	case "work-local":
		return CLIInvocation{Command: "work-local", LaunchArgs: nil}
	case "explore":
		return CLIInvocation{Command: "reflect", LaunchArgs: nil}
	default:
		return CLIInvocation{Command: firstArg, LaunchArgs: nil}
	}
}

func NormalizeCodexLaunchArgs(args []string) []string {
	normalized, _ := NormalizeCodexLaunchArgsWithFast(args)
	return normalized
}

func NormalizeCodexLaunchArgsWithFast(args []string) ([]string, bool) {
	normalized := make([]string, 0, len(args)+2)
	wantsBypass := false
	hasBypass := false
	reasoningMode := ""
	fastMode := false

	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == MadmaxFlag:
			wantsBypass = true
		case arg == CodexBypassFlag:
			wantsBypass = true
			if !hasBypass {
				normalized = append(normalized, arg)
				hasBypass = true
			}
		case arg == CodexFastFlag:
			fastMode = true
		case arg == "--effort":
			if index+1 < len(args) && reasoningModes[strings.TrimSpace(args[index+1])] {
				reasoningMode = strings.TrimSpace(args[index+1])
				index++
			} else {
				normalized = append(normalized, arg)
			}
		case strings.HasPrefix(arg, "--effort="):
			value := strings.TrimSpace(strings.TrimPrefix(arg, "--effort="))
			if reasoningModes[value] {
				reasoningMode = value
			} else {
				normalized = append(normalized, arg)
			}
		case arg == HighReasoningFlag:
			reasoningMode = "high"
		case arg == XHighReasoningFlag:
			reasoningMode = "xhigh"
		case arg == SparkFlag:
			// Spark model is worker-only. Do not forward to the leader.
		case arg == MadmaxSparkFlag:
			wantsBypass = true
		case arg == "--worktree" || arg == "-w":
			if index+1 < len(args) && !strings.HasPrefix(args[index+1], "-") && !strings.Contains(args[index+1], ":") {
				index++
			}
		case strings.HasPrefix(arg, "--worktree="), strings.HasPrefix(arg, "-w="):
			// Consumed by ParseWorktreeMode; keep Normalize defensive too.
		case strings.HasPrefix(arg, "-w") && len(arg) > 2:
			// Consumed by ParseWorktreeMode; keep Normalize defensive too.
		default:
			normalized = append(normalized, arg)
		}
	}

	if wantsBypass && !hasBypass {
		normalized = append(normalized, CodexBypassFlag)
	}
	if reasoningMode != "" {
		normalized = append(normalized, ConfigFlag, fmt.Sprintf(`%s="%s"`, ReasoningKey, reasoningMode))
	}

	return normalized, fastMode
}

func Launch(cwd string, args []string) error {
	return runCodexLaunch(cwd, nil, args)
}

func Resume(cwd string, args []string) error {
	return runCodexLaunch(cwd, []string{"resume"}, args)
}

func Exec(cwd string, args []string) error {
	return runCodexLaunch(cwd, []string{"exec"}, args)
}

func runCodexLaunch(cwd string, commandPrefix []string, rawArgs []string) error {
	parsedWorktree := ParseWorktreeMode(rawArgs)
	parsedNotify := ParseNotifyTempContract(parsedWorktree.RemainingArgs, currentEnvMap())

	launchCwd := cwd
	if parsedWorktree.Mode.Enabled {
		worktreePath, err := EnsureLaunchWorktree(cwd, parsedWorktree.Mode)
		if err != nil {
			return err
		}
		launchCwd = worktreePath
	}

	for _, warning := range parsedNotify.Contract.Warnings {
		fmt.Fprintf(os.Stderr, "[nana] %s\n", warning)
	}

	codexHome := ResolveCodexHomeForLaunch(launchCwd)
	normalized, fastMode := NormalizeCodexLaunchArgsWithFast(parsedNotify.PassthroughArgs)
	codexArgs := append(append([]string{}, commandPrefix...), normalized...)
	codexArgs = injectCodexFastSlashCommand(codexArgs, fastMode)
	repoRoot := resolvePackageRoot()
	if err := MaybeCheckAndPromptUpdate(repoRoot, launchCwd); err != nil {
		fmt.Fprintf(os.Stderr, "[nana-go] update warning: %v\n", err)
	}
	return runCodexSession(launchCwd, codexArgs, parsedNotify.Contract, codexHome)
}

func prefixCodexFastPrompt(prompt string, fast bool) string {
	if !fast {
		return prompt
	}
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return "/fast"
	}
	if strings.HasPrefix(trimmed, "/fast") {
		return prompt
	}
	return "/fast\n\n" + prompt
}

func injectCodexFastSlashCommand(args []string, fast bool) []string {
	if !fast {
		return args
	}
	out := append([]string{}, args...)
	if len(out) == 0 {
		return []string{"/fast"}
	}
	promptIndex := findCodexPromptArgIndex(out)
	if promptIndex < 0 {
		return append(out, "/fast")
	}
	out[promptIndex] = prefixCodexFastPrompt(out[promptIndex], true)
	return out
}

func findCodexPromptArgIndex(args []string) int {
	start := 0
	if len(args) > 0 {
		switch args[0] {
		case "exec", "review", "resume", "fork", "cloud":
			start = 1
		}
	}
	for index := start; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			if index+1 < len(args) {
				return index + 1
			}
			return -1
		}
		if strings.HasPrefix(arg, "-") {
			if codexOptionTakesValue(arg) && index+1 < len(args) {
				index++
			}
			continue
		}
		return index
	}
	return -1
}

func codexOptionTakesValue(arg string) bool {
	switch arg {
	case "-c", "--config", "-i", "--image", "-m", "--model", "-p", "--profile", "-s", "--sandbox", "-a", "--ask-for-approval", "-C", "--cd", "--add-dir", "--output-format", "--input-format", "--json-schema", "--settings", "--agent", "--agents", "--system-prompt", "--append-system-prompt", "--mcp-config", "--name", "-n", "--session-id":
		return true
	default:
		return false
	}
}

func runCodexSession(cwd string, codexArgs []string, notifyContract NotifyTempContract, codexHome string) error {
	if _, err := exec.LookPath("codex"); err != nil {
		return fmt.Errorf("codex is required: %w", err)
	}

	authManager, err := prepareManagedAuthManager(cwd, codexHome)
	if err != nil {
		return err
	}

	sessionID := fmt.Sprintf("nana-%d", time.Now().UnixNano())
	if err := writeLaunchSessionStart(cwd, sessionID); err != nil {
		return err
	}
	defer removeLaunchSessionState(cwd)

	sessionInstructionsPath, err := writeSessionModelInstructions(cwd, sessionID, codexHome)
	if err != nil {
		return err
	}
	defer removeSessionInstructionsFile(cwd, sessionID)

	codexArgs = injectModelInstructionsArgs(codexArgs, sessionInstructionsPath)

	cmd := exec.Command("codex", codexArgs...)
	cmd.Dir = cwd
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = authManager.wrapOutput(os.Stderr)
	cmd.Env = buildCodexEnv(notifyContract, codexHome)
	stopAuthMonitor := make(chan struct{})
	if authManager != nil {
		authManager.start(stopAuthMonitor, time.Now().UTC())
	}
	defer close(stopAuthMonitor)
	return cmd.Run()
}

func buildCodexEnv(notifyContract NotifyTempContract, codexHome string) []string {
	envMap := buildCodexEnvMap(notifyContract, codexHome)
	return envMapToList(envMap)
}

func buildGithubCodexEnv(notifyContract NotifyTempContract, codexHome string, apiBaseURL string) []string {
	envMap := buildCodexEnvMap(notifyContract, codexHome)
	hydrateGithubAuthEnv(envMap, apiBaseURL)
	return envMapToList(envMap)
}

func buildCodexEnvMap(notifyContract NotifyTempContract, codexHome string) map[string]string {
	envMap := currentEnvMap()
	if strings.TrimSpace(codexHome) != "" {
		envMap["CODEX_HOME"] = codexHome
	}
	if notifyContract.Active {
		envMap[NotifyTempEnv] = "1"
		if encoded, err := json.Marshal(notifyContract); err == nil {
			envMap[NotifyTempContractEnv] = string(encoded)
		}
	}
	return envMap
}

func hydrateGithubAuthEnv(envMap map[string]string, apiBaseURL string) {
	if token := strings.TrimSpace(envMap["GH_TOKEN"]); token != "" {
		if strings.TrimSpace(envMap["GITHUB_TOKEN"]) == "" {
			envMap["GITHUB_TOKEN"] = token
		}
		return
	}
	if token := strings.TrimSpace(envMap["GITHUB_TOKEN"]); token != "" {
		envMap["GH_TOKEN"] = token
		return
	}
	token, err := resolveGithubTokenForAPIBase(apiBaseURL)
	if err != nil || strings.TrimSpace(token) == "" {
		return
	}
	envMap["GH_TOKEN"] = token
	envMap["GITHUB_TOKEN"] = token
}

func bootstrapResolvedCodexAuth(cwd string) (bool, error) {
	codexHome := ResolveCodexHomeForLaunch(cwd)
	target := filepath.Join(codexHome, "auth.json")
	registry, err := loadManagedAuthRegistry(codexHome)
	if err != nil {
		return false, err
	}
	if len(registry.Accounts) > 0 {
		manager, managerErr := prepareManagedAuthManager(cwd, codexHome)
		if managerErr != nil {
			return false, managerErr
		}
		return manager != nil, nil
	}
	if _, err := os.Stat(target); err == nil {
		return false, nil
	}

	source := LegacyCodexAuthPath(os.Getenv("HOME"))
	if source == target {
		return false, nil
	}
	if _, err := os.Stat(source); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	if err := copyFile(source, target); err != nil {
		return false, err
	}
	fmt.Fprintf(os.Stdout, "[nana] Imported Codex credentials from %s to %s\n", source, target)
	return true, nil
}

func copyFile(source string, target string) error {
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	targetFile, err := os.Create(target)
	if err != nil {
		return err
	}
	defer targetFile.Close()

	_, err = io.Copy(targetFile, sourceFile)
	return err
}

func launchSessionPath(cwd string) string {
	return filepath.Join(BaseStateDir(cwd), "session.json")
}

func sessionStateDir(cwd string, sessionID string) string {
	return filepath.Join(BaseStateDir(cwd), "sessions", sessionID)
}

func sessionInstructionsPath(cwd string, sessionID string) string {
	return filepath.Join(sessionStateDir(cwd, sessionID), "AGENTS.md")
}

func writeLaunchSessionStart(cwd string, sessionID string) error {
	stateDir := BaseStateDir(cwd)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}

	state := sessionState{
		SessionID: sessionID,
		StartedAt: ISOTimeNow(),
		CWD:       cwd,
		PID:       os.Getpid(),
		Platform:  runtimePlatform(),
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(launchSessionPath(cwd), data, 0o644)
}

func runtimePlatform() string {
	return runtime.GOOS
}

func currentEnvMap() map[string]string {
	envMap := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		envMap[key] = value
	}
	return envMap
}

func envMapToList(envMap map[string]string) []string {
	entries := make([]string, 0, len(envMap))
	for key, value := range envMap {
		entries = append(entries, key+"="+value)
	}
	return entries
}

func removeLaunchSessionState(cwd string) {
	_ = os.Remove(launchSessionPath(cwd))
}

func removeSessionInstructionsFile(cwd string, sessionID string) {
	_ = os.Remove(sessionInstructionsPath(cwd, sessionID))
}

func writeSessionModelInstructions(cwd string, sessionID string, codexHome string) (string, error) {
	path := sessionInstructionsPath(cwd, sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}

	parts := []string{}
	for _, sourcePath := range []string{
		filepath.Join(codexHome, "AGENTS.md"),
		filepath.Join(cwd, "AGENTS.md"),
	} {
		content, err := os.ReadFile(sourcePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		trimmed := strings.TrimSpace(string(content))
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}

	overlay := strings.Join([]string{
		"<!-- NANA:RUNTIME:START -->",
		"<session_context>",
		fmt.Sprintf("**Session:** %s | %s", sessionID, ISOTimeNow()),
		"",
		"**Compaction Protocol:**",
		"Before context compaction, preserve critical state.",
		"</session_context>",
		"<!-- NANA:RUNTIME:END -->",
	}, "\n")

	parts = append(parts, overlay)
	content := strings.Join(parts, "\n\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func injectModelInstructionsArgs(args []string, instructionsPath string) []string {
	if strings.TrimSpace(os.Getenv("NANA_BYPASS_DEFAULT_SYSTEM_PROMPT")) == "0" {
		return append([]string{}, args...)
	}
	if hasModelInstructionsOverride(args) {
		return append([]string{}, args...)
	}
	updated := append([]string{}, args...)
	updated = append(updated, ConfigFlag, fmt.Sprintf(`%s="%s"`, ModelInstructionsFileKey, escapeTomlString(instructionsPath)))
	return updated
}

func hasModelInstructionsOverride(args []string) bool {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == ConfigFlag || arg == LongConfigFlag {
			if index+1 < len(args) && isModelInstructionsOverride(args[index+1]) {
				return true
			}
			continue
		}
		if strings.HasPrefix(arg, LongConfigFlag+"=") && isModelInstructionsOverride(strings.TrimPrefix(arg, LongConfigFlag+"=")) {
			return true
		}
	}
	return false
}

func isModelInstructionsOverride(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), ModelInstructionsFileKey+"=")
}

func ParseNotifyTempContract(args []string, env map[string]string) ParseNotifyTempContractResult {
	passthrough := make([]string, 0, len(args))
	selectors := []string{}
	warnings := []string{}
	cliActivated := false

	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "--notify-temp":
			cliActivated = true
			continue
		case "--discord", "--slack", "--telegram":
			selectors = append(selectors, strings.TrimPrefix(arg, "--"))
			continue
		case "--custom":
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") {
				warnings = append(warnings, "notify temp: ignoring --custom without a provider name")
				continue
			}
			normalized := normalizeCustomSelector(args[index+1])
			if normalized == "" {
				warnings = append(warnings, fmt.Sprintf("notify temp: ignoring invalid --custom selector %q", args[index+1]))
			} else {
				selectors = append(selectors, normalized)
			}
			index++
			continue
		default:
			if strings.HasPrefix(arg, "--custom=") {
				raw := strings.TrimPrefix(arg, "--custom=")
				normalized := normalizeCustomSelector(raw)
				if normalized == "" {
					warnings = append(warnings, fmt.Sprintf("notify temp: ignoring invalid --custom selector %q", raw))
				} else {
					selectors = append(selectors, normalized)
				}
				continue
			}
		}
		passthrough = append(passthrough, arg)
	}

	envActivated := env[NotifyTempEnv] == "1"
	canonical := uniqueStrings(selectors)
	providerActivated := len(canonical) > 0
	active := cliActivated || envActivated || providerActivated
	if providerActivated && !cliActivated && !envActivated {
		warnings = append(warnings, "notify temp: provider selectors imply temp mode (auto-activated)")
	}

	source := "none"
	switch {
	case cliActivated:
		source = "cli"
	case envActivated:
		source = "env"
	case providerActivated:
		source = "providers"
	}

	return ParseNotifyTempContractResult{
		Contract: NotifyTempContract{
			Active:             active,
			Selectors:          append([]string{}, selectors...),
			CanonicalSelectors: canonical,
			Warnings:           warnings,
			Source:             source,
		},
		PassthroughArgs: passthrough,
	}
}

func normalizeCustomSelector(raw string) string {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if normalized == "" {
		return ""
	}
	if strings.HasPrefix(normalized, "openclaw:") {
		gateway := strings.TrimSpace(strings.TrimPrefix(normalized, "openclaw:"))
		if gateway == "" {
			return ""
		}
		return "openclaw:" + gateway
	}
	return "custom:" + normalized
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		unique = append(unique, value)
	}
	return unique
}

func ParseWorktreeMode(args []string) ParsedWorktreeMode {
	mode := WorktreeMode{}
	remaining := []string{}

	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "--worktree" || arg == "-w":
			if index+1 < len(args) && args[index+1] != "" && !strings.HasPrefix(args[index+1], "-") && !strings.Contains(args[index+1], ":") {
				mode = WorktreeMode{Enabled: true, Detached: false, Name: args[index+1]}
				index++
			} else {
				mode = WorktreeMode{Enabled: true, Detached: true}
			}
		case strings.HasPrefix(arg, "--worktree="):
			value := strings.TrimSpace(strings.TrimPrefix(arg, "--worktree="))
			if value == "" {
				mode = WorktreeMode{Enabled: true, Detached: true}
			} else {
				mode = WorktreeMode{Enabled: true, Detached: false, Name: value}
			}
		case strings.HasPrefix(arg, "-w="):
			value := strings.TrimSpace(strings.TrimPrefix(arg, "-w="))
			if value == "" {
				mode = WorktreeMode{Enabled: true, Detached: true}
			} else {
				mode = WorktreeMode{Enabled: true, Detached: false, Name: value}
			}
		case strings.HasPrefix(arg, "-w") && len(arg) > 2:
			value := strings.TrimSpace(arg[2:])
			if value == "" {
				mode = WorktreeMode{Enabled: true, Detached: true}
			} else {
				mode = WorktreeMode{Enabled: true, Detached: false, Name: value}
			}
		default:
			remaining = append(remaining, args[index])
		}
	}

	return ParsedWorktreeMode{Mode: mode, RemainingArgs: remaining}
}

func EnsureLaunchWorktree(cwd string, mode WorktreeMode) (string, error) {
	repoRoot, err := readGitOutput(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	repoRoot = strings.TrimSpace(repoRoot)
	baseRef, err := readGitOutput(repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	baseRef = strings.TrimSpace(baseRef)

	branchName := ""
	if mode.Enabled && !mode.Detached {
		branchName = mode.Name
		if err := validateBranchName(repoRoot, branchName); err != nil {
			return "", err
		}
	}

	worktreePath := resolveLaunchWorktreePath(repoRoot, mode)
	existing, err := findWorktreeEntry(repoRoot, worktreePath)
	if err != nil {
		return "", err
	}
	if existing != nil {
		if mode.Detached {
			if !existing.Detached || existing.Head != baseRef {
				return "", fmt.Errorf("worktree_target_mismatch:%s", worktreePath)
			}
		} else {
			expectedBranchRef := "refs/heads/" + branchName
			if existing.BranchRef != expectedBranchRef {
				return "", fmt.Errorf("worktree_target_mismatch:%s", worktreePath)
			}
		}
		if isWorktreeDirty(worktreePath) {
			return "", fmt.Errorf("worktree_dirty:%s", worktreePath)
		}
		return filepath.Clean(worktreePath), nil
	}

	if _, err := os.Stat(worktreePath); err == nil {
		return "", fmt.Errorf("worktree_path_conflict:%s", worktreePath)
	}
	if branchName != "" && branchInUse(repoRoot, branchName, worktreePath) {
		return "", fmt.Errorf("branch_in_use:%s", branchName)
	}

	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return "", err
	}

	args := []string{"worktree", "add"}
	branchExistsAlready := branchName != "" && gitBranchExists(repoRoot, branchName)
	switch {
	case mode.Detached:
		args = append(args, "--detach", worktreePath, baseRef)
	case branchExistsAlready:
		args = append(args, worktreePath, branchName)
	default:
		args = append(args, "-b", branchName, worktreePath, baseRef)
	}

	if _, err := runGitCommand(repoRoot, args...); err != nil {
		return "", err
	}
	return filepath.Clean(worktreePath), nil
}

func validateBranchName(repoRoot string, branchName string) error {
	cmd := exec.Command("git", "check-ref-format", "--branch", branchName)
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		stderr := strings.TrimSpace(string(output))
		if stderr == "" {
			stderr = "invalid_worktree_branch:" + branchName
		}
		return fmt.Errorf("%s", stderr)
	}
	return nil
}

func resolveLaunchWorktreePath(repoRoot string, mode WorktreeMode) string {
	parent := filepath.Dir(repoRoot)
	bucket := filepath.Base(repoRoot) + ".nana-worktrees"
	if !mode.Enabled || mode.Detached {
		return filepath.Join(parent, bucket, "launch-detached")
	}
	return filepath.Join(parent, bucket, "launch-"+sanitizePathToken(mode.Name))
}

func sanitizePathToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, ch := range value {
		valid := (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')
		if valid {
			builder.WriteRune(ch)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteRune('-')
			lastDash = true
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		return "default"
	}
	return result
}

func findWorktreeEntry(repoRoot string, worktreePath string) (*GitWorktreeEntry, error) {
	entries, err := listWorktrees(repoRoot)
	if err != nil {
		return nil, err
	}
	cleanPath := filepath.Clean(worktreePath)
	for _, entry := range entries {
		if filepath.Clean(entry.Path) == cleanPath {
			entryCopy := entry
			return &entryCopy, nil
		}
	}
	return nil, nil
}

func listWorktrees(repoRoot string) ([]GitWorktreeEntry, error) {
	output, err := readGitOutput(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(output) == "" {
		return nil, nil
	}
	chunks := strings.Split(strings.TrimSpace(output), "\n\n")
	entries := make([]GitWorktreeEntry, 0, len(chunks))
	for _, chunk := range chunks {
		lines := strings.Split(strings.TrimSpace(chunk), "\n")
		entry := GitWorktreeEntry{}
		for _, line := range lines {
			switch {
			case strings.HasPrefix(line, "worktree "):
				entry.Path = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
			case strings.HasPrefix(line, "HEAD "):
				entry.Head = strings.TrimSpace(strings.TrimPrefix(line, "HEAD "))
			case strings.HasPrefix(line, "branch "):
				entry.BranchRef = strings.TrimSpace(strings.TrimPrefix(line, "branch "))
			case strings.TrimSpace(line) == "detached":
				entry.Detached = true
			}
		}
		if entry.Path != "" && entry.Head != "" {
			if entry.BranchRef == "" {
				entry.Detached = true
			}
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func branchInUse(repoRoot string, branchName string, worktreePath string) bool {
	entries, err := listWorktrees(repoRoot)
	if err != nil {
		return false
	}
	expectedBranchRef := "refs/heads/" + branchName
	cleanPath := filepath.Clean(worktreePath)
	for _, entry := range entries {
		if entry.BranchRef == expectedBranchRef && filepath.Clean(entry.Path) != cleanPath {
			return true
		}
	}
	return false
}

func gitBranchExists(repoRoot string, branchName string) bool {
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+branchName)
	cmd.Dir = repoRoot
	return cmd.Run() == nil
}

func isWorktreeDirty(worktreePath string) bool {
	output, err := readGitOutput(worktreePath, "status", "--porcelain")
	if err != nil {
		return false
	}
	return strings.TrimSpace(output) != ""
}

func readGitOutput(cwd string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if err != nil {
		stderr := strings.TrimSpace(string(output))
		if stderr == "" {
			stderr = fmt.Sprintf("git %s failed", strings.Join(args, " "))
		}
		return "", fmt.Errorf("%s", stderr)
	}
	return string(output), nil
}

func runGitCommand(cwd string, args ...string) (string, error) {
	return readGitOutput(cwd, args...)
}
