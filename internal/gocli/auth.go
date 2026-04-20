package gocli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	authRegistryVersion      = 1
	authRegistryFileName     = "auth-profiles.json"
	authRuntimeStateFileName = "auth-state.json"
	authAccountsDirName      = "auth-accounts"
	defaultAuthAccountName   = "primary"
	autoAuthAccountPrefix    = "account"
)

var (
	authDefaultRetryWindow  = time.Hour
	authFiveHourRetryWindow = 5 * time.Hour
	authWeeklyRetryWindow   = 7 * 24 * time.Hour
)

type ManagedAuthRegistry struct {
	Version   int                  `json:"version"`
	Preferred string               `json:"preferred,omitempty"`
	Accounts  []ManagedAuthAccount `json:"accounts"`
}

type ManagedAuthAccount struct {
	Name      string `json:"name"`
	AuthPath  string `json:"auth_path"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at,omitempty"`
}

type ManagedAuthRuntimeState struct {
	Version            int                                `json:"version"`
	Active             string                             `json:"active,omitempty"`
	PendingActive      string                             `json:"pending_active,omitempty"`
	PendingReason      string                             `json:"pending_reason,omitempty"`
	PendingSince       string                             `json:"pending_since,omitempty"`
	RestartRequired    bool                               `json:"restart_required,omitempty"`
	Degraded           bool                               `json:"degraded,omitempty"`
	DegradedReason     string                             `json:"degraded_reason,omitempty"`
	LastDecisionAt     string                             `json:"last_decision_at,omitempty"`
	LastDecisionReason string                             `json:"last_decision_reason,omitempty"`
	Accounts           map[string]ManagedAuthAccountState `json:"accounts,omitempty"`
}

type ManagedAuthAccountState struct {
	LastActivatedAt            string `json:"last_activated_at,omitempty"`
	DepletedAt                 string `json:"depleted_at,omitempty"`
	RetryAfter                 string `json:"retry_after,omitempty"`
	PrimaryRetryAfter          string `json:"primary_retry_after,omitempty"`
	SecondaryRetryAfter        string `json:"secondary_retry_after,omitempty"`
	LastFailureReason          string `json:"last_failure_reason,omitempty"`
	LastUsageCheckAt           string `json:"last_usage_check_at,omitempty"`
	LastUsageSource            string `json:"last_usage_source,omitempty"`
	LastSuccessfulUsageCheckAt string `json:"last_successful_usage_check_at,omitempty"`
	LastUsageFreshUntil        string `json:"last_usage_fresh_until,omitempty"`
	LastUsageResult            string `json:"last_usage_result,omitempty"`
	LastUsageError             string `json:"last_usage_error,omitempty"`
	AuthMode                   string `json:"auth_mode,omitempty"`
	PlanType                   string `json:"plan_type,omitempty"`
	FiveHourUsedPct            *int   `json:"five_hour_used_pct,omitempty"`
	WeeklyUsedPct              *int   `json:"weekly_used_pct,omitempty"`
	LimitReached               bool   `json:"limit_reached,omitempty"`
	CreditsAvailable           *bool  `json:"credits_available,omitempty"`
	SpendControlHit            bool   `json:"spend_control_hit,omitempty"`
}

type authImportOptions struct {
	Name    string
	Source  string
	Primary bool
}

type authExportOptions struct {
	Name   string
	Target string
}

func LegacyCodexAuthPath(home string) string {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".codex", "auth.json")
}

func ResolvedCodexAuthPath() string {
	return filepath.Join(CodexHome(), "auth.json")
}

func ManagedAuthRegistryPath() string {
	return managedAuthRegistryPathForHome(CodexHome())
}

func managedAuthRegistryPathForHome(codexHome string) string {
	return filepath.Join(codexHome, authRegistryFileName)
}

func managedAuthRuntimeStatePathForHome(codexHome string) string {
	return filepath.Join(codexHome, authRuntimeStateFileName)
}

func managedAuthAccountsDirForHome(codexHome string) string {
	return filepath.Join(codexHome, authAccountsDirName)
}

func managedAuthAccountPathForHome(codexHome string, name string) string {
	return filepath.Join(managedAuthAccountsDirForHome(codexHome), name+".json")
}

