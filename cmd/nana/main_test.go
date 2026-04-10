package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

var (
	buildNanaBinaryOnce sync.Once
	buildNanaBinaryPath string
	buildNanaBinaryErr  error
	buildNanaBinaryLog  []byte
)

const commandTimeout = 15 * time.Second

func runCommand(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	t.Cleanup(cancel)
	return exec.CommandContext(ctx, name, args...)
}

func buildNanaBinary(t *testing.T) string {
	t.Helper()
	buildNanaBinaryOnce.Do(func() {
		tempRoot, err := os.MkdirTemp("", "nana-go-main-test-")
		if err != nil {
			buildNanaBinaryErr = err
			return
		}
		buildNanaBinaryPath = filepath.Join(tempRoot, "nana")
		if runtime.GOOS == "windows" {
			buildNanaBinaryPath += ".exe"
		}
		cmd := runCommand(t, "go", "build", "-o", buildNanaBinaryPath, "./cmd/nana")
		cmd.Dir = repoRoot(t)
		buildNanaBinaryLog, buildNanaBinaryErr = cmd.CombinedOutput()
	})
	if buildNanaBinaryErr != nil {
		t.Fatalf("go build failed: %v\n%s", buildNanaBinaryErr, buildNanaBinaryLog)
	}
	testBinaryPath := filepath.Join(t.TempDir(), filepath.Base(buildNanaBinaryPath))
	content, err := os.ReadFile(buildNanaBinaryPath)
	if err != nil {
		t.Fatalf("read shared binary: %v", err)
	}
	if err := os.WriteFile(testBinaryPath, content, 0o755); err != nil {
		t.Fatalf("copy shared binary: %v", err)
	}
	return testBinaryPath
}

func TestBinaryDefaultLaunchRoutesToCodex(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	fakeBin := filepath.Join(cwd, "bin")
	fakeCodexPath := filepath.Join(fakeBin, "codex")

	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	if err := os.WriteFile(fakeCodexPath, []byte("#!/bin/sh\nprintf 'fake-codex:%s\\n' \"$*\"\n"), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	cmd := runCommand(t, binaryPath)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"CODEX_HOME="+filepath.Join(home, ".codex-home"),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary launch failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "fake-codex:") {
		t.Fatalf("expected codex launch output, got %q", output)
	}
}

func TestBinaryExecRoutesToCodexExec(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")
	fakeBin := filepath.Join(cwd, "bin")
	fakeCodexPath := filepath.Join(fakeBin, "codex")

	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	if err := os.WriteFile(fakeCodexPath, []byte("#!/bin/sh\nprintf 'fake-codex:%s\\n' \"$*\"\n"), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	cmd := runCommand(t, binaryPath, "exec", "--help")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"CODEX_HOME="+filepath.Join(home, ".codex-home"),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary exec failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "fake-codex:exec --help") {
		t.Fatalf("expected codex exec output, got %q", output)
	}
}

func TestBinaryNestedGithubHelpRoutesLocally(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()

	testCases := []struct {
		args     []string
		expected string
	}{
		{args: []string{"implement", "--help"}, expected: "nana issue - GitHub issue-oriented aliases"},
		{args: []string{"investigate", "--help"}, expected: "nana issue - GitHub issue-oriented aliases"},
		{args: []string{"sync", "--help"}, expected: "nana issue - GitHub issue-oriented aliases"},
		{args: []string{"issue", "--help"}, expected: "nana issue - GitHub issue-oriented aliases"},
		{args: []string{"review", "--help"}, expected: "nana review - Review an external GitHub PR with deterministic persistence"},
		{args: []string{"review-rules", "--help"}, expected: "nana review-rules - Persistent repo rules mined from PR review history"},
		{args: []string{"repo", "--help"}, expected: "nana repo - Repository onboarding and verification-plan inspection"},
		{args: []string{"work-on", "--help"}, expected: "nana work-on - GitHub-targeted issue/PR implementation helper"},
		{args: []string{"work-local", "--help"}, expected: "nana work-local - Autonomous local plan execution for git-backed local repos"},
	}

	for _, tc := range testCases {
		cmd := runCommand(t, binaryPath, tc.args...)
		cmd.Dir = cwd
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("binary help %v failed: %v\n%s", tc.args, err, output)
		}
		if !strings.Contains(string(output), tc.expected) {
			t.Fatalf("expected %q in output for %v, got %q", tc.expected, tc.args, output)
		}
	}
}

