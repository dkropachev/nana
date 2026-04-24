package gocli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAccountPullRegistersManagedPrimaryAccount(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".nana", "codex-home")
	source := filepath.Join(home, ".codex", "auth.json")

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)

	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(source, []byte(`{"token":"primary"}`), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	output, err := captureStdout(t, AccountPull)
	if err != nil {
		t.Fatalf("AccountPull(): %v", err)
	}
	if !strings.Contains(output, `Registered Codex credentials as account "primary"`) {
		t.Fatalf("unexpected output: %q", output)
	}

	registry, err := loadManagedAuthRegistry(codexHome)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	if registry.Preferred != "primary" || len(registry.Accounts) != 1 {
		t.Fatalf("unexpected registry: %#v", registry)
	}

	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Active != "primary" || state.RestartRequired {
		t.Fatalf("unexpected runtime state: %#v", state)
	}
}

func TestAccountAddLaunchesDeviceAuthAndAutoNamesPrimary(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".nana", "codex-home")
	fakeCodex := installFakeCodexLogin(t)

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("FAKE_CODEX_WRITE_AUTH", "1")
	t.Setenv("FAKE_CODEX_AUTH_CONTENT", chatgptProfileJSON("new-token", "new-refresh", "new-account"))

	output, err := captureStdout(t, func() error { return Account([]string{"add"}) })
	if err != nil {
		t.Fatalf("Account(add): %v", err)
	}
	if !strings.Contains(output, `Registered Codex credentials as account "primary"`) {
		t.Fatalf("unexpected output: %q", output)
	}

	argsRaw, err := os.ReadFile(fakeCodex.ArgsPath)
	if err != nil {
		t.Fatalf("read fake codex args: %v", err)
	}
	if got := strings.Fields(string(argsRaw)); strings.Join(got, "\x00") != strings.Join([]string{"login", "--device-auth"}, "\x00") {
		t.Fatalf("unexpected fake codex args: %q", string(argsRaw))
	}

	loginHomeRaw, err := os.ReadFile(fakeCodex.CodexHomePath)
	if err != nil {
		t.Fatalf("read fake codex CODEX_HOME: %v", err)
	}
	loginHome := strings.TrimSpace(string(loginHomeRaw))
	if loginHome == codexHome {
		t.Fatalf("expected isolated login CODEX_HOME, got %q", loginHome)
	}
	if !strings.HasPrefix(loginHome, filepath.Join(codexHome, ".tmp")+string(os.PathSeparator)) {
		t.Fatalf("expected login CODEX_HOME under %q, got %q", filepath.Join(codexHome, ".tmp"), loginHome)
	}

	registry, err := loadManagedAuthRegistry(codexHome)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	if registry.Preferred != "primary" || len(registry.Accounts) != 1 {
		t.Fatalf("unexpected registry: %#v", registry)
	}
	profile, err := readManagedAccountProfile(managedAuthAccountPathForHome(codexHome, "primary"))
	if err != nil {
		t.Fatalf("read imported profile: %v", err)
	}
	if profile.Tokens == nil || profile.Tokens.AccessToken != "new-token" {
		t.Fatalf("unexpected imported profile: %#v", profile)
	}
}

func TestAccountAddUsesExplicitName(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".nana", "codex-home")
	installFakeCodexLogin(t)

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("FAKE_CODEX_WRITE_AUTH", "1")
	t.Setenv("FAKE_CODEX_AUTH_CONTENT", chatgptProfileJSON("named-token", "named-refresh", "named-account"))

	output, err := captureStdout(t, func() error { return Account([]string{"add", "ops-team"}) })
	if err != nil {
		t.Fatalf("Account(add ops-team): %v", err)
	}
	if !strings.Contains(output, `Registered Codex credentials as account "ops-team"`) {
		t.Fatalf("unexpected output: %q", output)
	}

	registry, err := loadManagedAuthRegistry(codexHome)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	if account := registry.account("ops-team"); account == nil {
		t.Fatalf("expected named account, got %#v", registry)
	}
}

func TestAccountAddAutoNamesAdditionalAccount(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".nana", "codex-home")
	installFakeCodexLogin(t)
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary": {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-account")},
		},
		Active: "primary",
	})

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("FAKE_CODEX_WRITE_AUTH", "1")
	t.Setenv("FAKE_CODEX_AUTH_CONTENT", chatgptProfileJSON("secondary-token", "secondary-refresh", "secondary-account"))

	output, err := captureStdout(t, func() error { return Account([]string{"add"}) })
	if err != nil {
		t.Fatalf("Account(add): %v", err)
	}
	if !strings.Contains(output, `Registered Codex credentials as account "account-2"`) {
		t.Fatalf("unexpected output: %q", output)
	}

	registry, err := loadManagedAuthRegistry(codexHome)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	if account := registry.account("account-2"); account == nil {
		t.Fatalf("expected auto-generated account-2, got %#v", registry)
	}
}

func TestAccountAddRejectsExistingAccountName(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".nana", "codex-home")
	fakeCodex := installFakeCodexLogin(t)
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary":   {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-account")},
			"secondary": {Profile: chatgptProfileJSON("secondary-token", "secondary-refresh", "secondary-account")},
		},
		Active: "primary",
	})

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("FAKE_CODEX_WRITE_AUTH", "1")
	t.Setenv("FAKE_CODEX_AUTH_CONTENT", chatgptProfileJSON("new-token", "new-refresh", "new-account"))

	_, err := captureStdout(t, func() error { return Account([]string{"add", "secondary"}) })
	if err == nil {
		t.Fatal("expected duplicate-name failure")
	}
	if !strings.Contains(err.Error(), `managed account "secondary" already exists`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, readErr := os.ReadFile(fakeCodex.ArgsPath); !os.IsNotExist(readErr) {
		t.Fatalf("expected device login to be skipped, readErr=%v", readErr)
	}

	profile, readErr := readManagedAccountProfile(managedAuthAccountPathForHome(codexHome, "secondary"))
	if readErr != nil {
		t.Fatalf("read secondary profile: %v", readErr)
	}
	if profile.Tokens == nil || profile.Tokens.AccessToken != "secondary-token" {
		t.Fatalf("secondary profile was overwritten: %#v", profile)
	}
}

