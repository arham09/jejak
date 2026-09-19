package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

var errUsage = errors.New("invalid command usage")

// Run executes one Jejak CLI invocation and returns a process-style exit
// code. It renders each runtime error once on stderr; main owns os.Exit.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	app := New(stdout, stderr)
	options, command, commandArgs, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(app.stderr, "jejak: %v\n\n%s", err, usage())
		return 2
	}
	options, commandArgs, err = parseCommandOptions(options, commandArgs)
	if err != nil {
		fmt.Fprintf(app.stderr, "jejak: %v\n\n%s", err, usage())
		return 2
	}
	if command == "" || command == "help" {
		fmt.Fprint(app.stdout, usage())
		return 0
	}

	if err := app.run(ctx, options, command, commandArgs); err != nil {
		fmt.Fprintf(app.stderr, "jejak: %v\n", err)
		if errors.Is(err, errUsage) {
			fmt.Fprintf(app.stderr, "\n%s", usage())
			return 2
		}
		return 1
	}
	return 0
}

// parseCommandOptions accepts the build-selection flags after a command as a
// convenience while preserving positional command arguments verbatim.
func parseCommandOptions(options Options, args []string) (Options, []string, error) {
	remaining := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--download-deps" || arg == "--download-dependencies":
			options.DownloadDependencies = true
		case arg == "--quiet" || arg == "-q":
			options.Quiet = true
		case arg == "--no-hooks":
			options.NoHooks = true
		case arg == "--no-agent-skills":
			options.NoAgentSkills = true
		case arg == "--json":
			options.JSON = true
		case arg == "--committed":
			options.Committed = true
		case arg == "--tags" || arg == "--goos" || arg == "--goarch" || arg == "--cgo" || arg == "--cgo-enabled":
			if index+1 >= len(args) || args[index+1] == "" {
				return Options{}, nil, fmt.Errorf("%w: %s requires a value", errUsage, arg)
			}
			value := args[index+1]
			index++
			switch arg {
			case "--tags":
				options.Tags = append(options.Tags, strings.Split(value, ",")...)
			case "--goos":
				options.GOOS = value
			case "--goarch":
				options.GOARCH = value
			default:
				options.CGOEnabled = value
			}
		case strings.HasPrefix(arg, "--tags="):
			value := strings.TrimPrefix(arg, "--tags=")
			if value == "" {
				return Options{}, nil, fmt.Errorf("%w: --tags requires a comma-separated value", errUsage)
			}
			options.Tags = append(options.Tags, strings.Split(value, ",")...)
		case strings.HasPrefix(arg, "--goos="):
			value := strings.TrimPrefix(arg, "--goos=")
			if value == "" {
				return Options{}, nil, fmt.Errorf("%w: --goos requires a value", errUsage)
			}
			options.GOOS = value
		case strings.HasPrefix(arg, "--goarch="):
			value := strings.TrimPrefix(arg, "--goarch=")
			if value == "" {
				return Options{}, nil, fmt.Errorf("%w: --goarch requires a value", errUsage)
			}
			options.GOARCH = value
		case strings.HasPrefix(arg, "--cgo="):
			value := strings.TrimPrefix(arg, "--cgo=")
			if value == "" {
				return Options{}, nil, fmt.Errorf("%w: --cgo requires 0 or 1", errUsage)
			}
			options.CGOEnabled = value
		case strings.HasPrefix(arg, "--cgo-enabled="):
			value := strings.TrimPrefix(arg, "--cgo-enabled=")
			if value == "" {
				return Options{}, nil, fmt.Errorf("%w: --cgo-enabled requires 0 or 1", errUsage)
			}
			options.CGOEnabled = value
		default:
			remaining = append(remaining, arg)
		}
	}
	return options, remaining, nil
}

