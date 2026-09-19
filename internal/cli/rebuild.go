package cli

import (
	"context"
	"fmt"
)

// rebuild forces a complete graph candidate for the selected worktree. It is
// useful after analyzer or build-selection changes and remains generation
// safe because Manager validates the candidate before activation.
func (a *App) rebuild(ctx context.Context, options Options, args []string) error {
	quiet := options.Quiet
	for _, arg := range args {
		if arg == "--quiet" || arg == "-q" {
			quiet = true
			continue
		}
		return fmt.Errorf("%w: rebuild accepts only --quiet", errUsage)
	}
	ensured, target, handle, err := a.ensureCommand(ctx, options, true)
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
	fmt.Fprintln(a.stdout, "Graph rebuilt.")
	fmt.Fprintf(a.stdout, "Mode: %s\n", ensured.Mode)
	fmt.Fprintf(a.stdout, "Generation: %d\n", ensured.Generation.ID)
	fmt.Fprintf(a.stdout, "HEAD: %s\n", target.Worktree.Head)
	if ensured.Reason != "" {
		fmt.Fprintf(a.stdout, "Reason: %s\n", ensured.Reason)
	}
	return nil
}