func TestAccountAddSkipsExistingManagedAccountFilesWhenAutoNaming(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".nana", "codex-home")
	installFakeCodexLogin(t)
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary":   {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-account")},
			"secondary": {Profile: chatgptProfileJSON("secondary-token", "secondary-refresh", "secondary-account")},
		},
		Active: "primary",
	})

	orphanPath := managedAuthAccountPathForHome(codexHome, "account-2")
	orphanProfile := chatgptProfileJSON("orphan-token", "orphan-refresh", "orphan-account")
	if err := os.WriteFile(orphanPath, []byte(orphanProfile), 0o644); err != nil {
		t.Fatalf("write orphan account file: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("FAKE_CODEX_WRITE_AUTH", "1")
	t.Setenv("FAKE_CODEX_AUTH_CONTENT", chatgptProfileJSON("new-token", "new-refresh", "new-account"))

	output, err := captureStdout(t, func() error { return Account([]string{"add"}) })
	if err != nil {
		t.Fatalf("Account(add): %v", err)
	}
	if !strings.Contains(output, `Registered Codex credentials as account "account-3"`) {
		t.Fatalf("unexpected output: %q", output)
	}

	orphanContent, err := os.ReadFile(orphanPath)
	if err != nil {
		t.Fatalf("read orphan account file: %v", err)
	}
	if string(orphanContent) != orphanProfile {
		t.Fatalf("expected account-2 orphan file to stay intact, got %q", string(orphanContent))
	}

	profile, err := readManagedAccountProfile(managedAuthAccountPathForHome(codexHome, "account-3"))
	if err != nil {
		t.Fatalf("read account-3 profile: %v", err)
	}
	if profile.Tokens == nil || profile.Tokens.AccessToken != "new-token" {
		t.Fatalf("unexpected account-3 profile: %#v", profile)
	}
}

func TestAccountAddFailsWhenDeviceLoginExitsNonZero(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".nana", "codex-home")
	installFakeCodexLogin(t)

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("FAKE_CODEX_EXIT_CODE", "9")

	_, err := captureStdout(t, func() error { return Account([]string{"add"}) })
	if err == nil {
		t.Fatal("expected login failure")
	}
	if !strings.Contains(err.Error(), "non-zero status") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(".tmp", "")) {
		t.Fatalf("expected temp auth path in error, got %v", err)
	}
}

func TestAccountAddFailsWhenDeviceLoginWritesNoCredentials(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".nana", "codex-home")
	installFakeCodexLogin(t)

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)

	_, err := captureStdout(t, func() error { return Account([]string{"add"}) })
	if err == nil {
		t.Fatal("expected missing credentials failure")
	}
	if !strings.Contains(err.Error(), "no credentials were written") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAccountImportRegistersManagedAccountFromExplicitPath(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".nana", "codex-home")
	source := filepath.Join(home, "exports", "auth.json")

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)

	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	if err := os.WriteFile(source, []byte(chatgptProfileJSON("import-token", "import-refresh", "import-account")), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	output, err := captureStdout(t, func() error { return Account([]string{"import", "ops-team", "--from", source}) })
	if err != nil {
		t.Fatalf("Account(import): %v", err)
	}
	if !strings.Contains(output, `Registered Codex credentials as account "ops-team"`) {
		t.Fatalf("unexpected output: %q", output)
	}

	registry, err := loadManagedAuthRegistry(codexHome)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	if account := registry.account("ops-team"); account == nil {
		t.Fatalf("expected imported account, got %#v", registry)
	}

	profile, err := readManagedAccountProfile(managedAuthAccountPathForHome(codexHome, "ops-team"))
	if err != nil {
		t.Fatalf("read imported profile: %v", err)
	}
	if profile.Tokens == nil || profile.Tokens.AccessToken != "import-token" {
		t.Fatalf("unexpected imported profile: %#v", profile)
	}
}

func TestAccountImportRequiresFromPath(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".nana", "codex-home")

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)

	_, err := captureStdout(t, func() error { return Account([]string{"import", "ops-team"}) })
	if err == nil {
		t.Fatal("expected missing --from failure")
	}
	if !strings.Contains(err.Error(), "nana account import requires --from <path>") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAccountExportCopiesManagedAccountProfile(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), ".codex")
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary":   {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
			"secondary": {Profile: chatgptProfileJSON("secondary-token", "secondary-refresh", "secondary-acct")},
		},
		Active: "primary",
	})
	target := filepath.Join(t.TempDir(), "exports", "secondary.json")
	t.Setenv("CODEX_HOME", codexHome)

	output, err := captureStdout(t, func() error { return Account([]string{"export", "secondary", "--to", target}) })
	if err != nil {
		t.Fatalf("Account(export): %v", err)
	}
	if !strings.Contains(output, `Exported managed account "secondary"`) {
		t.Fatalf("unexpected output: %q", output)
	}

	sourceContent, err := os.ReadFile(managedAuthAccountPathForHome(codexHome, "secondary"))
	if err != nil {
		t.Fatalf("read source account: %v", err)
	}
	targetContent, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read exported account: %v", err)
	}
	if string(targetContent) != string(sourceContent) {
		t.Fatalf("expected exported account to match source, source=%q target=%q", string(sourceContent), string(targetContent))
	}
}

func TestAccountExportRejectsManagedTargetPath(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), ".codex")
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary": {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
		},
		Active: "primary",
	})
	target := managedAuthAccountPathForHome(codexHome, "primary")
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read primary account before export: %v", err)
	}

	_, err = captureStdout(t, func() error {
		return exportManagedAccount(codexHome, authExportOptions{Name: "primary", Target: target})
	})
	if err == nil {
		t.Fatal("expected self-export failure")
	}
	if !strings.Contains(err.Error(), `refusing to export managed account "primary" onto itself`) {
		t.Fatalf("unexpected error: %v", err)
	}

	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read primary account after export: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("managed account changed during rejected self-export, before=%q after=%q", string(before), string(after))
	}
}

