package gocli

import (
	"fmt"
	"strings"
	"testing"
)

func TestLiveGithubWorkRepoModeDirect(t *testing.T) {
	env := requireLiveGithubTestEnv(t)

	t.Run("local", func(t *testing.T) {
		home := liveGithubSetupIsolatedHome(t, env)
		prepareLiveGithubManagedCheckout(t, env.Repos.Local)

		marker := newLiveGithubMarker(t)
		logPath := installLiveGithubFakeCodex(t, home, marker)
		snapshots := liveGithubSnapshots(t, env, env.Repos.Local)
		t.Cleanup(func() {
			cleanupLiveGithubArtifactsByMarker(t, env, marker, []string{env.Repos.Local}, snapshots...)
		})

		issue := liveGithubCreateIssue(
			t,
			env,
			env.Repos.Local,
			liveGithubTaggedTitle(marker, "direct local smoke test"),
			liveGithubTaggedBody(marker, "Validate direct local repo-mode start."),
			[]string{"test"},
		)

		var result githubCommandResult
		output, err := captureStdout(t, func() error {
			var runErr error
			result, runErr = GithubWorkCommand(".", []string{"start", issue.HTMLURL, "--work-type", workTypeTestOnly, "--repo-mode", "local"})
			return runErr
		})
		if err != nil {
			t.Fatalf("GithubWorkCommand(start local): %v\n%s", err, output)
		}
		if result.RunID == "" {
			t.Fatalf("expected local start to produce a run id, output=%q", output)
		}
		if !strings.Contains(output, "Starting run gh-") {
			t.Fatalf("expected output to include run start, got %q", output)
		}
		manifestPath, _, err := resolveGithubRunManifestPath(result.RunID, false)
		if err != nil {
			t.Fatalf("resolve manifest: %v", err)
		}
		manifest, err := readGithubWorkManifest(manifestPath)
		if err != nil {
			t.Fatalf("read manifest: %v", err)
		}
		if manifest.PublishTarget != "local-branch" || manifest.CreatePROnComplete {
			t.Fatalf("expected local mode manifest to stay local, got %+v", manifest)
		}
		if got := liveGithubCountMatchingOpenPulls(t, env, marker, env.Repos.Local); got != 0 {
			t.Fatalf("expected no PR for local mode, marker=%s count=%d", marker, got)
		}
		if got := liveGithubFakeCodexInvocationCount(t, logPath); got == 0 {
			t.Fatalf("expected fake codex execution log for local mode, marker=%s", marker)
		}
		liveGithubAssertSnapshotsUnchanged(t, env, snapshots...)
	})

	t.Run("fork", func(t *testing.T) {
		home := liveGithubSetupIsolatedHome(t, env)
		liveGithubEnsureForkReady(t, env, env.Repos.ForkTarget)
		prepareLiveGithubManagedCheckout(t, env.Repos.ForkTarget)

		marker := newLiveGithubMarker(t)
		installLiveGithubFakeCodex(t, home, marker)
		snapshots := liveGithubSnapshots(t, env, env.Repos.ForkTarget, env.Repos.Fork)
		t.Cleanup(func() {
			cleanupLiveGithubArtifactsByMarker(t, env, marker, []string{env.Repos.ForkTarget, env.Repos.Fork}, snapshots...)
		})

		issue := liveGithubCreateIssue(
			t,
			env,
			env.Repos.ForkTarget,
			liveGithubTaggedTitle(marker, "direct fork smoke test"),
			liveGithubTaggedBody(marker, "Validate direct fork repo-mode start and publisher flow."),
			[]string{"test"},
		)

		var result githubCommandResult
		startOutput, err := captureStdout(t, func() error {
			var runErr error
			result, runErr = GithubWorkCommand(".", []string{"start", issue.HTMLURL, "--work-type", workTypeTestOnly, "--repo-mode", "fork"})
			return runErr
		})
		if err != nil {
			t.Fatalf("GithubWorkCommand(start fork): %v\n%s", err, startOutput)
		}
		if result.RunID == "" {
			t.Fatalf("expected fork start to produce a run id, output=%q", startOutput)
		}
		manifestPath, _, err := resolveGithubRunManifestPath(result.RunID, false)
		if err != nil {
			t.Fatalf("resolve manifest: %v", err)
		}
		manifest := patchLiveGithubPublisherManifest(t, manifestPath, marker, "fork")
		publisherOutput, err := captureStdout(t, func() error {
			_, runErr := GithubWorkCommand(".", []string{"lane-exec", "--run-id", manifest.RunID, "--lane", "publisher"})
			return runErr
		})
		if err != nil {
			t.Fatalf("GithubWorkCommand(publisher fork): %v\n%s", err, publisherOutput)
		}
		updated, err := readGithubWorkManifest(manifestPath)
		if err != nil {
			t.Fatalf("read updated manifest: %v", err)
		}
		if updated.PublishedPRNumber <= 0 {
			t.Fatalf("expected fork mode to publish a PR, got %+v", updated)
		}
		if updated.PublishRepoSlug != env.Repos.Fork {
			t.Fatalf("expected fork mode publish repo %q, got %+v", env.Repos.Fork, updated)
		}
		pull := liveGithubReadPull(t, env, updated.PublishRepoSlug, updated.PublishedPRNumber)
		if pull.Base.Repo.FullName != updated.PublishRepoSlug {
			t.Fatalf("expected fork PR base repo %q, got %+v", updated.PublishRepoSlug, pull)
		}
		if pull.Head.Repo.FullName != updated.PublishRepoSlug {
			t.Fatalf("expected fork PR head repo %q, got %+v", updated.PublishRepoSlug, pull)
		}
		if pull.Head.Ref != marker {
			t.Fatalf("expected fork PR head ref %q, got %+v", marker, pull)
		}
		if !strings.Contains(publisherOutput, "Lane publisher completed via native publication flow.") {
			t.Fatalf("expected publisher completion output, got %q", publisherOutput)
		}
	})

	t.Run("repo", func(t *testing.T) {
		home := liveGithubSetupIsolatedHome(t, env)
		prepareLiveGithubManagedCheckout(t, env.Repos.Repo)

		marker := newLiveGithubMarker(t)
		installLiveGithubFakeCodex(t, home, marker)
		snapshots := liveGithubSnapshots(t, env, env.Repos.Repo)
		t.Cleanup(func() {
			cleanupLiveGithubArtifactsByMarker(t, env, marker, []string{env.Repos.Repo}, snapshots...)
		})

		issue := liveGithubCreateIssue(
			t,
			env,
			env.Repos.Repo,
			liveGithubTaggedTitle(marker, "direct repo smoke test"),
			liveGithubTaggedBody(marker, "Validate direct repo repo-mode start and publisher flow."),
			[]string{"test"},
		)

		var result githubCommandResult
		startOutput, err := captureStdout(t, func() error {
			var runErr error
			result, runErr = GithubWorkCommand(".", []string{"start", issue.HTMLURL, "--work-type", workTypeTestOnly, "--repo-mode", "repo"})
			return runErr
		})
		if err != nil {
			t.Fatalf("GithubWorkCommand(start repo): %v\n%s", err, startOutput)
		}
		if result.RunID == "" {
			t.Fatalf("expected repo start to produce a run id, output=%q", startOutput)
		}
		manifestPath, _, err := resolveGithubRunManifestPath(result.RunID, false)
		if err != nil {
			t.Fatalf("resolve manifest: %v", err)
		}
		manifest := patchLiveGithubPublisherManifest(t, manifestPath, marker, "repo")
		repointLiveGithubSandboxOriginToRepo(t, env, manifest)
		publisherOutput, err := captureStdout(t, func() error {
			_, runErr := GithubWorkCommand(".", []string{"lane-exec", "--run-id", manifest.RunID, "--lane", "publisher"})
			return runErr
		})
		if err != nil {
			t.Fatalf("GithubWorkCommand(publisher repo): %v\n%s", err, publisherOutput)
		}
		updated, err := readGithubWorkManifest(manifestPath)
		if err != nil {
			t.Fatalf("read updated manifest: %v", err)
		}
		if updated.PublishedPRNumber <= 0 {
			t.Fatalf("expected repo mode to publish a PR, got %+v", updated)
		}
		pull := liveGithubReadPull(t, env, env.Repos.Repo, updated.PublishedPRNumber)
		if pull.Head.Repo.FullName != env.Repos.Repo {
			t.Fatalf("expected repo PR head repo %q, got %+v", env.Repos.Repo, pull)
		}
		if pull.Head.Ref != marker {
			t.Fatalf("expected repo PR head ref %q, got %+v", marker, pull)
		}
		if !strings.Contains(publisherOutput, "Lane publisher completed via native publication flow.") {
			t.Fatalf("expected publisher completion output, got %q", publisherOutput)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		home := liveGithubSetupIsolatedHome(t, env)
		prepareLiveGithubManagedCheckout(t, env.Repos.Disabled)

		marker := newLiveGithubMarker(t)
		logPath := installLiveGithubFakeCodex(t, home, marker)
		snapshots := liveGithubSnapshots(t, env, env.Repos.Disabled)
		t.Cleanup(func() {
			cleanupLiveGithubArtifactsByMarker(t, env, marker, []string{env.Repos.Disabled}, snapshots...)
		})

		if _, err := captureStdout(t, func() error {
			return Repo(".", []string{"config", env.Repos.Disabled, "--repo-mode", "disabled"})
		}); err != nil {
			t.Fatalf("Repo(config disabled): %v", err)
		}
		issue := liveGithubCreateIssue(
			t,
			env,
			env.Repos.Disabled,
			liveGithubTaggedTitle(marker, "direct disabled smoke test"),
			liveGithubTaggedBody(marker, "Validate disabled mode rejects work before Codex launch."),
			[]string{"test"},
		)
		output, err := captureStdout(t, func() error {
			_, runErr := GithubWorkCommand(".", []string{"start", issue.HTMLURL, "--work-type", workTypeTestOnly})
			return runErr
		})
		if err == nil {
			t.Fatalf("expected disabled repo-mode start to fail, output=%q", output)
		}
		if !strings.Contains(err.Error(), "repo-mode disabled") {
			t.Fatalf("expected disabled repo-mode error, got %v\n%s", err, output)
		}
		assertLiveGithubCodexNotLaunched(t, logPath)
	})
}

func TestLiveGithubStartAutomationRouting(t *testing.T) {
	env := requireLiveGithubTestEnv(t)
	testCases := []struct {
		name         string
		repoSlug     string
		repoMode     string
		wantLaunch   bool
		cleanupRepos []string
	}{
		{name: "fork", repoSlug: env.Repos.ForkTarget, repoMode: "fork", wantLaunch: true, cleanupRepos: []string{env.Repos.ForkTarget, env.Repos.Fork}},
		{name: "repo", repoSlug: env.Repos.Repo, repoMode: "repo", wantLaunch: true, cleanupRepos: []string{env.Repos.Repo, env.Repos.Fork}},
		{name: "local", repoSlug: env.Repos.Local, repoMode: "local", wantLaunch: true, cleanupRepos: []string{env.Repos.Local, env.Repos.Fork}},
		{name: "disabled", repoSlug: env.Repos.Disabled, repoMode: "disabled", wantLaunch: false, cleanupRepos: []string{env.Repos.Disabled, env.Repos.Fork}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			home := liveGithubSetupIsolatedHome(t, env)
			prepareLiveGithubManagedCheckout(t, tc.repoSlug)

			marker := newLiveGithubMarker(t)
			logPath := installLiveGithubFakeCodex(t, home, marker)
			snapshots := liveGithubSnapshots(t, env, tc.cleanupRepos...)
			t.Cleanup(func() {
				cleanupLiveGithubArtifactsByMarker(t, env, marker, tc.cleanupRepos, snapshots...)
			})

			if _, err := captureStdout(t, func() error {
				return Repo(".", []string{"config", tc.repoSlug, "--repo-mode", tc.repoMode, "--issue-pick", "label"})
			}); err != nil {
				t.Fatalf("Repo(config %s): %v", tc.repoMode, err)
			}
			issue := liveGithubCreateIssue(
				t,
				env,
				tc.repoSlug,
				liveGithubTaggedTitle(marker, fmt.Sprintf("automation %s routing smoke", tc.repoMode)),
				liveGithubTaggedBody(marker, "Route exactly one labeled issue through nana start."),
				[]string{"nana", "test"},
			)

			output, err := captureStdout(t, func() error {
				return Start(".", []string{"--once", "--parallel", "1", "--no-ui"})
			})
			if err != nil {
				t.Fatalf("Start(%s): %v\n%s", tc.repoMode, err, output)
			}
			issueState, launched := liveGithubRunObserved(tc.repoSlug, issue.Number)
			if launched != tc.wantLaunch {
				t.Fatalf("expected launch=%t for %s, got launch=%t issue=%+v output=%q", tc.wantLaunch, tc.repoMode, launched, issueState, output)
			}
			invocations := liveGithubFakeCodexInvocationCount(t, logPath)
			if tc.wantLaunch {
				if strings.TrimSpace(issueState.LastRunID) == "" {
					t.Fatalf("expected %s to record a run id, issue=%+v output=%q", tc.repoMode, issueState, output)
				}
				if invocations == 0 {
					t.Fatalf("expected %s automation to invoke fake codex, marker=%s output=%q", tc.repoMode, marker, output)
				}
			} else {
				if strings.TrimSpace(issueState.LastRunID) != "" {
					t.Fatalf("expected %s to skip launch, issue=%+v output=%q", tc.repoMode, issueState, output)
				}
				if invocations != 0 {
					t.Fatalf("expected %s automation to skip fake codex, invocations=%d output=%q", tc.repoMode, invocations, output)
				}
			}
			if tc.repoMode == "local" {
				if got := liveGithubCountMatchingOpenPulls(t, env, marker, env.Repos.Local); got != 0 {
					t.Fatalf("expected local automation to keep work local, marker=%s count=%d", marker, got)
				}
			}
			liveGithubAssertSnapshotsUnchanged(t, env, snapshots...)
		})
	}
}