func Account(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stdout, strings.TrimSpace(accountUsage()))
		return nil
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "help", "--help", "-h":
		fmt.Fprintln(os.Stdout, strings.TrimSpace(accountUsage()))
		return nil
	case "pull":
		options, err := parseAccountImportOptions(args[1:])
		if err != nil {
			return err
		}
		if strings.TrimSpace(options.Source) == "" {
			options.Source = LegacyCodexAuthPath(os.Getenv("HOME"))
		}
		if options.Name == "" {
			options.Name = defaultAuthAccountName
		}
		if len(args[1:]) == 0 {
			options.Primary = true
		}
		return importManagedAccount(CodexHome(), options)
	case "add":
		options, err := parseAccountImportOptions(args[1:])
		if err != nil {
			return err
		}
		return addManagedAccount(CodexHome(), options)
	case "import":
		options, err := parseAccountImportOptions(args[1:])
		if err != nil {
			return err
		}
		if strings.TrimSpace(options.Source) == "" {
			return fmt.Errorf("nana account import requires --from <path>\n%s", accountUsage())
		}
		return addManagedAccount(CodexHome(), options)
	case "export":
		options, err := parseAccountExportOptions(args[1:])
		if err != nil {
			return err
		}
		if strings.TrimSpace(options.Name) == "" {
			return fmt.Errorf("nana account export requires an account name\n%s", accountUsage())
		}
		if strings.TrimSpace(options.Target) == "" {
			return fmt.Errorf("nana account export requires --to <path>\n%s", accountUsage())
		}
		return exportManagedAccount(CodexHome(), options)
	case "list":
		return listManagedAccounts(CodexHome())
	case "status":
		return statusManagedAccounts(CodexHome())
	case "activate":
		if len(args) < 2 {
			return fmt.Errorf("nana account activate requires an account name\n%s", accountUsage())
		}
		return activateManagedAccount(CodexHome(), args[1])
	case "enable":
		if len(args) < 2 {
			return fmt.Errorf("nana account enable requires an account name\n%s", accountUsage())
		}
		return enableManagedAccount(CodexHome(), args[1])
	case "disable":
		if len(args) < 2 {
			return fmt.Errorf("nana account disable requires an account name\n%s", accountUsage())
		}
		return disableManagedAccount(CodexHome(), args[1])
	case "remove", "rm", "delete":
		if len(args) < 2 {
			return fmt.Errorf("nana account remove requires an account name\n%s", accountUsage())
		}
		return removeManagedAccount(CodexHome(), args[1])
	default:
		return fmt.Errorf("unknown account subcommand: %s\n%s", args[0], accountUsage())
	}
}

func AccountPull() error {
	return importManagedAccount(CodexHome(), authImportOptions{
		Name:    defaultAuthAccountName,
		Source:  LegacyCodexAuthPath(os.Getenv("HOME")),
		Primary: true,
	})
}

func accountUsage() string {
	return `Usage:
  nana account pull [name] [--from <path>] [--primary]
  nana account add [name] [--primary] [--from <path>]
  nana account import [name] --from <path> [--primary]
  nana account export <name> --to <path>
  nana account list
  nana account status
  nana account activate <name>
  nana account enable <name>
  nana account disable <name>
  nana account remove <name>

Notes:
  - Managed account profiles live under CODEX_HOME.
  - nana account add launches codex login --device-auth in an isolated temporary CODEX_HOME unless --from is provided.
  - nana account import adds a managed profile from an explicit auth.json path.
  - nana account export copies a managed profile to an explicit auth.json path.
  - When no account name is provided, NANA picks one automatically.
  - The preferred profile is tried first. When it is cooling down, NANA falls back to the next enabled profile.
  - Live sessions only queue account changes; fallback and switch-back apply on the next NANA-managed restart boundary.`
}

func parseAccountImportOptions(args []string) (authImportOptions, error) {
	options := authImportOptions{}
	for index := 0; index < len(args); index++ {
		token := strings.TrimSpace(args[index])
		switch {
		case token == "":
			continue
		case token == "--primary":
			options.Primary = true
		case token == "--from":
			if index+1 >= len(args) {
				return options, fmt.Errorf("missing value after --from\n%s", accountUsage())
			}
			options.Source = strings.TrimSpace(args[index+1])
			index++
		case strings.HasPrefix(token, "--from="):
			options.Source = strings.TrimSpace(strings.TrimPrefix(token, "--from="))
		case strings.HasPrefix(token, "-"):
			return options, fmt.Errorf("unknown account option: %s\n%s", token, accountUsage())
		case options.Name == "":
			options.Name = token
		default:
			return options, fmt.Errorf("unexpected account argument: %s\n%s", token, accountUsage())
		}
	}

	options.Name = normalizeManagedAuthName(options.Name)
	return options, nil
}

