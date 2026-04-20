package gocli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type doctorCheck struct {
	Name        string
	Status      string
	Message     string
	Remediation *doctorRemediation
}

type doctorRemediation struct {
	Path             string
	SafeAutomaticFix string
	ManualFallback   string
}

func Doctor(cwd string, repoRoot string) error {
	scope, source := resolveDoctorScope(cwd)
	paths := resolveDoctorPaths(cwd, scope)

	fmt.Fprintln(os.Stdout, "nana doctor")
	fmt.Fprintln(os.Stdout, "==================")
	fmt.Fprintln(os.Stdout)
	if source == "persisted" {
		fmt.Fprintf(os.Stdout, "Resolved setup scope: %s (from .nana/setup-scope.json)\n\n", scope)
	} else {
		fmt.Fprintf(os.Stdout, "Resolved setup scope: %s\n\n", scope)
	}

	checks := []doctorCheck{
		checkCodexCLI(),
		checkNodeVersion(),
		checkGithubCLI(),
		checkGithubAuth(),
		checkGithubAutomationRepos(),
		checkRepoGitDrift(cwd, repoRoot),
		checkExploreHarness(repoRoot),
		checkDirectory("Codex home", paths.codexHomeDir),
		checkManagedAccounts(paths.codexHomeDir),
		checkConfig(paths.configPath, scope),
		checkExploreRouting(paths.configPath),
		checkPrompts(paths.promptsDir),
		checkSkills(paths.skillsDir),
	}
	if scope == "user" {
		checks = append(checks, checkLegacySkillRootOverlap())
	}
	checks = append(checks,
		checkAgentsMD(scope, cwd, paths.codexHomeDir),
		checkAgentsRuntimeSections(scope, cwd, paths.codexHomeDir),
		checkManagedAgentsFreshness(scope, cwd, paths.codexHomeDir, repoRoot),
		checkManagedPromptFreshness(scope, cwd, paths.codexHomeDir, repoRoot),
		checkManagedSkillFreshness(scope, cwd, paths.codexHomeDir, repoRoot),
		checkDirectory("State dir", BaseStateDir(cwd)),
		checkNanaStatePaths(cwd, scope),
		checkNanaJSONStateFiles(cwd),
		checkMcpServers(paths.configPath),
		checkDirectory("Investigate Codex home", ResolveInvestigateCodexHome(cwd)),
		checkInvestigateConfig(cwd),
		checkInvestigateMCPStatus(cwd),
	)

	passCount, warnCount, failCount := 0, 0, 0
	for _, check := range checks {
		icon := "[OK]"
		switch check.Status {
		case "warn":
			icon = "[!!]"
			warnCount++
		case "fail":
			icon = "[XX]"
			failCount++
		default:
			passCount++
		}
		fmt.Fprintf(os.Stdout, "  %s %s: %s\n", icon, check.Name, check.Message)
	}

	fmt.Fprintf(os.Stdout, "\nResults: %d passed, %d warnings, %d failed\n", passCount, warnCount, failCount)
	remediationsPrinted := false
	if warnCount+failCount > 0 {
		remediationsPrinted = printDoctorRemediations(os.Stdout, checks)
	}
	printDoctorFooter(os.Stdout, scope, checks, warnCount, failCount, remediationsPrinted)
	return nil
}

func printDoctorFooter(out io.Writer, scope string, checks []doctorCheck, warnCount int, failCount int, remediationsPrinted bool) {
	if failCount > 0 {
		unsafeOrUnknownFailures := hasUnsafeOrUnknownDoctorChecks(checks, "fail")
		safeFailures := hasSafeAutomaticDoctorChecks(checks, "fail")
		switch {
		case unsafeOrUnknownFailures && safeFailures:
			if command, ok := singleSafeAutomaticDoctorCommand(checks, "fail"); ok {
				fmt.Fprintf(out, "\nSome failures require manual remediation; review the remediation section above. Run \"%s\" only for failures marked as safe automatic fixes.\n", command)
			} else {
				fmt.Fprintln(out, "\nSome failures require manual remediation; review the remediation section above. Run only the safe automatic fix commands shown above for failures marked safe.")
			}
		case unsafeOrUnknownFailures:
			fmt.Fprintln(out, "\nSome failures require manual remediation; review the remediation section above.")
		case safeFailures:
			if command, ok := singleSafeAutomaticDoctorCommand(checks, "fail"); ok {
				fmt.Fprintf(out, "\nRun \"%s\" to fix failures marked as safe automatic fixes.\n", command)
			} else {
				fmt.Fprintln(out, "\nRun the safe automatic fix commands shown above to fix failures marked safe.")
			}
		default:
			fmt.Fprintln(out, "\nReview failures above.")
		}
		return
	}
	if warnCount > 0 {
		manualWarnings := hasManualOnlyDoctorChecks(checks, "warn")
		safeWarnings := hasSafeAutomaticDoctorChecks(checks, "warn")
		switch {
		case manualWarnings && safeWarnings:
			if command, ok := singleSafeAutomaticDoctorCommand(checks, "warn"); ok {
				fmt.Fprintf(out, "\nSome warnings require manual remediation; review the remediation section above. Run \"%s\" only for warnings marked as safe automatic fixes.\n", command)
			} else {
				fmt.Fprintln(out, "\nSome warnings require manual remediation; review the remediation section above. Run only the safe automatic fix commands shown above for warnings marked safe.")
			}
		case manualWarnings:
			fmt.Fprintln(out, "\nSome warnings require manual remediation; review the remediation section above.")
		case remediationsPrinted:
			if command, ok := singleSafeAutomaticDoctorCommand(checks, "warn"); ok {
				fmt.Fprintf(out, "\nReview remediation above, or run \"%s\" for safe setup remediations.\n", command)
			} else {
				fmt.Fprintln(out, "\nReview remediation above and run the safe automatic fix commands shown there.")
			}
		default:
			fmt.Fprintf(out, "\nReview warnings above, or run \"%s\" to refresh setup-managed components.\n", setupFixCommand(scope))
		}
		return
	}
	fmt.Fprintln(out, "\nAll checks passed! nana is ready.")
}

func hasUnsafeOrUnknownDoctorChecks(checks []doctorCheck, status string) bool {
	for _, check := range checks {
		if check.Status != status {
			continue
		}
		if !isSafeAutomaticDoctorRemediation(check.Remediation) {
			return true
		}
	}
	return false
}

func hasManualOnlyDoctorChecks(checks []doctorCheck, status string) bool {
	for _, check := range checks {
		if check.Status == status && isManualOnlyDoctorRemediation(check.Remediation) {
			return true
		}
	}
	return false
}

func hasSafeAutomaticDoctorChecks(checks []doctorCheck, status string) bool {
	for _, check := range checks {
		if check.Status == status && isSafeAutomaticDoctorRemediation(check.Remediation) {
			return true
		}
	}
	return false
}