func TestBinaryTopLevelHelpListsWorkSurfaces(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()

	cmd := runCommand(t, binaryPath, "help")
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary help failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "nana repo onboard") || !strings.Contains(string(output), "nana work-on") || !strings.Contains(string(output), "nana work-local") {
		t.Fatalf("expected work surfaces in top-level help, got %q", output)
	}
}

func TestBinaryHelpTopicRoutesToWorkOnHelp(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()

	cmd := runCommand(t, binaryPath, "help", "work-on")
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary help work-on failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "nana work-on - GitHub-targeted issue/PR implementation helper") {
		t.Fatalf("expected work-on help output, got %q", output)
	}
	if !strings.Contains(string(output), "nana work-on start <github-issue-or-pr-url>") {
		t.Fatalf("expected work-on usage lines in output, got %q", output)
	}
}

func TestBinaryHelpTopicRoutesToWorkLocalHelp(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()

	cmd := runCommand(t, binaryPath, "help", "work-local")
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary help work-local failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "nana work-local - Autonomous local plan execution for git-backed local repos") {
		t.Fatalf("expected work-local help output, got %q", output)
	}
	if !strings.Contains(string(output), "nana work-local start") || !strings.Contains(string(output), "nana work-local logs") || !strings.Contains(string(output), "--global-last") {
		t.Fatalf("expected work-local usage lines in output, got %q", output)
	}
}

func TestBinaryHelpTopicRoutesToRepoHelp(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()

	cmd := runCommand(t, binaryPath, "help", "repo")
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary help repo failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "nana repo - Repository onboarding and verification-plan inspection") {
		t.Fatalf("expected repo help output, got %q", output)
	}
	if !strings.Contains(string(output), "nana repo onboard") {
		t.Fatalf("expected repo usage lines in output, got %q", output)
	}
}

func TestBinaryReviewRulesWithoutSubcommandPrintsNativeHelp(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()
	fakeBin := filepath.Join(cwd, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "node"), "#!/bin/sh\nprintf 'fake-node:%s\\n' \"$*\"\n")

	cmd := runCommand(t, binaryPath, "review-rules")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"HOME="+filepath.Join(cwd, "home"),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary review-rules help failed: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "fake-node:") {
		t.Fatalf("review-rules without subcommand should not bridge through node, got %q", output)
	}
	if !strings.Contains(string(output), "nana review-rules - Persistent repo rules mined from PR review history") {
		t.Fatalf("expected review-rules help output, got %q", output)
	}
}

func TestBinaryStandaloneSetupWithoutRepoRoot(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()
	home := filepath.Join(cwd, "home")

	cmd := runCommand(t, binaryPath, "setup", "--scope", "project")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"CODEX_HOME="+filepath.Join(home, ".codex-home"),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary standalone setup failed: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".codex", "prompts", "executor.md")); err != nil {
		t.Fatalf("expected embedded setup prompt to be installed: %v\n%s", err, output)
	}
}

func TestBinaryStandaloneAgentsInitWithoutRepoRoot(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "src", "index.ts"), []byte("export const x = 1;\n"), 0o644); err != nil {
		t.Fatalf("write src file: %v", err)
	}

	cmd := runCommand(t, binaryPath, "agents-init")
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary standalone agents-init failed: %v\n%s", err, output)
	}
	rootAgents, err := os.ReadFile(filepath.Join(cwd, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v\n%s", err, output)
	}
	if !strings.Contains(string(rootAgents), "<!-- NANA:AGENTS-INIT:MANAGED -->") {
		t.Fatalf("expected managed AGENTS output, got %q", rootAgents)
	}
}

