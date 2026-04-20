package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/dkropachev/nana/internal/gocli"
	"github.com/dkropachev/nana/internal/legacyshim"
	"github.com/dkropachev/nana/internal/version"
)

const helpText = `nana - Multi-agent orchestration for Codex CLI

Start and session:
  nana                         Launch Codex with NANA session instructions
  nana exec ...                Run codex exec with NANA session instructions
  nana resume ...              Run codex resume with NANA session instructions
  nana help [command]          Show top-level or command-specific help
  nana setup                   Install or refresh NANA prompts, skills, and hooks
  nana doctor                  Check installation health
  nana version                 Print the installed NANA version

Recommended in-session workflow:
  nana next                    Show the top item that needs attention now
  $deep-interview "..."        Clarify unclear intent, boundaries, and non-goals
  $ralplan "..."               Approve an implementation plan and tradeoffs
  /skills                      Browse installed skills and helpers inside Codex

Investigate and review:
  nana investigate "..."       Run source-backed investigation with validator enforcement
  nana investigate onboard     Bootstrap dedicated investigate configuration
  nana investigate doctor      Probe MCPs configured for investigate
  nana review <pr-url>         Review an external GitHub PR with persisted findings
  nana review-rules ...        Mine and manage persistent PR review rules

Work automation:
  nana work start --task "..." Run long-running local implementation in a managed sandbox
  nana work start <issue-or-pr-url>
                               Run GitHub-targeted issue or PR implementation
  nana work items ...          Queue, draft, review, and submit inbound work items
  nana work logs --last        Inspect current iteration logs and verification artifacts
  nana work status --last --json
                               Inspect machine-readable run and validation state
  nana work explain --last [--json]
                               Explain effective work policy and run gates
  nana issue ...               GitHub issue-oriented aliases for nana work

Repo automation and scouts:
  nana start                   Run onboarded repo automation and supported scouts
  nana improve [owner/repo]    Generate UX/perf improvement-scout proposals
  nana enhance [owner/repo]    Generate enhancement-scout proposals
  nana ui-scout [owner/repo]   Run ui-scout with preflighted UI audit
  nana repo onboard [--json]   Inspect detected verification split and repo profile
  nana repo drop <owner/repo>  Remove an onboarded repo and its persisted Nana state
  nana repo explain <owner/repo>
                               Explain how nana start treats an onboarded repo
  nana repo scout ...          Manage scout startup policies

Local tools and support:
  nana next                    Print the highest-priority next step and command
  nana status                  Show current local NANA runtime status
  nana verify [--json]         Run the repo-native verification profile
  nana route --explain <prompt>
                               Preview prompt-to-skill routing
  nana usage                   Report token spend across NANA-managed sessions
  nana cancel                  Cancel active NANA runtime modes
  nana config                  Show or update persisted NANA defaults
  nana reasoning               Inspect or configure reasoning defaults
  nana account <subcommand>    Manage account routing
  nana cleanup                 Clean NANA runtime artifacts
  nana artifacts list          List repo-local NANA artifacts and summaries
  nana ask                     Ask through the configured helper surface
  nana agents                  Inspect available role agents
  nana agents-init             Initialize repo agent instructions
  nana reflect | nana explore  Run read-only repo reflection/search helpers
  nana sparkshell              Run noisy shell commands with compact summaries
  nana session                 Search prior local session history
  nana trace                   Report child-agent telemetry and concurrency budget pressure
  nana hooks                   Manage NANA hook integration
  nana hud                     Show or watch the local NANA HUD
  nana uninstall               Remove installed NANA components

More help:
  nana help workflows
  nana help work
  nana help investigate
  nana help repo
  nana help usage
  nana help artifacts
  nana help review
  nana help review-rules
  nana help start
  nana help improve
  nana help enhance
  nana help ui-scout
`