func parseAccountExportOptions(args []string) (authExportOptions, error) {
	options := authExportOptions{}
	for index := 0; index < len(args); index++ {
		token := strings.TrimSpace(args[index])
		switch {
		case token == "":
			continue
		case token == "--to":
			if index+1 >= len(args) {
				return options, fmt.Errorf("missing value after --to\n%s", accountUsage())
			}
			options.Target = strings.TrimSpace(args[index+1])
			index++
		case strings.HasPrefix(token, "--to="):
			options.Target = strings.TrimSpace(strings.TrimPrefix(token, "--to="))
		case strings.HasPrefix(token, "-"):
			return options, fmt.Errorf("unknown account option: %s\n%s", token, accountUsage())
		case options.Name == "":
			options.Name = token
		default:
			return options, fmt.Errorf("unexpected account argument: %s\n%s", token, accountUsage())
		}
	}

	options.Name = normalizeManagedAuthName(options.Name)
	return options, nil
}

func addManagedAccount(codexHome string, options authImportOptions) error {
	name := normalizeManagedAuthName(options.Name)
	registry, err := loadManagedAuthRegistry(codexHome)
	if err != nil {
		return err
	}
	if name == "" {
		name, err = suggestManagedAuthAccountName(codexHome)
		if err != nil {
			return err
		}
	} else if managedAuthNameReserved(codexHome, registry, name) {
		return fmt.Errorf("managed account %q already exists; use a different name", name)
	}
	options.Name = name
	if strings.TrimSpace(options.Source) != "" {
		return importManagedAccount(codexHome, options)
	}
	return loginAndImportManagedAccount(codexHome, options)
}

func loginAndImportManagedAccount(codexHome string, options authImportOptions) error {
	tempDir, err := newManagedAccountLoginTempDir(codexHome)
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	authPath := filepath.Join(tempDir, "auth.json")
	if err := runManagedAccountDeviceLogin(tempDir); err != nil {
		if _, statErr := os.Stat(authPath); statErr == nil {
			return fmt.Errorf("codex login exited with a non-zero status after writing credentials to %s; credentials were not imported: %w", authPath, err)
		}
		return fmt.Errorf("codex login exited with a non-zero status and no credentials were written to %s: %w", authPath, err)
	}
	if err := validateManagedAccountLoginProfile(authPath); err != nil {
		return err
	}

	options.Source = authPath
	return importManagedAccount(codexHome, options)
}

func newManagedAccountLoginTempDir(codexHome string) (string, error) {
	parent := filepath.Join(codexHome, ".tmp")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	return os.MkdirTemp(parent, "account-login-")
}

func runManagedAccountDeviceLogin(codexHome string) error {
	if _, err := exec.LookPath("codex"); err != nil {
		return fmt.Errorf("codex is required: %w", err)
	}

	fmt.Fprintf(os.Stdout, "[nana] Starting `codex login --device-auth` with isolated CODEX_HOME %s\n", codexHome)
	cmd := exec.Command("codex", "login", "--device-auth")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = buildCodexEnv(NotifyTempContract{}, codexHome)
	return cmd.Run()
}

func validateManagedAccountLoginProfile(path string) error {
	profile, err := readManagedAccountProfile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("codex login completed successfully but no credentials were written to %s", path)
		}
		return fmt.Errorf("codex login completed but credentials at %s could not be read: %w", path, err)
	}
	if !isChatGPTBackedAuthMode(profile.AuthMode) {
		return fmt.Errorf("codex login completed but credentials at %s use unsupported auth mode %q", path, profile.AuthMode)
	}
	if profile.Tokens == nil {
		return fmt.Errorf("codex login completed but credentials at %s are missing tokens", path)
	}

	missing := []string{}
	if strings.TrimSpace(profile.Tokens.AccessToken) == "" {
		missing = append(missing, "access_token")
	}
	if strings.TrimSpace(profile.Tokens.RefreshToken) == "" {
		missing = append(missing, "refresh_token")
	}
	if strings.TrimSpace(profile.Tokens.AccountID) == "" {
		missing = append(missing, "account_id")
	}
	if len(missing) > 0 {
		return fmt.Errorf("codex login completed but credentials at %s are missing %s", path, strings.Join(missing, ", "))
	}
	return nil
}

func suggestManagedAuthAccountName(codexHome string) (string, error) {
	registry, err := loadManagedAuthRegistry(codexHome)
	if err != nil {
		return "", err
	}
	used, err := managedAuthReservedNames(codexHome, registry)
	if err != nil {
		return "", err
	}
	if len(used) == 0 {
		return defaultAuthAccountName, nil
	}

	for index := 2; ; index++ {
		name := fmt.Sprintf("%s-%d", autoAuthAccountPrefix, index)
		if !used[name] {
			return name, nil
		}
	}
}

