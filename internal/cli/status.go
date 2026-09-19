package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/arham09/jejak/internal/config"
	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/graphdb"
)

func (a *App) status(ctx context.Context, options Options, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("%w: status does not accept positional arguments", errUsage)
	}
	target, err := a.target(ctx, options)
	if err != nil {
		return err
	}
	dataRoot, err := config.ResolveDataRoot(options.DataDir, target.Repository.Root)
	if err != nil {
		return err
	}
	paths, err := config.RepositoryPathsFor(dataRoot, string(target.Repository.ID))
	if err != nil {
		return err
	}
	if _, err := os.Stat(paths.DB); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("status: repository is not registered (run `jejak init` first): %w", graphdb.ErrNotFound)
		}
		return fmt.Errorf("status: inspect repository store: %w", err)
	}
	handle, dataRoot, err := a.openTargetStore(ctx, options, target)
	if err != nil {
		return fmt.Errorf("status: %w (run `jejak init` if this repository is not registered)", err)
	}
	state, stateErr := handle.store.State(ctx, target.Repository.ID, target.Worktree.ID)
	hookReport, hookErr := a.hookManager(dataRoot).Status(ctx, target)
	worktreeChanges, worktreeErr := a.git.WorktreeChanges(ctx, target.Worktree.Path)
	closeErr := handle.Close()
	if stateErr != nil {
		return joinClose(stateErr, closeErr)
	}
	if worktreeErr != nil {
		return joinClose(fmt.Errorf("status: inspect working tree: %w", worktreeErr), closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if hookErr != nil {
		fmt.Fprintf(a.stderr, "jejak: hook status: %v\n", hookErr)
	}

	fmt.Fprintln(a.stdout, "Repository")
	fmt.Fprintf(a.stdout, "  ID          %s\n", target.Repository.ID)
	fmt.Fprintf(a.stdout, "  Identity    %s\n", target.Repository.CanonicalIdentity)
	fmt.Fprintf(a.stdout, "  Root        %s\n", target.Repository.Root)
	fmt.Fprintln(a.stdout, "Worktree")
	fmt.Fprintf(a.stdout, "  ID          %s\n", target.Worktree.ID)
	fmt.Fprintf(a.stdout, "  Path        %s\n", target.Worktree.Path)
	fmt.Fprintf(a.stdout, "  Branch      %s\n", displayBranch(target.Worktree.Branch))
	fmt.Fprintln(a.stdout, "Git")
	fmt.Fprintf(a.stdout, "  HEAD        %s\n", displayCommit(string(target.Worktree.Head), target.Worktree.HeadKnown))
	fmt.Fprintf(a.stdout, "  indexed HEAD %s\n", displayIndexedHead(state.IndexedHead))
	fmt.Fprintln(a.stdout, "Working tree")
	if len(worktreeChanges) == 0 {
		fmt.Fprintln(a.stdout, "  status      clean")
	} else {
		fmt.Fprintln(a.stdout, "  status      dirty")
		fmt.Fprintf(a.stdout, "  changes     %d\n", len(worktreeChanges))
		for _, change := range worktreeChanges {
			path := change.NewPath
			if path == "" {
				path = change.OldPath
			}
			fmt.Fprintf(a.stdout, "    %c%c %s\n", change.IndexStatus, change.WorktreeStatus, path)
		}
	}
	if state.ActiveGeneration != nil && target.Worktree.HeadKnown {
		fmt.Fprintln(a.stdout, "  overlay     ready (command-scoped)")
	} else {
		fmt.Fprintln(a.stdout, "  overlay     unavailable (no committed graph)")
	}
	fmt.Fprintln(a.stdout, "Graph")
	status := state.Status
	if state.Status == graph.StatusReady && state.ActiveGeneration != nil && graph.CommitSHA(target.Worktree.Head) != state.IndexedHead {
		status = graph.StatusStale
	}
	fmt.Fprintf(a.stdout, "  status      %s\n", status)
	fmt.Fprintf(a.stdout, "  generation  %s\n", displayGeneration(state.ActiveGeneration))
	if state.LastError != "" {
		fmt.Fprintf(a.stdout, "  last error  %s\n", state.LastError)
	}
	fmt.Fprintf(a.stdout, "Storage\n  data root   %s\n", dataRoot)
	if hookErr == nil {
		renderHookStatus(a.stdout, hookReport)
	} else {
		fmt.Fprintln(a.stdout, "Hooks\n  status      error (see stderr)")
	}
	return nil
}

func displayBranch(branch string) string {
	if branch == "" {
		return "(detached)"
	}
	return branch
}

func displayIndexedHead(head graph.CommitSHA) string {
	if head == "" {
		return "(none)"
	}
	return string(head)
}

func displayGeneration(generation *graph.GenerationID) string {
	if generation == nil {
		return "(none)"
	}
	return fmt.Sprintf("%d", *generation)
}
