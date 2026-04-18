package gocli

const TopLevelHelp = `nana - Multi-agent orchestration for Codex CLI

Start and session:
  nana                         Launch Codex with NANA session instructions
  nana exec ...                Run codex exec with NANA session instructions
  nana resume ...              Run codex resume with NANA session instructions
  nana help [command]          Show top-level or command-specific help
  nana setup                   Install or refresh NANA prompts, skills, and hooks
  nana doctor                  Check installation health
  nana version                 Print the installed NANA version

Recommended in-session workflow:
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
  nana repo onboard [--json]   Inspect detected verification split and repo profile
  nana repo explain <owner/repo>
                               Explain how nana start treats an onboarded repo
  nana repo scout ...          Manage scout startup policies

Local tools and support:
  nana status                  Show current local NANA runtime status
  nana cancel                  Cancel active NANA runtime modes
  nana config                  Show or update persisted NANA defaults
  nana reasoning               Inspect or configure reasoning defaults
  nana account <subcommand>    Manage account routing
  nana cleanup                 Clean NANA runtime artifacts
  nana ask                     Ask through the configured helper surface
  nana agents                  Inspect available role agents
  nana agents-init             Initialize repo agent instructions
  nana reflect | nana explore  Run read-only repo reflection/search helpers
  nana sparkshell              Run noisy shell commands with compact summaries
  nana session                 Search prior local session history
  nana hooks                   Manage NANA hook integration
  nana hud                     Show or watch the local NANA HUD
  nana uninstall               Remove installed NANA components

More help:
  nana help work
  nana help issue
  nana help investigate
  nana help repo
  nana help review
  nana help review-rules
  nana help start
  nana help improve
  nana help enhance
`