const workflowsHelpText = `nana help workflows - Modes, skills, triggers, and safe entry commands

Use this when:
  nana help workflows           Show this compact discovery index
  /skills                       Browse installed skills inside Codex

Safest CLI entry points:
  nana setup                    Install or refresh prompts, skills, hooks, and AGENTS guidance
  nana doctor                   Verify installation health
  nana help [command]           Show top-level or command-specific help
  nana hud [--watch]            Show local NANA runtime status
  nana explore --prompt "..."   Run a read-only repository lookup
  nana sparkshell <command>     Summarize noisy shell output or bounded verification
  nana next                     Show the next operator action
  nana status                   Show active modes
  nana cancel                   Cancel active runtime modes when stuck

Core modes:
  direct execution              Default when the request is clear and bounded
  deep-interview                Clarify unclear intent, boundaries, and non-goals
  ralplan                       Review plan, tradeoffs, and test shape before execution
  autopilot                     End-to-end autonomous delivery for concrete build tasks
  ultrawork                     Parallel/high-throughput execution when safe and well-scoped

Skill index:
  Planning                      $deep-interview, $plan, $ralplan
  Execution                     $autopilot, $ultrawork, $ecomode, $pipeline
  Investigation and repair      $analyze, $deepsearch, $tdd, $build-fix
  Review and cleanup            $code-review, $security-review, $review, $ai-slop-cleaner
  UI and visual                 $frontend-ui-ux, $visual-verdict, $web-clone
  Utility                       $help, $doctor, $hud, $trace, $skill, $note, $cancel, $nana-setup
  External helpers              $ask-claude, $ask-gemini, $configure-notifications

Common trigger phrases:
  autopilot | build me | I want a
      -> $autopilot
  ultrawork | ulw | parallel
      -> $ultrawork
  analyze | investigate
      -> $analyze
  plan this | plan the | let's plan
      -> $plan
  interview | deep interview | gather requirements | interview me | don't assume | ouroboros
      -> $deep-interview
  ralplan | consensus plan
      -> $ralplan
  ecomode | eco | budget
      -> $ecomode
  cancel | stop | abort
      -> $cancel
  tdd | test first
      -> $tdd
  fix build | type errors
      -> $build-fix
  review code | code review | code-review
      -> $code-review
  security review
      -> $security-review
  web-clone | clone site | clone website | copy webpage
      -> $web-clone

Safe in-session utilities:
  $help                         Guide to the NANA/Codex workflow surface
  $hud                          Explain HUD/statusline state
  $trace                        Show agent flow trace timeline and summary
  $doctor                       Diagnose NANA installation issues
  $skill                        List or manage local skills
  $note                         Save durable notes under .nana/notepad.md

Routing rules:
  - Explicit $skill names run before trigger phrases.
  - Trigger phrases are case-insensitive and can appear anywhere.
  - Prefer nana explore for simple read-only repo lookups.
  - Prefer nana sparkshell for noisy read-only command output or bounded verification.
  - Keep edits, diagnostics, and ambiguous investigations on the normal Codex path.

More:
  nana help
  nana help work
  nana help investigate
  nana help repo
`

func main() {
	args := os.Args[1:]
	invocation := gocli.ResolveCLIInvocation(args)
	if invocation.Command == "help" {
		if handleNestedHelp(args[1:]) {
			return
		}
		fmt.Fprint(os.Stdout, helpText)
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
	case "team", "autoresearch", "research":
		fmt.Fprintf(os.Stderr, "nana: removed command: %s\n", invocation.Command)
		os.Exit(1)
	case "status":
		if err := gocli.Status(mustGetwd()); err != nil {
			exitWithError(err)
		}
		return
	case "verify":
		if err := gocli.Verify(mustGetwd(), args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "route":
		if err := gocli.Route(mustGetwd(), args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "usage":
		if err := gocli.Usage(mustGetwd(), args[1:]); err != nil {
			exitWithError(err)
		}
		return
	case "next":
		if err := gocli.Next(mustGetwd(), args[1:]); err != nil {
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
	case "artifacts":
		if err := gocli.Artifacts(mustGetwd(), args[1:]); err != nil {
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
	case "trace":
		if err := gocli.Trace(mustGetwd(), args[1:]); err != nil {
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
	case "ui-scout":
		if err := gocli.UIScout(mustGetwd(), args[1:]); err != nil {
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
	case "workflows":
		fmt.Fprint(os.Stdout, workflowsHelpText)
		return true
	case "cleanup":
		mustHandleHelp(gocli.Cleanup([]string{"--help"}))
		return true
	case "artifacts":
		mustHandleHelp(gocli.Artifacts(cwd, []string{"help"}))
		return true
	case "config":
		mustHandleHelp(gocli.Config([]string{"--help"}))
		return true
	case "next":
		mustHandleHelp(gocli.Next(cwd, []string{"--help"}))
		return true
	case "verify":
		mustHandleHelp(gocli.Verify(cwd, []string{"--help"}))
		return true
	case "route":
		mustHandleHelp(gocli.Route(cwd, []string{"--help"}))
		return true
	case "usage":
		mustHandleHelp(gocli.Usage(cwd, []string{"--help"}))
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
	case "trace":
		mustHandleHelp(gocli.Trace(cwd, []string{"--help"}))
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
	case "ui-scout":
		mustHandleHelp(gocli.UIScout(cwd, []string{"help"}))
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
