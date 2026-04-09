package gocli

import (
	"path/filepath"
	"testing"
	"time"
)

func TestIsNewerVersion(t *testing.T) {
	cases := []struct {
		current string
		latest  string
		want    bool
	}{
		{"1.0.0", "2.0.0", true},
		{"1.0.0", "1.1.0", true},
		{"1.0.0", "1.0.1", true},
		{"1.2.3", "1.2.3", false},
		{"2.0.0", "1.9.9", false},
		{"invalid", "1.0.0", false},
		{"1.0.0", "invalid", false},
		{"v1.0.0", "v1.0.1", true},
	}
	for _, tc := range cases {
		if got := IsNewerVersion(tc.current, tc.latest); got != tc.want {
			t.Fatalf("IsNewerVersion(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
		}
	}
}

func TestShouldCheckForUpdates(t *testing.T) {
	now := time.Now()
	if !shouldCheckForUpdates(now, nil, updateCheckInterval) {
		t.Fatal("expected nil state to require check")
	}
	if !shouldCheckForUpdates(now, &updateState{}, updateCheckInterval) {
		t.Fatal("expected empty state to require check")
	}
	if shouldCheckForUpdates(now, &updateState{LastCheckedAt: now.Add(-time.Hour).Format(time.RFC3339)}, updateCheckInterval) {
		t.Fatal("expected recent check to skip")
	}
	if !shouldCheckForUpdates(now, &updateState{LastCheckedAt: now.Add(-13 * time.Hour).Format(time.RFC3339)}, updateCheckInterval) {
		t.Fatal("expected old check to run")
	}
}

func TestMaybeCheckAndPromptUpdateRunsSetupAfterSuccessfulUpdate(t *testing.T) {
	cwd := t.TempDir()
	prompts := []string{}
	setupCalls := 0
	err := maybeCheckAndPromptUpdateWithDeps("", cwd, updateDeps{
		now:             time.Now,
		isTTY:           func() bool { return true },
		isLocalCheckout: func() bool { return false },
		readState:       func(string) (*updateState, error) { return nil, nil },
		writeState:      func(string, updateState) error { return nil },
		fetchLatest:     func() (string, error) { return "0.9.0", nil },
		currentVersion:  func() (string, error) { return "0.8.9", nil },
		askYesNo:        func(prompt string) bool { prompts = append(prompts, prompt); return true },
		runGlobalUpdate: func() (bool, string) { return true, "" },
		runSetupRefresh: func() error { setupCalls++; return nil },
		logf:            func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("maybeCheckAndPromptUpdateWithDeps(): %v", err)
	}
	if setupCalls != 1 {
		t.Fatalf("expected one setup refresh, got %d", setupCalls)
	}
	if len(prompts) != 1 {
		t.Fatalf("expected one prompt, got %d", len(prompts))
	}
}

func TestMaybeCheckAndPromptUpdateSkipsForLocalCheckout(t *testing.T) {
	cwd := t.TempDir()
	calls := 0
	err := maybeCheckAndPromptUpdateWithDeps(filepath.Join(cwd, "repo"), cwd, updateDeps{
		now:             time.Now,
		isTTY:           func() bool { return true },
		isLocalCheckout: func() bool { return true },
		readState:       func(string) (*updateState, error) { return nil, nil },
		writeState:      func(string, updateState) error { calls++; return nil },
		fetchLatest:     func() (string, error) { calls++; return "0.9.0", nil },
		currentVersion:  func() (string, error) { calls++; return "0.8.9", nil },
		askYesNo:        func(string) bool { calls++; return true },
		runGlobalUpdate: func() (bool, string) { calls++; return true, "" },
		runSetupRefresh: func() error { calls++; return nil },
		logf:            func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("maybeCheckAndPromptUpdateWithDeps(): %v", err)
	}
	if calls != 0 {
		t.Fatalf("expected no calls for local checkout, got %d", calls)
	}
}