func writeExecutable(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func TestBinaryWorkOnStartRunsNatively(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()
	fakeBin := filepath.Join(cwd, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), "#!/bin/sh\nprintf 'fake-codex:%s\\n' \"$*\"\n")
	originRepo := filepath.Join(cwd, "origin")
	if err := os.MkdirAll(originRepo, 0o755); err != nil {
		t.Fatalf("mkdir origin repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(originRepo, "README.md"), []byte("# widget\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(originRepo, "package.json"), []byte(`{"name":"widget","scripts":{"lint":"eslint .","build":"tsc","test":"vitest"}}`), 0o644); err != nil {
		t.Fatalf("write repo package.json: %v", err)
	}
	gitEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	for _, args := range [][]string{{"init", "-b", "main"}, {"add", "."}, {"commit", "-m", "init"}} {
		cmd := runCommand(t, "git", args...)
		cmd.Dir = originRepo
		cmd.Env = gitEnv
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, output)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widget":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"name":"widget","full_name":"acme/widget","clone_url":%q,"default_branch":"main","html_url":"https://github.com/acme/widget"}`, originRepo)))
		case "/repos/acme/widget/issues/42":
			_, _ = w.Write([]byte(`{"title":"Start me","state":"open"}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s", r.URL.Path), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	cmd := runCommand(t, binaryPath, "work-on", "start", "https://github.com/acme/widget/issues/42", "--reviewer", "@me")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"HOME="+filepath.Join(cwd, "home"),
		"GH_TOKEN=test-token",
		"GITHUB_API_URL="+server.URL,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("work-on start native run failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Starting run gh-") {
		t.Fatalf("missing start output: %q", output)
	}
	if !strings.Contains(string(output), "fake-codex:exec -C") {
		t.Fatalf("expected native codex execution output, got %q", output)
	}
}

func TestBinaryReviewRunsNatively(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()
	fakeBin := filepath.Join(cwd, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), "#!/bin/sh\nprintf '{\"findings\":[{\"title\":\"Broken check\",\"severity\":\"medium\",\"path\":\"CHANGELOG.md\",\"line\":1,\"summary\":\"summary\",\"detail\":\"detail\",\"fix\":\"fix\",\"rationale\":\"why\"}]}'\n")
	originBare := filepath.Join(cwd, "origin.git")
	seedRepo := filepath.Join(cwd, "seed")
	if err := os.MkdirAll(seedRepo, 0o755); err != nil {
		t.Fatalf("mkdir seed repo: %v", err)
	}
	gitEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := runCommand(t, "git", args...)
		cmd.Dir = dir
		cmd.Env = gitEnv
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, output)
		}
	}
	runGit(cwd, "init", "--bare", originBare)
	if err := os.WriteFile(filepath.Join(seedRepo, "CHANGELOG.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(seedRepo, "init", "-b", "main")
	runGit(seedRepo, "add", ".")
	runGit(seedRepo, "commit", "-m", "init")
	runGit(seedRepo, "remote", "add", "origin", originBare)
	runGit(seedRepo, "push", "-u", "origin", "main")
	runGit(seedRepo, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(seedRepo, "CHANGELOG.md"), []byte("feature\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(seedRepo, "add", ".")
	runGit(seedRepo, "commit", "-m", "feature")
	headSHABytes, _ := exec.Command("git", "-C", seedRepo, "rev-parse", "HEAD").Output()
	headSHA := strings.TrimSpace(string(headSHABytes))
	baseSHABytes, _ := exec.Command("git", "-C", seedRepo, "rev-parse", "main").Output()
	baseSHA := strings.TrimSpace(string(baseSHABytes))
	runGit(seedRepo, "push", "-u", "origin", "feature")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user":
			_, _ = w.Write([]byte(`{"login":"reviewer-a"}`))
		case r.URL.Path == "/repos/acme/widget":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"name":"widget","full_name":"acme/widget","clone_url":%q,"default_branch":"main","html_url":"https://github.com/acme/widget"}`, originBare)))
		case r.URL.Path == "/repos/acme/widget/issues/7":
			_, _ = w.Write([]byte(`{"title":"Review me","state":"open","pull_request":{"url":"https://api.github.com/repos/acme/widget/pulls/7"}}`))
		case r.URL.Path == "/repos/acme/widget/pulls/7":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"number":7,"html_url":"https://example.invalid/pr/7","head":{"ref":"feature","sha":%q,"repo":{"full_name":"acme/widget"}},"base":{"ref":"main","sha":%q,"repo":{"full_name":"acme/widget"}}}`, headSHA, baseSHA)))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widget/pulls/7/reviews":
			_, _ = w.Write([]byte(`{"id":91,"html_url":"https://example.invalid/review/91"}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s", r.URL.Path), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	cmd := runCommand(t, binaryPath, "review", "https://github.com/acme/widget/pull/7", "--mode", "manual", "--per-item-context", "isolated")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"HOME="+filepath.Join(cwd, "home"),
		"GH_TOKEN=test-token",
		"GITHUB_API_URL="+server.URL,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("review native run failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Completed review for https://github.com/acme/widget/pull/7") {
		t.Fatalf("missing review output: %q", output)
	}
}