func singleSafeAutomaticDoctorCommand(checks []doctorCheck, status string) (string, bool) {
	commands := []string{}
	seen := map[string]bool{}
	for _, check := range checks {
		if check.Status != status || !isSafeAutomaticDoctorRemediation(check.Remediation) {
			continue
		}
		command := safeAutomaticDoctorCommand(check.Remediation)
		if command == "" || seen[command] {
			continue
		}
		seen[command] = true
		commands = append(commands, command)
	}
	if len(commands) != 1 {
		return "", false
	}
	return commands[0], true
}

func safeAutomaticDoctorCommand(remediation *doctorRemediation) string {
	if !isSafeAutomaticDoctorRemediation(remediation) {
		return ""
	}
	text := strings.TrimSpace(remediation.SafeAutomaticFix)
	start := strings.Index(text, "`")
	if start < 0 {
		return ""
	}
	remainder := text[start+1:]
	end := strings.Index(remainder, "`")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(remainder[:end])
}

func isSafeAutomaticDoctorRemediation(remediation *doctorRemediation) bool {
	if remediation == nil {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(remediation.SafeAutomaticFix)), "yes")
}

func isManualOnlyDoctorRemediation(remediation *doctorRemediation) bool {
	if remediation == nil {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(remediation.SafeAutomaticFix)), "no")
}

func printDoctorRemediations(out io.Writer, checks []doctorCheck) bool {
	printed := false
	for _, check := range checks {
		if check.Status == "pass" || check.Remediation == nil {
			continue
		}
		if !printed {
			fmt.Fprintln(out, "\nRemediation:")
			printed = true
		}
		fmt.Fprintf(out, "  - %s (%s)\n", check.Name, check.Status)
		if check.Remediation.Path != "" {
			fmt.Fprintf(out, "    Path: %s\n", check.Remediation.Path)
		}
		fmt.Fprintf(out, "    Cause: %s\n", check.Message)
		if check.Remediation.SafeAutomaticFix != "" {
			fmt.Fprintf(out, "    Safe automatic fix: %s\n", check.Remediation.SafeAutomaticFix)
		}
		if check.Remediation.ManualFallback != "" {
			fmt.Fprintf(out, "    Manual fallback: %s\n", check.Remediation.ManualFallback)
		}
	}
	return printed
}

func setupFixCommand(scope string) string {
	return setupCommand(scope, false)
}

func setupForceFixCommand(scope string) string {
	return setupCommand(scope, true)
}

func setupCommand(scope string, force bool) string {
	scope = strings.TrimSpace(scope)
	parts := []string{"nana", "setup"}
	if force {
		parts = append(parts, "--force")
	}
	if scope == "" {
		return strings.Join(parts, " ")
	}
	parts = append(parts, "--scope", scope)
	return strings.Join(parts, " ")
}

func setupDoctorRemediation(scope string, path string, manualFallback string) *doctorRemediation {
	if manualFallback == "" {
		manualFallback = fmt.Sprintf("inspect %s, then run `%s`", path, setupFixCommand(scope))
	}
	return &doctorRemediation{
		Path:             path,
		SafeAutomaticFix: fmt.Sprintf("yes — run `%s`", setupFixCommand(scope)),
		ManualFallback:   manualFallback,
	}
}

func manualDoctorRemediation(path string, manualFallback string) *doctorRemediation {
	return &doctorRemediation{
		Path:             path,
		SafeAutomaticFix: "no — manual review required",
		ManualFallback:   manualFallback,
	}
}

type teamDoctorIssue struct {
	Code     string
	Message  string
	Severity string
}

func DoctorTeam(cwd string) (bool, error) {
	fmt.Fprintln(os.Stdout, "nana doctor --team")
	fmt.Fprintln(os.Stdout, "=========================")
	fmt.Fprintln(os.Stdout)

	issues, err := collectTeamDoctorIssues(cwd)
	if err != nil {
		return false, err
	}
	if len(issues) == 0 {
		fmt.Fprintln(os.Stdout, "  [OK] team diagnostics: no issues")
		fmt.Fprintln(os.Stdout, "\nAll team checks passed.")
		return false, nil
	}

	failureCount := 0
	warningCount := 0
	for _, issue := range issues {
		icon := "[XX]"
		if issue.Severity == "warn" {
			icon = "[!!]"
			warningCount++
		} else {
			failureCount++
		}
		fmt.Fprintf(os.Stdout, "  %s %s: %s\n", icon, issue.Code, issue.Message)
	}
	fmt.Fprintf(os.Stdout, "\nResults: %d warnings, %d failed\n", warningCount, failureCount)
	return failureCount > 0, nil
}

func resolveDoctorScope(cwd string) (string, string) {
	scopePath := filepath.Join(cwd, ".nana", "setup-scope.json")
	content, err := os.ReadFile(scopePath)
	if err != nil {
		return "user", "default"
	}
	switch string(content) {
	case `{"scope":"project"}`, "{\n  \"scope\": \"project\"\n}", "{\n  \"scope\": \"project-local\"\n}":
		if strings.Contains(string(content), "project-local") {
			return "project", "persisted"
		}
		return "project", "persisted"
	}
	if strings.Contains(string(content), `"scope":"project"`) || strings.Contains(string(content), `"scope": "project"`) {
		return "project", "persisted"
	}
	if strings.Contains(string(content), `"scope":"project-local"`) || strings.Contains(string(content), `"scope": "project-local"`) {
		return "project", "persisted"
	}
	if strings.Contains(string(content), `"scope":"user"`) || strings.Contains(string(content), `"scope": "user"`) {
		return "user", "persisted"
	}
	return "user", "default"
}

type doctorPaths struct {
	codexHomeDir string
	configPath   string
	promptsDir   string
	skillsDir    string
}

func resolveDoctorPaths(cwd string, scope string) doctorPaths {
	if scope == "project" {
		codexHomeDir := filepath.Join(cwd, ".codex")
		return doctorPaths{
			codexHomeDir: codexHomeDir,
			configPath:   filepath.Join(codexHomeDir, "config.toml"),
			promptsDir:   filepath.Join(codexHomeDir, "prompts"),
			skillsDir:    filepath.Join(cwd, ".codex", "skills"),
		}
	}
	return doctorPaths{
		codexHomeDir: CodexHome(),
		configPath:   CodexConfigPath(),
		promptsDir:   filepath.Join(CodexHome(), "prompts"),
		skillsDir:    filepath.Join(CodexHome(), "skills"),
	}
}

func checkCodexCLI() doctorCheck {
	output, err := exec.Command("codex", "--version").CombinedOutput()
	if err != nil {
		return doctorCheck{
			Name:    "Codex CLI",
			Status:  "fail",
			Message: "not found - install from https://github.com/openai/codex",
			Remediation: manualDoctorRemediation(
				"PATH",
				"install Codex CLI from https://github.com/openai/codex, then rerun `nana doctor`",
			),
		}
	}
	return doctorCheck{Name: "Codex CLI", Status: "pass", Message: fmt.Sprintf("installed (%s)", strings.TrimSpace(string(output)))}
}