func managedAuthNameReserved(codexHome string, registry ManagedAuthRegistry, name string) bool {
	name = normalizeManagedAuthName(name)
	if name == "" {
		return false
	}
	used, err := managedAuthReservedNames(codexHome, registry)
	if err != nil {
		return registry.account(name) != nil
	}
	return used[name]
}

func managedAuthReservedNames(codexHome string, registry ManagedAuthRegistry) (map[string]bool, error) {
	used := map[string]bool{}
	for _, account := range registry.Accounts {
		name := normalizeManagedAuthName(account.Name)
		if name == "" {
			continue
		}
		used[name] = true
	}

	entries, err := os.ReadDir(managedAuthAccountsDirForHome(codexHome))
	if err != nil {
		if os.IsNotExist(err) {
			return used, nil
		}
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		name := normalizeManagedAuthName(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
		if name == "" {
			continue
		}
		used[name] = true
	}
	return used, nil
}

func importManagedAccount(codexHome string, options authImportOptions) error {
	name := normalizeManagedAuthName(options.Name)
	if name == "" {
		return fmt.Errorf("invalid account name %q", options.Name)
	}
	source := strings.TrimSpace(options.Source)
	if source == "" {
		source = LegacyCodexAuthPath(os.Getenv("HOME"))
	}
	if _, err := os.Stat(source); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("Codex credentials not found at %s", source)
		}
		return err
	}

	registry, err := loadManagedAuthRegistry(codexHome)
	if err != nil {
		return err
	}
	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		return err
	}
	if state.Accounts == nil {
		state.Accounts = map[string]ManagedAuthAccountState{}
	}

	target := managedAuthAccountPathForHome(codexHome, name)
	if err := copyFile(source, target); err != nil {
		return err
	}

	now := ISOTimeNow()
	account := registry.account(name)
	if account == nil {
		registry.Accounts = append(registry.Accounts, ManagedAuthAccount{
			Name:      name,
			AuthPath:  target,
			Enabled:   true,
			CreatedAt: now,
		})
	} else {
		account.AuthPath = target
		account.Enabled = true
		if strings.TrimSpace(account.CreatedAt) == "" {
			account.CreatedAt = now
		}
	}
	if registry.Preferred == "" || len(registry.Accounts) == 1 || options.Primary {
		setManagedAuthPreferred(&registry, name)
	}
	if err := saveManagedAuthRegistry(codexHome, registry); err != nil {
		return err
	}

	shouldActivate := state.Active == "" || registry.Preferred == name
	if shouldActivate {
		if err := copyFile(target, filepath.Join(codexHome, "auth.json")); err != nil {
			return err
		}
		accountState := state.Accounts[name]
		accountState.LastActivatedAt = now
		accountState.RetryAfter = ""
		accountState.DepletedAt = ""
		accountState.LastFailureReason = ""
		state.Accounts[name] = accountState
		state.Active = name
		clearPendingAccountSwitch(&state)
		if err := saveManagedAuthRuntimeState(codexHome, state); err != nil {
			return err
		}
	}

	fmt.Fprintf(os.Stdout, "[nana] Registered Codex credentials as account %q from %s\n", name, source)
	if shouldActivate {
		fmt.Fprintf(os.Stdout, "[nana] Active account is now %q\n", name)
	}
	return nil
}

func exportManagedAccount(codexHome string, options authExportOptions) error {
	name := normalizeManagedAuthName(options.Name)
	if name == "" {
		return fmt.Errorf("invalid account name %q", options.Name)
	}
	target := strings.TrimSpace(options.Target)
	if target == "" {
		return fmt.Errorf("missing export target")
	}

	registry, err := loadManagedAuthRegistry(codexHome)
	if err != nil {
		return err
	}
	account := registry.account(name)
	if account == nil {
		return fmt.Errorf("managed account %q not found", name)
	}
	if sameManagedAuthFile(account.AuthPath, target) {
		return fmt.Errorf("refusing to export managed account %q onto itself at %s", name, target)
	}
	if err := copyFile(account.AuthPath, target); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "[nana] Exported managed account %q to %s\n", name, target)
	return nil
}

