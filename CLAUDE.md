# Working on Jejak

This file is the shared repository guide for Claude Code and Codex.
`AGENTS.md` is a relative symlink to `CLAUDE.md`; maintain instructions here.

## Project context

Jejak is a Go CLI for repository intelligence and change-impact analysis. It
stores semantic graphs in SQLite, inspects symbols and relationships, and
produces context, implementation, and validation recommendations for agents.
The module is `github.com/arham09/jejak`; `go.mod` requires Go 1.27.0.

Start with `README.md` for commands and with the package ownership and
behavioral contract sections below for boundaries and invariants. The `docs/`
directory holds local architecture notes, phase plans, and completion
evidence. Git does not track `docs/`, so a fresh clone does not contain it.
Read it when it is present, but verify every claim against the current code
and tests.

Before editing, inspect the current Git diff and the relevant implementation
and tests. Preserve existing work, including untracked source files. Keep
changes scoped to the request and update affected documentation when behavior
changes. Historical phase notes provide context; verify claims against current
code and tests.

## Package ownership

- `cmd/jejak` and `internal/cli`: entry point, argument parsing, dependency
  wiring, command execution, and output.
- `internal/repository`, `internal/config`, and `internal/git`: repository and
  worktree identity, external storage paths, and Git object/snapshot access.
- `internal/graph`: domain records, validation, generations, and build/sync
  orchestration. It owns the analyzer and store interfaces it consumes.
- `internal/analyzer/golang`: Go package loading and semantic extraction.
- `internal/graphdb`: SQLite schema, transactions, queries, caches, and
  maintenance. Migrations live in `internal/graphdb/migrations/`.
- `internal/impact`, `internal/brief`, and `internal/overlay`: impact analysis,
  bounded source context, and temporary working-tree graphs.
- `internal/hooks` and `internal/agentskill`: Git lifecycle and agent discovery
  integrations.

Keep Go AST/type objects in the analyzer and SQL implementation details in
`graphdb`. Prefer concrete constructors and narrow interfaces owned by their
consumers. Follow existing Go conventions, wrap errors with context, close
owned resources, and preserve cancellation and transaction boundaries.

## Behavioral contracts

- Scope every graph operation to a repository and worktree. Multiple
  repositories have separate graphs; cross-repository traversal is outside v1.
- Support single Go modules and explicit `go.work` workspaces with members
  inside the selected repository. Preserve offline indexing by default;
  dependency downloads require explicit opt-in.
- Build committed graphs from immutable Git objects. Working-tree overlays are
  temporary and must never publish dirty source as a committed generation.
- Validate candidate generations before atomic activation. Failed analysis
  retains the previous valid generation. Queries must respect generation and
  freshness boundaries.
- Keep SQLite databases, caches, and snapshots outside the target checkout.
  Indexing does not execute application code, tests, or generators.
- Preserve user-owned hook and skill paths on conflicts. The embedded skill
  source is `internal/agentskill/jejak/SKILL.md`. Target-project installation
  uses one physical `.claude/skills/jejak/SKILL.md` and the relative symlink
  `.agents/skills/jejak -> ../../.claude/skills/jejak`.
- Keep output deterministic and uncertainty explicit. Static-analysis
  confidence and relevance scores do not establish runtime certainty.

## Development and validation

Use Git and the Go toolchain from `PATH`. Run focused package tests while
developing and select broader checks according to the affected behavior.
The CI checks include:

```sh
go test ./...
go test -race ./...
go vet ./...
staticcheck ./...
go build ./cmd/jejak
go test ./tests/benchmarks -run '^$' -bench . -benchmem
```

CI pins Staticcheck v0.8.1. Format changed Go files with `gofmt` and check
`git diff --check`. Documentation-only edits need link/content verification,
not a full Go test run. Report which checks actually ran and any failures.

Use disposable repositories from `internal/testrepo` for filesystem, Git,
hook, and storage tests. Tests of explicit synchronization should initialize
with `--no-hooks` so an installed `jejak` binary cannot synchronize through a
commit hook first. Use `--no-agent-skills` when a fixture requires a clean
checkout or measures source-only change ratios. Real-repository acceptance
uses `remittance-service` when available; do not hardcode its local path into
production code or tests.

## Using Jejak during development

When an index is available, use `jejak status` and task-scoped `context` or
`impact` reports to orient, then read the relevant source. Keep `-C` and
`--data-dir` consistent across commands. Inspection uses a working-tree overlay
by default; `--committed` selects the durable graph. When `docs/` is present
locally, `docs/agent-workflow.md` gives the full workflow.

Initialization creates storage and integrations. For graph-only setup use
`init --no-hooks --no-agent-skills` within the authorized task. Keep repair,
cleanup, installation, and real-repository mutations within the user's scope.
An unavailable index does not prevent source-based investigation.