func checkNodeVersion() doctorCheck {
	output, err := exec.Command("node", "--version").CombinedOutput()
	if err != nil {
		return doctorCheck{Name: "Node.js", Status: "warn", Message: "not found"}
	}
	version := strings.TrimSpace(string(output))
	if strings.HasPrefix(version, "v") {
		majorParts := strings.Split(strings.TrimPrefix(version, "v"), ".")
		if len(majorParts) > 0 {
			if majorParts[0] >= "20" {
				return doctorCheck{Name: "Node.js", Status: "pass", Message: version}
			}
		}
	}
	return doctorCheck{Name: "Node.js", Status: "warn", Message: version}
}

func checkGithubCLI() doctorCheck {
	path, err := exec.LookPath("gh")
	if err != nil {
		return doctorCheck{Name: "GitHub CLI", Status: "warn", Message: "not found - GitHub-backed Nana workflows require `gh`"}
	}
	output, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil {
		return doctorCheck{Name: "GitHub CLI", Status: "warn", Message: "installed but not runnable"}
	}
	return doctorCheck{Name: "GitHub CLI", Status: "pass", Message: strings.TrimSpace(string(output))}
}

func checkGithubAuth() doctorCheck {
	_, host, err := githubCLIAuthStatus(strings.TrimSpace(os.Getenv("GITHUB_API_URL")))
	if err != nil {
		return doctorCheck{Name: "GitHub auth", Status: "warn", Message: err.Error()}
	}
	if host != "" {
		return doctorCheck{Name: "GitHub auth", Status: "pass", Message: fmt.Sprintf("authenticated for %s", host)}
	}
	return doctorCheck{Name: "GitHub auth", Status: "pass", Message: "authenticated"}
}

func checkGithubAutomationRepos() doctorCheck {
	repos, err := listOnboardedGithubRepos()
	if err != nil {
		return doctorCheck{Name: "GitHub automation repos", Status: "warn", Message: err.Error()}
	}
	eligible := []string{}
	failures := []string{}
	for _, repoSlug := range repos {
		settings, _ := readGithubRepoSettings(githubRepoSettingsPath(repoSlug))
		if !githubRepoModeAllowsDevelopment(resolvedGithubRepoMode(settings)) || resolvedGithubIssuePickMode(settings) == "manual" {
			continue
		}
		eligible = append(eligible, repoSlug)
		if err := githubAutomationRepoPreflight(repoSlug, false); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %s", repoSlug, err.Error()))
		}
	}
	if len(eligible) == 0 {
		return doctorCheck{Name: "GitHub automation repos", Status: "pass", Message: "not configured"}
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return doctorCheck{Name: "GitHub automation repos", Status: "warn", Message: strings.Join(failures, "; ")}
	}
	return doctorCheck{Name: "GitHub automation repos", Status: "pass", Message: fmt.Sprintf("%d repo(s) ready", len(eligible))}
}

func checkRepoGitDrift(cwd string, repoRoot string) doctorCheck {
	root, err := resolveDoctorRepoGitRoot(cwd, repoRoot)
	if err != nil {
		return doctorCheck{Name: "Repo drift", Status: "pass", Message: "skipped - not in a git repo"}
	}
	upstream, err := githubGitOutput(root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil {
		return doctorCheck{Name: "Repo drift", Status: "pass", Message: "skipped - no upstream tracking branch"}
	}
	upstream = strings.TrimSpace(upstream)
	parts := strings.SplitN(upstream, "/", 2)
	if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" {
		if err := githubRunGit(root, "fetch", "--quiet", "--prune", strings.TrimSpace(parts[0])); err != nil {
			return doctorCheck{Name: "Repo drift", Status: "warn", Message: fmt.Sprintf("fetch-failed upstream=%s: %v", upstream, err)}
		}
	}
	branch, branchErr := githubGitOutput(root, "rev-parse", "--abbrev-ref", "HEAD")
	if branchErr != nil {
		branch = "HEAD"
	}
	statusOutput, err := githubGitOutput(root, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return doctorCheck{Name: "Repo drift", Status: "warn", Message: err.Error()}
	}
	countsOutput, err := githubGitOutput(root, "rev-list", "--left-right", "--count", "HEAD..."+upstream)
	if err != nil {
		return doctorCheck{Name: "Repo drift", Status: "warn", Message: err.Error()}
	}
	countFields := strings.Fields(strings.TrimSpace(countsOutput))
	if len(countFields) != 2 {
		return doctorCheck{Name: "Repo drift", Status: "warn", Message: fmt.Sprintf("unexpected divergence output: %s", strings.TrimSpace(countsOutput))}
	}
	ahead, err := strconv.Atoi(countFields[0])
	if err != nil {
		return doctorCheck{Name: "Repo drift", Status: "warn", Message: fmt.Sprintf("invalid ahead count: %s", countFields[0])}
	}
	behind, err := strconv.Atoi(countFields[1])
	if err != nil {
		return doctorCheck{Name: "Repo drift", Status: "warn", Message: fmt.Sprintf("invalid behind count: %s", countFields[1])}
	}
	dirty := strings.TrimSpace(statusOutput) != ""
	branch = strings.TrimSpace(branch)
	switch {
	case behind > 0 && ahead > 0 && dirty:
		return doctorCheck{Name: "Repo drift", Status: "warn", Message: fmt.Sprintf("diverged-dirty branch=%s upstream=%s ahead=%d behind=%d", branch, upstream, ahead, behind)}
	case behind > 0 && ahead > 0:
		return doctorCheck{Name: "Repo drift", Status: "warn", Message: fmt.Sprintf("diverged-clean branch=%s upstream=%s ahead=%d behind=%d", branch, upstream, ahead, behind)}
	case behind > 0 && dirty:
		return doctorCheck{Name: "Repo drift", Status: "warn", Message: fmt.Sprintf("behind-dirty branch=%s upstream=%s behind=%d", branch, upstream, behind)}
	case behind > 0:
		return doctorCheck{Name: "Repo drift", Status: "warn", Message: fmt.Sprintf("behind-clean branch=%s upstream=%s behind=%d", branch, upstream, behind)}
	case ahead > 0:
		return doctorCheck{Name: "Repo drift", Status: "pass", Message: fmt.Sprintf("ahead branch=%s upstream=%s ahead=%d", branch, upstream, ahead)}
	default:
		return doctorCheck{Name: "Repo drift", Status: "pass", Message: fmt.Sprintf("current branch=%s upstream=%s", branch, upstream)}
	}
}