func TestAccountStatusShowsUsageState(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), ".codex")
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary":   {Profile: chatgptProfileJSONWithIdentity("primary-token", "primary-refresh", "primary-acct", "primary@example.com", "primary-login")},
			"secondary": {Profile: chatgptProfileJSONWithIdentity("secondary-token", "secondary-refresh", "secondary-acct", "", "secondary-login")},
		},
		Active:          "primary",
		PendingActive:   "secondary",
		PendingReason:   "usage-api-near-limit",
		PendingSince:    "2026-04-10T00:00:00Z",
		RestartRequired: true,
		AccountState: map[string]ManagedAuthAccountState{
			"primary": {
				AuthMode:            "chatgpt",
				PlanType:            "pro",
				FiveHourUsedPct:     intPtr(96),
				WeeklyUsedPct:       intPtr(20),
				RetryAfter:          "2026-04-10T02:00:00Z",
				PrimaryRetryAfter:   "2026-04-10T02:00:00Z",
				SecondaryRetryAfter: "2026-04-17T00:00:00Z",
				LastUsageResult:     "ok",
				LastUsageCheckAt:    "2026-04-10T00:01:00Z",
			},
		},
	})

	output, err := captureStdout(t, func() error { return statusManagedAccounts(codexHome) })
	if err != nil {
		t.Fatalf("statusManagedAccounts(): %v", err)
	}
	for _, needle := range []string{
		"Usage threshold: 95%",
		"Load balance policy: usage",
		"Preferred: primary",
		"Active: primary",
		"Pending: secondary",
		"Pending reason: usage-api-near-limit",
		"Restart required: yes",
		"auth_mode=chatgpt",
		"plan=pro",
		"usage=5h:96%,wk:20%",
		"retry_after=2026-04-10T02:00:00Z",
		"primary_retry_after=2026-04-10T02:00:00Z",
		"secondary_retry_after=2026-04-17T00:00:00Z",
		"- primary <primary@example.com> [enabled",
		"- secondary <secondary-login> [enabled",
	} {
		if !strings.Contains(output, needle) {
			t.Fatalf("expected %q in output, got %q", needle, output)
		}
	}
}

func TestAccountLimitsShowsLiveWindowsAndAdditionalRateLimits(t *testing.T) {
	location := time.FixedZone("AST", -4*60*60)
	now := time.Date(2026, time.April, 20, 23, 43, 59, 0, location)
	reset5h := time.Date(2026, time.April, 21, 0, 35, 9, 0, location)
	resetWeekly := time.Date(2026, time.April, 27, 19, 35, 9, 0, location)
	resetSpark5h := time.Date(2026, time.April, 21, 4, 43, 59, 0, location)
	resetSparkWeekly := time.Date(2026, time.April, 27, 23, 43, 59, 0, location)

	oldNow := managedAuthNow
	oldLocation := managedAuthDisplayLocation
	managedAuthNow = func() time.Time { return now }
	managedAuthDisplayLocation = func() *time.Location { return location }
	defer func() {
		managedAuthNow = oldNow
		managedAuthDisplayLocation = oldLocation
	}()

	server := newManagedAccountTestServer(t, managedAccountTestResponses{
		usage: map[string]managedAccountUsageReply{
			"main-token": {
				statusCode: http.StatusOK,
				body:       `{"plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":96,"limit_window_seconds":18000,"reset_at":` + strconv.FormatInt(reset5h.UTC().Unix(), 10) + `},"secondary_window":{"used_percent":20,"limit_window_seconds":604800,"reset_at":` + strconv.FormatInt(resetWeekly.UTC().Unix(), 10) + `}},"additional_rate_limits":[{"limit_name":"GPT-5.3-Codex-Spark","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0,"limit_window_seconds":18000,"reset_at":` + strconv.FormatInt(resetSpark5h.UTC().Unix(), 10) + `},"secondary_window":{"used_percent":0,"limit_window_seconds":604800,"reset_at":` + strconv.FormatInt(resetSparkWeekly.UTC().Unix(), 10) + `}}}],"credits":{"has_credits":false,"unlimited":false,"overage_limit_reached":false,"balance":"0"},"spend_control":{"reached":false}}`,
			},
		},
	})
	withManagedAccountEndpoints(t, server)

	codexHome := filepath.Join(t.TempDir(), ".codex")
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "main",
		Accounts: map[string]managedAccountFixtureEntry{
			"main": {
				Profile: chatgptProfileJSONWithIdentity(
					"main-token",
					"main-refresh",
					"a125b746-163d-4fc4-865f-b42e358c7d85",
					"dmitry.kropachev@gmail.com",
					"dmitry",
				),
			},
		},
		Active: "main",
	})
	t.Setenv("CODEX_HOME", codexHome)

	output, err := captureStdout(t, func() error { return Account([]string{"limits"}) })
	if err != nil {
		t.Fatalf("Account(limits): %v", err)
	}

	for _, needle := range []string{
		"main",
		"  email: dmitry.kropachev@gmail.com",
		"  account_id: a125b746-163d-4fc4-865f-b42e358c7d85",
		"  plan: pro",
		"  Codex: available",
		"    5h: 96% used; refreshes 2026-04-21 00:35:09 AST (in 51m 10s)",
		"    weekly: 20% used; refreshes 2026-04-27 19:35:09 AST (in 6d 19h 51m)",
		"  GPT-5.3-Codex-Spark: available",
		"    5h: 0% used; refreshes 2026-04-21 04:43:59 AST (in 5h)",
		"    weekly: 0% used; refreshes 2026-04-27 23:43:59 AST (in 1w)",
	} {
		if !strings.Contains(output, needle) {
			t.Fatalf("expected %q in output, got %q", needle, output)
		}
	}

	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Accounts["main"].FiveHourUsedPct == nil || *state.Accounts["main"].FiveHourUsedPct != 96 {
		t.Fatalf("expected refreshed runtime state, got %#v", state.Accounts["main"])
	}
}

func TestAccountLimitsReturnsErrorWhenUsageFetchFails(t *testing.T) {
	server := newManagedAccountTestServer(t, managedAccountTestResponses{
		usage: map[string]managedAccountUsageReply{
			"primary-token": {statusCode: http.StatusServiceUnavailable, body: `{"error":"busy"}`},
		},
	})
	withManagedAccountEndpoints(t, server)

	codexHome := filepath.Join(t.TempDir(), ".codex")
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary": {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
		},
		Active: "primary",
	})
	t.Setenv("CODEX_HOME", codexHome)

	_, err := captureStdout(t, func() error { return Account([]string{"limits"}) })
	if err == nil {
		t.Fatal("expected limits command to fail")
	}
	if !strings.Contains(err.Error(), `managed account "primary" usage check failed:`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAccountListShowsIdentityWhenAvailable(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), ".codex")
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary":   {Profile: chatgptProfileJSONWithIdentity("primary-token", "primary-refresh", "primary-acct", "primary@example.com", "primary-login")},
			"secondary": {Profile: chatgptProfileJSONWithIdentity("secondary-token", "secondary-refresh", "secondary-acct", "", "secondary-login")},
		},
		Active: "primary",
	})

	output, err := captureStdout(t, func() error { return listManagedAccounts(codexHome) })
	if err != nil {
		t.Fatalf("listManagedAccounts(): %v", err)
	}
	for _, needle := range []string{
		"- primary <primary@example.com> [preferred, active]",
		"- secondary <secondary-login> [standby]",
	} {
		if !strings.Contains(output, needle) {
			t.Fatalf("expected %q in output, got %q", needle, output)
		}
	}
}