func (a *App) run(ctx context.Context, options Options, command string, args []string) error {
	switch command {
	case "init":
		return a.init(ctx, options, args)
	case "status":
		return a.status(ctx, options, args)
	case "repos":
		return a.repos(ctx, options, args)
	case "graph":
		return a.graph(ctx, options, args)
	case "sync":
		return a.sync(ctx, options, args)
	case "rebuild":
		return a.rebuild(ctx, options, args)
	case "hooks":
		return a.hooks(ctx, options, args)
	case "impact":
		return a.impact(ctx, options, args)
	case "context":
		return a.context(ctx, options, args)
	case "doctor":
		return a.doctor(ctx, options, args)
	case "gc":
		return a.gc(ctx, options, args)
	case "version":
		return a.version(args)
	default:
		return fmt.Errorf("%w: unknown command %q", errUsage, command)
	}
}

func parseArgs(args []string) (Options, string, []string, error) {
	var options Options
	for len(args) > 0 {
		arg := args[0]
		switch {
		case arg == "-h" || arg == "--help":
			return options, "help", nil, nil
		case arg == "-V" || arg == "--version":
			return options, "version", nil, nil
		case arg == "-C" || arg == "--worktree":
			if len(args) < 2 || args[1] == "" {
				return Options{}, "", nil, fmt.Errorf("%w: %s requires a path", errUsage, arg)
			}
			options.WorktreePath = args[1]
			args = args[2:]
			continue
		case strings.HasPrefix(arg, "-C="):
			if len(arg) == len("-C=") {
				return Options{}, "", nil, fmt.Errorf("%w: -C requires a path", errUsage)
			}
			options.WorktreePath = strings.TrimPrefix(arg, "-C=")
		case strings.HasPrefix(arg, "--worktree="):
			if len(arg) == len("--worktree=") {
				return Options{}, "", nil, fmt.Errorf("%w: --worktree requires a path", errUsage)
			}
			options.WorktreePath = strings.TrimPrefix(arg, "--worktree=")
		case arg == "--data-dir":
			if len(args) < 2 || args[1] == "" {
				return Options{}, "", nil, fmt.Errorf("%w: --data-dir requires a path", errUsage)
			}
			options.DataDir = args[1]
			args = args[2:]
			continue
		case strings.HasPrefix(arg, "--data-dir="):
			if len(arg) == len("--data-dir=") {
				return Options{}, "", nil, fmt.Errorf("%w: --data-dir requires a path", errUsage)
			}
			options.DataDir = strings.TrimPrefix(arg, "--data-dir=")
		case arg == "--download-deps" || arg == "--download-dependencies":
			options.DownloadDependencies = true
		case arg == "--quiet" || arg == "-q":
			options.Quiet = true
		case arg == "--no-hooks":
			options.NoHooks = true
		case arg == "--no-agent-skills":
			options.NoAgentSkills = true
		case arg == "--json":
			options.JSON = true
		case arg == "--committed":
			options.Committed = true
		case arg == "--tags":
			if len(args) < 2 || args[1] == "" {
				return Options{}, "", nil, fmt.Errorf("%w: --tags requires a comma-separated value", errUsage)
			}
			options.Tags = append(options.Tags, strings.Split(args[1], ",")...)
			args = args[2:]
			continue
		case strings.HasPrefix(arg, "--tags="):
			if len(arg) == len("--tags=") {
				return Options{}, "", nil, fmt.Errorf("%w: --tags requires a comma-separated value", errUsage)
			}
			options.Tags = append(options.Tags, strings.Split(strings.TrimPrefix(arg, "--tags="), ",")...)
		case arg == "--goos":
			if len(args) < 2 || args[1] == "" {
				return Options{}, "", nil, fmt.Errorf("%w: --goos requires a value", errUsage)
			}
			options.GOOS = args[1]
			args = args[2:]
			continue
		case strings.HasPrefix(arg, "--goos="):
			if len(arg) == len("--goos=") {
				return Options{}, "", nil, fmt.Errorf("%w: --goos requires a value", errUsage)
			}
			options.GOOS = strings.TrimPrefix(arg, "--goos=")
		case arg == "--goarch":
			if len(args) < 2 || args[1] == "" {
				return Options{}, "", nil, fmt.Errorf("%w: --goarch requires a value", errUsage)
			}
			options.GOARCH = args[1]
			args = args[2:]
			continue
		case strings.HasPrefix(arg, "--goarch="):
			if len(arg) == len("--goarch=") {
				return Options{}, "", nil, fmt.Errorf("%w: --goarch requires a value", errUsage)
			}
			options.GOARCH = strings.TrimPrefix(arg, "--goarch=")
		case arg == "--cgo" || arg == "--cgo-enabled":
			if len(args) < 2 || args[1] == "" {
				return Options{}, "", nil, fmt.Errorf("%w: %s requires 0 or 1", errUsage, arg)
			}
			options.CGOEnabled = args[1]
			args = args[2:]
			continue
		case strings.HasPrefix(arg, "--cgo=") || strings.HasPrefix(arg, "--cgo-enabled="):
			prefix := "--cgo="
			if strings.HasPrefix(arg, "--cgo-enabled=") {
				prefix = "--cgo-enabled="
			}
			if len(arg) == len(prefix) {
				return Options{}, "", nil, fmt.Errorf("%w: %s requires 0 or 1", errUsage, prefix[:len(prefix)-1])
			}
			options.CGOEnabled = strings.TrimPrefix(arg, prefix)
		case arg == "--":
			args = args[1:]
			if len(args) == 0 {
				return options, "", nil, nil
			}
		default:
			return options, arg, args[1:], nil
		}
		args = args[1:]
	}
	return options, "", nil, nil
}

