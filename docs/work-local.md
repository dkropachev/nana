# Work-local Migration

`nana work-local` is no longer a supported user-facing command.

Use `nana work` instead:

```bash
nana work start --task "execute the approved local refactor plan" --work-type refactor
nana work resume --repo ~/src/widget --last
nana work status --repo ~/src/widget --last --json
nana work logs --repo ~/src/widget --last --json
nana work retrospective --global-last
```

The merged runtime now stores authoritative state in `~/.nana/work/state.db`.

See [docs/work.md](./work.md) for:

- the canonical `nana work` command surface
- storage and runtime layout
- validation grouping controls such as `--grouping-policy` and `--validation-parallelism`
- resume, troubleshooting, and GitHub-backed run behavior