func TestResolveManagedAuthSettingsPrefersEnvOverConfig(t *testing.T) {
	codexHome := t.TempDir()
	config := strings.Join([]string{
		authConfigUsageThresholdKey + " = 80",
		authConfigUsagePollSecondsKey + " = 120",
		authConfigLoadBalancePolicyKey + ` = "` + authLoadBalancePolicyPreferred + `"`,
		authConfigUsageRetryAttemptsKey + " = 5",
		authConfigUsageRetryDelayMsKey + " = 900",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv(authEnvUsageThresholdPct, "91")
	t.Setenv(authEnvUsagePollIntervalSeconds, "15")
	t.Setenv(authEnvLoadBalancePolicy, authLoadBalancePolicyUsage)
	t.Setenv(authEnvUsageRetryMaxAttempts, "2")

	settings := resolveManagedAuthSettings(codexHome)
	if settings.usageThresholdPct != 91 {
		t.Fatalf("threshold = %d", settings.usageThresholdPct)
	}
	if settings.pollInterval != 15*time.Second {
		t.Fatalf("poll interval = %s", settings.pollInterval)
	}
	if settings.loadBalancePolicy != authLoadBalancePolicyUsage {
		t.Fatalf("load balance policy = %q", settings.loadBalancePolicy)
	}
	if settings.retryMaxAttempts != 2 {
		t.Fatalf("retry max attempts = %d", settings.retryMaxAttempts)
	}
	if settings.retryBaseDelay != 900*time.Millisecond {
		t.Fatalf("retry base delay = %s", settings.retryBaseDelay)
	}
}

func TestDisableManagedAccountSwitchesAwayFromActive(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), ".codex")
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary":   {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
			"secondary": {Profile: chatgptProfileJSON("secondary-token", "secondary-refresh", "secondary-acct")},
		},
		Active: "primary",
	})

	if _, err := captureStdout(t, func() error { return disableManagedAccount(codexHome, "primary") }); err != nil {
		t.Fatalf("disableManagedAccount(): %v", err)
	}

	registry, err := loadManagedAuthRegistry(codexHome)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	if registry.Preferred != "secondary" {
		t.Fatalf("registry.Preferred = %q", registry.Preferred)
	}
	if account := registry.account("primary"); account == nil || account.Enabled {
		t.Fatalf("expected primary to be disabled, got %#v", account)
	}

	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Active != "secondary" || state.RestartRequired {
		t.Fatalf("unexpected runtime state: %#v", state)
	}
}

func TestPrepareManagedAuthManagerUsesUsageAPIOnStartup(t *testing.T) {
	server := newManagedAccountTestServer(t, managedAccountTestResponses{
		usage: map[string]managedAccountUsageReply{
			"primary-token":   nearLimitUsageReply(),
			"secondary-token": healthyUsageReply(),
		},
	})
	withManagedAccountEndpoints(t, server)

	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatalf("mkdir codexHome: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(authConfigLoadBalancePolicyKey+` = "`+authLoadBalancePolicyPreferred+`"`+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary":   {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
			"secondary": {Profile: chatgptProfileJSON("secondary-token", "secondary-refresh", "secondary-acct")},
		},
		Active: "primary",
	})

	if _, err := captureStdout(t, func() error {
		_, prepareErr := prepareManagedAuthManager(cwd, codexHome)
		return prepareErr
	}); err != nil {
		t.Fatalf("prepareManagedAuthManager(): %v", err)
	}

	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Active != "secondary" || state.RestartRequired {
		t.Fatalf("unexpected runtime state: %#v", state)
	}
	if state.Accounts["primary"].FiveHourUsedPct == nil || *state.Accounts["primary"].FiveHourUsedPct != 96 {
		t.Fatalf("expected primary usage data, got %#v", state.Accounts["primary"])
	}
}

func TestPrepareManagedAuthManagerUsesLeastUsedAccountByDefault(t *testing.T) {
	server := newManagedAccountTestServer(t, managedAccountTestResponses{
		usage: map[string]managedAccountUsageReply{
			"primary-token": {
				statusCode: http.StatusOK,
				body:       `{"plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":44,"limit_window_seconds":18000,"reset_after_seconds":100,"reset_at":1775813275},"secondary_window":{"used_percent":18,"limit_window_seconds":604800,"reset_after_seconds":100,"reset_at":1776358512}},"credits":{"has_credits":false,"unlimited":false,"overage_limit_reached":false,"balance":"0"},"spend_control":{"reached":false}}`,
			},
			"secondary-token": healthyUsageReply(),
		},
	})
	withManagedAccountEndpoints(t, server)

	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex")
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary":   {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
			"secondary": {Profile: chatgptProfileJSON("secondary-token", "secondary-refresh", "secondary-acct")},
		},
		Active: "primary",
	})

	if _, err := captureStdout(t, func() error {
		_, prepareErr := prepareManagedAuthManager(cwd, codexHome)
		return prepareErr
	}); err != nil {
		t.Fatalf("prepareManagedAuthManager(): %v", err)
	}

	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Active != "secondary" {
		t.Fatalf("expected least-used account to become active, got %#v", state)
	}
}

func TestPrepareManagedAuthManagerCanPreferPreferredPolicy(t *testing.T) {
	server := newManagedAccountTestServer(t, managedAccountTestResponses{
		usage: map[string]managedAccountUsageReply{
			"primary-token": {
				statusCode: http.StatusOK,
				body:       `{"plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":44,"limit_window_seconds":18000,"reset_after_seconds":100,"reset_at":1775813275},"secondary_window":{"used_percent":18,"limit_window_seconds":604800,"reset_after_seconds":100,"reset_at":1776358512}},"credits":{"has_credits":false,"unlimited":false,"overage_limit_reached":false,"balance":"0"},"spend_control":{"reached":false}}`,
			},
			"secondary-token": healthyUsageReply(),
		},
	})
	withManagedAccountEndpoints(t, server)

	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatalf("mkdir codexHome: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(authConfigLoadBalancePolicyKey+` = "`+authLoadBalancePolicyPreferred+`"`+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary":   {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
			"secondary": {Profile: chatgptProfileJSON("secondary-token", "secondary-refresh", "secondary-acct")},
		},
		Active: "primary",
	})

	if _, err := captureStdout(t, func() error {
		_, prepareErr := prepareManagedAuthManager(cwd, codexHome)
		return prepareErr
	}); err != nil {
		t.Fatalf("prepareManagedAuthManager(): %v", err)
	}

	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Active != "primary" {
		t.Fatalf("expected preferred-policy account to stay active, got %#v", state)
	}
}

