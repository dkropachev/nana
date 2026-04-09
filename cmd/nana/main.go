package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/Yeachan-Heo/nana/internal/gocli"
	"github.com/Yeachan-Heo/nana/internal/legacyshim"
	"github.com/Yeachan-Heo/nana/internal/version"
)

const helpText = `nana - Multi-agent orchestration for Codex CLI

Go-native commands:
  nana                Launch Codex with NANA session instructions
  nana exec ...       Run codex exec with NANA session instructions
  nana resume ...     Run codex resume with NANA session instructions
  nana help
  nana version
  nana status
  nana cancel
  nana reasoning
  nana auth pull
  nana cleanup
  nana ask
  nana agents
  nana agents-init
  nana uninstall
  nana setup
  nana reflect
  nana sparkshell
  nana session
  nana hooks
  nana doctor
`

func main() {
	args := os.Args[1:]
	invocation := gocli.ResolveCLIInvocation(args)
	if invocation.Command == "help" {
		fmt.Fprint(os.Stdout, helpText)
		return
	}
	if gocli.MaybeHandleGithubHelp(invocation.Command, args) {
		return
	}
	if invocation.Command == "version" {
		version.Print(os.Stdout)
		return
	}
	switch invocation.Command {
	case "launch":
		if err := gocli.Launch(mustGetwd(), invocation.LaunchArgs); err != nil {
			exitWithError(err)
		}
		return
	case "resume":
		if err := gocli.Resume(mustGetwd(), invocation.LaunchArgs); err != nil {
			exitWithError(err)
		}
		return
	case "exec":
		if err := gocli.Exec(mustGetwd(), invocation.LaunchArgs); err != nil {
			exitWithError(err)
		}
		return
	case "team", "ralph", "autoresearch", "research":
		fmt.Fprintf(os.Stderr, "nana: removed command: %s\n", invocation.Command)
		os.Exit(1)
	case "status":
		if err := gocli.Status(mustGetwd()); err != nil {
			exitWithError(err)
		}
		return
	case "cancel":
		if err := gocli.Cancel(mustGetwd()); err != nil {
			exitWithError(err)
		}
		return
	case "reasoning":
		if err := gocli.Reasoning(args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "auth":
		if len(args) < 2 || args[1] != "pull" {
			fmt.Fprintln(os.Stdout, "Usage: nana auth pull")
			return
		}
		if err := gocli.AuthPull(); err != nil {
			exitWithError(err)
		}
		return
	case "cleanup":
		if err := gocli.Cleanup(args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "ask":
		repoRoot := legacyshim.ResolveRepoRoot(os.Getenv(legacyshim.RepoRootEnv), os.Args[0])
		if err := gocli.Ask(repoRoot, mustGetwd(), args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "agents-init", "deepinit":
		repoRoot := legacyshim.ResolveRepoRoot(os.Getenv(legacyshim.RepoRootEnv), os.Args[0])
		if err := gocli.AgentsInit(repoRoot, mustGetwd(), args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "agents":
		if err := gocli.Agents(mustGetwd(), args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "uninstall":
		repoRoot := legacyshim.ResolveRepoRoot(os.Getenv(legacyshim.RepoRootEnv), os.Args[0])
		if err := gocli.Uninstall(repoRoot, mustGetwd(), args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "setup":
		repoRoot := legacyshim.ResolveRepoRoot(os.Getenv(legacyshim.RepoRootEnv), os.Args[0])
		if err := gocli.Setup(repoRoot, mustGetwd(), args[1:]); err != nil {
			if err.Error() == "help requested" {
				fmt.Fprintln(os.Stdout, "Usage: nana setup [--scope user|project] [--dry-run] [--force] [--verbose]")
				return
			}
			exitWithError(err)
		}
		return
	case "reflect", "explore":
		repoRoot := legacyshim.ResolveRepoRoot(os.Getenv(legacyshim.RepoRootEnv), os.Args[0])
		if repoRoot == "" {
			fmt.Fprintln(os.Stderr, "nana: repo root not found for reflect")
			os.Exit(1)
		}
		if err := gocli.Reflect(repoRoot, mustGetwd(), args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "sparkshell":
		repoRoot := legacyshim.ResolveRepoRoot(os.Getenv(legacyshim.RepoRootEnv), os.Args[0])
		if repoRoot == "" {
			fmt.Fprintln(os.Stderr, "nana: repo root not found for sparkshell")
			os.Exit(1)
		}
		if err := gocli.SparkShell(repoRoot, mustGetwd(), args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "session":
		if err := gocli.Session(args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "hooks":
		repoRoot := legacyshim.ResolveRepoRoot(os.Getenv(legacyshim.RepoRootEnv), os.Args[0])
		if err := gocli.Hooks(mustGetwd(), repoRoot, args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "hud":
		if err := gocli.HUD(mustGetwd(), os.Args[0], args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "doctor":
		if len(args) > 1 && args[1] == "--team" {
			hasFailures, err := gocli.DoctorTeam(mustGetwd())
			if err != nil {
				exitWithError(err)
			}
			if hasFailures {
				os.Exit(1)
			}
			return
		}
		repoRoot := legacyshim.ResolveRepoRoot(os.Getenv(legacyshim.RepoRootEnv), os.Args[0])
		if err := gocli.Doctor(mustGetwd(), repoRoot); err != nil {
			exitWithError(err)
		}
		return
	case "work-on":
		if len(args) > 1 && (args[1] == "defaults" || args[1] == "stats" || args[1] == "retrospective") {
			if err := gocli.GithubWorkOn(mustGetwd(), args[1:]); err != nil {
				exitWithError(err)
			}
			return
		}
		repoRoot := legacyshim.ResolveRepoRoot(os.Getenv(legacyshim.RepoRootEnv), os.Args[0])
		if err := runLegacyJSBridge(repoRoot, args); err != nil {
			exitWithError(err)
		}
		return
	case "review-rules":
		if len(args) > 1 && (args[1] == "config" || args[1] == "scan" || args[1] == "list" || args[1] == "approve" || args[1] == "disable" || args[1] == "enable" || args[1] == "archive" || args[1] == "explain") {
			if err := gocli.GithubReviewRules(mustGetwd(), args[1:]); err != nil {
				exitWithError(err)
			}
			return
		}
		repoRoot := legacyshim.ResolveRepoRoot(os.Getenv(legacyshim.RepoRootEnv), os.Args[0])
		if err := runLegacyJSBridge(repoRoot, args); err != nil {
			exitWithError(err)
		}
		return
	case "implement", "investigate", "sync", "issue":
		repoRoot := legacyshim.ResolveRepoRoot(os.Getenv(legacyshim.RepoRootEnv), os.Args[0])
		result, err := gocli.GithubIssue(mustGetwd(), args)
		if err != nil {
			exitWithError(err)
		}
		if result.Handled {
			return
		}
		if err := runLegacyJSBridge(repoRoot, result.LegacyArgs); err != nil {
			exitWithError(err)
		}
		return
	case "review":
		repoRoot := legacyshim.ResolveRepoRoot(os.Getenv(legacyshim.RepoRootEnv), os.Args[0])
		result, err := gocli.GithubReview(mustGetwd(), args[1:])
		if err != nil {
			exitWithError(err)
		}
		if result.Handled {
			return
		}
		if err := runLegacyJSBridge(repoRoot, result.LegacyArgs); err != nil {
			exitWithError(err)
		}
		return
	}

	fmt.Fprintf(os.Stderr, "nana: unknown command: %s\n", invocation.Command)
	os.Exit(1)
}

func mustGetwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nana: %v\n", err)
		os.Exit(1)
	}
	return cwd
}

func exitWithError(err error) {
	if err == nil {
		return
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.ExitCode())
	}
	fmt.Fprintf(os.Stderr, "nana: %v\n", err)
	os.Exit(1)
}

func runLegacyJSBridge(repoRoot string, args []string) error {
	if repoRoot == "" {
		return fmt.Errorf("GitHub/work-on commands are not yet available in standalone Go binaries; use a checkout with built dist assets or the npm wrapper")
	}
	if err := legacyshim.Run(repoRoot, args); err == nil {
		return nil
	} else if exitErr := (*exec.ExitError)(nil); errors.As(err, &exitErr) {
		return exitErr
	} else if errors.Is(err, legacyshim.ErrLegacyEntrypointUnavailable) {
		return fmt.Errorf("GitHub/work-on commands still require the legacy JS entrypoint. Run \"npm run build\" in this checkout or use the npm wrapper")
	} else {
		return err
	}
}
