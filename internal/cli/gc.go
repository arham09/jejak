package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/arham09/jejak/internal/config"
	"github.com/arham09/jejak/internal/graphdb"
	"github.com/arham09/jejak/internal/repository"
)

type gcCommandOptions struct {
	JSON            bool
	DryRun          bool
	KeepGenerations int
	OlderThan       time.Duration
}

func (a *App) gc(ctx context.Context, options Options, args []string) error {
	commandOptions, err := parseGCCommandOptions(options, args)
	if err != nil {
		return err
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
			return fmt.Errorf("gc: repository is not registered (run `jejak init` first): %w", graphdb.ErrNotFound)
		}
		return fmt.Errorf("gc: inspect repository store: %w", err)
	}
	handle, _, err := a.openTargetStore(ctx, options, target)
	if err != nil {
		return fmt.Errorf("gc: %w", err)
	}
	gcOptions := graphdb.GCOptions{DryRun: commandOptions.DryRun, KeepGenerations: commandOptions.KeepGenerations, OlderThan: commandOptions.OlderThan}
	report, gcErr := handle.store.GC(ctx, target.Repository.ID, gcOptions)
	closeErr := handle.Close()
	if gcErr != nil {
		return joinClose(fmt.Errorf("gc: %w", gcErr), closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if commandOptions.JSON {
		return writeJSON(a.stdout, report)
	}
	renderGCText(a.stdout, target, report)
	return nil
}

func parseGCCommandOptions(options Options, args []string) (gcCommandOptions, error) {
	result := gcCommandOptions{JSON: options.JSON}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--dry-run" {
			result.DryRun = true
			continue
		}
		if arg == "--json" {
			result.JSON = true
			continue
		}
		name, value, hasValue := strings.Cut(arg, "=")
		if !hasValue {
			switch arg {
			case "--keep-generations", "--older-than":
				if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
					return gcCommandOptions{}, fmt.Errorf("%w: %s requires a value", errUsage, arg)
				}
				value = args[index+1]
				index++
			default:
				return gcCommandOptions{}, fmt.Errorf("%w: unknown gc option %q", errUsage, arg)
			}
		}
		switch name {
		case "--keep-generations":
			keep, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || keep <= 0 {
				return gcCommandOptions{}, fmt.Errorf("%w: --keep-generations requires a positive integer", errUsage)
			}
			result.KeepGenerations = keep
		case "--older-than":
			older, err := time.ParseDuration(strings.TrimSpace(value))
			if err != nil || older <= 0 {
				return gcCommandOptions{}, fmt.Errorf("%w: --older-than requires a positive duration (for example 24h)", errUsage)
			}
			result.OlderThan = older
		default:
			return gcCommandOptions{}, fmt.Errorf("%w: unknown gc option %q", errUsage, name)
		}
	}
	return result, nil
}

func renderGCText(output io.Writer, target repository.Target, report graphdb.GCReport) {
	fmt.Fprintln(output, "Jejak GC Report v1")
	fmt.Fprintf(output, "Repository  %s\n", target.Repository.ID)
	fmt.Fprintf(output, "Database    %s\n", report.DBPath)
	if report.DryRun {
		fmt.Fprintln(output, "Mode        dry-run (no data deleted)")
	} else {
		fmt.Fprintln(output, "Mode        execute")
	}
	fmt.Fprintf(output, "Keep        %d generation(s) per worktree\n", report.KeepGenerations)
	fmt.Fprintf(output, "Retained    %d generation(s)\n", report.RetainedGenerations)
	fmt.Fprintf(output, "Planned     %d item(s)\n", report.PlannedItems)
	fmt.Fprintf(output, "Deleted     %d item(s)\n", report.DeletedItems)
	fmt.Fprintf(output, "Reclaimed   %d bytes\n", report.ReclaimedBytes)
	if !report.DryRun {
		fmt.Fprintf(output, "Compacted   %d bytes returned to the filesystem\n", report.CompactedBytes)
	}
	if report.DeletedItems > 0 {
		fmt.Fprintf(output, "  generations %d  parse-cache %d  blobs %d  temporary %d  logs %d\n", report.DeletedGenerations, report.DeletedParseCache, report.DeletedBlobs, report.DeletedTemporary, report.DeletedLogs)
	}
	if len(report.Candidates) > 0 {
		if report.DryRun {
			fmt.Fprintln(output, "CANDIDATES (not deleted)")
		} else {
			fmt.Fprintln(output, "CANDIDATES")
		}
		for _, candidate := range report.Candidates {
			identifier := candidate.Path
			if identifier == "" {
				switch candidate.Kind {
				case graphdb.GCGeneration:
					identifier = fmt.Sprintf("%s generation %d", candidate.WorktreeID, candidate.GenerationID)
				case graphdb.GCParseCache, graphdb.GCBlob:
					identifier = candidate.BlobSHA
				}
			}
			fmt.Fprintf(output, "  %-12s %s (%d bytes)\n", candidate.Kind, identifier, candidate.Bytes)
		}
	}
	if len(report.Skipped) > 0 {
		fmt.Fprintln(output, "SKIPPED")
		for _, skipped := range report.Skipped {
			fmt.Fprintf(output, "  %-12s %s: %s\n", skipped.Kind, skipped.WorktreeID, skipped.Reason)
		}
	}
	if len(report.Errors) > 0 {
		fmt.Fprintln(output, "ERRORS")
		for _, message := range report.Errors {
			fmt.Fprintf(output, "  %s\n", message)
		}
	}
}
