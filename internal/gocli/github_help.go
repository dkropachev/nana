package gocli

import (
	"fmt"
	"os"
)

const IssueHelp = `nana issue - GitHub issue-oriented aliases for the work-on runtime

Usage:
  nana issue implement <github-issue-url> [work-on start flags...]
  nana issue investigate <github-issue-url> [work-on start flags...]
  nana issue sync [work-on sync flags...]
  nana issue help

Behavior:
  - implement routes to: nana work-on start <issue-url> ...
  - investigate fetches issue + repo context, updates managed repo metadata, infers considerations, and stops before implementation
  - sync routes to: nana work-on sync ...
`

const ReviewRulesHelp = `nana review-rules - Persistent repo rules mined from PR review history

Usage:
  nana review-rules scan <owner/repo|github-issue-url|github-pr-url>
  nana review-rules list <owner/repo|github-issue-url|github-pr-url>
  nana review-rules approve <owner/repo|github-issue-url|github-pr-url> <rule-id|all> [more-ids...]
  nana review-rules disable <owner/repo|github-issue-url|github-pr-url> <rule-id|all> [more-ids...]
  nana review-rules enable <owner/repo|github-issue-url|github-pr-url> <rule-id|all> [more-ids...]
  nana review-rules archive <owner/repo|github-issue-url|github-pr-url> <rule-id|all> [more-ids...]
  nana review-rules explain <owner/repo|github-issue-url|github-pr-url> <rule-id>
  nana review-rules config set [--mode <manual|automatic>] [--trusted-reviewers <a,b>|none] [--blocked-reviewers <a,b>|none] [--min-distinct-reviewers <n>]
  nana review-rules config show [owner/repo|github-issue-url|github-pr-url]

Behavior:
  - scan mines PR reviews and review comments into pending repo rule candidates
  - global config controls the default extraction mode
  - global reviewer policy can trust, block, or require multiple distinct reviewers
  - repo-specific mode is configured via: nana work-on defaults set <owner/repo> --review-rules-mode <manual|automatic>
  - repo-specific reviewer policy is configured via: nana work-on defaults set <owner/repo> --review-rules-trusted-reviewers <a,b>|none --review-rules-blocked-reviewers <a,b>|none --review-rules-min-distinct-reviewers <n>
  - approve promotes pending candidates into approved rules
  - disable, enable, and archive manage rule lifecycle without deleting evidence
  - explain prints full rule metadata and evidence
  - approved rules are injected into related work-on role instructions
`

const GithubReviewHelp = `nana review - Review an external GitHub PR with deterministic persistence

Usage:
  nana review <github-pr-url> [--mode automatic|manual] [--reviewer <login|@me>] [--per-item-context shared|isolated]
  nana review followup <github-pr-url> [--allow-open]
  nana review help

Behavior:
  - automatically onboards the repo into ~/.nana/repos/<owner>/<repo> when needed
  - automatically resumes an unfinished review run when the same PR already has one
  - persists accepted, user-dropped, not-real, and pre-existing findings separately
  - manual mode opens an editable markdown review file and loops until no argue items remain
  - followup prints findings that predated the reviewed PR and fails when the PR is still open unless --allow-open is passed
`

