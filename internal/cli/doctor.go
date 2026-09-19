package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/arham09/jejak/internal/config"
	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/graphdb"
	hookpkg "github.com/arham09/jejak/internal/hooks"
	"github.com/arham09/jejak/internal/repository"
)

type doctorCommandOptions struct {
	JSON   bool
	Repair bool
}

func (a *App) doctor(ctx context.Context, options Options, args []string) error {
	commandOptions, err := parseDoctorCommandOptions(options, args)
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
			return fmt.Errorf("doctor: repository is not registered (run `jejak init` first): %w", graphdb.ErrNotFound)
		}
		return fmt.Errorf("doctor: inspect repository store: %w", err)
	}
	report, inspectErr := a.inspectDoctor(ctx, options, target, dataRoot, paths)
	if inspectErr != nil && !commandOptions.Repair {
		return fmt.Errorf("doctor: %w", inspectErr)
	}
	if commandOptions.Repair && (inspectErr != nil || report.HasErrors()) {
		recovery, recoveryErr := graphdb.RecoverDatabase(ctx, paths.DB)
		if recoveryErr != nil {
			return fmt.Errorf("doctor: recover corrupt storage: %w", recoveryErr)
		}
		if rebuildErr := a.rebuildRecovered(ctx, options, target); rebuildErr != nil {
			return fmt.Errorf("doctor: recovered storage but rebuild failed (quarantine %s): %w", recovery.QuarantinePath, rebuildErr)
		}
		report, inspectErr = a.inspectDoctor(ctx, options, target, dataRoot, paths)
		if inspectErr != nil {
			return fmt.Errorf("doctor: inspect recovered storage: %w", inspectErr)
		}
		report.AddCheck("recovery", graphdb.DoctorCheckPass, fmt.Sprintf("recovered storage; quarantined copy at %s", recovery.QuarantinePath))
		report.Finalize()
	}
	if commandOptions.JSON {
		if err := writeJSON(a.stdout, report); err != nil {
			return err
		}
	} else {
		renderDoctorText(a.stdout, target, report)
	}
	if report.HasErrors() {
		return fmt.Errorf("doctor found %d error(s); see the report for remediation", countDoctorErrors(report))
	}
	return nil
}

func parseDoctorCommandOptions(options Options, args []string) (doctorCommandOptions, error) {
	result := doctorCommandOptions{JSON: options.JSON}
	for _, arg := range args {
		switch arg {
		case "--json":
			result.JSON = true
		case "--repair":
			result.Repair = true
		default:
			return doctorCommandOptions{}, fmt.Errorf("%w: doctor accepts only --repair and --json", errUsage)
		}
	}
	return result, nil
}

func (a *App) inspectDoctor(ctx context.Context, options Options, target repository.Target, dataRoot string, paths config.RepositoryPaths) (graphdb.DoctorReport, error) {
	store, err := graphdb.Open(ctx, paths.DB)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return graphdb.DoctorReport{}, ctxErr
		}
		report := graphdb.DoctorReport{Version: "jejak.doctor.v1", RepositoryID: target.Repository.ID, WorktreeID: target.Worktree.ID, DBPath: paths.DB, Healthy: false, Status: graphdb.DoctorUnhealthy}
		report.AddIssue("storage.open", graph.SeverityError, fmt.Sprintf("open graph database: %v", err))
		report.Finalize()
		return report, nil
	}
	report, doctorErr := store.Doctor(ctx, target.Repository.ID, target.Worktree.ID)
	if doctorErr != nil {
		return report, joinClose(doctorErr, store.Close())
	}
	a.addDoctorGitChecks(ctx, target, store, &report)
	a.addDoctorHookChecks(ctx, options, target, dataRoot, &report)
	report.Finalize()
	return report, store.Close()
}

func (a *App) addDoctorGitChecks(ctx context.Context, target repository.Target, store *graphdb.Store, report *graphdb.DoctorReport) {
	if report == nil || report.State == nil {
		return
	}
	state := report.State
	if state.IndexedHead == "" {
		if target.Worktree.HeadKnown {
			report.AddCheck("git.head", graphdb.DoctorCheckWarning, "worktree has a committed HEAD but no indexed HEAD")
		} else {
			report.AddCheck("git.head", graphdb.DoctorCheckPass, "repository has no committed HEAD (unborn)")
		}
		return
	}
	exists, err := a.git.CommitExists(ctx, target.Worktree.Path, string(state.IndexedHead))
	if err != nil {
		report.AddIssue("git.commit_check", graph.SeverityWarning, fmt.Sprintf("could not verify indexed commit %s: %v", state.IndexedHead, err))
	} else if !exists {
		report.AddIssue("git.commit_missing", graph.SeverityError, fmt.Sprintf("indexed commit %s is unavailable in the local Git object database", state.IndexedHead))
	} else {
		report.AddCheck("git.commit", graphdb.DoctorCheckPass, fmt.Sprintf("indexed commit %s is available", state.IndexedHead))
	}
	if !target.Worktree.HeadKnown {
		report.AddIssue("git.head_missing", graph.SeverityError, "graph has an indexed commit but the worktree HEAD is unborn")
		return
	}
	if graph.CommitSHA(target.Worktree.Head) != state.IndexedHead {
		report.AddIssue("git.stale", graph.SeverityWarning, fmt.Sprintf("current HEAD %s differs from indexed HEAD %s; run `jejak sync`", target.Worktree.Head, state.IndexedHead))
	} else {
		report.AddCheck("git.head", graphdb.DoctorCheckPass, "current HEAD matches indexed HEAD")
	}
	if report.Generation == nil || store == nil {
		return
	}
	blobs, err := store.GenerationFileBlobSHAs(ctx, target.Repository.ID, target.Worktree.ID, report.Generation.ID)
	if err != nil {
		report.AddIssue("git.blobs_check", graph.SeverityWarning, fmt.Sprintf("could not enumerate indexed file blobs: %v", err))
		return
	}
	missing := make([]string, 0)
	for _, blob := range blobs {
		exists, checkErr := a.git.BlobExists(ctx, target.Worktree.Path, blob)
		if checkErr != nil {
			report.AddIssue("git.blob_check", graph.SeverityWarning, fmt.Sprintf("could not verify indexed blob %s: %v", blob, checkErr))
			continue
		}
		if !exists {
			missing = append(missing, blob)
		}
	}
	if len(missing) > 0 {
		report.AddIssue("git.blobs_missing", graph.SeverityError, fmt.Sprintf("%d indexed file blob(s) are unavailable in the local Git object database", len(missing)))
	} else {
		report.AddCheck("git.blobs", graphdb.DoctorCheckPass, fmt.Sprintf("%d indexed file blob(s) are available", len(blobs)))
	}
}