func sameManagedAuthFile(source string, target string) bool {
	source = filepath.Clean(strings.TrimSpace(source))
	target = filepath.Clean(strings.TrimSpace(target))
	if source == "" || target == "" {
		return false
	}
	if source == target {
		return true
	}
	sourceAbs, sourceErr := filepath.Abs(source)
	targetAbs, targetErr := filepath.Abs(target)
	return sourceErr == nil && targetErr == nil && sourceAbs == targetAbs
}

func listManagedAccounts(codexHome string) error {
	registry, err := loadManagedAuthRegistry(codexHome)
	if err != nil {
		return err
	}
	if len(registry.Accounts) == 0 {
		fmt.Fprintf(os.Stdout, "No managed accounts. Use `nana account pull` or `nana account add`.\n")
		return nil
	}
	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "Managed accounts (%s)\n", codexHome)
	for _, account := range registry.orderedAccounts() {
		identity := managedAccountIdentityForAccount(account)
		flags := []string{}
		if registry.Preferred == account.Name {
			flags = append(flags, "preferred")
		}
		if state.Active == account.Name {
			flags = append(flags, "active")
		}
		if !account.Enabled {
			flags = append(flags, "disabled")
		}
		accountState := state.Accounts[account.Name]
		if retryAfter := strings.TrimSpace(accountState.RetryAfter); retryAfter != "" {
			flags = append(flags, "retry_after="+retryAfter)
		}
		if len(flags) == 0 {
			flags = append(flags, "standby")
		}
		fmt.Fprintf(os.Stdout, "- %s%s [%s]\n", account.Name, formatManagedAccountIdentitySuffix(identity), strings.Join(flags, ", "))
	}
	return nil
}

func statusManagedAccounts(codexHome string) error {
	registry, err := loadManagedAuthRegistry(codexHome)
	if err != nil {
		return err
	}
	if len(registry.Accounts) == 0 {
		fmt.Fprintf(os.Stdout, "No managed accounts. Use `nana account pull` or `nana account add`.\n")
		return nil
	}
	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		return err
	}
	settings := resolveManagedAuthSettings(codexHome)
	fmt.Fprintf(os.Stdout, "Managed account status (%s)\n", codexHome)
	fmt.Fprintf(os.Stdout, "Usage threshold: %d%%\n", settings.usageThresholdPct)
	fmt.Fprintf(os.Stdout, "Poll interval: %s\n", settings.pollInterval)
	fmt.Fprintf(os.Stdout, "Preferred: %s\n", displayOrFallback(registry.Preferred, "(none)"))
	fmt.Fprintf(os.Stdout, "Active: %s\n", displayOrFallback(state.Active, "(none)"))
	if strings.TrimSpace(state.PendingActive) != "" {
		fmt.Fprintf(os.Stdout, "Pending: %s\n", state.PendingActive)
	} else {
		fmt.Fprintln(os.Stdout, "Pending: (none)")
	}
	if strings.TrimSpace(state.PendingReason) != "" {
		fmt.Fprintf(os.Stdout, "Pending reason: %s\n", state.PendingReason)
	}
	if strings.TrimSpace(state.PendingSince) != "" {
		fmt.Fprintf(os.Stdout, "Pending since: %s\n", state.PendingSince)
	}
	fmt.Fprintf(os.Stdout, "Restart required: %s\n", boolWord(state.RestartRequired))
	fmt.Fprintf(os.Stdout, "Degraded: %s\n", boolWord(state.Degraded))
	if strings.TrimSpace(state.DegradedReason) != "" {
		fmt.Fprintf(os.Stdout, "Degraded reason: %s\n", state.DegradedReason)
	}
	if strings.TrimSpace(state.LastDecisionReason) != "" {
		fmt.Fprintf(os.Stdout, "Last decision: %s\n", state.LastDecisionReason)
	}
	if strings.TrimSpace(state.LastDecisionAt) != "" {
		fmt.Fprintf(os.Stdout, "Last decision at: %s\n", state.LastDecisionAt)
	}
	fmt.Fprintln(os.Stdout, "Accounts:")
	for _, account := range registry.orderedAccounts() {
		identity := managedAccountIdentityForAccount(account)
		accountState := state.Accounts[account.Name]
		flags := []string{}
		if account.Enabled {
			flags = append(flags, "enabled")
		} else {
			flags = append(flags, "disabled")
		}
		if strings.TrimSpace(accountState.RetryAfter) != "" {
			flags = append(flags, "retry_after="+accountState.RetryAfter)
		}
		if strings.TrimSpace(accountState.PrimaryRetryAfter) != "" {
			flags = append(flags, "primary_retry_after="+accountState.PrimaryRetryAfter)
		}
		if strings.TrimSpace(accountState.SecondaryRetryAfter) != "" {
			flags = append(flags, "secondary_retry_after="+accountState.SecondaryRetryAfter)
		}
		if strings.TrimSpace(accountState.LastFailureReason) != "" {
			flags = append(flags, "reason="+accountState.LastFailureReason)
		}
		if strings.TrimSpace(accountState.AuthMode) != "" {
			flags = append(flags, "auth_mode="+accountState.AuthMode)
		}
		if strings.TrimSpace(accountState.PlanType) != "" {
			flags = append(flags, "plan="+accountState.PlanType)
		}
		usageParts := []string{}
		if accountState.FiveHourUsedPct != nil {
			usageParts = append(usageParts, fmt.Sprintf("5h:%d%%", *accountState.FiveHourUsedPct))
		}
		if accountState.WeeklyUsedPct != nil {
			usageParts = append(usageParts, fmt.Sprintf("wk:%d%%", *accountState.WeeklyUsedPct))
		}
		if len(usageParts) > 0 {
			flags = append(flags, "usage="+strings.Join(usageParts, ","))
		}
		if accountState.CreditsAvailable != nil {
			flags = append(flags, "credits="+boolWord(*accountState.CreditsAvailable))
		}
		if accountState.LimitReached {
			flags = append(flags, "limit_reached=yes")
		}
		if accountState.SpendControlHit {
			flags = append(flags, "spend_control=yes")
		}
		if strings.TrimSpace(accountState.LastUsageResult) != "" {
			flags = append(flags, "usage_check="+accountState.LastUsageResult)
		}
		if strings.TrimSpace(accountState.LastUsageCheckAt) != "" {
			flags = append(flags, "checked_at="+accountState.LastUsageCheckAt)
		}
		if strings.TrimSpace(accountState.LastSuccessfulUsageCheckAt) != "" {
			flags = append(flags, "last_ok="+accountState.LastSuccessfulUsageCheckAt)
		}
		if strings.TrimSpace(accountState.LastUsageFreshUntil) != "" {
			flags = append(flags, "fresh_until="+accountState.LastUsageFreshUntil)
		}
		if strings.TrimSpace(accountState.LastUsageError) != "" {
			flags = append(flags, "usage_error="+accountState.LastUsageError)
		}
		fmt.Fprintf(os.Stdout, "- %s%s [%s]\n", account.Name, formatManagedAccountIdentitySuffix(identity), strings.Join(flags, ", "))
	}
	return nil
}