func TestManagedAuthManagerQueuesFallbackFromUsageAPI(t *testing.T) {
	responses := managedAccountTestResponses{
		usage: map[string]managedAccountUsageReply{
			"primary-token":   healthyUsageReply(),
			"secondary-token": healthyUsageReply(),
		},
	}
	server := newManagedAccountTestServer(t, responses)
	withManagedAccountEndpoints(t, server)

	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex")
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary":   {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
			"secondary": {Profile: chatgptProfileJSON("secondary-token", "secondary-refresh", "secondary-acct")},
		},
		Active: "primary",
	})

	manager, err := prepareManagedAuthManager(cwd, codexHome)
	if err != nil {
		t.Fatalf("prepareManagedAuthManager(): %v", err)
	}
	manager.sessionStart = time.Now().UTC().Add(-time.Minute)
	responses.usage["primary-token"] = nearLimitUsageReply()

	if _, err := captureStdout(t, manager.evaluateUsage); err != nil {
		t.Fatalf("evaluateUsage(): %v", err)
	}

	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Active != "primary" {
		t.Fatalf("state.Active = %q", state.Active)
	}
	if state.PendingActive != "secondary" || !state.RestartRequired {
		t.Fatalf("expected queued fallback, got %#v", state)
	}
	if strings.TrimSpace(state.Accounts["primary"].RetryAfter) == "" {
		t.Fatalf("expected primary retry_after to be populated, got %#v", state.Accounts["primary"])
	}
}

func TestManagedAuthManagerQueuesPreferredReturnFromUsageAPI(t *testing.T) {
	responses := managedAccountTestResponses{
		usage: map[string]managedAccountUsageReply{
			"primary-token":   nearLimitUsageReply(),
			"secondary-token": healthyUsageReply(),
		},
	}
	server := newManagedAccountTestServer(t, responses)
	withManagedAccountEndpoints(t, server)

	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatalf("mkdir codexHome: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(authConfigLoadBalancePolicyKey+` = "`+authLoadBalancePolicyPreferred+`"`+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary":   {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
			"secondary": {Profile: chatgptProfileJSON("secondary-token", "secondary-refresh", "secondary-acct")},
		},
		Active: "secondary",
	})

	manager, err := prepareManagedAuthManager(cwd, codexHome)
	if err != nil {
		t.Fatalf("prepareManagedAuthManager(): %v", err)
	}
	manager.sessionStart = time.Now().UTC().Add(-time.Minute)
	responses.usage["primary-token"] = healthyUsageReply()

	if _, err := captureStdout(t, manager.evaluateUsage); err != nil {
		t.Fatalf("evaluateUsage(): %v", err)
	}

	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Active != "secondary" || state.PendingActive != "primary" || !state.RestartRequired {
		t.Fatalf("expected queued preferred return, got %#v", state)
	}
}

func TestManagedAuthManagerHandlesExecutionRateLimitBySwitchingAccounts(t *testing.T) {
	responses := managedAccountTestResponses{
		usage: map[string]managedAccountUsageReply{
			"primary-token":   healthyUsageReply(),
			"secondary-token": healthyUsageReply(),
		},
	}
	server := newManagedAccountTestServer(t, responses)
	withManagedAccountEndpoints(t, server)

	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex")
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary":   {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
			"secondary": {Profile: chatgptProfileJSON("secondary-token", "secondary-refresh", "secondary-acct")},
		},
		Active: "primary",
	})

	manager, err := prepareManagedAuthManager(cwd, codexHome)
	if err != nil {
		t.Fatalf("prepareManagedAuthManager(): %v", err)
	}
	responses.usage["primary-token"] = nearLimitUsageReply()

	decision, err := manager.handleExecutionRateLimit("rate limited")
	if err != nil {
		t.Fatalf("handleExecutionRateLimit(): %v", err)
	}
	if decision.SwitchedTo != "secondary" {
		t.Fatalf("expected switch to secondary, got %#v", decision)
	}
	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Active != "secondary" {
		t.Fatalf("expected active secondary, got %#v", state)
	}
	if state.Accounts["primary"].LastFailureReason != "exec-rate-limit" {
		t.Fatalf("expected primary execution failure reason, got %#v", state.Accounts["primary"])
	}
}

func TestManagedAuthManagerHandlesExecutionRateLimitByReturningRetryAfter(t *testing.T) {
	responses := managedAccountTestResponses{
		usage: map[string]managedAccountUsageReply{
			"primary-token":   nearLimitUsageReply(),
			"secondary-token": nearLimitUsageReply(),
		},
	}
	server := newManagedAccountTestServer(t, responses)
	withManagedAccountEndpoints(t, server)

	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex")
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary":   {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
			"secondary": {Profile: chatgptProfileJSON("secondary-token", "secondary-refresh", "secondary-acct")},
		},
		Active: "primary",
	})

	manager, err := prepareManagedAuthManager(cwd, codexHome)
	if err != nil {
		t.Fatalf("prepareManagedAuthManager(): %v", err)
	}

	decision, err := manager.handleExecutionRateLimit("rate limited")
	if err != nil {
		t.Fatalf("handleExecutionRateLimit(): %v", err)
	}
	if decision.SwitchedTo != "" {
		t.Fatalf("did not expect account switch, got %#v", decision)
	}
	if strings.TrimSpace(decision.RetryAfter) == "" {
		t.Fatalf("expected retry_after, got %#v", decision)
	}
	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if !state.Degraded {
		t.Fatalf("expected degraded state, got %#v", state)
	}
}

