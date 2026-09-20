package cli

import (
	"context"
	"fmt"

	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/repository"
)

// sync refreshes the selected worktree's committed graph. It is safe to call
// after a missed hook because EnsureGraph rechecks the current Git HEAD and
// only activates a validated candidate.
func (a *App) sync(ctx context.Context, options Options, args []string) error {
	quiet := options.Quiet
	for _, arg := range args {
		if arg == "--quiet" {
			quiet = true
			continue
		}
		return fmt.Errorf("%w: sync accepts only --quiet", errUsage)
	}
	ensured, target, handle, err := a.ensureCommand(ctx, options, false)
	if err != nil {
		return err
	}
	closeErr := handle.Close()
	if closeErr != nil {
		return closeErr
	}
	if quiet {
		return nil
	}
	if ensured.Reused {
		fmt.Fprintln(a.stdout, "Graph already synchronized.")
		return nil
	}
	fmt.Fprintln(a.stdout, "Graph synchronized.")
	fmt.Fprintf(a.stdout, "Mode: %s\n", ensured.Mode)
	fmt.Fprintf(a.stdout, "Generation: %d\n", ensured.Generation.ID)
	fmt.Fprintf(a.stdout, "HEAD: %s\n", target.Worktree.Head)
	if len(ensured.Changes) > 0 {
		fmt.Fprintf(a.stdout, "Changed files: %d\n", len(ensured.Changes))
	}
	if ensured.Reason != "" {
		fmt.Fprintf(a.stdout, "Reason: %s\n", ensured.Reason)
	}
	return nil
}

func (a *App) ensureCommand(ctx context.Context, options Options, forceRebuild bool) (graph.EnsureResult, repository.Target, *targetStore, error) {
	target, err := a.target(ctx, options)
	if err != nil {
		return graph.EnsureResult{}, repository.Target{}, nil, err
	}
	handle, dataRoot, err := a.openTargetStore(ctx, options, target)
	if err != nil {
		return graph.EnsureResult{}, repository.Target{}, nil, err
	}
	_, err = handle.store.Register(ctx, target)
	if err != nil {
		return graph.EnsureResult{}, repository.Target{}, handle, joinClose(err, handle.Close())
	}
	if !target.Worktree.HeadKnown {
		return graph.EnsureResult{}, repository.Target{}, handle, joinClose(graph.ErrNoCommittedHead, handle.Close())
	}
	manager, err := a.manager(target, handle, dataRoot)
	if err != nil {
		return graph.EnsureResult{}, repository.Target{}, handle, joinClose(err, handle.Close())
	}
	var ensured graph.EnsureResult
	if forceRebuild {
		ensured, err = manager.RebuildGraph(ctx, target, buildConfig(options))
	} else {
		ensured, err = manager.EnsureGraph(ctx, target, buildConfig(options))
	}
	if err != nil {
		for _, diagnostic := range ensured.Analysis.Diagnostics {
			fmt.Fprintf(a.stderr, "jejak: %s", diagnostic.Message)
			if diagnostic.File != "" {
				fmt.Fprintf(a.stderr, " (%s)", diagnostic.File)
			}
			fmt.Fprintln(a.stderr)
		}
		return ensured, target, handle, joinClose(err, handle.Close())
	}
	return ensured, target, handle, nil
}

func buildConfig(options Options) graph.BuildConfig {
	return graph.BuildConfig{GOOS: options.GOOS, GOARCH: options.GOARCH, CGOEnabled: options.CGOEnabled, Tags: options.Tags, DownloadDependencies: options.DownloadDependencies, IncludeTests: options.IncludeTests}
}
