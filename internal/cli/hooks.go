package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/arham09/jejak/internal/config"
	hookpkg "github.com/arham09/jejak/internal/hooks"
)

func (a *App) hooks(ctx context.Context, options Options, args []string) error {
	if options.NoHooks {
		return fmt.Errorf("%w: --no-hooks is only valid for init", errUsage)
	}
	if len(args) != 1 {
		return fmt.Errorf("%w: hooks requires `install`, `status`, or `uninstall`", errUsage)
	}
	switch args[0] {
	case "status":
		return a.hookStatus(ctx, options)
	case "install":
		return a.hookMutation(ctx, options, false)
	case "uninstall":
		return a.hookMutation(ctx, options, true)
	default:
		return fmt.Errorf("%w: unknown hooks action %q", errUsage, args[0])
	}
}

func (a *App) hookStatus(ctx context.Context, options Options) error {
	target, err := a.target(ctx, options)
	if err != nil {
		return err
	}
	dataRoot, err := config.ResolveDataRoot(options.DataDir, target.Repository.Root)
	if err != nil {
		return err
	}
	report, err := a.hookManager(dataRoot).Status(ctx, target)
	if err != nil {
		return err
	}
	renderHookStatus(a.stdout, report)
	return nil
}

func (a *App) hookMutation(ctx context.Context, options Options, uninstall bool) error {
	target, err := a.target(ctx, options)
	if err != nil {
		return err
	}
	handle, dataRoot, err := a.openTargetStore(ctx, options, target)
	if err != nil {
		return err
	}
	if _, err := handle.store.Register(ctx, target); err != nil {
		return joinClose(err, handle.Close())
	}
	manager := a.hookManager(dataRoot)
	var report hookpkg.Report
	if uninstall {
		report, err = manager.Uninstall(ctx, target)
	} else {
		report, err = manager.Install(ctx, target)
	}
	closeErr := handle.Close()
	if err != nil {
		renderHookStatus(a.stdout, report)
		return joinClose(err, closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	renderHookStatus(a.stdout, report)
	return nil
}

func renderHookReport(output io.Writer, report hookpkg.Report) {
	fmt.Fprintln(output, "Hooks")
	if report.ManifestPath != "" {
		fmt.Fprintf(output, "  manifest    %s\n", report.ManifestPath)
	}
	for _, state := range report.States {
		fmt.Fprintf(output, "  %-12s %-13s", state.Name, state.Status)
		if state.Detail != "" {
			fmt.Fprintf(output, " %s", state.Detail)
		}
		fmt.Fprintln(output)
	}
}

func renderHookStatus(output io.Writer, report hookpkg.Report) {
	renderHookReport(output, report)
}