func usage() string {
	return `Usage:
  jejak [global options] <command> [arguments]

Global options:
  -h, --help              show this usage text
  -V, --version           show the build version, revision, and platform
  -C, --worktree <path>  resolve the target from a worktree path
      --data-dir <path>  override the external Jejak data root
      --download-deps     explicitly allow missing Go dependencies to download
  -q, --quiet             suppress successful synchronization output
      --json              render agent-facing reports (and graph queries) as versioned JSON
      --committed         inspect only the durable committed graph
      --tags <a,b>        select additional Go build tags
      --goos <name>       select a Go target operating system
      --goarch <name>     select a Go target architecture
      --cgo <0|1>         select CGO availability

Commands implemented in the foundation, Go intelligence, semantic graph, incremental graph, and Git lifecycle phases:
  init                   build and activate the committed Go graph
      --no-hooks          skip safe Git lifecycle hook installation during init
      --no-agent-skills   skip project-local Claude and Codex skill integration
  status                 show repository, worktree, and graph state
  repos list             list registered repositories
  graph symbol <name>    inspect an effective symbol and relationships
  graph file <path>      inspect an effective source file
      --format <name>    graph output: text (default), json, or dot
      --committed         force graph inspection to the durable generation
      --working-tree      inspect the temporary working-tree overlay
  sync                   synchronize the graph with committed HEAD
  rebuild                force a complete graph rebuild
  hooks install           install safe Git lifecycle dispatchers
  hooks status            show Git lifecycle hook ownership/capability
  hooks uninstall         restore attributable original hooks
  impact --task <text>    show bounded effective context, implementation, and validation radii
      --working-tree      seed impact from actual working-tree changes when task is omitted
      --committed          force impact to the durable generation
  context --task <text>   show bounded effective source context for a task
  doctor                  inspect graph/storage integrity and Git integration
      --repair             quarantine corrupt storage and rebuild it
  gc                      collect obsolete Jejak-owned graph/cache data
      --dry-run             preview cleanup without deleting anything
      --keep-generations N  retain N newest generations per worktree
      --older-than D        remove temporary/log entries older than duration
  version                 show the build version, revision, and platform
`
}
