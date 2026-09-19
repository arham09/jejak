package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/arham09/jejak/internal/graphdb"
	"github.com/arham09/jejak/internal/impact"
	"github.com/arham09/jejak/internal/overlay"
	"github.com/arham09/jejak/internal/repository"
)

type taskCommandOptions struct {
	Request      impact.Request
	JSON         bool
	WorkingTree  bool
	Committed    bool
	ExcerptLines int
	ExcerptBytes int
}

func (a *App) impact(ctx context.Context, options Options, args []string) error {
	commandOptions, err := parseTaskCommandOptions(options, args)
	if err != nil {
		return err
	}
	workingTree := commandOptions.WorkingTree || !commandOptions.Committed
	var target repository.Target
	var report impact.Report
	if workingTree {
		var handle *targetStore
		var base *graphdb.View
		var effective *overlay.View
		var openErr error
		target, handle, base, effective, openErr = a.openEffectiveView(ctx, options)
		if openErr != nil {
			return openErr
		}
		request := commandOptions.Request
		if commandOptions.WorkingTree && strings.TrimSpace(request.Task) == "" {
			request = actualChangeRequest(request, effective)
		}
		report, err = impact.Analyze(ctx, effective, request)
		if err == nil {
			applyOverlayMetadata(&report, effective)
		}
		closeErr := closeEffectiveResources(effective, base, handle)
		if err != nil {
			return joinClose(err, closeErr)
		}
		if closeErr != nil {
			return closeErr
		}
	} else {
		var handle *targetStore
		var view *graphdb.View
		var openErr error
		target, handle, view, openErr = a.openTaskView(ctx, options)
		if openErr != nil {
			return openErr
		}
		report, err = impact.Analyze(ctx, view, commandOptions.Request)
		closeErr := joinClose(view.Close(), handle.Close())
		if err != nil {
			return joinClose(err, closeErr)
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if commandOptions.JSON {
		return writeJSON(a.stdout, report)
	}
	return renderImpactText(a.stdout, target, report)
}

func (a *App) openTaskView(ctx context.Context, options Options) (target repository.Target, handle *targetStore, view *graphdb.View, err error) {
	_, target, handle, err = a.ensureCommand(ctx, options, false)
	if err != nil {
		return repository.Target{}, nil, nil, err
	}
	state, err := handle.store.State(ctx, target.Repository.ID, target.Worktree.ID)
	if err != nil {
		return repository.Target{}, handle, nil, joinClose(err, handle.Close())
	}
	if state.ActiveGeneration == nil {
		return repository.Target{}, handle, nil, joinClose(fmt.Errorf("%w: graph has no active generation", graphdb.ErrNotFound), handle.Close())
	}
	view, err = handle.store.OpenView(ctx, target.Repository.ID, target.Worktree.ID, *state.ActiveGeneration)
	if err != nil {
		return repository.Target{}, handle, nil, joinClose(err, handle.Close())
	}
	return target, handle, view, nil
}

func parseTaskCommandOptions(options Options, args []string) (taskCommandOptions, error) {
	result := taskCommandOptions{JSON: options.JSON, Committed: options.Committed, Request: impact.Request{}}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--working-tree" {
			result.WorkingTree = true
			continue
		}
		if arg == "--committed" {
			result.Committed = true
			continue
		}
		if arg == "--json" {
			result.JSON = true
			continue
		}
		name, value, hasValue := strings.Cut(arg, "=")
		if !hasValue {
			switch arg {
			case "--task", "--seed", "--max-seeds", "--max-depth", "--max-fanout", "--context-limit", "--implementation-limit", "--validation-limit", "--excerpt-lines", "--excerpt-bytes":
				if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
					return taskCommandOptions{}, fmt.Errorf("%w: %s requires a value", errUsage, arg)
				}
				value = args[index+1]
				index++
			default:
				return taskCommandOptions{}, fmt.Errorf("%w: unknown impact option %q", errUsage, arg)
			}
		}
		switch name {
		case "--task":
			if result.Request.Task != "" {
				return taskCommandOptions{}, fmt.Errorf("%w: --task may be specified once", errUsage)
			}
			result.Request.Task = value
		case "--seed":
			result.Request.SeedKeys = append(result.Request.SeedKeys, value)
		case "--max-seeds":
			parsed, parseErr := parsePositiveOption(name, value)
			if parseErr != nil {
				return taskCommandOptions{}, parseErr
			}
			result.Request.MaxSeeds = parsed
		case "--max-depth":
			parsed, parseErr := parsePositiveOption(name, value)
			if parseErr != nil {
				return taskCommandOptions{}, parseErr
			}
			result.Request.MaxDepth = parsed
		case "--max-fanout":
			parsed, parseErr := parsePositiveOption(name, value)
			if parseErr != nil {
				return taskCommandOptions{}, parseErr
			}
			result.Request.MaxFanout = parsed
		case "--context-limit":
			parsed, parseErr := parsePositiveOption(name, value)
			if parseErr != nil {
				return taskCommandOptions{}, parseErr
			}
			result.Request.ContextLimit = parsed
		case "--implementation-limit":
			parsed, parseErr := parsePositiveOption(name, value)
			if parseErr != nil {
				return taskCommandOptions{}, parseErr
			}
			result.Request.ImplementationLimit = parsed
		case "--validation-limit":
			parsed, parseErr := parsePositiveOption(name, value)
			if parseErr != nil {
				return taskCommandOptions{}, parseErr
			}
			result.Request.ValidationLimit = parsed
		case "--excerpt-lines":
			parsed, parseErr := parsePositiveOption(name, value)
			if parseErr != nil {
				return taskCommandOptions{}, parseErr
			}
			result.ExcerptLines = parsed
		case "--excerpt-bytes":
			parsed, parseErr := parsePositiveOption(name, value)
			if parseErr != nil {
				return taskCommandOptions{}, parseErr
			}
			result.ExcerptBytes = parsed
		default:
			return taskCommandOptions{}, fmt.Errorf("%w: unknown impact option %q", errUsage, name)
		}
	}
	if strings.TrimSpace(result.Request.Task) == "" && !result.WorkingTree {
		return taskCommandOptions{}, fmt.Errorf("%w: impact requires --task <text>", errUsage)
	}
	if result.WorkingTree && result.Committed {
		return taskCommandOptions{}, fmt.Errorf("%w: --working-tree and --committed cannot be combined", errUsage)
	}
	return result, nil
}

func parsePositiveOption(name, value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%w: %s requires a positive integer", errUsage, name)
	}
	return parsed, nil
}