func resolveDoctorRepoGitRoot(cwd string, repoRoot string) (string, error) {
	target := strings.TrimSpace(repoRoot)
	if target == "" {
		target = strings.TrimSpace(cwd)
	}
	root, err := githubGitOutput(target, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(root), nil
}

func checkExploreHarness(repoRoot string) doctorCheck {
	override := strings.TrimSpace(os.Getenv("NANA_EXPLORE_BIN"))
	if override != "" {
		if _, err := os.Stat(override); err == nil {
			return doctorCheck{Name: "Explore Harness", Status: "pass", Message: fmt.Sprintf("NANA_EXPLORE_BIN configured (%s)", override)}
		}
		if repoRoot != "" {
			if _, err := os.Stat(filepath.Join(repoRoot, override)); err == nil {
				return doctorCheck{Name: "Explore Harness", Status: "pass", Message: fmt.Sprintf("NANA_EXPLORE_BIN configured (%s)", override)}
			}
		}
		return doctorCheck{Name: "Explore Harness", Status: "warn", Message: fmt.Sprintf("NANA_EXPLORE_BIN is set but path was not found (%s)", override)}
	}

	if repoRoot != "" {
		meta := filepath.Join(repoRoot, "bin", "nana-explore-harness.meta.json")
		bin := filepath.Join(repoRoot, "bin", map[bool]string{true: "nana-explore-harness.exe", false: "nana-explore-harness"}[runtime.GOOS == "windows"])
		if _, err := os.Stat(meta); err == nil {
			if _, err := os.Stat(bin); err == nil {
				return doctorCheck{Name: "Explore Harness", Status: "pass", Message: fmt.Sprintf("ready (packaged native binary: %s)", bin)}
			}
		}
	}

	if _, err := exec.LookPath("go"); err == nil {
		return doctorCheck{Name: "Explore Harness", Status: "pass", Message: "ready (go available)"}
	}
	return doctorCheck{Name: "Explore Harness", Status: "warn", Message: "Go harness sources are packaged, but no compatible packaged prebuilt or go toolchain was found (install Go or set NANA_EXPLORE_BIN for nana explore)"}
}

func checkDirectory(name string, path string) doctorCheck {
	if _, err := os.Stat(path); err == nil {
		return doctorCheck{Name: name, Status: "pass", Message: path}
	}
	return doctorCheck{Name: name, Status: "warn", Message: fmt.Sprintf("%s (not created yet)", path)}
}

func checkManagedAccounts(codexHomeDir string) doctorCheck {
	registryPath := managedAuthRegistryPathForHome(codexHomeDir)
	statePath := managedAuthRuntimeStatePathForHome(codexHomeDir)
	registry, err := loadManagedAuthRegistry(codexHomeDir)
	if err != nil {
		return failedAccountsCheck(
			fmt.Sprintf("invalid account registry: %v", err),
			registryPath,
			fmt.Sprintf("repair or remove %s, then rerun `nana doctor`; if removed, re-add profiles with `nana account pull` or `nana account add`", registryPath),
		)
	}
	if len(registry.Accounts) == 0 {
		return doctorCheck{Name: "Accounts", Status: "pass", Message: "not configured"}
	}
	state, err := loadManagedAuthRuntimeState(codexHomeDir)
	if err != nil {
		return failedAccountsCheck(
			fmt.Sprintf("invalid account runtime state: %v", err),
			statePath,
			fmt.Sprintf("repair or remove %s, then rerun `nana doctor`; runtime state will be recreated as accounts are used", statePath),
		)
	}
	for _, account := range registry.Accounts {
		if strings.TrimSpace(account.AuthPath) == "" {
			return failedAccountsCheck(
				fmt.Sprintf("account %s has no credential path", account.Name),
				registryPath,
				fmt.Sprintf("edit %s to set auth_path for account %s, or remove and re-add it with `nana account remove %s` then `nana account add %s`", registryPath, account.Name, account.Name, account.Name),
			)
		}
		if _, err := os.Stat(account.AuthPath); err != nil {
			return failedAccountsCheck(
				fmt.Sprintf("account %s credential file missing (%s)", account.Name, account.AuthPath),
				account.AuthPath,
				fmt.Sprintf("restore %s, or remove and re-add account %s with `nana account remove %s` then `nana account add %s`", account.AuthPath, account.Name, account.Name, account.Name),
			)
		}
		profile, err := readManagedAccountProfile(account.AuthPath)
		if err != nil {
			return failedAccountsCheck(
				fmt.Sprintf("account %s credentials unreadable: %v", account.Name, err),
				account.AuthPath,
				fmt.Sprintf("fix permissions or JSON at %s, or refresh account %s with `nana account add %s`", account.AuthPath, account.Name, account.Name),
			)
		}
		if !isChatGPTBackedAuthMode(profile.AuthMode) {
			return failedAccountsCheck(
				fmt.Sprintf("account %s uses unsupported auth mode %q", account.Name, profile.AuthMode),
				account.AuthPath,
				fmt.Sprintf("refresh account %s with ChatGPT-backed credentials using `nana account add %s` or `nana account pull %s`", account.Name, account.Name, account.Name),
			)
		}
		if profile.Tokens == nil || strings.TrimSpace(profile.Tokens.AccessToken) == "" || strings.TrimSpace(profile.Tokens.RefreshToken) == "" || strings.TrimSpace(profile.Tokens.AccountID) == "" {
			return failedAccountsCheck(
				fmt.Sprintf("account %s is missing ChatGPT token fields required for usage API checks", account.Name),
				account.AuthPath,
				fmt.Sprintf("refresh account %s with `nana account add %s` or import a complete auth.json with `nana account pull %s --from <path>`", account.Name, account.Name, account.Name),
			)
		}
	}
	if active := strings.TrimSpace(state.Active); active != "" && registry.account(active) == nil {
		return failedAccountsCheck(
			fmt.Sprintf("active account %s not present in registry", active),
			statePath,
			fmt.Sprintf("edit or remove %s, then activate a configured account with `nana account activate <name>`", statePath),
		)
	}
	if pending := strings.TrimSpace(state.PendingActive); pending != "" && registry.account(pending) == nil {
		return failedAccountsCheck(
			fmt.Sprintf("pending account %s not present in registry", pending),
			statePath,
			fmt.Sprintf("edit or remove %s, then queue a configured account with `nana account activate <name>`", statePath),
		)
	}
	message := fmt.Sprintf("%d configured (preferred=%s, active=%s)", len(registry.Accounts), displayOrFallback(registry.Preferred, "(none)"), displayOrFallback(state.Active, "(none)"))
	if state.Degraded {
		return doctorCheck{Name: "Accounts", Status: "warn", Message: message + fmt.Sprintf(", degraded=%s", displayOrFallback(state.DegradedReason, "yes"))}
	}
	for _, account := range registry.Accounts {
		accountState := state.Accounts[account.Name]
		switch accountState.LastUsageResult {
		case accountUsageResultStale:
			return doctorCheck{Name: "Accounts", Status: "warn", Message: message + fmt.Sprintf(", account %s usage telemetry stale", account.Name)}
		case accountUsageResultPermanent:
			return failedAccountsCheck(
				message+fmt.Sprintf(", account %s usage auth failed: %s", account.Name, displayOrFallback(accountState.LastUsageError, "unknown")),
				account.AuthPath,
				fmt.Sprintf("refresh credentials for account %s with `nana account add %s`, then rerun `nana doctor`", account.Name, account.Name),
			)
		case accountUsageResultTransient:
			return doctorCheck{Name: "Accounts", Status: "warn", Message: message + fmt.Sprintf(", account %s usage API unavailable", account.Name)}
		}
	}
	if state.RestartRequired && strings.TrimSpace(state.PendingActive) != "" {
		return doctorCheck{Name: "Accounts", Status: "warn", Message: message + fmt.Sprintf(", pending=%s restart required", state.PendingActive)}
	}
	return doctorCheck{Name: "Accounts", Status: "pass", Message: message}
}

func failedAccountsCheck(message string, path string, manualFallback string) doctorCheck {
	return doctorCheck{
		Name:        "Accounts",
		Status:      "fail",
		Message:     message,
		Remediation: manualDoctorRemediation(path, manualFallback),
	}
}

func checkConfig(configPath string, scope string) doctorCheck {
	content, err := os.ReadFile(configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return doctorCheck{
				Name:    "Config",
				Status:  "fail",
				Message: fmt.Sprintf("config.toml cannot be read: %v", err),
				Remediation: manualDoctorRemediation(
					configPath,
					fmt.Sprintf("fix permissions or move the path conflict at %s, then run `%s`", configPath, setupFixCommand(scope)),
				),
			}
		}
		return doctorCheck{
			Name:        "Config",
			Status:      "warn",
			Message:     "config.toml not found",
			Remediation: setupDoctorRemediation(scope, configPath, fmt.Sprintf("create the parent directory with `mkdir -p %s`, then run `%s`", filepath.Dir(configPath), setupFixCommand(scope))),
		}
	}
	text := string(content)
	if countTopLevelTable(text, "[tui]") > 1 {
		return doctorCheck{
			Name:        "Config",
			Status:      "fail",
			Message:     "invalid config.toml (possible duplicate TOML table such as [tui])",
			Remediation: manualDoctorRemediation(configPath, fmt.Sprintf("edit %s to remove duplicate top-level tables, then run `%s`", configPath, setupFixCommand(scope))),
		}
	}
	if strings.Contains(text, "[mcp_servers.nana_") || strings.Contains(strings.ToLower(text), "managed by nana setup") || strings.Contains(text, "USE_NANA_") {
		return doctorCheck{Name: "Config", Status: "pass", Message: "config.toml has NANA entries"}
	}
	return doctorCheck{Name: "Config", Status: "warn", Message: fmt.Sprintf("config.toml exists but no NANA entries yet (expected before first setup; run %q once)", setupFixCommand(scope))}
}