func TestLiveGithubStartAutomationIdempotence(t *testing.T) {
	env := requireLiveGithubTestEnv(t)
	testCases := []struct {
		name         string
		repoSlug     string
		repoMode     string
		wantLaunch   bool
		cleanupRepos []string
	}{
		{name: "fork", repoSlug: env.Repos.ForkTarget, repoMode: "fork", wantLaunch: true, cleanupRepos: []string{env.Repos.ForkTarget, env.Repos.Fork}},
		{name: "repo", repoSlug: env.Repos.Repo, repoMode: "repo", wantLaunch: true, cleanupRepos: []string{env.Repos.Repo, env.Repos.Fork}},
		{name: "local", repoSlug: env.Repos.Local, repoMode: "local", wantLaunch: true, cleanupRepos: []string{env.Repos.Local, env.Repos.Fork}},
		{name: "disabled", repoSlug: env.Repos.Disabled, repoMode: "disabled", wantLaunch: false, cleanupRepos: []string{env.Repos.Disabled, env.Repos.Fork}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			home := liveGithubSetupIsolatedHome(t, env)
			prepareLiveGithubManagedCheckout(t, tc.repoSlug)

			marker := newLiveGithubMarker(t)
			logPath := installLiveGithubFakeCodex(t, home, marker)
			snapshots := liveGithubSnapshots(t, env, tc.cleanupRepos...)
			t.Cleanup(func() {
				cleanupLiveGithubArtifactsByMarker(t, env, marker, tc.cleanupRepos, snapshots...)
			})

			if _, err := captureStdout(t, func() error {
				return Repo(".", []string{"config", tc.repoSlug, "--repo-mode", tc.repoMode, "--issue-pick", "label"})
			}); err != nil {
				t.Fatalf("Repo(config %s): %v", tc.repoMode, err)
			}
			issue := liveGithubCreateIssue(
				t,
				env,
				tc.repoSlug,
				liveGithubTaggedTitle(marker, fmt.Sprintf("automation %s idempotence", tc.repoMode)),
				liveGithubTaggedBody(marker, "Run nana start twice and ensure the launch stays stable."),
				[]string{"nana", "test"},
			)

			output1, err := captureStdout(t, func() error {
				return Start(".", []string{"--once", "--parallel", "1", "--no-ui"})
			})
			if err != nil {
				t.Fatalf("first Start(%s): %v\n%s", tc.repoMode, err, output1)
			}
			issueState1, observed1 := liveGithubRunObserved(tc.repoSlug, issue.Number)
			invocations1 := liveGithubFakeCodexInvocationCount(t, logPath)
			openIssues1 := liveGithubCountMatchingOpenIssues(t, env, marker, tc.cleanupRepos...)

			output2, err := captureStdout(t, func() error {
				return Start(".", []string{"--once", "--parallel", "1", "--no-ui"})
			})
			if err != nil {
				t.Fatalf("second Start(%s): %v\n%s", tc.repoMode, err, output2)
			}
			issueState2, observed2 := liveGithubRunObserved(tc.repoSlug, issue.Number)
			invocations2 := liveGithubFakeCodexInvocationCount(t, logPath)
			openIssues2 := liveGithubCountMatchingOpenIssues(t, env, marker, tc.cleanupRepos...)

			if tc.wantLaunch {
				if !observed1 || !observed2 {
					t.Fatalf("expected %s to stay launched across repeats, issue1=%+v issue2=%+v", tc.repoMode, issueState1, issueState2)
				}
				if strings.TrimSpace(issueState1.LastRunID) == "" || issueState2.LastRunID != issueState1.LastRunID {
					t.Fatalf("expected %s to keep a stable run id across repeats, first=%+v second=%+v", tc.repoMode, issueState1, issueState2)
				}
				if invocations1 == 0 || invocations2 != invocations1 {
					t.Fatalf("expected %s to launch exactly once, invocations first=%d second=%d", tc.repoMode, invocations1, invocations2)
				}
			} else {
				if observed1 || observed2 {
					t.Fatalf("expected %s to remain skipped, first=%+v second=%+v", tc.repoMode, issueState1, issueState2)
				}
				if invocations1 != 0 || invocations2 != 0 {
					t.Fatalf("expected %s to stay unlaunched, invocations first=%d second=%d", tc.repoMode, invocations1, invocations2)
				}
			}
			if openIssues2 != openIssues1 {
				t.Fatalf("expected %s to keep a stable issue artifact count, first=%d second=%d", tc.repoMode, openIssues1, openIssues2)
			}
			if tc.repoMode == "local" {
				if got := liveGithubCountMatchingOpenPulls(t, env, marker, env.Repos.Local); got != 0 {
					t.Fatalf("expected local automation to keep PR count at zero, marker=%s count=%d", marker, got)
				}
			}
			liveGithubAssertSnapshotsUnchanged(t, env, snapshots...)
		})
	}
}