func managedAccountIdentityForAccount(account ManagedAuthAccount) string {
	profile, err := readManagedAccountProfile(account.AuthPath)
	if err != nil {
		return ""
	}
	return managedAccountProfileDisplayIdentity(profile)
}

func formatManagedAccountIdentitySuffix(identity string) string {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return ""
	}
	return " <" + identity + ">"
}

func activateManagedAccount(codexHome string, rawName string) error {
	name := normalizeManagedAuthName(rawName)
	registry, err := loadManagedAuthRegistry(codexHome)
	if err != nil {
		return err
	}
	account := registry.account(name)
	if account == nil {
		return fmt.Errorf("managed account %q not found", name)
	}
	account.Enabled = true
	setManagedAuthPreferred(&registry, name)
	if err := saveManagedAuthRegistry(codexHome, registry); err != nil {
		return err
	}
	if err := copyFile(account.AuthPath, filepath.Join(codexHome, "auth.json")); err != nil {
		return err
	}
	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		return err
	}
	if state.Accounts == nil {
		state.Accounts = map[string]ManagedAuthAccountState{}
	}
	accountState := state.Accounts[name]
	accountState.LastActivatedAt = ISOTimeNow()
	accountState.RetryAfter = ""
	accountState.DepletedAt = ""
	accountState.LastFailureReason = ""
	state.Accounts[name] = accountState
	state.Active = name
	clearPendingAccountSwitch(&state)
	if err := saveManagedAuthRuntimeState(codexHome, state); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "[nana] Preferred and active account set to %q\n", name)
	return nil
}

func enableManagedAccount(codexHome string, rawName string) error {
	name := normalizeManagedAuthName(rawName)
	registry, err := loadManagedAuthRegistry(codexHome)
	if err != nil {
		return err
	}
	account := registry.account(name)
	if account == nil {
		return fmt.Errorf("managed account %q not found", name)
	}
	if account.Enabled {
		fmt.Fprintf(os.Stdout, "[nana] Account %q already enabled\n", name)
		return nil
	}
	account.Enabled = true
	if strings.TrimSpace(registry.Preferred) == "" {
		setManagedAuthPreferred(&registry, name)
	}
	if err := saveManagedAuthRegistry(codexHome, registry); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "[nana] Enabled account %q\n", name)
	return nil
}

