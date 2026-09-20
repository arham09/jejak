package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/arham09/jejak/internal/agentskill"
	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/hooks"
)

func (a *App) init(ctx context.Context, options Options, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("%w: init does not accept positional arguments", errUsage)
	}
	target, err := a.target(ctx, options)
	if err != nil {
		return err
	}
	handle, dataRoot, err := a.openTargetStore(ctx, options, target)
	if err != nil {
		return err
	}
	state, err := handle.store.Register(ctx, target)
	if err != nil {
		return joinClose(err, handle.Close())
	}
	var counts graph.Counts
	if target.Worktree.HeadKnown {
		manager, managerErr := a.manager(target, handle, dataRoot)
		if managerErr != nil {
			return joinClose(managerErr, handle.Close())
		}
		ensured, ensureErr := manager.EnsureGraph(ctx, target, buildConfig(options))
		if ensureErr != nil {
			for _, diagnostic := range ensured.Analysis.Diagnostics {
				fmt.Fprintf(a.stderr, "jejak: %s", diagnostic.Message)
				if diagnostic.File != "" {
					fmt.Fprintf(a.stderr, " (%s)", diagnostic.File)
				}
				fmt.Fprintln(a.stderr)
			}
			return joinClose(ensureErr, handle.Close())
		}
		counts = ensured.Counts
		state, err = handle.store.State(ctx, target.Repository.ID, target.Worktree.ID)
		if err != nil {
			return joinClose(err, handle.Close())
		}
	}
	var hookReport hooks.Report
	if !options.NoHooks {
		hookReport, err = a.hookManager(dataRoot).Install(ctx, target)
		if err != nil {
			fmt.Fprintf(a.stderr, "jejak: hook integration: %v\n", err)
		}
	}
	var agentSkillReport agentskill.Report
	if !options.NoAgentSkills {
		var agentSkillErr error
		agentSkillReport, agentSkillErr = agentskill.Install(target.Worktree.Path)
		if agentSkillErr != nil {
			fmt.Fprintf(a.stderr, "jejak: agent skill integration: %v\n", agentSkillErr)
		}
	}
	closeErr := handle.Close()
	if closeErr != nil {
		return closeErr
	}

	fmt.Fprintln(a.stdout, "Repository initialized.")
	fmt.Fprintf(a.stdout, "ID: %s\n", target.Repository.ID)
	fmt.Fprintf(a.stdout, "Identity: %s\n", target.Repository.CanonicalIdentity)
	fmt.Fprintf(a.stdout, "Root: %s\n", target.Repository.Root)
	fmt.Fprintf(a.stdout, "Worktree: %s\n", target.Worktree.Path)
	fmt.Fprintf(a.stdout, "Worktree ID: %s\n", target.Worktree.ID)
	fmt.Fprintf(a.stdout, "HEAD: %s\n", displayCommit(string(target.Worktree.Head), target.Worktree.HeadKnown))
	if options.NoHooks {
		fmt.Fprintln(a.stdout, "Hooks: disabled (--no-hooks)")
	} else {
		renderHookReport(a.stdout, hookReport)
	}
	if options.NoAgentSkills {
		fmt.Fprintln(a.stdout, "Agent skills: disabled (--no-agent-skills)")
	} else {
		renderAgentSkillReport(a.stdout, agentSkillReport)
	}
	if state.Status == graph.StatusReady {
		fmt.Fprintln(a.stdout, "Analyzing repository...")
		fmt.Fprintf(a.stdout, "Packages       %d\n", counts.Packages)
		fmt.Fprintf(a.stdout, "Files          %d\n", counts.Files)
		fmt.Fprintf(a.stdout, "Symbols        %d\n", counts.Symbols)
		fmt.Fprintf(a.stdout, "Edges          %d\n", counts.Edges)
		if options.IncludeTests {
			fmt.Fprintf(a.stdout, "Tests          %d\n", counts.Tests)
		} else {
			fmt.Fprintln(a.stdout, "Tests          excluded (pass --include-tests to index test packages)")
		}
		fmt.Fprintf(a.stdout, "Graph: %s\n", state.Status)
		fmt.Fprintln(a.stdout, "Graph ready.")
	} else {
		fmt.Fprintf(a.stdout, "Graph: %s\n", state.Status)
	}
	return nil
}

func renderAgentSkillReport(output io.Writer, report agentskill.Report) {
	fmt.Fprintln(output, "Agent skills")
	if report.CanonicalPath != "" {
		fmt.Fprintf(output, "  canonical    %s\n", report.CanonicalPath)
	}
	for _, state := range report.States {
		fmt.Fprintf(output, "  %-12s %-10s %s", state.Name, state.Status, state.Path)
		if state.Detail != "" {
			fmt.Fprintf(output, " (%s)", state.Detail)
		}
		fmt.Fprintln(output)
	}
}

func displayCommit(commit string, known bool) string {
	if !known || commit == "" {
		return "(unborn)"
	}
	return string(commit)
}