func TestManagedAuthManagerPreservesStaleActiveSnapshot(t *testing.T) {
	responses := managedAccountTestResponses{
		usage: map[string]managedAccountUsageReply{
			"primary-token":   healthyUsageReply(),
			"secondary-token": healthyUsageReply(),
		},
	}
	server := newManagedAccountTestServer(t, responses)
	withManagedAccountEndpoints(t, server)
	t.Setenv(authEnvUsageStaleAfterSeconds, "300")

	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex")
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary":   {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
			"secondary": {Profile: chatgptProfileJSON("secondary-token", "secondary-refresh", "secondary-acct")},
		},
		Active: "primary",
	})
	manager, err := prepareManagedAuthManager(cwd, codexHome)
	if err != nil {
		t.Fatalf("prepareManagedAuthManager(): %v", err)
	}
	manager.sessionStart = time.Now().UTC().Add(-time.Minute)
	responses.usage["primary-token"] = managedAccountUsageReply{statusCode: http.StatusServiceUnavailable, body: `{"error":"temporary"}`}

	if _, err := captureStdout(t, manager.evaluateUsage); err != nil {
		t.Fatalf("evaluateUsage(): %v", err)
	}
	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	active := state.Accounts["primary"]
	if active.LastUsageResult != accountUsageResultStale {
		t.Fatalf("expected stale result, got %#v", active)
	}
	if active.FiveHourUsedPct == nil || *active.FiveHourUsedPct != 12 {
		t.Fatalf("expected retained usage snapshot, got %#v", active)
	}
	if state.Degraded {
		t.Fatalf("did not expect degraded state: %#v", state)
	}
}

func TestManagedAuthManagerDoesNotSwitchToDormantStaleTarget(t *testing.T) {
	responses := managedAccountTestResponses{
		usage: map[string]managedAccountUsageReply{
			"primary-token":   healthyUsageReply(),
			"secondary-token": healthyUsageReply(),
		},
	}
	server := newManagedAccountTestServer(t, responses)
	withManagedAccountEndpoints(t, server)
	t.Setenv(authEnvUsageStaleAfterSeconds, "300")

	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex")
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary":   {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
			"secondary": {Profile: chatgptProfileJSON("secondary-token", "secondary-refresh", "secondary-acct")},
		},
		Active: "secondary",
	})
	manager, err := prepareManagedAuthManager(cwd, codexHome)
	if err != nil {
		t.Fatalf("prepareManagedAuthManager(): %v", err)
	}
	manager.sessionStart = time.Now().UTC().Add(-time.Minute)
	responses.usage["primary-token"] = managedAccountUsageReply{statusCode: http.StatusServiceUnavailable, body: `{"error":"temporary"}`}

	if _, err := captureStdout(t, manager.evaluateUsage); err != nil {
		t.Fatalf("evaluateUsage(): %v", err)
	}
	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.PendingActive != "" {
		t.Fatalf("did not expect pending active target, got %#v", state)
	}
}

func TestManagedAuthManagerMarksDegradedWhenAllAccountsUnavailable(t *testing.T) {
	responses := managedAccountTestResponses{
		usage: map[string]managedAccountUsageReply{
			"primary-token":   healthyUsageReply(),
			"secondary-token": healthyUsageReply(),
		},
	}
	server := newManagedAccountTestServer(t, responses)
	withManagedAccountEndpoints(t, server)
	t.Setenv(authEnvUsageStaleAfterSeconds, "1")

	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex")
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary":   {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
			"secondary": {Profile: chatgptProfileJSON("secondary-token", "secondary-refresh", "secondary-acct")},
		},
		Active: "primary",
	})
	manager, err := prepareManagedAuthManager(cwd, codexHome)
	if err != nil {
		t.Fatalf("prepareManagedAuthManager(): %v", err)
	}
	manager.sessionStart = time.Now().UTC().Add(-time.Minute)
	time.Sleep(1200 * time.Millisecond)
	responses.usage["primary-token"] = managedAccountUsageReply{statusCode: http.StatusServiceUnavailable, body: `{"error":"temporary"}`}
	responses.usage["secondary-token"] = managedAccountUsageReply{statusCode: http.StatusServiceUnavailable, body: `{"error":"temporary"}`}

	if _, err := captureStdout(t, manager.evaluateUsage); err != nil {
		t.Fatalf("evaluateUsage(): %v", err)
	}
	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if !state.Degraded || !strings.Contains(state.DegradedReason, "active-usage-unavailable") {
		t.Fatalf("expected degraded state, got %#v", state)
	}
	if state.Active != "primary" {
		t.Fatalf("active = %q", state.Active)
	}
}

func TestManagedAuthManagerHonorsActiveDwellBeforeFallback(t *testing.T) {
	responses := managedAccountTestResponses{
		usage: map[string]managedAccountUsageReply{
			"primary-token":   nearLimitUsageReply(),
			"secondary-token": healthyUsageReply(),
		},
	}
	server := newManagedAccountTestServer(t, responses)
	withManagedAccountEndpoints(t, server)
	t.Setenv(authEnvUsageMinActiveDwell, "600")

	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex")
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary":   {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
			"secondary": {Profile: chatgptProfileJSON("secondary-token", "secondary-refresh", "secondary-acct")},
		},
		Active: "primary",
		AccountState: map[string]ManagedAuthAccountState{
			"primary": {LastActivatedAt: time.Now().UTC().Add(-30 * time.Second).Format(time.RFC3339Nano)},
		},
	})
	manager, err := prepareManagedAuthManager(cwd, codexHome)
	if err != nil {
		t.Fatalf("prepareManagedAuthManager(): %v", err)
	}
	manager.sessionStart = time.Now().UTC().Add(-time.Minute)

	if _, err := captureStdout(t, manager.evaluateUsage); err != nil {
		t.Fatalf("evaluateUsage(): %v", err)
	}
	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.PendingActive != "" {
		t.Fatalf("did not expect pending switch due to dwell guard, got %#v", state)
	}
}

func TestManagedAuthManagerRefreshesTokenOnUnauthorized(t *testing.T) {
	server := newManagedAccountTestServer(t, managedAccountTestResponses{
		usage: map[string]managedAccountUsageReply{
			"primary-token":     {statusCode: http.StatusUnauthorized, body: `{"error":"expired"}`},
			"primary-token-new": healthyUsageReply(),
			"secondary-token":   healthyUsageReply(),
		},
		refresh: map[string]managedAccountRefreshReply{
			"primary-refresh": {
				statusCode: http.StatusOK,
				body:       `{"access_token":"primary-token-new","refresh_token":"primary-refresh-new"}`,
			},
		},
	})
	withManagedAccountEndpoints(t, server)

	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex")
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary": {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
		},
		Active: "primary",
	})

	if _, err := captureStdout(t, func() error {
		_, prepareErr := prepareManagedAuthManager(cwd, codexHome)
		return prepareErr
	}); err != nil {
		t.Fatalf("prepareManagedAuthManager(): %v", err)
	}

	profile, err := readManagedAccountProfile(managedAuthAccountPathForHome(codexHome, "primary"))
	if err != nil {
		t.Fatalf("readManagedAccountProfile(): %v", err)
	}
	if profile.Tokens == nil || profile.Tokens.AccessToken != "primary-token-new" || profile.Tokens.RefreshToken != "primary-refresh-new" {
		t.Fatalf("expected refreshed tokens, got %#v", profile.Tokens)
	}
}

