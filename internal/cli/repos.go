package cli

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/arham09/jejak/internal/graphdb"
	"github.com/arham09/jejak/internal/repository"
)

func (a *App) repos(ctx context.Context, options Options, args []string) error {
	if len(args) != 1 || args[0] != "list" {
		return fmt.Errorf("%w: use `repos list`", errUsage)
	}
	dataRoot, err := resolveDataRoot(options)
	if err != nil {
		return err
	}
	entries, issues, err := graphdb.ScanCatalog(ctx, dataRoot)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CanonicalIdentity == entries[j].CanonicalIdentity {
			return entries[i].ID < entries[j].ID
		}
		return entries[i].CanonicalIdentity < entries[j].CanonicalIdentity
	})
	if len(entries) == 0 && len(issues) == 0 {
		fmt.Fprintln(a.stdout, "No repositories registered.")
		return nil
	}
	for _, entry := range entries {
		writeCatalogEntry(a.stdout, entry)
	}
	for _, issue := range issues {
		fmt.Fprintf(a.stderr, "jejak: unreadable repository store %s: %v\n", issue.Path, issue.Err)
	}
	if len(issues) > 0 {
		return fmt.Errorf("%d repository store(s) could not be read", len(issues))
	}
	return nil
}

func writeCatalogEntry(out io.Writer, entry repository.CatalogEntry) {
	fmt.Fprintf(out, "%s\n", entry.ID)
	fmt.Fprintf(out, "  identity   %s\n", entry.CanonicalIdentity)
	fmt.Fprintf(out, "  root       %s\n", entry.Root)
	fmt.Fprintf(out, "  worktrees  %d\n", entry.WorktreeCount)
}