const GithubWorkOnHelp = `nana work-on - GitHub-targeted issue/PR implementation helper

Usage:
  nana work-on start <github-issue-or-pr-url> [--considerations <list>] [--role-layout <split|reviewer+executor>] [--new-pr] [--create-pr | --local-only] [--reviewer <login|@me>] [codex-args...]
  nana work-on sync [--run-id <id> | --last] [--reviewer <login|@me>] [--resume-last] [codex-args...]
  nana work-on defaults set <owner/repo> [--considerations <list>] [--role-layout <split|reviewer+executor>] [--review-rules-mode <manual|automatic>]
  nana work-on defaults show <owner/repo>
  nana work-on stats <github-issue-or-pr-url>
  nana work-on retrospective [--run-id <id> | --last]
  nana work-on explain [--run-id <id> | --last] [--json]
  nana work-on help

Examples:
  nana work-on start https://github.com/dkropachev/alternator-client-java/issues/1
  nana work-on start https://github.com/dkropachev/alternator-client-java/issues/1 --considerations arch,perf,api,style,qa
  nana work-on start https://github.com/dkropachev/alternator-client-java/issues/1 --considerations security,api --role-layout reviewer+executor
  nana work-on start https://github.com/dkropachev/alternator-client-java/issues/1 --new-pr --create-pr
  nana work-on start https://github.com/openai/codex/pull/456 --reviewer @me -- --model gpt-5.4
  nana work-on defaults set dkropachev/alternator-client-java --considerations arch,perf,api --role-layout split
  nana work-on stats https://github.com/dkropachev/alternator-client-java/issues/1
  nana work-on retrospective --last
  nana work-on explain --last --json
  nana work-on sync --last --resume-last

Storage:
  - managed repo state: ~/.nana/repos/<owner>/<repo-name>
  - managed sandboxes: ~/.nana/repos/<owner>/<repo-name>/sandboxes/issue-<n> or pr-<n>
  - repo concern overrides: .nana/work-on-concerns.json or .github/nana-work-on-concerns.json
  - repo hot-path API overrides: .nana/work-on-hot-path-apis.json or .github/nana-work-on-hot-path-apis.json
  - repo policy overrides: .nana/work-on-policy.json or .github/nana-work-on-policy.json
  - repo profile: ~/.nana/repos/<owner>/<repo-name>/repo-profile.json

Override shapes:
  - concerns: {"version":1,"lanes":{"security-reviewer":{"pathPrefixes":["policies/"]}}}
  - hot-path apis: {"version":1,"hot_path_api_files":["docs/openapi/search.yaml"],"api_identifier_tokens":["searchDocuments"]}
  - policy: {"version":1,"experimental":true,"allowed_actions":{"commit":true,"push":true,"open_draft_pr":true,"request_review":true,"merge":false},"feedback_source":"assigned_trusted","human_gate":"publish_time","merge_method":"squash"}

Policy notes:
  - merge automation is experimental and requires allowed_actions.merge, local verification, green GitHub CI, and control-plane approval.
  - any_human feedback mode excludes bots, target authors, and blocked reviewers.

Auth:
  Uses GH_TOKEN / GITHUB_TOKEN when set, otherwise falls back to ` + "`gh auth token`" + `.
`

func MaybeHandleGithubHelp(command string, args []string) bool {
	switch command {
	case "implement", "sync", "issue":
		if wantsIssueHelp(command, args) {
			fmt.Fprint(os.Stdout, IssueHelp)
			return true
		}
	case "review":
		if wantsSubcommandHelp(args) {
			fmt.Fprint(os.Stdout, GithubReviewHelp)
			return true
		}
	case "review-rules":
		if wantsSubcommandHelp(args) {
			fmt.Fprint(os.Stdout, ReviewRulesHelp)
			return true
		}
	case "work-on":
		if wantsSubcommandHelp(args) {
			fmt.Fprint(os.Stdout, GithubWorkOnHelp)
			return true
		}
	}
	return false
}

func wantsIssueHelp(command string, args []string) bool {
	if command == "implement" || command == "sync" {
		return len(args) > 1 && isHelpToken(args[1])
	}
	if command == "issue" {
		if len(args) < 2 {
			return true
		}
		if isHelpToken(args[1]) {
			return true
		}
		return len(args) > 2 && isHelpToken(args[2])
	}
	return false
}

func wantsSubcommandHelp(args []string) bool {
	if len(args) < 2 {
		return false
	}
	return isHelpToken(args[1]) || (len(args) > 2 && isHelpToken(args[2]))
}

func isHelpToken(value string) bool {
	return value == "--help" || value == "-h" || value == "help"
}