func TestBinaryIssueInvestigateRunsNativelyWithoutLegacyBridge(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()
	fakeBin := filepath.Join(cwd, "bin")
	originRepo := filepath.Join(cwd, "origin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	if err := os.MkdirAll(originRepo, 0o755); err != nil {
		t.Fatalf("mkdir origin repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "package.json"), []byte("{\"name\":\"nana-test\"}\n"), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, "dist", "cli"), 0o755); err != nil {
		t.Fatalf("mkdir dist cli: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "dist", "cli", "nana.js"), []byte("// bridge target\n"), 0o644); err != nil {
		t.Fatalf("write bridge entry: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "node"), "#!/bin/sh\nprintf 'fake-node:%s\\n' \"$*\"\n")
	if err := os.WriteFile(filepath.Join(originRepo, "README.md"), []byte("# widget\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(originRepo, "package.json"), []byte(`{"name":"widget","scripts":{"lint":"eslint .","build":"tsc","test":"vitest"}}`), 0o644); err != nil {
		t.Fatalf("write repo package.json: %v", err)
	}
	gitEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"add", "."},
		{"commit", "-m", "init"},
	} {
		cmd := runCommand(t, "git", args...)
		cmd.Dir = originRepo
		cmd.Env = gitEnv
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, output)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widget":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"name":"widget","full_name":"acme/widget","clone_url":%q,"default_branch":"main","html_url":"https://github.com/acme/widget"}`, originRepo)))
		case "/repos/acme/widget/issues/42":
			_, _ = w.Write([]byte(`{"title":"Investigate me","state":"open"}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s", r.URL.Path), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	cmd := runCommand(t, binaryPath, "investigate", "https://github.com/acme/widget/issues/42")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"HOME="+filepath.Join(cwd, "home"),
		"GH_TOKEN=test-token",
		"GITHUB_API_URL="+server.URL,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary investigate failed: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "fake-node:") {
		t.Fatalf("investigate should not bridge through node, got %q", output)
	}
	if !strings.Contains(string(output), "Investigated acme/widget issue #42") {
		t.Fatalf("missing investigate output: %q", output)
	}
}

func TestBinaryIssueImplementRunsNativelyWithoutLegacyBridge(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()
	fakeBin := filepath.Join(cwd, "bin")
	originRepo := filepath.Join(cwd, "origin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	if err := os.MkdirAll(originRepo, 0o755); err != nil {
		t.Fatalf("mkdir origin repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "package.json"), []byte("{\"name\":\"nana-test\"}\n"), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, "dist", "cli"), 0o755); err != nil {
		t.Fatalf("mkdir dist cli: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "dist", "cli", "nana.js"), []byte("// bridge target\n"), 0o644); err != nil {
		t.Fatalf("write bridge entry: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "node"), "#!/bin/sh\nprintf 'fake-node:%s\\n' \"$*\"\n")
	writeExecutable(t, filepath.Join(fakeBin, "codex"), "#!/bin/sh\nprintf 'fake-codex:%s\\n' \"$*\"\n")
	if err := os.WriteFile(filepath.Join(originRepo, "README.md"), []byte("# widget\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(originRepo, "package.json"), []byte(`{"name":"widget","scripts":{"lint":"eslint .","build":"tsc","test":"vitest"}}`), 0o644); err != nil {
		t.Fatalf("write repo package.json: %v", err)
	}
	gitEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	for _, args := range [][]string{{"init", "-b", "main"}, {"add", "."}, {"commit", "-m", "init"}} {
		cmd := runCommand(t, "git", args...)
		cmd.Dir = originRepo
		cmd.Env = gitEnv
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, output)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widget":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"name":"widget","full_name":"acme/widget","clone_url":%q,"default_branch":"main","html_url":"https://github.com/acme/widget"}`, originRepo)))
		case "/repos/acme/widget/issues/42":
			_, _ = w.Write([]byte(`{"title":"Implement me","state":"open"}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s", r.URL.Path), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	cmd := runCommand(t, binaryPath, "issue", "implement", "https://github.com/acme/widget/issues/42")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"HOME="+filepath.Join(cwd, "home"),
		"GH_TOKEN=test-token",
		"GITHUB_API_URL="+server.URL,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary issue implement failed: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "fake-node:") {
		t.Fatalf("issue implement should not bridge through node, got %q", output)
	}
	if !strings.Contains(string(output), "Starting run gh-") || !strings.Contains(string(output), "fake-codex:exec -C") {
		t.Fatalf("missing native issue implement output: %q", output)
	}
}

func TestBinaryIssueSyncRunsNativelyWithoutLegacyBridge(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()
	fakeBin := filepath.Join(cwd, "bin")
	managedRepoRoot := filepath.Join(cwd, "home", ".nana", "repos", "acme", "widget")
	sandboxPath := filepath.Join(managedRepoRoot, "sandboxes", "issue-42")
	repoCheckoutPath := filepath.Join(sandboxPath, "repo")
	runID := "gh-run-issue-sync-bin"
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "node"), "#!/bin/sh\nprintf 'fake-node:%s\\n' \"$*\"\n")
	writeExecutable(t, filepath.Join(fakeBin, "codex"), "#!/bin/sh\nprintf 'fake-codex:%s\\n' \"$*\"\n")
	if err := os.MkdirAll(filepath.Join(managedRepoRoot, "runs", runID), 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.MkdirAll(repoCheckoutPath, 0o755); err != nil {
		t.Fatalf("mkdir repo checkout: %v", err)
	}
	manifest := fmt.Sprintf(`{
  "run_id": %q,
  "repo_slug": "acme/widget",
  "repo_owner": "acme",
  "repo_name": "widget",
  "sandbox_id": "issue-42",
  "sandbox_path": %q,
  "sandbox_repo_path": %q,
  "target_kind": "issue",
  "target_number": 42,
  "target_url": "https://github.com/acme/widget/issues/42",
  "review_reviewer": "reviewer-a",
  "last_seen_issue_comment_id": 0,
  "last_seen_review_id": 0,
  "last_seen_review_comment_id": 0
}`, runID, sandboxPath, repoCheckoutPath)
	if err := os.WriteFile(filepath.Join(managedRepoRoot, "runs", runID, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path + "?" + r.URL.RawQuery {
		case "/repos/acme/widget/issues/42/comments?per_page=100":
			_, _ = w.Write([]byte(`[{"id":101,"html_url":"https://example.invalid/comment/101","body":"please update tests","updated_at":"2026-04-09T10:00:00Z","user":{"login":"reviewer-a"}}]`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s?%s", r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	cmd := runCommand(t, binaryPath, "issue", "sync", "https://github.com/acme/widget/issues/42", "--resume-last")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"HOME="+filepath.Join(cwd, "home"),
		"GH_TOKEN=test-token",
		"GITHUB_API_URL="+server.URL,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary issue sync failed: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "fake-node:") {
		t.Fatalf("issue sync should not bridge through node, got %q", output)
	}
	if !strings.Contains(string(output), "Stored new feedback for run "+runID) || !strings.Contains(string(output), "fake-codex:exec -C "+repoCheckoutPath) {
		t.Fatalf("missing native issue sync output: %q", output)
	}
}

func TestBinaryWorkOnVerifyRefreshRunsNativelyWithoutLegacyBridge(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()
	fakeBin := filepath.Join(cwd, "bin")
	managedRepoRoot := filepath.Join(cwd, "home", ".nana", "repos", "acme", "widget")
	sandboxPath := filepath.Join(managedRepoRoot, "sandboxes", "issue-42")
	repoCheckoutPath := filepath.Join(sandboxPath, "repo")
	runID := "gh-run-refresh-bin"
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "node"), "#!/bin/sh\nprintf 'fake-node:%s\\n' \"$*\"\n")
	if err := os.MkdirAll(filepath.Join(managedRepoRoot, "runs", runID), 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.MkdirAll(repoCheckoutPath, 0o755); err != nil {
		t.Fatalf("mkdir repo checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoCheckoutPath, "package.json"), []byte(`{"name":"widget","scripts":{"lint":"eslint .","build":"tsc","test":"vitest"}}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	manifest := fmt.Sprintf(`{
  "run_id": %q,
  "repo_slug": "acme/widget",
  "repo_owner": "acme",
  "repo_name": "widget",
  "sandbox_id": "issue-42",
  "sandbox_path": %q,
  "sandbox_repo_path": %q
}`, runID, sandboxPath, repoCheckoutPath)
	if err := os.WriteFile(filepath.Join(managedRepoRoot, "runs", runID, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	cmd := runCommand(t, binaryPath, "work-on", "verify-refresh", "--run-id", runID)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"HOME="+filepath.Join(cwd, "home"),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary verify-refresh failed: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "fake-node:") {
		t.Fatalf("verify-refresh should not bridge through node, got %q", output)
	}
	if !strings.Contains(string(output), "Verification artifacts for run "+runID) {
		t.Fatalf("missing verify-refresh output: %q", output)
	}
}

func TestBinaryWorkOnSyncRunsNatively(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()
	fakeBin := filepath.Join(cwd, "bin")
	managedRepoRoot := filepath.Join(cwd, "home", ".nana", "repos", "acme", "widget")
	sandboxPath := filepath.Join(managedRepoRoot, "sandboxes", "issue-42")
	repoCheckoutPath := filepath.Join(sandboxPath, "repo")
	runID := "gh-run-sync-bin"
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), "#!/bin/sh\nprintf 'fake-codex:%s\\n' \"$*\"\n")
	if err := os.MkdirAll(filepath.Join(managedRepoRoot, "runs", runID), 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.MkdirAll(repoCheckoutPath, 0o755); err != nil {
		t.Fatalf("mkdir repo checkout: %v", err)
	}
	manifest := fmt.Sprintf(`{
  "run_id": %q,
  "repo_slug": "acme/widget",
  "repo_owner": "acme",
  "repo_name": "widget",
  "sandbox_id": "issue-42",
  "sandbox_path": %q,
  "sandbox_repo_path": %q,
  "target_kind": "issue",
  "target_number": 42,
  "target_url": "https://github.com/acme/widget/issues/42",
  "review_reviewer": "reviewer-a",
  "last_seen_issue_comment_id": 0,
  "last_seen_review_id": 0,
  "last_seen_review_comment_id": 0
}`, runID, sandboxPath, repoCheckoutPath)
	if err := os.WriteFile(filepath.Join(managedRepoRoot, "runs", runID, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path + "?" + r.URL.RawQuery {
		case "/repos/acme/widget/issues/42/comments?per_page=100":
			_, _ = w.Write([]byte(`[{"id":101,"html_url":"https://example.invalid/comment/101","body":"please update tests","updated_at":"2026-04-09T10:00:00Z","user":{"login":"reviewer-a"}}]`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s?%s", r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	cmd := runCommand(t, binaryPath, "work-on", "sync", "--run-id", runID)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"HOME="+filepath.Join(cwd, "home"),
		"GH_TOKEN=test-token",
		"GITHUB_API_URL="+server.URL,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("work-on sync native run failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Stored new feedback for run "+runID) {
		t.Fatalf("missing sync output: %q", output)
	}
	if !strings.Contains(string(output), "fake-codex:exec -C "+repoCheckoutPath) {
		t.Fatalf("expected native codex execution output, got %q", output)
	}
}

func TestBinaryPublisherLaneRunsNatively(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()
	originBare := filepath.Join(cwd, "origin.git")
	seedRepo := filepath.Join(cwd, "seed")
	managedRepoRoot := filepath.Join(cwd, "home", ".nana", "repos", "acme", "widget")
	sandboxPath := filepath.Join(managedRepoRoot, "sandboxes", "issue-42")
	repoCheckoutPath := filepath.Join(sandboxPath, "repo")
	runID := "gh-run-publisher-bin"
	if err := os.MkdirAll(seedRepo, 0o755); err != nil {
		t.Fatalf("mkdir seed repo: %v", err)
	}
	gitEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := runCommand(t, "git", args...)
		cmd.Dir = dir
		cmd.Env = gitEnv
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, output)
		}
	}
	runGit(cwd, "init", "--bare", originBare)
	if err := os.WriteFile(filepath.Join(seedRepo, "README.md"), []byte("# widget\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(seedRepo, "init", "-b", "main")
	runGit(seedRepo, "add", ".")
	runGit(seedRepo, "commit", "-m", "init")
	runGit(seedRepo, "remote", "add", "origin", originBare)
	runGit(seedRepo, "push", "-u", "origin", "main")
	if err := os.MkdirAll(filepath.Join(managedRepoRoot, "runs", runID), 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	runGit(cwd, "clone", originBare, repoCheckoutPath)
	if err := os.WriteFile(filepath.Join(repoCheckoutPath, "CHANGELOG.md"), []byte("updated\n"), 0o644); err != nil {
		t.Fatalf("write sandbox file: %v", err)
	}
	manifest := fmt.Sprintf(`{
  "run_id": %q,
  "repo_slug": "acme/widget",
  "repo_owner": "acme",
  "repo_name": "widget",
  "sandbox_id": "issue-42",
  "sandbox_path": %q,
  "sandbox_repo_path": %q,
  "target_kind": "issue",
  "target_number": 42,
  "target_title": "Publish me",
  "target_url": "https://github.com/acme/widget/issues/42",
  "considerations_active": ["qa"],
  "role_layout": "split",
  "default_branch": "main",
  "create_pr_on_complete": true
}`, runID, sandboxPath, repoCheckoutPath)
	if err := os.WriteFile(filepath.Join(managedRepoRoot, "runs", runID, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widget/pulls":
			_, _ = w.Write([]byte(`{"number":77,"html_url":"https://example.invalid/pr/77","head":{"sha":"head-sha"}}`))
		case strings.HasPrefix(r.URL.Path, "/repos/acme/widget/commits/") && strings.HasSuffix(r.URL.Path, "/check-runs"):
			_, _ = w.Write([]byte(`{"check_runs":[]}`))
		case r.URL.Path == "/repos/acme/widget/actions/runs":
			_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
		case strings.HasPrefix(r.URL.Path, "/repos/acme/widget/pulls"):
			_, _ = w.Write([]byte(`[]`))
		default:
			http.Error(w, fmt.Sprintf("unexpected route: %s?%s", r.URL.Path, r.URL.RawQuery), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	cmd := runCommand(t, binaryPath, "work-on", "lane-exec", "--run-id", runID, "--lane", "publisher")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"HOME="+filepath.Join(cwd, "home"),
		"GH_TOKEN=test-token",
		"GITHUB_API_URL="+server.URL,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("publisher native run failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Created draft PR #77") {
		t.Fatalf("missing publisher output: %q", output)
	}
	if !strings.Contains(string(output), "Lane publisher completed via native publication flow.") {
		t.Fatalf("missing completion output: %q", output)
	}
}

func TestBinaryLaneExecRunsNativelyForNonPublisherLane(t *testing.T) {
	binaryPath := buildNanaBinary(t)
	cwd := t.TempDir()
	fakeBin := filepath.Join(cwd, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "codex"), "#!/bin/sh\nprintf 'fake-codex:%s\\n' \"$*\"\n")
	managedRepoRoot := filepath.Join(cwd, "home", ".nana", "repos", "acme", "widget")
	sandboxPath := filepath.Join(managedRepoRoot, "sandboxes", "issue-42")
	repoCheckoutPath := filepath.Join(sandboxPath, "repo")
	runID := "gh-run-lane-bin"
	if err := os.MkdirAll(filepath.Join(managedRepoRoot, "runs", runID), 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.MkdirAll(repoCheckoutPath, 0o755); err != nil {
		t.Fatalf("mkdir repo checkout: %v", err)
	}
	manifest := fmt.Sprintf(`{
  "run_id": %q,
  "repo_slug": "acme/widget",
  "repo_owner": "acme",
  "repo_name": "widget",
  "sandbox_id": "issue-42",
  "sandbox_path": %q,
  "sandbox_repo_path": %q,
  "target_kind": "issue",
  "target_number": 42,
  "consideration_pipeline": [
    {
      "alias": "coder",
      "role": "executor",
      "prompt_roles": ["executor"],
      "activation": "bootstrap",
      "phase": "impl",
      "mode": "execute",
      "owner": "self",
      "blocking": true,
      "purpose": "Implement the requested change."
    }
  ]
}`, runID, sandboxPath, repoCheckoutPath)
	if err := os.WriteFile(filepath.Join(managedRepoRoot, "runs", runID, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	cmd := runCommand(t, binaryPath, "work-on", "lane-exec", "--run-id", runID, "--lane", "coder")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"HOME="+filepath.Join(cwd, "home"),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("lane-exec native run failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Lane coder completed via isolated CODEX_HOME") {
		t.Fatalf("missing lane-exec completion output: %q", output)
	}
	if !strings.Contains(string(output), "fake-codex:exec -C "+repoCheckoutPath) {
		t.Fatalf("expected native codex execution output, got %q", output)
	}
}
