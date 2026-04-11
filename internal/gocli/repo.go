package gocli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const RepoHelp = `nana repo - Repository onboarding and verification-plan inspection

Usage:
  nana repo onboard [--repo <path>]
  nana repo help

Notes:
  - Usually you do not need to run this manually; Nana performs onboarding automatically when workflows such as ` + "`nana work start --task ...`" + ` begin.
  - Run it manually when you want to inspect the detected lint/unit/integration/benchmark split before a long run, after changing Makefile/build scripts, or when a warning suggests the repo should split unit/integration/benchmark targets more cleanly.
`

func Repo(cwd string, args []string) error {
	if len(args) == 0 || isHelpToken(args[0]) {
		fmt.Fprint(os.Stdout, RepoHelp)
		return nil
	}

	switch args[0] {
	case "onboard":
		repoPath := ""
		for index := 1; index < len(args); index++ {
			token := args[index]
			switch {
			case token == "--repo":
				if index+1 >= len(args) {
					return fmt.Errorf("Missing value after --repo.\n%s", RepoHelp)
				}
				repoPath = args[index+1]
				index++
			case strings.HasPrefix(token, "--repo="):
				repoPath = strings.TrimSpace(strings.TrimPrefix(token, "--repo="))
			default:
				return fmt.Errorf("Unknown repo onboard option: %s\n\n%s", token, RepoHelp)
			}
		}
		return repoOnboard(cwd, repoPath)
	default:
		return fmt.Errorf("Unknown repo subcommand: %s\n\n%s", args[0], RepoHelp)
	}
}

func repoOnboard(cwd string, repoPath string) error {
	repoRoot, err := resolveLocalWorkRepoRoot(cwd, repoPath)
	if err != nil {
		return err
	}
	plan := detectGithubVerificationPlan(repoRoot)
	considerations := inferGithubInitialRepoConsiderations(repoRoot, filepath.Base(repoRoot), plan)

	fmt.Fprintf(os.Stdout, "[repo] Onboarding %s\n", repoRoot)
	fmt.Fprintf(
		os.Stdout,
		"[repo] Verification plan: lint=%d compile=%d unit=%d integration=%d benchmark=%d\n",
		len(plan.Lint),
		len(plan.Compile),
		len(plan.Unit),
		len(plan.Integration),
		len(plan.Benchmarks),
	)
	fmt.Fprintf(os.Stdout, "[repo] Suggested considerations: %s\n", joinOrNone(considerations.Considerations))
	printRepoPlanSection("Lint", plan.Lint)
	printRepoPlanSection("Compile", plan.Compile)
	printRepoPlanSection("Unit", plan.Unit)
	printRepoPlanSection("Integration", plan.Integration)
	printRepoPlanSection("Benchmark", plan.Benchmarks)
	if len(plan.Warnings) == 0 {
		fmt.Fprintln(os.Stdout, "[repo] Warnings: (none)")
	} else {
		for _, warning := range plan.Warnings {
			fmt.Fprintf(os.Stdout, "[repo] Warning: %s\n", warning)
		}
	}
	return nil
}

func printRepoPlanSection(label string, commands []string) {
	if len(commands) == 0 {
		fmt.Fprintf(os.Stdout, "[repo] %s: (none)\n", label)
		return
	}
	fmt.Fprintf(os.Stdout, "[repo] %s: %s\n", label, strings.Join(commands, " | "))
}
