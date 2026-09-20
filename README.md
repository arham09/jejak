# Jejak

Jejak is a local Go repository-intelligence and blast-radius CLI. It builds a
generation-pinned semantic graph in SQLite so a coding agent can inspect
symbols, understand likely impact, and choose validation targets before
editing code.

## Install

Install with Homebrew from the `arham09/tools` tap:

```sh
brew trust --formula arham09/tools/jejak   # Homebrew 7 and later only
brew tap arham09/tools
brew install jejak
```

Homebrew 7 refuses formulae from a third-party tap until you trust them, so
run `brew trust` first. After the tap, the short name `jejak` resolves to
`arham09/tools/jejak`. The single command `brew install arham09/tools/jejak`
adds the tap and installs in one step instead. Homebrew builds Jejak from
source and downloads Go as a build dependency. Upgrade with
`brew upgrade jejak` and remove with `brew uninstall jejak`.

Install with the Go toolchain instead:

```sh
go install github.com/arham09/jejak/cmd/jejak@latest
```

Run `jejak version` to show the installed version, source revision, Go
toolchain, and platform.

## Quick start

Build the CLI from a source checkout with Go 1.27 or newer:

```sh
go build -o jejak ./cmd/jejak
./jejak init
./jejak status
./jejak graph symbol MyFunction
./jejak graph --format json symbol MyFunction
./jejak graph --format svg symbol MyFunction > graph.svg
./jejak graph --format dot symbol MyFunction | dot -Tsvg -o graph.svg
./jejak impact --task "change MyFunction behavior"
./jejak context --task "change MyFunction behavior"
```

Use `-C <worktree>` to select a repository explicitly and `--data-dir <path>`
to override the external application data root. `init`, `sync`, and graph
dependent commands analyze the selected committed HEAD; inspection commands
also provide a command-scoped working-tree overlay by default. Pass
`--committed` to inspect only the durable committed graph.

Test packages are not indexed by default: `_test.go` files, test symbols, and
test relationships roughly double a graph. Pass `--include-tests` to `init`
and to every later command when validation targets need them; the setting is
part of the graph fingerprint, so changing it rebuilds the graph.

Jejak manages multiple repositories in one data root, with a separate graph
for each repository and independent state for each worktree. It does not join
or traverse graphs across repositories.

## Claude and Codex skill

`jejak init` materializes one canonical project skill at
`.claude/skills/jejak/SKILL.md`. Codex discovers that same skill through the
relative directory symlink `.agents/skills/jejak -> ../../.claude/skills/jejak`;
Jejak keeps one authored and one project-local `SKILL.md` file.

Existing different files, directories, or symlinks are reported as conflicts
and preserved. Repeated initialization is idempotent while the managed paths
still match. Pass `--no-agent-skills` to leave these project paths untouched.

## Dependency and source policy

Indexing is offline by default. Missing Go dependencies produce an actionable
diagnostic; pass `--download-deps` explicitly when the environment permits
downloads. Jejak reads Git objects and temporary external snapshots and does
not modify existing tracked source, run tests, run generators, or commit
changes. The Claude skill file and Codex symlink created by `init` are the
explicit project-local integration described above; all graph state remains
external.

The v1 analyzer supports one Go module and explicit `go.work` workspaces whose
members are contained in the selected repository. The selected build tags,
GOOS, GOARCH, and CGO setting are part of the graph fingerprint.

## Maintenance

```sh
jejak doctor                 # inspect SQLite, graph, Git, and hook health
jejak doctor --repair        # quarantine corrupt storage, then rebuild
jejak gc --dry-run           # preview repository-scoped cleanup
jejak gc                     # collect obsolete generations/cache/temp/log data
```

`doctor --repair` preserves the previous database and SQLite sidecars in a
private timestamped quarantine directory before rebuilding through the normal
validated activation path. A failed repair leaves the quarantine evidence and
never promotes an incomplete graph.

Every successful `init`, `sync`, or `rebuild` replaces the worktree's previous
generation once the new one is active, so a store holds one committed graph
per worktree and running `init` twice does not add a second copy. `gc` removes
what that retention leaves behind: failed or interrupted generations, parse
cache and blob rows that no retained generation references, and aged
temporary/log entries. It then compacts the database file so freed pages
return to the filesystem. `gc` never removes an active generation or one
with an open in-process reader, and it only touches Jejak-owned data below
the selected repository store. Use `--keep-generations N` and
`--older-than 24h` to tune what it keeps.

## Storage and privacy

Data is stored outside the checkout under the platform application-data
directory (`~/.local/share/jejak` on Linux and
`~/Library/Application Support/jejak` on macOS), in
`repos/<repo-id>/graph.db` with private directory/file permissions. A custom
`--data-dir` or `JEJAK_DATA_DIR` is accepted only when it is outside the source
repository. SQLite uses WAL mode; parse cache and temporary snapshots are
rebuildable. A committed graph of a mid-size Go service (about 150 files and
30,000 relationships) takes roughly 15 MB per worktree without test packages
and about three times that with them. Remove a repository store only after
confirming the exact `repos/<repo-id>` path, or use `gc` for scoped cleanup.

Stores written by earlier versions used a larger row layout. The first
command against such a store migrates it: stored graphs are dropped, the file
is compacted, and the next `init` or `sync` rebuilds the graph.

## Graph exports, JSON, and diagnostics

Pass `--format json` to a `graph symbol` or `graph file` query for a versioned,
deterministic graph projection intended for agents and other tools. The JSON
includes the query, repository/worktree/generation provenance, typed nodes,
directed edges, confidence, evidence locations, and historical markers. The
existing `--json` flag is also accepted as the graph JSON compatibility alias.

Pass `--format dot` for deterministic Graphviz DOT from the same projection,
or `--format svg` for a rendered picture:

```sh
jejak graph --format svg symbol MyFunction > graph.svg
jejak graph --format svg --committed file internal/payment.go > graph.svg
jejak graph --format dot symbol MyFunction | dot -Tsvg -o graph.svg
```

`--format svg` renders in process. Jejak embeds the Graphviz layout engine
compiled to WebAssembly, so it needs no installed `dot` command, no CGO, and
no network access. The rendered SVG is deterministic for a given export. Use
`--format dot` instead when you want the text, another layout engine, or an
output format that Jejak does not render, such as PNG or PDF; the `dot`
command is then an optional external renderer that Jejak neither installs nor
invokes.

Graph exports are query-scoped and read-only. Every format follows the default
working-tree overlay and explicit `--committed` source boundaries. Possible,
unresolved, inferred, and historical relationships remain explicitly labelled
rather than implying runtime certainty.

Pass `--json` to `doctor`, `gc`, `impact`, or `context` for a versioned,
machine-readable report. Health and maintenance commands distinguish healthy,
warning, stale, incomplete, missing-commit/dependency, busy/cancelled, and
corrupt-storage conditions. Terminal errors are rendered once by the CLI.

## Supported environments and known limits

The tested v1 matrix is macOS and Linux with Go 1.27.x and Git available on
`PATH`. SQLite is embedded through the pure-Go `modernc.org/sqlite` driver.
Other operating systems may report an unsupported writer-lock primitive until
validated. Static analysis cannot prove reflection, plugin loading, runtime
registration, or arbitrary function-variable dispatch; uncertain relationships
are labelled rather than presented as exact.

Run `jejak help` for the command syntax and `jejak doctor` for the health of
an installation. `CLAUDE.md` describes the package boundaries and the
behavioral contracts that this repository keeps.

## License

Jejak is released under the MIT License. See [LICENSE](LICENSE).
