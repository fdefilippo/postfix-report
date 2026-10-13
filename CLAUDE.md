# Project Instructions for AI Agents

This file provides instructions and context for AI coding agents working on this project.

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:1105d646 -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/core-concepts/sync-concepts.md for details and anti-patterns.

## Agent Context Profiles

The managed Beads block is task-tracking guidance, not permission to override repository, user, or orchestrator instructions.

- **Conservative (default)**: Use `bd` for task tracking. Do not run git commits, git pushes, or Dolt remote sync unless explicitly asked. At handoff, report changed files, validation, and suggested next commands.
- **Minimal**: Keep tool instruction files as pointers to `bd prime`; use the same conservative git policy unless active instructions say otherwise.
- **Team-maintainer**: Only when the repository explicitly opts in, agents may close beads, run quality gates, commit, and push as part of session close. A current "do not commit" or "do not push" instruction still wins.

## Session Completion

This protocol applies when ending a Beads implementation workflow. It is subordinate to explicit user, repository, and orchestrator instructions.

1. **File issues for remaining work** - Create beads for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **Handle git/sync by active profile**:
   ```bash
   # Conservative/minimal/default: report status and proposed commands; wait for approval.
   git status

   # Team-maintainer opt-in only, unless current instructions forbid it:
   git pull --rebase
   git push
   git status
   ```
5. **Hand off** - Summarize changes, validation, issue status, and any blocked sync/commit/push step

**Critical rules:**
- Explicit user or orchestrator instructions override this Beads block.
- Do not commit or push without clear authority from the active profile or the current user request.
- If a required sync or push is blocked, stop and report the exact command and error.
<!-- END BEADS INTEGRATION -->


## Build & Test

```bash
make build          # static binary in bin/postfix-report (CGO_ENABLED=0)
make check          # gofmt check, go vet, full test suite
make test-short     # skips real-log regression and headless-browser tests
make bench          # parser and pipeline benchmarks
make validate       # independent recount vs program on samples/maillog or ./maillog
make demo           # regenerate demo/ from synthetic data (cmd/synthlog)
make rpm            # dist/rpmbuild RPM+SRPM from packaging/rpm/postfix-report.spec (EL9/EL10, binary in /usr/bin)
```

Set `GOTMPDIR`/`TMPDIR` under `~/tmp` when running go commands (user preference: never /tmp).

## Architecture Overview

Pipeline: `internal/logparse` (syntax only) → `internal/ingest` (sources by content
fingerprint, checkpoints, dedup, coverage segments) → `internal/correlate` (queue-instance
lifecycles, reinjection linking, recipients, deliveries, rejects) → `internal/aggregate`
(agg_hour/agg_dim per local day, always recomputed per whole day) → `internal/query` →
`internal/report` (static HTML, embedded assets, atomic writes, hash-based skip).
`internal/app` sequences stages and invalidation; `internal/verify` checks invariants.
Metric definitions: METRICS.md. Observed log formats: LOG_ANALYSIS.md.

## Conventions & Patterns

- The real `maillog` contains personal data: never commit it, nor reports/DBs built from it
  (`reports-real/` is git-ignored). Test fixtures must be synthetic (example.org, RFC 5737 IPs).
- Every Go file starts with the GPL-3.0-or-later SPDX header (author Francesco Defilippo).
- Missing data is never shown as zero; every count states its unit.
- Any change to metric semantics must update METRICS.md and keep `make validate` at zero differences.