func TestManagedAuthManagerRejectsUnsupportedAuthMode(t *testing.T) {
	server := newManagedAccountTestServer(t, managedAccountTestResponses{})
	withManagedAccountEndpoints(t, server)

	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex")
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary": {Profile: `{"auth_mode":"api_key","OPENAI_API_KEY":"sk-test"}`},
		},
		Active: "primary",
	})

	manager, err := prepareManagedAuthManager(cwd, codexHome)
	if err != nil {
		t.Fatalf("prepareManagedAuthManager(): %v", err)
	}
	manager.sessionStart = time.Now().UTC().Add(-time.Minute)
	if _, err := captureStdout(t, manager.evaluateUsage); err != nil {
		t.Fatalf("evaluateUsage(): %v", err)
	}
	state, err := loadManagedAuthRuntimeState(codexHome)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Accounts["primary"].LastUsageResult != accountUsageResultPermanent || !strings.Contains(state.Accounts["primary"].LastUsageError, "unsupported auth mode") {
		t.Fatalf("unexpected state: %#v", state.Accounts["primary"])
	}
}

func TestRunManagedAccountRequestRetriesTransientHTTPFailures(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	oldSleep := managedAuthRetrySleep
	sleepCalls := 0
	managedAuthRetrySleep = func(time.Duration) {
		sleepCalls++
	}
	defer func() {
		managedAuthRetrySleep = oldSleep
	}()

	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest(): %v", err)
	}
	result := runManagedAccountRequest(request, managedAuthSettings{
		httpTimeout:      time.Second,
		retryMaxAttempts: 3,
		retryBaseDelay:   time.Millisecond,
	})
	if result.statusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected final 503 result, got %+v", result)
	}
	if attempts != 3 || sleepCalls != 2 {
		t.Fatalf("expected 3 attempts and 2 sleeps, got attempts=%d sleeps=%d", attempts, sleepCalls)
	}
}

func TestRunManagedAccountRequestDoesNotRetryCanceledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("canceled request should not reach the server")
	}))
	defer server.Close()

	oldSleep := managedAuthRetrySleep
	sleepCalls := 0
	managedAuthRetrySleep = func(time.Duration) {
		sleepCalls++
	}
	defer func() {
		managedAuthRetrySleep = oldSleep
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext(): %v", err)
	}
	result := runManagedAccountRequest(request, managedAuthSettings{
		httpTimeout:      time.Second,
		retryMaxAttempts: 3,
		retryBaseDelay:   time.Millisecond,
	})
	if result.err == nil {
		t.Fatalf("expected canceled request error, got %+v", result)
	}
	if sleepCalls != 0 {
		t.Fatalf("expected no retry sleep for canceled context, got %d", sleepCalls)
	}
}