func countTopLevelTable(content string, table string) int {
	count := 0
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == table {
			count++
		}
	}
	return count
}

func checkExploreRouting(configPath string) doctorCheck {
	envValue := strings.TrimSpace(os.Getenv("USE_NANA_EXPLORE_CMD"))
	if envValue != "" && !exploreRoutingEnabled(envValue) {
		return doctorCheck{Name: "Explore routing", Status: "warn", Message: "disabled by environment override; enable with USE_NANA_EXPLORE_CMD=1 (or remove the explicit opt-out)"}
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		return doctorCheck{Name: "Explore routing", Status: "pass", Message: "enabled by default (config.toml not found yet)"}
	}
	text := string(content)
	if strings.Contains(text, `USE_NANA_EXPLORE_CMD = "off"`) || strings.Contains(text, `USE_NANA_EXPLORE_CMD = "0"`) || strings.Contains(text, `USE_NANA_EXPLORE_CMD = "false"`) {
		return doctorCheck{Name: "Explore routing", Status: "warn", Message: "disabled in config.toml [env]; set USE_NANA_EXPLORE_CMD = \"1\" to restore default explore-first routing"}
	}
	return doctorCheck{Name: "Explore routing", Status: "pass", Message: "enabled by default"}
}

func exploreRoutingEnabled(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value != "0" && value != "false" && value != "off" && value != "no"
}

func checkPrompts(dir string) doctorCheck {
	count := countFilesWithExt(dir, ".md")
	if count == 0 {
		return doctorCheck{Name: "Prompts", Status: "warn", Message: "prompts directory not found"}
	}
	if count >= 25 {
		return doctorCheck{Name: "Prompts", Status: "pass", Message: fmt.Sprintf("%d agent prompts installed", count)}
	}
	return doctorCheck{Name: "Prompts", Status: "warn", Message: fmt.Sprintf("%d prompts (expected >= 25)", count)}
}

func checkSkills(dir string) doctorCheck {
	count := countSkillDirs(dir)
	if count == 0 {
		return doctorCheck{Name: "Skills", Status: "warn", Message: "skills directory not found"}
	}
	if count >= 30 {
		return doctorCheck{Name: "Skills", Status: "pass", Message: fmt.Sprintf("%d skills installed", count)}
	}
	return doctorCheck{Name: "Skills", Status: "warn", Message: fmt.Sprintf("%d skills (expected >= 30)", count)}
}

func countFilesWithExt(dir string, ext string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ext) {
			count++
		}
	}
	return count
}

func countSkillDirs(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, entry.Name(), "SKILL.md")); err == nil {
			count++
		}
	}
	return count
}

func checkLegacySkillRootOverlap() doctorCheck {
	canonicalDir := filepath.Join(CodexHome(), "skills")
	legacyDir := filepath.Join(homeDir(), ".agents", "skills")
	if _, err := os.Stat(legacyDir); err != nil {
		return doctorCheck{Name: "Legacy skill roots", Status: "pass", Message: "no ~/.agents/skills overlap detected"}
	}

	canonicalResolved, canonicalErr := filepath.EvalSymlinks(canonicalDir)
	legacyResolved, legacyErr := filepath.EvalSymlinks(legacyDir)
	if canonicalErr == nil && legacyErr == nil && canonicalResolved == legacyResolved {
		return doctorCheck{Name: "Legacy skill roots", Status: "pass", Message: fmt.Sprintf("~/.agents/skills links to canonical %s; treating both paths as one shared skill root", canonicalDir)}
	}

	canonicalSkills := readSkillHashes(canonicalDir)
	legacySkills := readSkillHashes(legacyDir)
	overlap := 0
	mismatch := 0
	for name, hash := range canonicalSkills {
		if legacyHash, ok := legacySkills[name]; ok {
			overlap++
			if legacyHash != hash {
				mismatch++
			}
		}
	}
	if overlap == 0 {
		return doctorCheck{Name: "Legacy skill roots", Status: "warn", Message: fmt.Sprintf("legacy ~/.agents/skills still exists (%d skills) alongside canonical %s; remove or archive it if Codex shows duplicate entries", len(legacySkills), canonicalDir)}
	}
	extra := ""
	if mismatch > 0 {
		extra = fmt.Sprintf("; %d differ in SKILL.md content", mismatch)
	}
	return doctorCheck{Name: "Legacy skill roots", Status: "warn", Message: fmt.Sprintf("%d overlapping skill names between %s and %s%s; Codex Enable/Disable Skills may show duplicates until ~/.agents/skills is cleaned up", overlap, canonicalDir, legacyDir, extra)}
}