func disableManagedAccount(codexHome string, rawName string) error {
	name := normalizeManagedAuthName(rawName)
	registry, err := loadManagedAuthRegistry(codexHome)
	if err != nil {
		return err
	}
	account := registry.account(name)
	if account == nil {
		return fmt.Errorf("managed account %q not found", name)
	}
	if !account.Enabled {
		fmt.Fprintf(os.Stdout, "[nana] Account %q already disabled\n", name)
		return nil
	}
	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		return err
	}
	account.Enabled = false
	if state.Active == name {
		next := firstEnabledAccountName(registry, name)
		if next == "" {
			return fmt.Errorf("cannot disable active account %q with no other enabled accounts", name)
		}
		if err := applyManagedAccountActivation(codexHome, &state, registry, next); err != nil {
			return err
		}
	}
	if registry.Preferred == name {
		registry.Preferred = firstEnabledAccountName(registry, "")
	}
	if state.PendingActive == name {
		clearPendingAccountSwitch(&state)
	}
	if err := saveManagedAuthRegistry(codexHome, registry); err != nil {
		return err
	}
	if err := saveManagedAuthRuntimeState(codexHome, state); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "[nana] Disabled account %q\n", name)
	return nil
}

func removeManagedAccount(codexHome string, rawName string) error {
	name := normalizeManagedAuthName(rawName)
	registry, err := loadManagedAuthRegistry(codexHome)
	if err != nil {
		return err
	}
	filtered := make([]ManagedAuthAccount, 0, len(registry.Accounts))
	removed := false
	for _, account := range registry.Accounts {
		if account.Name == name {
			removed = true
			continue
		}
		filtered = append(filtered, account)
	}
	if !removed {
		return fmt.Errorf("managed account %q not found", name)
	}
	registry.Accounts = filtered
	if registry.Preferred == name {
		registry.Preferred = ""
		if len(filtered) > 0 {
			registry.Preferred = filtered[0].Name
		}
	}
	if err := saveManagedAuthRegistry(codexHome, registry); err != nil {
		return err
	}
	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		return err
	}
	delete(state.Accounts, name)
	if state.Active == name {
		state.Active = ""
	}
	if state.PendingActive == name {
		clearPendingAccountSwitch(&state)
	}
	if err := saveManagedAuthRuntimeState(codexHome, state); err != nil {
		return err
	}
	_ = os.Remove(managedAuthAccountPathForHome(codexHome, name))
	if state.Active == "" && registry.Preferred != "" {
		return activateManagedAccount(codexHome, registry.Preferred)
	}
	fmt.Fprintf(os.Stdout, "[nana] Removed managed account %q\n", name)
	return nil
}

func applyManagedAccountActivation(codexHome string, state *ManagedAuthRuntimeState, registry ManagedAuthRegistry, name string) error {
	account := registry.account(name)
	if account == nil {
		return fmt.Errorf("managed account %q not found", name)
	}
	if err := copyFile(account.AuthPath, filepath.Join(codexHome, "auth.json")); err != nil {
		return err
	}
	if state.Accounts == nil {
		state.Accounts = map[string]ManagedAuthAccountState{}
	}
	accountState := state.Accounts[name]
	accountState.LastActivatedAt = ISOTimeNow()
	accountState.DepletedAt = ""
	accountState.RetryAfter = ""
	accountState.LastFailureReason = ""
	state.Accounts[name] = accountState
	state.Active = name
	clearPendingAccountSwitch(state)
	return nil
}

func clearPendingAccountSwitch(state *ManagedAuthRuntimeState) {
	if state == nil {
		return
	}
	state.PendingActive = ""
	state.PendingReason = ""
	state.PendingSince = ""
	state.RestartRequired = false
}

func firstEnabledAccountName(registry ManagedAuthRegistry, skip string) string {
	skip = normalizeManagedAuthName(skip)
	for _, account := range registry.orderedAccounts() {
		if !account.Enabled || account.Name == skip {
			continue
		}
		return account.Name
	}
	return ""
}