func TestIsTransientManagedAccountErrorClassifiesExpectedFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "deadline", err: context.DeadlineExceeded, want: true},
		{name: "eof", err: io.EOF, want: true},
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, want: true},
		{name: "timeout url error", err: &url.Error{Err: managedAuthTimeoutError{}}, want: true},
		{name: "canceled", err: context.Canceled, want: false},
		{name: "plain error", err: errors.New("boom"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientManagedAccountError(tc.err); got != tc.want {
				t.Fatalf("isTransientManagedAccountError(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}

func TestManagedAuthManagerFetchesUsageOutsideLock(t *testing.T) {
	server := newManagedAccountTestServer(t, managedAccountTestResponses{
		usage: map[string]managedAccountUsageReply{
			"primary-token":   healthyUsageReply(),
			"secondary-token": healthyUsageReply(),
		},
	})
	withManagedAccountEndpoints(t, server)

	cwd := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex")
	writeManagedAccountFixture(t, codexHome, managedAccountFixture{
		Preferred: "primary",
		Accounts: map[string]managedAccountFixtureEntry{
			"primary":   {Profile: chatgptProfileJSON("primary-token", "primary-refresh", "primary-acct")},
			"secondary": {Profile: chatgptProfileJSON("secondary-token", "secondary-refresh", "secondary-acct")},
		},
		Active: "primary",
	})
	manager, err := prepareManagedAuthManager(cwd, codexHome)
	if err != nil {
		t.Fatalf("prepareManagedAuthManager(): %v", err)
	}

	oldFetch := managedAuthFetchUsage
	calls := []string{}
	managedAuthFetchUsage = func(account ManagedAuthAccount, settings managedAuthSettings) managedAccountUsageCheck {
		lockAcquired := make(chan struct{}, 1)
		go func() {
			manager.mu.Lock()
			manager.mu.Unlock()
			lockAcquired <- struct{}{}
		}()
		select {
		case <-lockAcquired:
		case <-time.After(time.Second):
			t.Fatalf("manager mutex stayed locked during usage fetch for %s", account.Name)
		}
		calls = append(calls, account.Name)
		return managedAccountUsageCheck{
			authMode: "chatgpt",
			result:   accountUsageResultOK,
			snapshot: &managedAccountUsageSnapshot{PlanType: "pro"},
		}
	}
	defer func() {
		managedAuthFetchUsage = oldFetch
	}()

	done := make(chan error, 1)
	go func() {
		done <- manager.evaluateUsage()
	}()

	if err := <-done; err != nil {
		t.Fatalf("evaluateUsage(): %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected usage fetch for both accounts, got %+v", calls)
	}
}

type managedAuthTimeoutError struct{}

func (managedAuthTimeoutError) Error() string   { return "timeout" }
func (managedAuthTimeoutError) Timeout() bool   { return true }
func (managedAuthTimeoutError) Temporary() bool { return true }

type managedAccountFixture struct {
	Preferred       string
	Accounts        map[string]managedAccountFixtureEntry
	Active          string
	PendingActive   string
	PendingReason   string
	PendingSince    string
	RestartRequired bool
	AccountState    map[string]ManagedAuthAccountState
}

type managedAccountFixtureEntry struct {
	Profile  string
	Disabled bool
}

type managedAccountTestResponses struct {
	usage   map[string]managedAccountUsageReply
	refresh map[string]managedAccountRefreshReply
}

type managedAccountUsageReply struct {
	statusCode int
	body       string
}

type managedAccountRefreshReply struct {
	statusCode int
	body       string
}

func newManagedAccountTestServer(t *testing.T, responses managedAccountTestResponses) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wham/usage":
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			reply, ok := responses.usage[token]
			if !ok {
				http.Error(w, "missing usage fixture", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(reply.statusCode)
			_, _ = w.Write([]byte(reply.body))
		case "/oauth/token":
			if err := r.ParseForm(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			reply, ok := responses.refresh[r.FormValue("refresh_token")]
			if !ok {
				http.Error(w, "missing refresh fixture", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(reply.statusCode)
			_, _ = w.Write([]byte(reply.body))
		default:
			http.NotFound(w, r)
		}
	}))
}

func withManagedAccountEndpoints(t *testing.T, server *httptest.Server) {
	t.Helper()
	t.Setenv(authEnvUsageEndpointURL, server.URL+"/wham/usage")
	t.Setenv(authEnvRefreshURL, server.URL+"/oauth/token")
	t.Setenv(authEnvUsageRetryMaxAttempts, "1")
	t.Setenv(authEnvUsageRetryBaseDelayMs, "1")
	t.Cleanup(server.Close)
}

func writeManagedAccountFixture(t *testing.T, codexHome string, fixture managedAccountFixture) {
	t.Helper()
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatalf("mkdir codexHome: %v", err)
	}
	registry := ManagedAuthRegistry{
		Version:   authRegistryVersion,
		Preferred: fixture.Preferred,
		Accounts:  []ManagedAuthAccount{},
	}
	names := make([]string, 0, len(fixture.Accounts))
	for name := range fixture.Accounts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry, ok := fixture.Accounts[name]
		if !ok {
			continue
		}
		path := managedAuthAccountPathForHome(codexHome, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir account dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(entry.Profile), 0o644); err != nil {
			t.Fatalf("write account %s: %v", name, err)
		}
		registry.Accounts = append(registry.Accounts, ManagedAuthAccount{
			Name:     name,
			AuthPath: path,
			Enabled:  !entry.Disabled,
		})
	}
	if err := saveManagedAuthRegistry(codexHome, registry); err != nil {
		t.Fatalf("save registry: %v", err)
	}

	state := ManagedAuthRuntimeState{
		Version:         authRegistryVersion,
		Active:          fixture.Active,
		PendingActive:   fixture.PendingActive,
		PendingReason:   fixture.PendingReason,
		PendingSince:    fixture.PendingSince,
		RestartRequired: fixture.RestartRequired,
		Accounts:        map[string]ManagedAuthAccountState{},
	}
	for name := range fixture.Accounts {
		if fixture.AccountState != nil {
			state.Accounts[name] = fixture.AccountState[name]
		} else {
			state.Accounts[name] = ManagedAuthAccountState{}
		}
	}
	if err := saveManagedAuthRuntimeState(codexHome, state); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if fixture.Active != "" {
		if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(fixture.Accounts[fixture.Active].Profile), 0o644); err != nil {
			t.Fatalf("write live auth: %v", err)
		}
	}
}

func chatgptProfileJSON(accessToken string, refreshToken string, accountID string) string {
	return chatgptProfileJSONWithIdentity(accessToken, refreshToken, accountID, "", "")
}

func chatgptProfileJSONWithIdentity(accessToken string, refreshToken string, accountID string, email string, login string) string {
	idToken := "dummy-id-token"
	if email != "" || login != "" {
		payload := map[string]any{}
		if strings.TrimSpace(email) != "" {
			payload["email"] = strings.TrimSpace(email)
		}
		if strings.TrimSpace(login) != "" {
			payload["preferred_username"] = strings.TrimSpace(login)
		}
		idToken = fakeJWT(payload)
	}
	payload := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"access_token":  accessToken,
			"refresh_token": refreshToken,
			"account_id":    accountID,
			"id_token":      idToken,
		},
	}
	content, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(content)
}

func fakeJWT(payload map[string]any) string {
	headerRaw, err := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	if err != nil {
		panic(err)
	}
	payloadRaw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(headerRaw) + "." +
		base64.RawURLEncoding.EncodeToString(payloadRaw) + "."
}

func nearLimitUsageReply() managedAccountUsageReply {
	return managedAccountUsageReply{
		statusCode: http.StatusOK,
		body:       `{"plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":96,"limit_window_seconds":18000,"reset_after_seconds":100,"reset_at":1775813275},"secondary_window":{"used_percent":20,"limit_window_seconds":604800,"reset_after_seconds":100,"reset_at":1776358512}},"credits":{"has_credits":false,"unlimited":false,"overage_limit_reached":false,"balance":"0"},"spend_control":{"reached":false}}`,
	}
}

func healthyUsageReply() managedAccountUsageReply {
	return managedAccountUsageReply{
		statusCode: http.StatusOK,
		body:       `{"plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":12,"limit_window_seconds":18000,"reset_after_seconds":100,"reset_at":1775813275},"secondary_window":{"used_percent":8,"limit_window_seconds":604800,"reset_after_seconds":100,"reset_at":1776358512}},"credits":{"has_credits":false,"unlimited":false,"overage_limit_reached":false,"balance":"0"},"spend_control":{"reached":false}}`,
	}
}

func intPtr(value int) *int {
	return &value
}

func formatInt(value int) string {
	return strconv.Itoa(value)
}

type fakeCodexLoginPaths struct {
	ArgsPath      string
	CodexHomePath string
}

func installFakeCodexLogin(t *testing.T) fakeCodexLoginPaths {
	t.Helper()
	root := t.TempDir()
	fakeBin := filepath.Join(root, "bin")
	argsPath := filepath.Join(root, "codex-args.txt")
	codexHomePath := filepath.Join(root, "codex-home.txt")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}

	script := strings.Join([]string{
		"#!/bin/sh",
		`printf '%s\n' "$@" > "$FAKE_CODEX_ARGS_PATH"`,
		`printf '%s\n' "$CODEX_HOME" > "$FAKE_CODEX_HOME_PATH"`,
		`if [ "${FAKE_CODEX_WRITE_AUTH:-}" = "1" ]; then`,
		`  mkdir -p "$CODEX_HOME"`,
		`  printf '%s' "$FAKE_CODEX_AUTH_CONTENT" > "$CODEX_HOME/auth.json"`,
		`fi`,
		`exit "${FAKE_CODEX_EXIT_CODE:-0}"`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(fakeBin, "codex"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("FAKE_CODEX_ARGS_PATH", argsPath)
	t.Setenv("FAKE_CODEX_HOME_PATH", codexHomePath)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	return fakeCodexLoginPaths{
		ArgsPath:      argsPath,
		CodexHomePath: codexHomePath,
	}
}