func readSkillHashes(root string) map[string]string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return map[string]string{}
	}
	result := map[string]string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(root, entry.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		result[entry.Name()] = string(content)
	}
	return result
}

func checkAgentsMD(scope string, cwd string, codexHomeDir string) doctorCheck {
	if scope == "user" {
		path := filepath.Join(codexHomeDir, "AGENTS.md")
		if _, err := os.Stat(path); err == nil {
			return doctorCheck{Name: "AGENTS.md", Status: "pass", Message: fmt.Sprintf("found in %s", path)}
		}
		return doctorCheck{
			Name:        "AGENTS.md",
			Status:      "warn",
			Message:     fmt.Sprintf("not found in %s (run nana setup --scope user)", path),
			Remediation: setupDoctorRemediation(scope, path, fmt.Sprintf("create %s from templates/AGENTS.md or run `%s`", path, setupFixCommand(scope))),
		}
	}
	path := filepath.Join(cwd, "AGENTS.md")
	if _, err := os.Stat(path); err == nil {
		return doctorCheck{Name: "AGENTS.md", Status: "pass", Message: "found in project root"}
	}
	return doctorCheck{
		Name:        "AGENTS.md",
		Status:      "warn",
		Message:     "not found in project root (run nana agents-init . or nana setup --scope project)",
		Remediation: setupDoctorRemediation(scope, path, "run `nana agents-init .` for lightweight guidance, or `nana setup --scope project` for full setup without forcing an overwrite"),
	}
}

type doctorMarkerPair struct {
	label string
	start string
	end   string
}

func checkAgentsRuntimeSections(scope string, cwd string, codexHomeDir string) doctorCheck {
	path := resolveManagedAgentsPath(scope, cwd, codexHomeDir)
	contentBytes, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return doctorCheck{
				Name:        "AGENTS runtime guidance",
				Status:      "warn",
				Message:     fmt.Sprintf("%s not found", path),
				Remediation: setupDoctorRemediation(scope, path, fmt.Sprintf("create %s from templates/AGENTS.md or run `%s`", path, setupFixCommand(scope))),
			}
		}
		return doctorCheck{
			Name:        "AGENTS runtime guidance",
			Status:      "warn",
			Message:     err.Error(),
			Remediation: manualDoctorRemediation(path, fmt.Sprintf("fix permissions for %s, then run `%s`", path, setupFixCommand(scope))),
		}
	}
	content := string(contentBytes)
	failures := []string{}
	warnings := []string{}

	if !isNanaManagedAgentsContent(content) {
		warnings = append(warnings, "missing generated AGENTS marker")
	}
	hasLegacyStateSection := strings.Contains(content, "<state_management>") && strings.Contains(content, "</state_management>")
	hasCompactStateSection := strings.Contains(content, "## Runtime State and Setup")
	if !hasLegacyStateSection && !hasCompactStateSection {
		warnings = append(warnings, "missing runtime state section")
	}
	for _, literal := range []string{
		".nana/state/",
		".nana/notepad.md",
		".nana/project-memory.json",
		".nana/plans/",
		".nana/logs/",
	} {
		if !strings.Contains(content, literal) {
			warnings = append(warnings, fmt.Sprintf("missing %s reference", literal))
		}
	}

	for _, pair := range []doctorMarkerPair{
		{label: "NANA guidance operating", start: "<!-- NANA:GUIDANCE:OPERATING:START -->", end: "<!-- NANA:GUIDANCE:OPERATING:END -->"},
		{label: "NANA guidance verify", start: "<!-- NANA:GUIDANCE:VERIFYSEQ:START -->", end: "<!-- NANA:GUIDANCE:VERIFYSEQ:END -->"},
		{label: "NANA models", start: "<!-- NANA:MODELS:START -->", end: "<!-- NANA:MODELS:END -->"},
		{label: "NANA runtime", start: "<!-- NANA:RUNTIME:START -->", end: "<!-- NANA:RUNTIME:END -->"},
		{label: "NANA team worker", start: "<!-- NANA:TEAM:WORKER:START -->", end: "<!-- NANA:TEAM:WORKER:END -->"},
	} {
		status, detail := validateDoctorMarkerPair(content, pair)
		switch status {
		case "missing":
			warnings = append(warnings, detail)
		case "broken":
			failures = append(failures, detail)
		}
	}

	if len(failures) > 0 {
		return doctorCheck{
			Name:    "AGENTS runtime guidance",
			Status:  "fail",
			Message: strings.Join(limitStrings(failures, 3), "; "),
			Remediation: manualDoctorRemediation(
				path,
				fmt.Sprintf("restore balanced NANA marker pairs in %s, or back up and merge custom instructions before any force refresh", path),
			),
		}
	}
	if len(warnings) > 0 {
		if isNanaSetupGeneratedAgentsContent(content) {
			return doctorCheck{
				Name:    "AGENTS runtime guidance",
				Status:  "warn",
				Message: fmt.Sprintf("%s; run %s", strings.Join(limitStrings(warnings, 3), "; "), setupForceFixCommand(scope)),
				Remediation: &doctorRemediation{
					Path:             path,
					SafeAutomaticFix: fmt.Sprintf("yes — run `%s`", setupForceFixCommand(scope)),
					ManualFallback:   fmt.Sprintf("inspect %s, then run `%s` to refresh setup-generated AGENTS.md content", path, setupForceFixCommand(scope)),
				},
			}
		}
		return doctorCheck{
			Name:    "AGENTS runtime guidance",
			Status:  "warn",
			Message: fmt.Sprintf("%s; manual merge required before force-refreshing %s", strings.Join(limitStrings(warnings, 3), "; "), path),
			Remediation: manualDoctorRemediation(
				path,
				fmt.Sprintf("copy missing generated guidance from templates/AGENTS.md into %s, or back up and merge custom instructions before any force refresh", path),
			),
		}
	}
	return doctorCheck{Name: "AGENTS runtime guidance", Status: "pass", Message: "generated sections and overlay markers present"}
}

func validateDoctorMarkerPair(content string, pair doctorMarkerPair) (string, string) {
	starts := allStringIndexes(content, pair.start)
	ends := allStringIndexes(content, pair.end)
	if len(starts) == 0 && len(ends) == 0 {
		return "missing", fmt.Sprintf("%s markers missing", pair.label)
	}
	if len(starts) != len(ends) {
		return "broken", fmt.Sprintf("%s marker count mismatch (start=%d end=%d)", pair.label, len(starts), len(ends))
	}
	lastEnd := -1
	for index := range starts {
		if starts[index] < lastEnd {
			return "broken", fmt.Sprintf("%s markers overlap or are out of order", pair.label)
		}
		if ends[index] < starts[index] {
			return "broken", fmt.Sprintf("%s end marker appears before start marker", pair.label)
		}
		lastEnd = ends[index] + len(pair.end)
	}
	return "ok", ""
}

func allStringIndexes(content string, needle string) []int {
	indexes := []int{}
	offset := 0
	for {
		index := strings.Index(content[offset:], needle)
		if index < 0 {
			return indexes
		}
		absolute := offset + index
		indexes = append(indexes, absolute)
		offset = absolute + len(needle)
	}
}

