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

func main() {
	args := os.Args[1:]
	invocation := gocli.ResolveCLIInvocation(args)
	if invocation.Command == "help" {
		if handleNestedHelp(args[1:]) {
			return
		}
		fmt.Fprint(os.Stdout, gocli.TopLevelHelp)
		return
	}
	if gocli.MaybeHandleWorkHelp(invocation.Command, args) {
		return
	}
	if gocli.MaybeHandleGithubHelp(invocation.Command, args) {
		return
	}
	if gocli.MaybeHandleInvestigateHelp(invocation.Command, args) {
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
	case "config":
		if err := gocli.Config(args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "account":
		if err := gocli.Account(args[1:]); err != nil {
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
	case "repo":
		if err := gocli.Repo(mustGetwd(), args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "investigate":
		if err := gocli.Investigate(mustGetwd(), args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "start":
		if err := gocli.Start(mustGetwd(), args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "improve":
		if err := gocli.Improve(mustGetwd(), args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "enhance":
		if err := gocli.Enhance(mustGetwd(), args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "work":
		if err := gocli.Work(mustGetwd(), args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "work-on", "work-local":
		exitWithError(gocli.WorkLegacyCommandError(invocation.Command))
		return
	case "review-rules":
		if err := gocli.GithubReviewRules(mustGetwd(), args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "implement", "sync", "issue":
		if _, err := gocli.GithubIssue(mustGetwd(), args); err != nil {
			exitWithError(err)
		}
		return
	case "review":
		if _, err := gocli.GithubReview(mustGetwd(), args[1:]); err != nil {
			exitWithError(err)
		}
		return
	}

	fmt.Fprintf(os.Stderr, "nana: unknown command: %s\n", invocation.Command)
	os.Exit(1)
}

func handleNestedHelp(args []string) bool {
	if len(args) == 0 {
		return false
	}

	command := args[0]
	if command == "investigate" {
		fmt.Fprint(os.Stdout, gocli.InvestigateHelp)
		return true
	}
	if gocli.MaybeHandleWorkHelp(command, []string{command, "help"}) {
		return true
	}
	if gocli.MaybeHandleGithubHelp(command, []string{command, "help"}) {
		return true
	}

	cwd := mustGetwd()
	repoRoot := legacyshim.ResolveRepoRoot(os.Getenv(legacyshim.RepoRootEnv), os.Args[0])

	switch command {
	case "cleanup":
		mustHandleHelp(gocli.Cleanup([]string{"--help"}))
		return true
	case "config":
		mustHandleHelp(gocli.Config([]string{"--help"}))
		return true
	case "ask":
		mustHandleHelp(gocli.Ask(repoRoot, cwd, []string{"--help"}))
		return true
	case "agents":
		mustHandleHelp(gocli.Agents(cwd, []string{"--help"}))
		return true
	case "agents-init", "deepinit":
		mustHandleHelp(gocli.AgentsInit(repoRoot, cwd, []string{"--help"}))
		return true
	case "uninstall":
		mustHandleHelp(gocli.Uninstall(repoRoot, cwd, []string{"--help"}))
		return true
	case "setup":
		if err := gocli.Setup(repoRoot, cwd, []string{"--help"}); err != nil {
			if err.Error() == "help requested" {
				fmt.Fprintln(os.Stdout, "Usage: nana setup [--scope user|project] [--dry-run] [--force] [--verbose]")
				return true
			}
			exitWithError(err)
		}
		return true
	case "reflect", "explore":
		mustHandleHelp(gocli.Reflect(repoRoot, cwd, []string{"--help"}))
		return true
	case "sparkshell":
		mustHandleHelp(gocli.SparkShell(repoRoot, cwd, []string{"--help"}))
		return true
	case "session":
		mustHandleHelp(gocli.Session([]string{"--help"}))
		return true
	case "hooks":
		mustHandleHelp(gocli.Hooks(cwd, repoRoot, []string{"help"}))
		return true
	case "hud":
		mustHandleHelp(gocli.HUD(cwd, os.Args[0], []string{"--help"}))
		return true
	case "repo":
		mustHandleHelp(gocli.Repo(cwd, []string{"help"}))
		return true
	case "start":
		mustHandleHelp(gocli.Start(cwd, []string{"help"}))
		return true
	case "improve":
		mustHandleHelp(gocli.Improve(cwd, []string{"help"}))
		return true
	case "enhance":
		mustHandleHelp(gocli.Enhance(cwd, []string{"help"}))
		return true
	case "work":
		mustHandleHelp(gocli.Work(cwd, []string{"help"}))
		return true
	case "work-local":
		fmt.Fprintf(os.Stdout, "nana work-local has been replaced by `nana work`.\n\n")
		mustHandleHelp(gocli.Work(cwd, []string{"help"}))
		return true
	case "work-on":
		fmt.Fprintf(os.Stdout, "nana work-on has been replaced by `nana work`.\n\n")
		mustHandleHelp(gocli.Work(cwd, []string{"help"}))
		return true
	}

	return false
}

func mustHandleHelp(err error) {
	if err != nil {
		exitWithError(err)
	}
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