func (a *App) addDoctorHookChecks(ctx context.Context, options Options, target repository.Target, dataRoot string, report *graphdb.DoctorReport) {
	if report == nil {
		return
	}
	hookReport, err := a.hookManager(dataRoot).Status(ctx, target)
	if err != nil {
		report.AddIssue("hooks.status", graph.SeverityWarning, fmt.Sprintf("inspect hook integration: %v", err))
		return
	}
	report.Hooks = make([]graphdb.DoctorHook, 0, len(hookReport.States))
	installed := 0
	problem := false
	for _, state := range hookReport.States {
		report.Hooks = append(report.Hooks, graphdb.DoctorHook{Name: state.Name, Status: string(state.Status), Detail: state.Detail})
		switch state.Status {
		case hookpkg.StatusInstalled:
			installed++
		case hookpkg.StatusConflict, hookpkg.StatusError:
			problem = true
		}
	}
	if problem {
		report.AddIssue("hooks.conflict", graph.SeverityWarning, "one or more lifecycle hooks are conflicting or unreadable; existing hook ownership was preserved")
	} else if installed == 0 {
		report.AddCheck("hooks.integration", graphdb.DoctorCheckWarning, "no Jejak lifecycle dispatcher is installed; run `jejak hooks install` if desired")
	} else {
		report.AddCheck("hooks.integration", graphdb.DoctorCheckPass, fmt.Sprintf("%d Jejak lifecycle dispatcher(s) installed", installed))
	}
}

func (a *App) rebuildRecovered(ctx context.Context, options Options, target repository.Target) error {
	handle, dataRoot, err := a.openTargetStore(ctx, options, target)
	if err != nil {
		return err
	}
	if _, err := handle.store.Register(ctx, target); err != nil {
		return joinClose(err, handle.Close())
	}
	if target.Worktree.HeadKnown {
		manager, managerErr := a.manager(target, handle, dataRoot)
		if managerErr != nil {
			return joinClose(managerErr, handle.Close())
		}
		if _, ensureErr := manager.EnsureGraph(ctx, target, buildConfig(options)); ensureErr != nil {
			return joinClose(ensureErr, handle.Close())
		}
	}
	return handle.Close()
}

func renderDoctorText(output io.Writer, target repository.Target, report graphdb.DoctorReport) {
	fmt.Fprintln(output, "Jejak Doctor Report v1")
	fmt.Fprintf(output, "Repository  %s\n", target.Repository.ID)
	fmt.Fprintf(output, "Worktree    %s\n", target.Worktree.ID)
	fmt.Fprintf(output, "Database    %s\n", report.DBPath)
	fmt.Fprintf(output, "Status      %s\n", report.Status)
	fmt.Fprintf(output, "Healthy     %t\n", report.Healthy)
	if report.SchemaVersion > 0 {
		fmt.Fprintf(output, "Schema      %d\n", report.SchemaVersion)
	}
	if report.State != nil {
		fmt.Fprintf(output, "Graph       %s\n", report.State.Status)
		if report.State.ActiveGeneration != nil {
			fmt.Fprintf(output, "Generation  %d\n", *report.State.ActiveGeneration)
		}
	}
	fmt.Fprintln(output, "CHECKS")
	for _, check := range report.Checks {
		fmt.Fprintf(output, "  %-24s %-7s %s\n", check.Name, check.Status, check.Detail)
	}
	if len(report.Hooks) > 0 {
		fmt.Fprintln(output, "HOOKS")
		for _, hook := range report.Hooks {
			if hook.Detail == "" {
				fmt.Fprintf(output, "  %-16s %s\n", hook.Name, hook.Status)
			} else {
				fmt.Fprintf(output, "  %-16s %-12s %s\n", hook.Name, hook.Status, hook.Detail)
			}
		}
	}
	if len(report.Issues) > 0 {
		fmt.Fprintln(output, "ISSUES")
		for _, issue := range report.Issues {
			fmt.Fprintf(output, "  [%s] %s: %s\n", issue.Severity, issue.Code, issue.Message)
		}
	}
}

func countDoctorErrors(report graphdb.DoctorReport) int {
	count := 0
	for _, issue := range report.Issues {
		if issue.Severity == graph.SeverityError {
			count++
		}
	}
	return count
}