func checkNanaStatePaths(cwd string, scope string) doctorCheck {
	nanaDir := filepath.Join(cwd, ".nana")
	dirs := []string{
		nanaDir,
		filepath.Join(nanaDir, "state"),
		filepath.Join(nanaDir, "plans"),
		filepath.Join(nanaDir, "logs"),
	}
	files := []string{
		filepath.Join(nanaDir, "notepad.md"),
		filepath.Join(nanaDir, "project-memory.json"),
	}
	failures := []string{}
	missing := []string{}
	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				missing = append(missing, filepath.ToSlash(mustRelative(cwd, dir)))
				continue
			}
			failures = append(failures, fmt.Sprintf("%s: %v", filepath.ToSlash(mustRelative(cwd, dir)), err))
			continue
		}
		if !info.IsDir() {
			failures = append(failures, fmt.Sprintf("%s is not a directory", filepath.ToSlash(mustRelative(cwd, dir))))
		}
	}
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			if os.IsNotExist(err) {
				missing = append(missing, filepath.ToSlash(mustRelative(cwd, file)))
				continue
			}
			failures = append(failures, fmt.Sprintf("%s: %v", filepath.ToSlash(mustRelative(cwd, file)), err))
			continue
		}
		if info.IsDir() {
			failures = append(failures, fmt.Sprintf("%s is not a file", filepath.ToSlash(mustRelative(cwd, file))))
		}
	}
	if len(failures) > 0 {
		return doctorCheck{
			Name:        "NANA state paths",
			Status:      "fail",
			Message:     strings.Join(limitStrings(failures, 3), "; "),
			Remediation: manualDoctorRemediation(nanaDir, fmt.Sprintf("move the conflicting path aside, then run `%s`", setupFixCommand(scope))),
		}
	}
	if len(missing) > 0 {
		return doctorCheck{
			Name:        "NANA state paths",
			Status:      "warn",
			Message:     fmt.Sprintf("missing %s (run nana setup)", strings.Join(limitStrings(missing, 4), ", ")),
			Remediation: setupDoctorRemediation(scope, nanaDir, fmt.Sprintf("run `mkdir -p %s %s %s`, then `%s`", filepath.Join(nanaDir, "state"), filepath.Join(nanaDir, "plans"), filepath.Join(nanaDir, "logs"), setupFixCommand(scope))),
		}
	}
	return doctorCheck{Name: "NANA state paths", Status: "pass", Message: "required .nana paths present"}
}

func checkNanaJSONStateFiles(cwd string) doctorCheck {
	paths := []string{}
	for _, path := range []string{
		filepath.Join(cwd, ".nana", "project-memory.json"),
		filepath.Join(cwd, ".nana", "setup-scope.json"),
		filepath.Join(cwd, ".nana", "hud-config.json"),
	} {
		if fileExists(path) {
			paths = append(paths, path)
		}
	}

	stateDir := BaseStateDir(cwd)
	if info, err := os.Stat(stateDir); err == nil && info.IsDir() {
		_ = filepath.WalkDir(stateDir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				return nil
			}
			paths = append(paths, path)
			return nil
		})
	}

	sort.Strings(paths)
	invalid := []string{}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			invalid = append(invalid, fmt.Sprintf("%s: %v", filepath.ToSlash(mustRelative(cwd, path)), err))
			continue
		}
		var parsed any
		if err := json.Unmarshal(content, &parsed); err != nil {
			invalid = append(invalid, fmt.Sprintf("%s: %v", filepath.ToSlash(mustRelative(cwd, path)), err))
		}
	}
	if len(invalid) > 0 {
		return doctorCheck{
			Name:        "NANA JSON state",
			Status:      "fail",
			Message:     strings.Join(limitStrings(invalid, 4), "; "),
			Remediation: manualDoctorRemediation(filepath.Join(cwd, ".nana"), "repair the malformed JSON shown above, or move the corrupt file aside and rerun `nana doctor`"),
		}
	}
	if len(paths) == 0 {
		return doctorCheck{Name: "NANA JSON state", Status: "pass", Message: "no JSON state files yet"}
	}
	return doctorCheck{Name: "NANA JSON state", Status: "pass", Message: fmt.Sprintf("%d JSON state file(s) valid", len(paths))}
}

func limitStrings(values []string, limit int) []string {
	if limit <= 0 || len(values) <= limit {
		return values
	}
	limited := append([]string{}, values[:limit]...)
	limited = append(limited, fmt.Sprintf("+%d more", len(values)-limit))
	return limited
}

func checkMcpServers(configPath string) doctorCheck {
	content, err := os.ReadFile(configPath)
	if err != nil {
		return doctorCheck{Name: "MCP Servers", Status: "warn", Message: "config.toml not found"}
	}
	text := string(content)
	mcpCount := strings.Count(text, "[mcp_servers.")
	if mcpCount == 0 {
		if strings.Contains(text, "USE_NANA_") || strings.Contains(text, "[agents]") {
			return doctorCheck{Name: "MCP Servers", Status: "pass", Message: "no external MCP servers configured (current setup)"}
		}
		return doctorCheck{Name: "MCP Servers", Status: "warn", Message: "no MCP servers configured"}
	}
	if strings.Contains(text, "nana_state") || strings.Contains(text, "nana_memory") {
		return doctorCheck{Name: "MCP Servers", Status: "pass", Message: fmt.Sprintf("%d servers configured (NANA present)", mcpCount)}
	}
	return doctorCheck{Name: "MCP Servers", Status: "pass", Message: fmt.Sprintf("%d servers configured", mcpCount)}
}

func checkInvestigateConfig(cwd string) doctorCheck {
	configPath := InvestigateCodexConfigPath(cwd)
	content, err := os.ReadFile(configPath)
	if err != nil {
		return doctorCheck{Name: "Investigate config", Status: "warn", Message: fmt.Sprintf("config.toml not found at %s (run `nana investigate onboard`)", configPath)}
	}
	text := string(content)
	if strings.Contains(text, investigateConfigBlockHeader) {
		return doctorCheck{Name: "Investigate config", Status: "pass", Message: fmt.Sprintf("config present at %s", configPath)}
	}
	return doctorCheck{Name: "Investigate config", Status: "warn", Message: fmt.Sprintf("config present at %s but missing the investigate managed block", configPath)}
}