func TestLiveGithubFailureCleanup(t *testing.T) {
	env := requireLiveGithubTestEnv(t)
	testCases := []struct {
		name         string
		repoSlug     string
		repoMode     string
		fakeMode     string
		cleanupRepos []string
	}{
		{name: "local", repoSlug: env.Repos.Local, repoMode: "local", fakeMode: liveGithubFakeCodexModeImplement, cleanupRepos: []string{env.Repos.Local}},
		{name: "fork", repoSlug: env.Repos.ForkTarget, repoMode: "fork", fakeMode: liveGithubFakeCodexModePublisher, cleanupRepos: []string{env.Repos.ForkTarget, env.Repos.Fork}},
		{name: "repo", repoSlug: env.Repos.Repo, repoMode: "repo", fakeMode: liveGithubFakeCodexModePublisher, cleanupRepos: []string{env.Repos.Repo}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			home := liveGithubSetupIsolatedHome(t, env)
			if tc.repoMode == "fork" {
				liveGithubEnsureForkReady(t, env, tc.repoSlug)
			}
			prepareLiveGithubManagedCheckout(t, tc.repoSlug)

			marker := newLiveGithubMarker(t)
			logPath := installLiveGithubFakeCodex(t, home, marker)
			setLiveGithubFakeCodexMode(t, tc.fakeMode)
			snapshots := liveGithubSnapshots(t, env, tc.cleanupRepos...)
			t.Cleanup(func() {
				cleanupLiveGithubArtifactsByMarker(t, env, marker, tc.cleanupRepos, snapshots...)
			})

			issue := liveGithubCreateIssue(
				t,
				env,
				tc.repoSlug,
				liveGithubTaggedTitle(marker, fmt.Sprintf("%s failure cleanup", tc.repoMode)),
				liveGithubTaggedBody(marker, "Force a deterministic failure and verify remote state stays clean."),
				[]string{"test"},
			)

			var result githubCommandResult
			startOutput, err := captureStdout(t, func() error {
				var runErr error
				result, runErr = GithubWorkCommand(".", []string{"start", issue.HTMLURL, "--work-type", workTypeTestOnly, "--repo-mode", tc.repoMode})
				return runErr
			})
			if tc.fakeMode == liveGithubFakeCodexModeImplement {
				if err == nil {
					t.Fatalf("expected %s implementation failure, output=%q", tc.repoMode, startOutput)
				}
				if liveGithubFakeCodexInvocationCount(t, logPath) == 0 {
					t.Fatalf("expected %s implementation failure to invoke fake codex, marker=%s", tc.repoMode, marker)
				}
				if got := liveGithubCountMatchingOpenPulls(t, env, marker, tc.cleanupRepos...); got != 0 {
					t.Fatalf("expected %s failure to leave no open PRs, marker=%s count=%d", tc.repoMode, marker, got)
				}
				liveGithubAssertSnapshotsUnchanged(t, env, snapshots...)
				return
			}

			if err != nil {
				t.Fatalf("GithubWorkCommand(start %s): %v\n%s", tc.repoMode, err, startOutput)
			}
			if result.RunID == "" {
				t.Fatalf("expected %s start to produce a run id before publisher failure, output=%q", tc.repoMode, startOutput)
			}
			manifestPath, _, err := resolveGithubRunManifestPath(result.RunID, false)
			if err != nil {
				t.Fatalf("resolve manifest: %v", err)
			}
			manifest := patchLiveGithubPublisherManifest(t, manifestPath, marker, tc.repoMode)
			if tc.repoMode == "repo" {
				repointLiveGithubSandboxOriginToRepo(t, env, manifest)
			}
			publisherOutput, err := captureStdout(t, func() error {
				_, runErr := GithubWorkCommand(".", []string{"lane-exec", "--run-id", manifest.RunID, "--lane", "publisher"})
				return runErr
			})
			if err == nil {
				t.Fatalf("expected %s publisher failure, output=%q", tc.repoMode, publisherOutput)
			}
			if liveGithubFakeCodexInvocationCount(t, logPath) == 0 {
				t.Fatalf("expected %s publisher failure path to invoke fake codex, marker=%s", tc.repoMode, marker)
			}
			if got := liveGithubCountMatchingOpenPulls(t, env, marker, tc.cleanupRepos...); got != 0 {
				t.Fatalf("expected %s publisher failure to leave no open PRs, marker=%s count=%d", tc.repoMode, marker, got)
			}
			liveGithubAssertSnapshotsUnchanged(t, env, snapshots...)
		})
	}
}
