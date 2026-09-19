---
name: jejak
description: Use Jejak's local semantic graph to understand a Go repository before implementation, identify relevant symbols and files, estimate blast radius, and choose validation targets. Apply for repository investigation, implementation planning, change-impact analysis, or test scoping when Jejak is available; do not infer cross-repository relationships or use it as a substitute for tests.
---

# Jejak repository intelligence

Use Jejak as evidence for understanding and planning Go changes. Explicit user
instructions take precedence over this workflow.

## Locate the command

- Work from the selected repository root unless `-C <worktree>` selects another
  repository explicitly.
- Keep the selected worktree and data root consistent across commands. Preserve
  any `--data-dir` override or `JEJAK_DATA_DIR`; a missing index in a different
  data root does not mean the repository needs initialization. Put `-C` and
  `--data-dir` before the subcommand.
- Preserve the intended build configuration on graph-producing commands:
  `--tags`, `--goos`, `--goarch`, and `--cgo`, including relevant environment
  settings. These affect the graph fingerprint; do not silently switch to
  host defaults or infer the configuration from a successful `status` check.
- Prefer `jejak` when it is available on `PATH` and confirm it with
  `jejak status`.
- In the Jejak source repository, if no binary is available, substitute
  `go run ./cmd/jejak` for `jejak`. Do not install a global binary merely to
  use this skill.
- Preserve Jejak's offline default. Never add `--download-deps` unless the user
  explicitly authorizes dependency downloads.

## Inspect state first

1. Run `jejak status` for the selected worktree.
2. If state is stale, incomplete, or unhealthy, run `jejak doctor --json` and
   report the diagnostics before relying on graph results.
3. If the repository is not initialized, explain that `init` creates external
   graph storage, installs safe Git lifecycle hooks by default, creates a
   project-local Claude skill file, and adds a Codex discovery symlink. Ask
   before running it.
   Use `jejak init --no-hooks --no-agent-skills` when those integrations were
   not requested and graph initialization is authorized.
4. For another repository, pass `-C <worktree>` to every command. Treat each
   repository graph independently; Jejak v1 does not traverse across them.

## Investigate a task

`context`, `impact`, and `graph` do not edit repository source, but they are
not storage-read-only: they open/register the selected store and run a
freshness check that can build or synchronize the committed graph. Effective
reports also create temporary snapshots. `--committed` only excludes the
working-tree overlay; it neither disables synchronization nor pins an older
indexed commit. If the task forbids these writes, or initializing/synchronizing
the graph is outside its authority, explain the effects and ask before running
reports. Do not use a report to bypass the initialization or mutation boundary.
Use source inspection instead when Jejak is unavailable or its use is not
authorized; an index is not a prerequisite for continuing the user's task.

Run agent-facing reports as JSON when precise extraction is useful. The
examples below omit worktree, data-root, and build flags for brevity; retain
the selected configuration:

```sh
jejak --json context --task "<task>"
jejak --json impact --task "<task>"
```

- The default report uses a command-scoped effective working-tree overlay.
  Add `--committed` when the question is specifically about committed HEAD,
  after the normal freshness check.
- Use `jejak --json impact --working-tree` when the task is to assess the
  actual changed, added, deleted, or renamed code without a textual task.
- Inspect ambiguous results with `jejak graph symbol <name>` or
  `jejak graph file <path>`. When lexical matching remains ambiguous, rerun the
  task report with an exact canonical `--seed` from graph output.
- Read the selected source and graph evidence. Treat relevance and confidence
  as ranking evidence, not runtime certainty. Broaden validation for
  reflection, registration, plugins, and unresolved dynamic calls.
- Check seed status, diagnostics, and truncation flags before drawing
  conclusions. `no-match` is not proof of no impact; `truncated` means seeds,
  traversal, report items, or source excerpts may be omitted. Search source
  and use more specific task terms or a verified `--seed`. Increase the
  relevant limits only when needed for the task; if coverage remains partial,
  state that limitation and broaden source inspection and validation rather
  than repeatedly expanding the graph or declaring the change safe.

Summarize the evidence as:

- selected repository, worktree, source mode, commit, and generation;
- must-read context and supporting symbols;
- high/medium/low or inspect-only implementation candidates;
- direct tests and broader package/dependency validation;
- incomplete, stale, missing-source, ambiguity, no-match, or truncation
  diagnostics and their effect on coverage.

Do not dump a large JSON report when a concise evidence-backed summary is
enough.

## Before and after changes

- Before editing, record existing staged, unstaged, and untracked changes
  with Git status/diff. Preserve them and track which edits belong to this
  task, including when user and task changes share a file.
- Rerun `jejak --json impact --working-tree` to assess the current worktree.
  This report includes all working-tree changes, not only edits from this
  task. Compare it with the pre-edit baseline and task diff; distinguish
  pre-existing changes from task changes. Do not claim the entire report as
  this task's blast radius or expand editing scope to unrelated candidates.
- Run the tests and checks selected by the report; Jejak itself does not run
  tests, generators, formatters, or commits.
- Do not run `sync` as a substitute for analyzing uncommitted edits. Sync the
  committed graph after a commit only when the user requests or authorizes it.

## Mutation boundaries

Do not run `doctor --repair`, non-dry-run `gc`, `rebuild`, `sync`,
`hooks install`, or `hooks uninstall` merely because a read-only command
reported a problem. Explain the proposed mutation and obtain the authority
appropriate to the task. The automatic graph writes described above require
the same scope check. Use `gc --dry-run --json` for a cleanup preview.

Use `jejak help` for command syntax. When working in the Jejak source
repository, consult `README.md` and `CLAUDE.md` for deeper operating and
storage details.