func boolWord(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func displayOrFallback(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func loadManagedAuthRegistry(codexHome string) (ManagedAuthRegistry, error) {
	registry := ManagedAuthRegistry{
		Version:  authRegistryVersion,
		Accounts: []ManagedAuthAccount{},
	}
	content, err := os.ReadFile(managedAuthRegistryPathForHome(codexHome))
	if err != nil {
		if os.IsNotExist(err) {
			return registry, nil
		}
		return registry, err
	}
	if err := json.Unmarshal(content, &registry); err != nil {
		return registry, err
	}
	registry.Version = authRegistryVersion
	normalized := make([]ManagedAuthAccount, 0, len(registry.Accounts))
	seen := map[string]bool{}
	for _, account := range registry.Accounts {
		account.Name = normalizeManagedAuthName(account.Name)
		if account.Name == "" || seen[account.Name] {
			continue
		}
		seen[account.Name] = true
		if strings.TrimSpace(account.AuthPath) == "" {
			account.AuthPath = managedAuthAccountPathForHome(codexHome, account.Name)
		}
		normalized = append(normalized, account)
	}
	registry.Accounts = normalized
	if registry.Preferred != "" {
		registry.Preferred = normalizeManagedAuthName(registry.Preferred)
	}
	if registry.Preferred == "" && len(registry.Accounts) > 0 {
		registry.Preferred = registry.Accounts[0].Name
	}
	return registry, nil
}

func saveManagedAuthRegistry(codexHome string, registry ManagedAuthRegistry) error {
	registry.Version = authRegistryVersion
	if err := os.MkdirAll(filepath.Dir(managedAuthRegistryPathForHome(codexHome)), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(managedAuthRegistryPathForHome(codexHome), append(content, '\n'), 0o644)
}

func loadManagedAuthRuntimeState(codexHome string) (ManagedAuthRuntimeState, error) {
	state := ManagedAuthRuntimeState{
		Version:  authRegistryVersion,
		Accounts: map[string]ManagedAuthAccountState{},
	}
	content, err := os.ReadFile(managedAuthRuntimeStatePathForHome(codexHome))
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, err
	}
	if err := json.Unmarshal(content, &state); err != nil {
		return state, err
	}
	state.Version = authRegistryVersion
	if state.Accounts == nil {
		state.Accounts = map[string]ManagedAuthAccountState{}
	}
	return state, nil
}

func saveManagedAuthRuntimeState(codexHome string, state ManagedAuthRuntimeState) error {
	state.Version = authRegistryVersion
	if state.Accounts == nil {
		state.Accounts = map[string]ManagedAuthAccountState{}
	}
	if err := os.MkdirAll(filepath.Dir(managedAuthRuntimeStatePathForHome(codexHome)), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(managedAuthRuntimeStatePathForHome(codexHome), append(content, '\n'), 0o644)
}

func normalizeManagedAuthName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "-")
	value = strings.ReplaceAll(value, "/", "-")
	value = strings.ReplaceAll(value, "\\", "-")
	builder := strings.Builder{}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			builder.WriteRune(r)
		}
	}
	value = builder.String()
	for strings.Contains(value, "--") {
		value = strings.ReplaceAll(value, "--", "-")
	}
	return strings.Trim(value, "-")
}

func setManagedAuthPreferred(registry *ManagedAuthRegistry, name string) {
	name = normalizeManagedAuthName(name)
	if registry == nil || name == "" {
		return
	}
	registry.Preferred = name
	ordered := make([]ManagedAuthAccount, 0, len(registry.Accounts))
	for _, account := range registry.Accounts {
		if account.Name == name {
			ordered = append([]ManagedAuthAccount{account}, ordered...)
			continue
		}
		ordered = append(ordered, account)
	}
	registry.Accounts = ordered
}

func (registry ManagedAuthRegistry) account(name string) *ManagedAuthAccount {
	name = normalizeManagedAuthName(name)
	for index := range registry.Accounts {
		if registry.Accounts[index].Name == name {
			return &registry.Accounts[index]
		}
	}
	return nil
}

func (registry ManagedAuthRegistry) orderedAccounts() []ManagedAuthAccount {
	accounts := append([]ManagedAuthAccount{}, registry.Accounts...)
	sort.SliceStable(accounts, func(i, j int) bool {
		if accounts[i].Name == registry.Preferred {
			return true
		}
		if accounts[j].Name == registry.Preferred {
			return false
		}
		return i < j
	})
	return accounts
}

func parseManagedAuthTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func readManagedAuthMetrics(cwd string) (*HUDMetrics, error) {
	content, err := os.ReadFile(filepath.Join(cwd, ".nana", "metrics.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var metrics HUDMetrics
	if err := json.Unmarshal(content, &metrics); err != nil {
		return nil, err
	}
	return &metrics, nil
}