func checkInvestigateMCPStatus(cwd string) doctorCheck {
	statusPath := investigateMCPStatusPath(ResolveInvestigateCodexHome(cwd))
	var status investigateMCPStatus
	if err := readGithubJSON(statusPath, &status); err != nil {
		if os.IsNotExist(err) {
			return doctorCheck{Name: "Investigate MCP status", Status: "warn", Message: fmt.Sprintf("no cached investigate MCP status at %s (run `nana investigate doctor`)", statusPath)}
		}
		return doctorCheck{Name: "Investigate MCP status", Status: "warn", Message: fmt.Sprintf("failed to read cached investigate MCP status: %v", err)}
	}
	if len(status.ConfiguredServers) == 0 {
		return doctorCheck{Name: "Investigate MCP status", Status: "pass", Message: "no MCPs configured for investigate (local-source-only mode)"}
	}
	if status.AllOK {
		return doctorCheck{Name: "Investigate MCP status", Status: "pass", Message: fmt.Sprintf("%d configured MCP(s) healthy from last probe", len(status.ConfiguredServers))}
	}
	return doctorCheck{Name: "Investigate MCP status", Status: "warn", Message: fmt.Sprintf("one or more configured investigate MCPs failed last probe (run `nana investigate doctor`)")}
}

func homeDir() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	home, _ := os.UserHomeDir()
	return home
}

func collectTeamDoctorIssues(cwd string) ([]teamDoctorIssue, error) {
	stateDir := BaseStateDir(cwd)
	teamsRoot := filepath.Join(stateDir, "team")
	now := time.Now()
	lagThreshold := time.Minute
	shutdownThreshold := 30 * time.Second
	leaderStaleThreshold := 3 * time.Minute

	teamEntries, err := os.ReadDir(teamsRoot)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	tmuxSessions, tmuxUnavailable := listTeamTmuxSessions()
	knownTeamSessions := map[string]bool{}
	var issues []teamDoctorIssue

	for _, entry := range teamEntries {
		if !entry.IsDir() {
			continue
		}
		teamName := entry.Name()
		teamDir := filepath.Join(teamsRoot, teamName)
		tmuxSession := "nana-team-" + teamName

		for _, configName := range []string{"manifest.v2.json", "config.json"} {
			path := filepath.Join(teamDir, configName)
			content, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var parsed map[string]any
			if json.Unmarshal(content, &parsed) == nil {
				if value, ok := parsed["tmux_session"].(string); ok && strings.TrimSpace(value) != "" {
					tmuxSession = value
					break
				}
			}
		}

		knownTeamSessions[tmuxSession] = true
		if !tmuxUnavailable && !tmuxSessions[tmuxSession] {
			issues = append(issues, teamDoctorIssue{
				Code:     "resume_blocker",
				Message:  fmt.Sprintf("%s references missing tmux session %s", teamName, tmuxSession),
				Severity: "fail",
			})
		}

		workersRoot := filepath.Join(teamDir, "workers")
		workers, _ := os.ReadDir(workersRoot)
		for _, worker := range workers {
			if !worker.IsDir() {
				continue
			}
			workerDir := filepath.Join(workersRoot, worker.Name())
			statusPath := filepath.Join(workerDir, "status.json")
			heartbeatPath := filepath.Join(workerDir, "heartbeat.json")
			shutdownReqPath := filepath.Join(workerDir, "shutdown-request.json")
			shutdownAckPath := filepath.Join(workerDir, "shutdown-ack.json")

			if fileExists(statusPath) && fileExists(heartbeatPath) {
				statusRaw, statusErr := os.ReadFile(statusPath)
				hbRaw, hbErr := os.ReadFile(heartbeatPath)
				if statusErr == nil && hbErr == nil {
					var status map[string]any
					var heartbeat map[string]any
					if json.Unmarshal(statusRaw, &status) == nil && json.Unmarshal(hbRaw, &heartbeat) == nil {
						state, _ := status["state"].(string)
						lastTurn, _ := heartbeat["last_turn_at"].(string)
						if state == "working" {
							if ts, err := time.Parse(time.RFC3339, lastTurn); err == nil && now.Sub(ts) > lagThreshold {
								issues = append(issues, teamDoctorIssue{
									Code:     "delayed_status_lag",
									Message:  fmt.Sprintf("%s/%s working with stale heartbeat", teamName, worker.Name()),
									Severity: "fail",
								})
							}
						}
					}
				}
			}

			if fileExists(shutdownReqPath) && !fileExists(shutdownAckPath) {
				content, err := os.ReadFile(shutdownReqPath)
				if err == nil {
					var parsed map[string]any
					if json.Unmarshal(content, &parsed) == nil {
						if requestedAt, ok := parsed["requested_at"].(string); ok {
							if ts, err := time.Parse(time.RFC3339, requestedAt); err == nil && now.Sub(ts) > shutdownThreshold {
								issues = append(issues, teamDoctorIssue{
									Code:     "slow_shutdown",
									Message:  fmt.Sprintf("%s/%s has stale shutdown request without ack", teamName, worker.Name()),
									Severity: "fail",
								})
							}
						}
					}
				}
			}
		}
	}

	if teamLeaderIsStale(stateDir, leaderStaleThreshold, now) && !tmuxUnavailable {
		for session := range tmuxSessions {
			if knownTeamSessions[session] {
				issues = append(issues, teamDoctorIssue{
					Code:     "stale_leader",
					Message:  fmt.Sprintf("%s has active tmux session but leader has no recent activity", strings.TrimPrefix(session, "nana-team-")),
					Severity: "fail",
				})
			}
		}
	}

	if !tmuxUnavailable {
		for session := range tmuxSessions {
			if !knownTeamSessions[session] {
				issues = append(issues, teamDoctorIssue{
					Code:     "orphan_tmux_session",
					Message:  fmt.Sprintf("%s exists without matching team state (possibly external project)", session),
					Severity: "warn",
				})
			}
		}
	}

	return dedupeTeamIssues(issues), nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func listTeamTmuxSessions() (map[string]bool, bool) {
	cmd := exec.Command("tmux", "list-sessions", "-F", "#{session_name}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.ToLower(string(output))
		if strings.Contains(text, "no server running") || strings.Contains(text, "failed to connect to server") {
			return map[string]bool{}, false
		}
		return map[string]bool{}, true
	}
	sessions := map[string]bool{}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "nana-team-") {
			sessions[line] = true
		}
	}
	return sessions, false
}

func teamLeaderIsStale(stateDir string, threshold time.Duration, now time.Time) bool {
	latest := time.Time{}
	for _, path := range []string{
		filepath.Join(stateDir, "hud-state.json"),
		filepath.Join(stateDir, "leader-runtime-activity.json"),
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var parsed map[string]any
		if json.Unmarshal(content, &parsed) != nil {
			continue
		}
		for _, key := range []string{"last_activity_at", "last_turn_at"} {
			if raw, ok := parsed[key].(string); ok {
				if ts, err := time.Parse(time.RFC3339, raw); err == nil && ts.After(latest) {
					latest = ts
				}
			}
		}
	}
	if latest.IsZero() {
		return false
	}
	return now.Sub(latest) > threshold
}

func dedupeTeamIssues(issues []teamDoctorIssue) []teamDoctorIssue {
	seen := map[string]bool{}
	out := make([]teamDoctorIssue, 0, len(issues))
	for _, issue := range issues {
		key := issue.Code + ":" + issue.Message
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, issue)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Code == out[j].Code {
			return out[i].Message < out[j].Message
		}
		return out[i].Code < out[j].Code
	})
	return out
}
